package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"

	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/settleup/internal/db"
	"github.com/phakeandy/settleup/internal/order"
	"github.com/phakeandy/settleup/internal/payment"
	"github.com/phakeandy/settleup/internal/product"
	_ "github.com/phakeandy/tq"
)

func main() {
	db.Init()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
			slog.Info("encode response failed", "error", err)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("todo...\n"))
	})
	mux.Handle("POST /api/orders", internal.AppHandler(order.CreateHandler))
	mux.Handle("GET /api/products", internal.AppHandler(product.ListHandler))
	mux.Handle("POST /api/payments", internal.AppHandler(payment.CreateHandler))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
