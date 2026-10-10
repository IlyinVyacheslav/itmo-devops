package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"lab3/src/db"
)

type Order struct {
	ID          int64      `json:"id"`
	Item        string     `json:"item"`
	Quantity    int        `json:"quantity"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at"`
}

type CreateOrder struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

func main() {
	ctx := context.Background()

	pool, err := db.Open(ctx)
	if err != nil {
		log.Fatal("database connection failed: ", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("HEALTH_FAIL") == "true" {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /order", func(w http.ResponseWriter, r *http.Request) {
		var input CreateOrder

		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if input.Item == "" || input.Quantity <= 0 {
			http.Error(w, "item and positive quantity are required",
				http.StatusBadRequest)
			return
		}

		var order Order

		err := pool.QueryRow(r.Context(), `
			INSERT INTO orders (item, quantity)
			VALUES ($1, $2)
			RETURNING id, item, quantity, status, created_at, processed_at
		`, input.Item, input.Quantity).Scan(
			&order.ID,
			&order.Item,
			&order.Quantity,
			&order.Status,
			&order.CreatedAt,
			&order.ProcessedAt,
		)
		if err != nil {
			log.Printf("create order: %v", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(order)
	})

	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		rows, err := pool.Query(r.Context(), `
			SELECT id, item, quantity, status, created_at, processed_at
			FROM orders
			ORDER BY id
		`)
		if err != nil {
			log.Printf("list orders: %v", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		orders := make([]Order, 0)

		for rows.Next() {
			var order Order

			if err := rows.Scan(
				&order.ID,
				&order.Item,
				&order.Quantity,
				&order.Status,
				&order.CreatedAt,
				&order.ProcessedAt,
			); err != nil {
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}

			orders = append(orders, order)
		}

		if err := rows.Err(); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(orders)
	})

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("API listening on :8080")
	log.Fatal(server.ListenAndServe())
}
