package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"

	_ "github.com/go-sql-driver/mysql"
	"github.com/phakeandy/settleup/internal/db"
	_ "github.com/phakeandy/tq"
)

type H map[string]interface{}

type httpError struct {
	status int
	msg    string
	err    error
}

func (e *httpError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.err)
	}
	return e.msg
}

func (e *httpError) Unwrap() error {
	return e.err
}

func badRequest(msg string, err error) error {
	return &httpError{status: http.StatusBadRequest, msg: msg, err: err}
}

func main() {
	db.Init()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		if err := json.NewEncoder(w).Encode(H{"status": "ok"}); err != nil {
			slog.Info("encode response failed", "error", err)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("todo...\n"))
	})

	mux.Handle("POST /api/orders", appHandler(createOrderHandler))
	mux.Handle("GET /api/products", appHandler(listProductHandler))

	if err := http.ListenAndServe(":8080", mux); err != nil {
		slog.Error("fail to start", "error", err)
	}
}

type appHandler func(w http.ResponseWriter, r *http.Request) error

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(H{"error": msg}); err != nil {
		slog.Info("encode error response failed", "error", err)
	}
}

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := fn(w, r); err != nil {
		var he *httpError
		if errors.As(err, &he) {
			log.Printf("HTTP Error: %v", err)
			writeError(w, he.status, he.msg)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Resource not found")
			return
		}
		log.Printf("HTTP Error: %v", err)
		writeError(w, http.StatusInternalServerError, "An unexpected error occurred")
	}
}

func createOrderHandler(w http.ResponseWriter, req *http.Request) error {
	var payload struct {
		// UserID         int    `json:"user_id"`
		ProductID      int    `json:"product_id"`
		Quantity       int64  `json:"quantity"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		return badRequest("invalid request body", err)
	}
	if payload.Quantity <= 0 || payload.IdempotencyKey == "" || payload.ProductID == 0 {
		return badRequest("invalid request payload", nil)
	}

	var priceCent int64
	if err := db.DB.QueryRow(`SELECT price_cent FROM products WHERE id = ?`, payload.ProductID).Scan(&priceCent); err != nil {
		return err
	}

	amountCent := priceCent * payload.Quantity
	res, err := db.DB.Exec(`INSERT INTO orders (user_id, product_id, quantity, amount_cent, status, idempotency_key) VALUES (-1, ?, ?, ?, 1, ?) `,
		// payload.UserID, TODO: sigle user
		payload.ProductID, payload.Quantity, amountCent, payload.IdempotencyKey)
	if err != nil {
		return err
	}
	orderID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(H{
		"id": orderID,
		// "user_id":         payload.UserID,
		"user_id":         -1,
		"product_id":      payload.ProductID,
		"quantity":        payload.Quantity,
		"amount_cent":     amountCent,
		"status":          1,
		"idempotency_key": payload.IdempotencyKey,
	})
}

func listProductHandler(w http.ResponseWriter, req *http.Request) error {
	rows, err := db.DB.Query("SELECT id, name, price_cent FROM products")
	if err != nil {
		return err
	}
	defer rows.Close()

	var res []interface{}
	var i int
	for rows.Next() {
		var id int
		var name string
		var price_cent int
		if err := rows.Scan(&id, &name, &price_cent); err != nil {
			return err
		}
		res = append(res, H{"id": id, "name": name, "price_cent": price_cent})
		i++
	}
	if err := json.NewEncoder(w).Encode(res); err != nil {
		return err
	}

	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
