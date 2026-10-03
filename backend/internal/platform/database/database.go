package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// minPoolConns is the smallest default pool size.
const minPoolConns = 16

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	// Workflows hold a transaction while reading through the pool (directory checks), so a
	// pool of pgx's default size (max(4, CPUs)) could starve itself under concurrent submits.
	// An explicit pool_max_conns in the URL wins.
	if !strings.Contains(databaseURL, "pool_max_conns") && cfg.MaxConns < minPoolConns {
		cfg.MaxConns = minPoolConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
