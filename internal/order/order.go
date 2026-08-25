package order

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/tq"
	"github.com/redis/go-redis/v9"
)

// grantOnceScript 幂等扣减库存的 Lua 脚本。
// KEYS[1] = granted_key
// KEYS[2] = inventory_key
// ARGV[1] = quantity
var grantOnceScript = redis.NewScript(`if redis.call("EXISTS", KEYS[1]) == 1 then
  return redis.error_reply("ALREADY_GRANTED")
end
local quantity = tonumber(ARGV[1])
local stock = tonumber(redis.call('GET', KEYS[2])) or 0
if stock < quantity then
    return 'INSUFFICIENT_STOCK'
end
redis.call('DECRBY', KEYS[2], quantity)
redis.call('SETEX', KEYS[1], 300, 1)
return "OK"`)

func CreateHandler(db *sql.DB, rdb *redis.Client) internal.AppHandler {
	// tqRDB 在构造 handler 时建一次,所有请求共用;不要每个请求都 NewRDB。
	tqRDB := tq.NewRDB(rdb)
	return func(w http.ResponseWriter, req *http.Request) error {
		var payload struct {
			// UserID         int    `json:"user_id"` TODO: sigal user
			ProductID      int    `json:"product_id"`
			Quantity       int64  `json:"quantity"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return internal.BadRequest("invalid request body", err)
		}
		if payload.Quantity <= 0 || payload.IdempotencyKey == "" || payload.ProductID == 0 {
			return internal.BadRequest("invalid request payload", nil)
		}

		ctx := req.Context()

		// granted:Redis 已扣减库存。committed:MySQL 事务已提交。
		// 只有"扣了库存但没提交成功"这一种状态需要补偿。
		granted, committed := false, false
		defer func() {
			if !granted || committed {
				return
			}
			// 补偿入队要脱离请求的 context:如果这次 return 正是因为 req.Context()
			// 被取消(客户端断开/超时),那恰恰是最需要补偿的时刻,不能因为 ctx 已取消
			// 就把补偿任务也一起丢了。WithoutCancel 保留 ctx 的值但去掉取消信号。
			bgCtx := context.WithoutCancel(ctx)
			p := &CompensatePayload{
				ProductID:      payload.ProductID,
				Quantity:       payload.Quantity,
				IdempotencyKey: payload.IdempotencyKey,
			}
			if err := compensate(bgCtx, tqRDB, p); err != nil {
				// 入队都失败了(通常是 Redis 挂了)——这是一笔静默库存泄漏,
				// 把定位需要的字段全打出来,交给对账兜底。
				slog.Error("enqueue compensation failed, stock may be leaked",
					"error", err,
					"product_id", p.ProductID,
					"quantity", p.Quantity,
					"idempotency_key", p.IdempotencyKey)
			}
		}()

		// 库存检查+幂等发放先问 Redis,在打 MySQL 之前就把库存不够的请求挡掉——
		// 这一步必须在开事务、插订单之前,不然 MySQL 还是要扛全部流量的 INSERT。
		grantedKey := internal.GrantedKey(payload.ProductID, payload.IdempotencyKey)
		inventoryKey := internal.InventoryKey(payload.ProductID)
		stockRes, err := grantOnceScript.Run(ctx, rdb, []string{grantedKey, inventoryKey}, payload.Quantity).Result()
		if err != nil {
			if err.Error() == "ALREADY_GRANTED" {
				// Redis 已经发放过这个 idempotency_key,去 MySQL 找对应的订单。
				// 查不到,说明 MySQL 那次写还没成功(可能正卡在补偿任务里),
				// 用 409 让客户端知道这次还没定,稍后重试。
				existing, lookupErr := lookupOrderWithPayment(db, payload.IdempotencyKey)
				if errors.Is(lookupErr, sql.ErrNoRows) {
					return internal.Conflict("order not settled yet, retry shortly", nil)
				}
				if lookupErr != nil {
					return lookupErr
				}
				return writeOrderWithPayment(w, existing)
			}
			return err
		}
		switch stockRes {
		case "OK":
			granted = true
		case "INSUFFICIENT_STOCK":
			return internal.BadRequest("insufficient stock", nil)
		default:
			return errors.New("unexpected script result")
		}

		var priceCent int64
		if err := db.QueryRow(`SELECT price_cent FROM products WHERE id = ?`, payload.ProductID).Scan(&priceCent); err != nil {
			return err
		}

		// Open database's transaction.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		amountCent := priceCent * payload.Quantity
		res, err := tx.Exec(`INSERT INTO orders (user_id, product_id, quantity, amount_cent, status, idempotency_key)
VALUES (-1, ?, ?, ?, ?, ?)`, payload.ProductID, payload.Quantity, amountCent, internal.StatusCreated, payload.IdempotencyKey)
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1062 {
				// 罕见窗口:Redis 判定 OK 放行了,但 MySQL 里这个 idempotency_key
				// 已经有订单了(比如两个并发请求几乎同时通过 Redis 校验)。回放已有订单。
				existing, lookupErr := lookupOrderWithPayment(tx, payload.IdempotencyKey)
				if lookupErr != nil {
					return lookupErr
				}
				return writeOrderWithPayment(w, existing)
			}
			return err
		}
		orderID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		paymentRes, err := tx.Exec(`INSERT INTO payments (user_id, order_id, amount_cent, status)
VALUES (-1, ?, ?, ?)`,
			orderID, amountCent, internal.PaymentPending)
		if err != nil {
			return err
		}
		paymentID, err := paymentRes.LastInsertId()
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		return json.NewEncoder(w).Encode(OrderWithPayment{
			Order: Order{
				ID:             orderID,
				UserID:         -1,
				ProductID:      payload.ProductID,
				Quantity:       payload.Quantity,
				AmountCent:     amountCent,
				Status:         internal.StatusCreated,
				IdempotencyKey: payload.IdempotencyKey,
			},
			PaymentID: paymentID,
		})
	}
}

// queryRower 是 *sql.DB 和 *sql.Tx 的公共子集,用来在"事务外查"和"事务内查"两种
// 场景下复用同一段查询代码。
type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// lookupOrderWithPayment 按 idempotency_key 查已经存在的订单+支付记录。
// 用于两种回放场景:Redis 判定 ALREADY_GRANTED,或者 MySQL 插入撞了 1062。
func lookupOrderWithPayment(q queryRower, idempotencyKey string) (OrderWithPayment, error) {
	var row OrderWithPayment
	err := q.QueryRow(`SELECT o.id, o.user_id, o.product_id, o.quantity, o.amount_cent, o.status, o.idempotency_key, p.id
FROM orders o
JOIN payments p ON p.order_id = o.id
WHERE o.idempotency_key = ?`, idempotencyKey).Scan(
		&row.ID, &row.UserID, &row.ProductID, &row.Quantity, &row.AmountCent, &row.Status, &row.IdempotencyKey, &row.PaymentID)
	if err != nil {
		return OrderWithPayment{}, err
	}
	return row, nil
}

// writeOrderWithPayment 把订单+支付按 200(回放)状态码写回去。
func writeOrderWithPayment(w http.ResponseWriter, row OrderWithPayment) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(row)
}

type CompensatePayload struct {
	ProductID      int    `json:"product_id"`
	Quantity       int64  `json:"quantity"`
	IdempotencyKey string `json:"idempotency_key"`
}

func compensate(ctx context.Context, rdb *tq.RDB, payload *CompensatePayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// 不设 WithIdempotencyKey:grant(granted_key)已经保证每一代扣减最多只有一个
	// 请求会走到 granted=true,所以补偿入队天然是"每代恰好一次"。反而如果用一个跨代
	// 稳定的 "compensate:"+idem 当幂等键,客户端重试触发第二代扣减、再次失败时,tq 会
	// 认为这个键已终态、直接返回旧结果而不再入队,第二代的库存就补不回来了。
	// 执行层的"至少一次"由补偿脚本自身的幂等(DEL granted_key 守卫)兜住。
	task := tq.NewTask(internal.OrderCompensateKey, b, tq.WithMaxRetries(5))
	if _, err := tq.Enqueue(ctx, rdb, task); err != nil {
		return err
	}
	return nil
}
