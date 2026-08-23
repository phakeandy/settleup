package product

import (
	"encoding/json"
	"net/http"

	"github.com/phakeandy/settleup/internal/db"
)

func ListHandler(w http.ResponseWriter, req *http.Request) error {
	rows, err := db.DB.Query("SELECT id, name, price_cent FROM products")
	if err != nil {
		return err
	}
	defer rows.Close()

	var res []interface{}
	for rows.Next() {
		var (
			id         int
			name       string
			price_cent int
		)
		if err := rows.Scan(&id, &name, &price_cent); err != nil {
			return err
		}
		res = append(res, map[string]any{"id": id, "name": name, "price_cent": price_cent})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(res); err != nil {
		return err
	}

	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
