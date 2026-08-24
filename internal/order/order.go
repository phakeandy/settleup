package order

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-sql-driver/mysql"
	"github.com/phakeandy/settleup/internal"
	"github.com/redis/go-redis/v9"
)

const (
	StatusCreated = iota + 1
	StatusPaid
	StatusCancelled
)

const (
	PaymentPending = iota + 1
	PaymentSucceeded
	PaymentCancelled
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

type Order struct {
	ID             int64  `json:"id"`
	UserID         int    `json:"user_id"`
	ProductID      int    `json:"product_id"`
	Quantity       int64  `json:"quantity"`
	AmountCent     int64  `json:"amount_cent"`
	Status         int    `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

type OrderWithPayment struct {
	Order
	PaymentID int64 `json:"payment_id"`
}

// queryRower 是 *sql.DB 和 *sql.Tx 的公共子集,用来在"事务外查"和"事务内查"两种
// 场景下复用同一段查询代码。
type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// lookupOrderWithPayment 按 idempotency_key 查已经存在的订单+支付记录。
// 用于两种回放场景:Redis 判定 ALREADY_GRANTED,或者 MySQL 插入撞了 1062。
func lookupOrderWithPayment(q queryRower, idempotencyKey string) (Order, int64, error) {
	var order Order
	if err := q.QueryRow(`SELECT id, user_id, product_id, quantity, amount_cent, status, idempotency_key
FROM orders WHERE idempotency_key = ?`, idempotencyKey).Scan(&order.ID, &order.UserID, &order.ProductID, &order.Quantity, &order.AmountCent, &order.Status, &order.IdempotencyKey); err != nil {
		return Order{}, 0, err
	}
	var paymentID int64
	if err := q.QueryRow(`SELECT id FROM payments WHERE order_id = ?`, order.ID).Scan(&paymentID); err != nil {
		return Order{}, 0, err
	}
	return order, paymentID, nil
}

// writeOrderWithPayment 把订单+支付按 200(回放)状态码写回去。
func writeOrderWithPayment(w http.ResponseWriter, order Order, paymentID int64) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(OrderWithPayment{Order: order, PaymentID: paymentID})
}

func CreateHandler(db *sql.DB, rdb *redis.Client) internal.AppHandler {
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

		// TODO
		// tq.Enqueue(ctx, rdb *RDB, tq.NewTask("order.create", paylaod))

		// 库存检查+幂等发放先问 Redis,在打 MySQL 之前就把库存不够的请求挡掉——
		// 这一步必须在开事务、插订单之前,不然 MySQL 还是要扛全部流量的 INSERT。
		grantedKey := fmt.Sprintf("settleup:{%d}:granted:%s", payload.ProductID, payload.IdempotencyKey)
		inventoryKey := fmt.Sprintf("settleup:inventory:{%d}", payload.ProductID)
		stockRes, err := grantOnceScript.Run(ctx, rdb, []string{grantedKey, inventoryKey}, payload.Quantity).Result()
		if err != nil {
			if err.Error() == "ALREADY_GRANTED" {
				// Redis 已经发放过这个 idempotency_key,去 MySQL 找对应的订单。
				// 查不到,说明 MySQL 那次写还没成功(可能正卡在补偿任务里),
				// 用 409 让客户端知道这次还没定,稍后重试。
				order, paymentID, lookupErr := lookupOrderWithPayment(db, payload.IdempotencyKey)
				if errors.Is(lookupErr, sql.ErrNoRows) {
					return internal.Conflict("order not settled yet, retry shortly", nil)
				}
				if lookupErr != nil {
					return lookupErr
				}
				return writeOrderWithPayment(w, order, paymentID)
			}
			return err
		}
		switch stockRes {
		case "OK":
			// 库存已扣减,继续写订单和支付
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
VALUES (-1, ?, ?, ?, ?, ?)`, payload.ProductID, payload.Quantity, amountCent, StatusCreated, payload.IdempotencyKey)
		if err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1062 {
				// 罕见窗口:Redis 判定 OK 放行了,但 MySQL 里这个 idempotency_key
				// 已经有订单了(比如两个并发请求几乎同时通过 Redis 校验)。回放已有订单。
				order, paymentID, lookupErr := lookupOrderWithPayment(tx, payload.IdempotencyKey)
				if lookupErr != nil {
					return lookupErr
				}
				return writeOrderWithPayment(w, order, paymentID)
			}
			return err
		}
		orderID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		paymentRes, err := tx.Exec(`INSERT INTO payments (user_id, order_id, amount_cent, status)
VALUES (-1, ?, ?, ?)`,
			orderID, amountCent, PaymentPending)
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

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		return json.NewEncoder(w).Encode(OrderWithPayment{
			Order: Order{
				ID:             orderID,
				UserID:         -1,
				ProductID:      payload.ProductID,
				Quantity:       payload.Quantity,
				AmountCent:     amountCent,
				Status:         StatusCreated,
				IdempotencyKey: payload.IdempotencyKey,
			},
			PaymentID: paymentID,
		})
	}
}
