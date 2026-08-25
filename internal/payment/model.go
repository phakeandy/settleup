package payment

import "time"

type Payment struct {
	ID         int64     `json:"id"`
	OrderID    int64     `json:"order_id"`
	UserID     int       `json:"user_id"`
	AmountCent int64     `json:"amount_cent"`
	Status     int       `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}
