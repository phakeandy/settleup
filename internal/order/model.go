package order

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
