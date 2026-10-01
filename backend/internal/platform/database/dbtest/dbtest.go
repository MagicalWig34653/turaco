// Package dbtest provides the shared PostgreSQL connection helper for
// database-backed tests. Import it only from _test.go files.
package dbtest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequireEnv makes an unavailable database a test failure instead of a skip.
// CI and the Claude Code cloud bootstrap set it so the quality gate cannot
// pass while silently skipping database tests.
const RequireEnv = "TURACO_REQUIRE_DB_TESTS"

// Pool connects to DATABASE_URL and closes the pool when the test ends.
// Without a reachable database the test is skipped, or fails when
// TURACO_REQUIRE_DB_TESTS=true.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		Unavailable(t, "DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		Unavailable(t, "database unavailable: "+err.Error())
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		Unavailable(t, "database unreachable: "+err.Error())
	}
	t.Cleanup(pool.Close)
	return pool
}

// Unavailable skips the test, or fails it when TURACO_REQUIRE_DB_TESTS=true.
// Use it for missing schema objects as well as missing connectivity.
func Unavailable(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv(RequireEnv) == "true" {
		t.Fatalf("%s (%s=true)", reason, RequireEnv)
	}
	t.Skip(reason)
}
