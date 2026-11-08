package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

func Connect(ctx context.Context, url string, withVector bool) (*pgxpool.Pool, error) {
	pool, err := connect(ctx, url, false)
	if err != nil {
		return nil, err
	}
	if !withVector {
		return pool, nil
	}
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create vector extension: %w", err)
	}
	pool.Close()
	return connect(ctx, url, true)
}

func connect(ctx context.Context, url string, withVector bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if withVector {
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			return pgxvec.RegisterTypes(ctx, conn)
		}
	}
	var pool *pgxpool.Pool
	var last error
	for i := 0; i < 30; i++ {
		pool, last = pgxpool.NewWithConfig(ctx, cfg)
		if last == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				return pool, nil
			} else {
				last = pingErr
				pool.Close()
			}
		}
		slog.Warn("waiting for postgres", "attempt", i+1, "err", last)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("postgres not ready: %w", last)
}

func ExecAll(ctx context.Context, pool *pgxpool.Pool, statements ...string) error {
	for _, sql := range statements {
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, sql)
		}
	}
	return nil
}
