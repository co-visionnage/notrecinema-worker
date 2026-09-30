// Package postgres — тонкая обёртка над пулом pgx для воркера. В отличие
// от notrecinema-api воркеру не нужен RLS-скоуп по пользователю: он
// работает с системной таблицей public.worker_processed_events, у которой
// RLS не включена.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	pool *pgxpool.Pool
}

func Connect(ctx context.Context, databaseURL string) (*Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}

	poolConfig.MaxConns = 10
	poolConfig.MaxConnIdleTime = 30 * time.Second
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}

	return &Pool{pool: pool}, nil
}

func (p *Pool) Close() {
	p.pool.Close()
}

func (p *Pool) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

// Exec выполняет одиночный запрос без транзакции.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.pool.Exec(ctx, sql, args...)
}

func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.pool.Query(ctx, sql, args...)
}

func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.pool.QueryRow(ctx, sql, args...)
}
