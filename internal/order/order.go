package order

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-sql-driver/mysql"
	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/settleup/internal/db"

	"github.com/phakeandy/tq"
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

func CreateHandler(w http.ResponseWriter, req *http.Request) error {
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

	// TODO
	// ctx := req.Context()
	// tq.Enqueue(ctx, rdb *RDB, tq.NewTask("order.create", paylaod))

	var priceCent int64
	if err := db.DB.QueryRow(`SELECT price_cent FROM products WHERE id = ?`, payload.ProductID).Scan(&priceCent); err != nil {
		return err
	}

	tx, err := db.DB.Begin()
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
			var (
				order     Order
				paymentID int64
			)
			if err := tx.QueryRow(`SELECT id, user_id, product_id, quantity, amount_cent, status, idempotency_key
FROM orders WHERE idempotency_key = ?`, payload.IdempotencyKey).Scan(&order.ID, &order.UserID, &order.ProductID, &order.Quantity, &order.AmountCent, &order.Status, &order.IdempotencyKey); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT id FROM payments WHERE order_id = ?`, order.ID).Scan(&paymentID); err != nil {
				return err
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return json.NewEncoder(w).Encode(OrderWithPayment{Order: order, PaymentID: paymentID})
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
