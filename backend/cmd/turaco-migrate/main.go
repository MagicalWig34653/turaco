package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var migrationName = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

type migration struct {
	Version int64
	Name    string
	Path    string
}

func main() {
	dir := flag.String("dir", getenv("MIGRATIONS_DIR", "backend/migrations"), "migration directory")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Error("create database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		logger.Error("ping database", "error", err)
		os.Exit(1)
	}
	if err := run(ctx, pool, *dir, logger); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, pool *pgxpool.Pool, dir string, logger *slog.Logger) error {
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS platform`); err != nil {
		return fmt.Errorf("create platform schema: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS platform.schema_migrations (
		version bigint PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	migrations, err := discover(dir)
	if err != nil {
		return err
	}
	applied := map[int64]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM platform.schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate migration versions: %w", err)
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		sqlBytes, err := os.ReadFile(m.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", m.Name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("execute %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.schema_migrations(version, name, applied_at) VALUES ($1,$2,$3)`, m.Version, m.Name, time.Now().UTC()); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", m.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", m.Name, err)
		}
		logger.Info("migration applied", "version", m.Version, "name", m.Name)
	}
	return nil
}

func discover(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migration directory %s: %w", dir, err)
	}
	var out []migration
	seen := map[int64]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", entry.Name(), err)
		}
		if previous, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", version, previous, entry.Name())
		}
		seen[version] = entry.Name()
		out = append(out, migration{Version: version, Name: entry.Name(), Path: filepath.Join(dir, entry.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func getenv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
