package query

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Page is one page of results.
type Page[T any] struct {
	Items      []T
	NextCursor string
	// Count and CountCapped are set when the request asked for a count.
	Count       *int
	CountCapped bool
	Warnings    []Warning
}

// ReadTx runs fn in a read-only transaction under statement_timeout. A
// timeout is reported as ErrTimeout.
func ReadTx(ctx context.Context, pool *pgxpool.Pool, timeout int64, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`, strconv.FormatInt(timeout, 10)); err != nil {
		return fmt.Errorf("set statement timeout: %w", err)
	}
	if err := fn(tx); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "57014" {
			return ErrTimeout
		}
		return err
	}
	return tx.Commit(ctx)
}

// Run executes the page statement. scan reads one row: it scans the module's
// columns and must append extra (the sort-key texts) to the scan targets.
func Run[T any](ctx context.Context, pool *pgxpool.Pool, p *Plan, sel Select, scan func(rows pgx.Rows, extra []any) (T, error)) (Page[T], error) {
	sql, args := p.Statement(sel)
	page := Page[T]{Items: []T{}, Warnings: p.Warnings}
	var lastKeys []*string
	err := ReadTx(ctx, pool, p.engine.timeout.Milliseconds(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			keys := make([]*string, len(p.keys))
			extra := make([]any, len(keys))
			for i := range keys {
				extra[i] = &keys[i]
			}
			item, err := scan(rows, extra)
			if err != nil {
				return err
			}
			if len(page.Items) == p.limit {
				// One row beyond the page proves a next page exists.
				page.NextCursor = p.engine.codec.encode(p.hash, lastKeys)
				return nil
			}
			page.Items = append(page.Items, item)
			lastKeys = keys
		}
		return rows.Err()
	})
	if err != nil {
		return Page[T]{}, err
	}
	if p.wantCount {
		n, capped, err := Count(ctx, pool, p, sel)
		if err != nil {
			return Page[T]{}, err
		}
		page.Count, page.CountCapped = &n, capped
	}
	return page, nil
}

// Count returns the number of rows matching visibility and filter, at most
// CountCap, and whether the real number is larger.
func Count(ctx context.Context, pool *pgxpool.Pool, p *Plan, sel Select) (int, bool, error) {
	sql, args := p.CountStatement(sel)
	var n int
	err := ReadTx(ctx, pool, p.engine.timeout.Milliseconds(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	})
	if err != nil {
		return 0, false, err
	}
	if n > CountCap {
		return CountCap, true, nil
	}
	return n, false, nil
}
