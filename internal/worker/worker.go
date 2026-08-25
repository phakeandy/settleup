// Package worker 跑 tq 的消费端:目前只有一个补偿 handler,负责把"Redis 扣了库存、
// MySQL 没写成功"那批订单的库存原子地加回去。
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/settleup/internal/order"
	"github.com/phakeandy/tq"
	"github.com/redis/go-redis/v9"
)

// compensateScript 把库存加回去,幂等。
// KEYS[1] = inventory_key, KEYS[2] = granted_key
// ARGV[1] = quantity
//
// 幂等的关键在 DEL 的返回值:DEL 返回真正删掉的 key 数。只有当 granted_key 还在
// (== 1)才 INCRBY,并且删 key 这一步和加库存在同一个原子脚本里。于是:
//   - tq 至少一次重投同一个 job:第二次 DEL 返回 0,直接跳过,不会重复加库存。
//   - granted_key 每次 grant 都会新写一份,所以它天然是"这一代扣减"的 fencing token,
//     补偿只会把当前这一代的扣减加回来。
//
// 顺手删掉 granted_key 还有一个作用:让卡在"已 granted 但订单没建成"的 idempotency_key
// 解封,客户端后续重试能重新走完整流程。
//
// 注意 backoff:5 次重试的总退避约 31s,远小于 granted_key 的 300s TTL,所以正常情况下
// 补偿一定在 granted_key 过期前跑完。只有 Redis 长时间不可用才会漏,那种情况交给对账兜底。
var compensateScript = redis.NewScript(`if redis.call('DEL', KEYS[2]) == 1 then
  redis.call('INCRBY', KEYS[1], ARGV[1])
  return 'OK'
end
return 'ALREADY_COMPENSATED'`)

// Run 起 tq 消费端,阻塞到 ctx 取消。
func Run(ctx context.Context, rdb *redis.Client, concurrency int) error {
	handlers := tq.H{
		internal.OrderCompensateKey: compensateHandler(rdb),
	}
	return tq.Run(ctx, tq.NewRDB(rdb), handlers, concurrency)
}

func compensateHandler(rdb *redis.Client) tq.Handle {
	return func(ctx context.Context, j *tq.Job) ([]byte, error) {
		var p order.CompensatePayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			// payload 坏了重试也没用,但 tq 只认 error 触发重试/终态;
			// 这里返回 error 让它走完重试后落到 finally failed,由对账发现。
			return nil, fmt.Errorf("compensate: unmarshal payload: %w", err)
		}
		keys := []string{
			internal.InventoryKey(p.ProductID),
			internal.GrantedKey(p.ProductID, p.IdempotencyKey),
		}
		res, err := compensateScript.Run(ctx, rdb, keys, p.Quantity).Result()
		if err != nil {
			// 返回非 nil error → tq 按 WithMaxRetries 退避重试。
			return nil, fmt.Errorf("compensate: run script: %w", err)
		}
		return []byte(fmt.Sprintf("%v", res)), nil
	}
}
