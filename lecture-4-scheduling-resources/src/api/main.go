package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"lab3/src/db"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RED-метрики для мониторинга Части 9: счётчик запросов по коду/роуту и
// гистограмма времени ответа. Отдаются на /metrics, скребёт ServiceMonitor.
var (
	httpReqs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "shop_api_requests_total",
		Help: "Total HTTP requests by status code and route",
	}, []string{"code", "route"})

	httpDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "shop_api_request_duration_seconds",
		Help:    "HTTP request latency seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
)

// statusRecorder запоминает код ответа, чтобы посчитать его в метрике.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next(rec, r)
		if rec.code == 0 {
			rec.code = http.StatusOK
		}
		httpReqs.WithLabelValues(strconv.Itoa(rec.code), r.URL.Path).Inc()
		httpDur.WithLabelValues(r.URL.Path).Observe(time.Since(start).Seconds())
	}
}

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

// Пул появляется, когда БД становится доступна. Сервис и /health стартуют без неё.
var dbPool atomic.Pointer[pgxpool.Pool]

func getPool(w http.ResponseWriter) *pgxpool.Pool {
	p := dbPool.Load()
	if p == nil {
		http.Error(w, "database is not ready", http.StatusServiceUnavailable)
	}
	return p
}

func main() {
	ctx := context.Background()

	go func() {
		for {
			pool, err := db.Open(ctx)
			if err == nil {
				dbPool.Store(pool)
				log.Println("database connected")
				return
			}
			log.Printf("database connection failed: %v (retry in 5s)", err)
			time.Sleep(5 * time.Second)
		}
	}()

	mux := http.NewServeMux()

	// /metrics — сервисная RED-метрика для ServiceMonitor (Часть 9)
	mux.Handle("/metrics", promhttp.Handler())

	mux.Handle("GET /health", instrument(func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("HEALTH_FAIL") == "true" {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	mux.Handle("POST /order", instrument(func(w http.ResponseWriter, r *http.Request) {
		pool := getPool(w)
		if pool == nil {
			return
		}

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
	}))

	mux.Handle("GET /orders", instrument(func(w http.ResponseWriter, r *http.Request) {
		pool := getPool(w)
		if pool == nil {
			return
		}

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
	}))

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("API listening on :8080")
	log.Fatal(server.ListenAndServe())
}
