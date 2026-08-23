package payment

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/settleup/internal/db"
	"github.com/phakeandy/settleup/internal/order"
)

type Payment struct {
	ID         int64     `json:"id"`
	OrderID    int64     `json:"order_id"`
	UserID     int       `json:"user_id"`
	AmountCent int64     `json:"amount_cent"`
	Status     int       `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateHandler 执行支付：在一个事务里原子翻转两个状态机——
// payment: pending -> succeeded 且 order: created -> paid，要么全成要么全不成。
//
// 幂等不靠客户端键，靠条件更新：UPDATE payments SET status=succeeded WHERE id=? AND status=pending。
// 重试时该更新影响行数为 0，说明已付过，直接返回成功（200，不是 4xx）。
func CreateHandler(w http.ResponseWriter, req *http.Request) error {
	var payload struct {
		PaymentID int64 `json:"payment_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		return internal.BadRequest("invalid request body", err)
	}
	if payload.PaymentID <= 0 {
		return internal.BadRequest("invalid request payload", nil)
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 条件更新：只有 pending 的支付才能被翻转为 succeeded。
	res, err := tx.Exec(`UPDATE payments SET status = ? WHERE id = ? AND status = ?`,
		order.PaymentSucceeded, payload.PaymentID, order.PaymentPending)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}

	var p Payment
	if affected == 0 {
		// 影响行数为 0：支付不存在 / 已付过 / 不可支付——查当前状态区分。
		if err := tx.QueryRow(`SELECT id, order_id, user_id, amount_cent, status, created_at
FROM payments WHERE id = ?`, payload.PaymentID).Scan(&p.ID, &p.OrderID, &p.UserID, &p.AmountCent, &p.Status, &p.CreatedAt); err != nil {
			return err // sql.ErrNoRows -> 404
		}
		if p.Status == order.PaymentSucceeded {
			// 幂等重放：已付过，直接返回成功。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return json.NewEncoder(w).Encode(p)
		}
		return internal.BadRequest("payment is not payable", nil)
	}

	// 支付翻转成功：读回支付拿到 order_id，再把订单 created -> paid。
	if err := tx.QueryRow(`SELECT id, order_id, user_id, amount_cent, status, created_at
FROM payments WHERE id = ?`, payload.PaymentID).Scan(&p.ID, &p.OrderID, &p.UserID, &p.AmountCent, &p.Status, &p.CreatedAt); err != nil {
		return err
	}

	res, err = tx.Exec(`UPDATE orders SET status = ?, paid_at = NOW() WHERE id = ? AND status = ?`,
		order.StatusPaid, p.OrderID, order.StatusCreated)
	if err != nil {
		return err
	}
	affected, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// 订单不是 created 状态（数据不一致）——整体回滚，支付保持 pending。
		return fmt.Errorf("order %d is not in created status", p.OrderID)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(p)
}
