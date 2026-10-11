package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestDiscoverOrdersMigrations(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000002_second.up.sql", "ignore.txt", "000001_first.up.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("unexpected migration order: %+v", got)
	}
}

func TestWithLockSerializesRuns(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var second bool
	err := withLock(ctx, pool, logger, func() error {
		other, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer other.Release()
		var got bool
		if err := other.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&got); err != nil {
			return err
		}
		second = got
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("a second session obtained the migration lock while it was held")
	}
	// Released after fn returns: the lock can be taken again.
	if err := withLock(ctx, pool, logger, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("boom")
	if err := withLock(ctx, pool, logger, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
}
