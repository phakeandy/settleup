package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"github.com/phakeandy/tq"
)

type H map[string]interface{}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		if err := json.NewEncoder(w).Encode(H{"status": "ok"}); err != nil {
			slog.Info("encode response failed", "error", err)
		})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("todo...\n"))
	})

	mux.HandleFunc("POST /v1/orders", createOrderHandler))

	if err := http.ListenAndServe(":8080", mux); err != nil {
		slog.Error("fail to start", "error", err)
	}
}

func createOrderHandler(w http.ResponseWriter, req *http.Request) {
}
