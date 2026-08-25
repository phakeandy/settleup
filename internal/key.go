package internal

import "fmt"

const (
	OrderCompensateKey = "order.compensate"
)

// InventoryKey 商品剩余库存的 Redis key。
// {%d} 是 hash tag:保证 inventory / granted 落在同一个 slot,Lua 多 key 操作在
// cluster 下才合法。
func InventoryKey(productID int) string {
	return fmt.Sprintf("settleup:inventory:{%d}", productID)
}

// GrantedKey 标记"这个 idempotency_key 已经拿到过库存"的 key,带 TTL。
// 它同时充当补偿的 fencing token:每次 grant 会新写一份,补偿脚本靠删它来保证
// 每一代扣减最多被加回一次(见 worker.compensateScript)。
func GrantedKey(productID int, idempotencyKey string) string {
	return fmt.Sprintf("settleup:{%d}:granted:%s", productID, idempotencyKey)
}
