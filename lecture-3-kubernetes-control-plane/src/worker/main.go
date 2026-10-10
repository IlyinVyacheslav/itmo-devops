package main

import (
	"context"
	"errors"
	"log"
	"time"

	"lab3/src/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()

	pool, err := db.Open(ctx)
	if err != nil {
		log.Fatal("database connection failed: ", err)
	}
	defer pool.Close()

	log.Println("Worker started")

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		if err := processOne(ctx, pool); err != nil {
			log.Printf("processing error: %v", err)
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
