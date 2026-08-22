package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"

	_ "github.com/go-sql-driver/mysql"
	"github.com/phakeandy/settleup/internal/db"
	_ "github.com/phakeandy/tq"
)

type H map[string]interface{}

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

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := fn(w, r); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Resource not found", http.StatusNotFound)
			return
		}
		log.Printf("HTTP Error: %v", err)
		http.Error(w, "An unexpected error occurred", http.StatusInternalServerError)
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
		return err
	}
	if payload.Quantity <= 0 || payload.IdempotencyKey == "" || payload.ProductID == 0 {
		return errors.New("invalid request payload")
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
	return json.NewEncoder(w).Encode(H{
		"id":              orderID,
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
