// TODO: understand the relationship between row-level lock and index.  See
// round 4.

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
		// db.Stats() 里 WaitCount/WaitDuration 是 database/sql 连接池自己的计数器:
		// 有多少次调用在等一个池内连接、总共等了多久。这段等待发生在应用进程内部,
		// 请求还没碰到 MySQL,所以不会出现在 InnoDB 的 Innodb_row_lock% 计数器里——
		// 见 docs/loadtest-journal.md Round 3。
		stats := db.DB.Stats()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"db_max_open_connections": stats.MaxOpenConnections,
			"db_open_connections":     stats.OpenConnections,
			"db_in_use":               stats.InUse,
			"db_idle":                 stats.Idle,
			"db_wait_count":           stats.WaitCount,
			"db_wait_duration_ms":     stats.WaitDuration.Milliseconds(),
		}); err != nil {
			slog.Info("encode metrics failed", "error", err)
		}
	})
	mux.Handle("POST /api/orders", internal.AppHandler(order.CreateHandler))
	mux.Handle("GET /api/products", internal.AppHandler(product.ListHandler))
	mux.Handle("POST /api/payments", internal.AppHandler(payment.CreateHandler))

	log.Fatal(http.ListenAndServe(":8080", mux))
}
