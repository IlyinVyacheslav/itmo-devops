package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"lab3/src/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Метрика ошибок обработки для алерта ShopWorkerFailure (Часть 9).
var (
	processErrs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shop_worker_errors_total",
		Help: "Total async order processing errors",
	})
	processJobs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "shop_worker_jobs_total",
		Help: "Total processed jobs",
	})
)

// Пул появляется, когда БД становится доступна. /health работает и без неё.
var dbPool atomic.Pointer[pgxpool.Pool]

func main() {
	ctx := context.Background()

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		mux.Handle("/metrics", promhttp.Handler())
		log.Fatal(http.ListenAndServe(":8081", mux))
	}()

	// Подключение к БД в фоне, с повторами
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

	log.Println("Worker started")

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		if pool := dbPool.Load(); pool != nil {
			if err := processOne(ctx, pool); err != nil {
				processErrs.Inc()
				log.Printf("processing error: %v", err)
			} else {
				processJobs.Inc()
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func processOne(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var id int64
	var item string
	var quantity int

	err = tx.QueryRow(ctx, `
		SELECT id, item, quantity
		FROM orders
		WHERE status = 'pending'
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&id, &item, &quantity)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	log.Printf("Processing order #%d: item=%s quantity=%d",
		id, item, quantity)

	_, err = tx.Exec(ctx, `
		UPDATE orders
		SET status = 'processed', processed_at = NOW()
		WHERE id = $1
	`, id)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
