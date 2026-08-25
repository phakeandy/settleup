// TODO: understand the relationship between row-level lock and index.  See
// round 4.

package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"

	"github.com/phakeandy/settleup/internal"
	"github.com/phakeandy/settleup/internal/db"
	"github.com/phakeandy/settleup/internal/metrics"
	"github.com/phakeandy/settleup/internal/order"
	"github.com/phakeandy/settleup/internal/payment"
	"github.com/phakeandy/settleup/internal/product"
	"github.com/phakeandy/settleup/internal/worker"
)

func main() {
	db.Init()

	// 起 tq 消费端处理补偿任务。Run 会阻塞,所以放 goroutine 里。
	// TODO: 接 signal 做优雅退出,现在用 context.Background()。
	go func() {
		if err := worker.Run(context.Background(), db.RDB, 4); err != nil {
			log.Fatalf("worker stopped: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", HealthHandle)
	mux.HandleFunc("/metrics", metrics.Handler)
	mux.Handle("POST /api/orders", order.CreateHandler(db.DB, db.RDB))
	mux.Handle("GET /api/products", internal.AppHandler(product.ListHandler))
	mux.Handle("POST /api/payments", internal.AppHandler(payment.CreateHandler))

	log.Fatal(http.ListenAndServe(":8080", mux))
}

func HealthHandle(w http.ResponseWriter, req *http.Request) {
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Info("encode response failed", "error", err)
	}
}
