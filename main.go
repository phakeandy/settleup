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

	mux.HandleFunc("POST /api/orders", createOrderHandler)
	mux.Handle("GET /api/products", appHandler(listProductHandler))

	if err := http.ListenAndServe(":8080", mux); err != nil {
		slog.Error("fail to start", "error", err)
	}
}

func createOrderHandler(w http.ResponseWriter, req *http.Request) {
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
