package internal

// OrderStatus 订单状态机。order 和 payment 包都要引用它（payment 翻转支付状态时
// 需要把订单从 created 打到 paid），放在 internal 里避免 order/payment 互相 import。
const (
	StatusCreated = iota + 1
	StatusPaid
	StatusCancelled
)

// PaymentStatus 支付状态机。order 包创建订单时要写入初始的 PaymentPending，
// 同样放在这里避免 order/payment 互相 import。
const (
	PaymentPending = iota + 1
	PaymentSucceeded
	PaymentCancelled
)
