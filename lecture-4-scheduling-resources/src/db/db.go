package db

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context) (*pgxpool.Pool, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://app:app@postgres:5432/orders?sslmode=disable"
	}

	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}

	config.MaxConns = 5

	var pool *pgxpool.Pool

	for i := 0; i < 30; i++ {
		pool, err = pgxpool.NewWithConfig(ctx, config)
		if err == nil {
			err = pool.Ping(ctx)
		}

		if err == nil {
			break
		}

		if pool != nil {
			pool.Close()
			pool = nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	if err != nil {
		return nil, err
	}

	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS orders (
			id BIGSERIAL PRIMARY KEY,
			item TEXT NOT NULL,
			quantity INTEGER NOT NULL CHECK (quantity > 0),
			status TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'processed')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			processed_at TIMESTAMPTZ
		)
	`)
	if err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}
