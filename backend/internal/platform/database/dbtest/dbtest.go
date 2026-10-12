// Package dbtest provides the shared PostgreSQL connection helper for
// database-backed tests. Import it only from _test.go files.
package dbtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequireEnv makes an unavailable database a test failure instead of a skip.
// CI and the Claude Code cloud bootstrap set it so the quality gate cannot
// pass while silently skipping database tests.
const RequireEnv = "TURACO_REQUIRE_DB_TESTS"

// Pool connects to TEST_DATABASE_URL (a separate, migrated test database in
// local and cloud development), else DATABASE_URL (CI's fresh database), and
// closes the pool when the test ends.
// Without a reachable database the test is skipped, or fails when
// TURACO_REQUIRE_DB_TESTS=true.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		Unavailable(t, "neither TEST_DATABASE_URL nor DATABASE_URL is set")
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

// PurgeSLAFixtures removes the Queues whose key starts with one of the prefixes together with their Tickets, SLA
// clocks (including the immutable transitions) and SLA policies of the same test. Tests that start SLA clocks commit
// their data, and the transition trigger blocks the usual cleanup, so the trigger is disabled inside one transaction.
// Run it from t.Cleanup. It is for test databases only.
func PurgeSLAFixtures(t testing.TB, pool *pgxpool.Pool, keyPrefixes ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("purge SLA fixtures: %v", err)
		return
	}
	defer tx.Rollback(ctx)
	steps := []string{
		`ALTER TABLE servicedesk.ticket_sla_clock_transitions DISABLE TRIGGER ticket_sla_transitions_immutable`,
		`DELETE FROM servicedesk.ticket_sla_clock_transitions WHERE ticket_id IN (SELECT t.id FROM servicedesk.tickets t JOIN servicedesk.queues q ON q.id = t.queue_id WHERE q.key LIKE ANY($1))`,
		`ALTER TABLE servicedesk.ticket_sla_clock_transitions ENABLE TRIGGER ticket_sla_transitions_immutable`,
		`DELETE FROM servicedesk.ticket_sla_clocks WHERE ticket_id IN (SELECT t.id FROM servicedesk.tickets t JOIN servicedesk.queues q ON q.id = t.queue_id WHERE q.key LIKE ANY($1))`,
		`DELETE FROM servicedesk.reference_registry WHERE queue_id IN (SELECT id FROM servicedesk.queues WHERE key LIKE ANY($1))`,
		`DELETE FROM servicedesk.tickets WHERE queue_id IN (SELECT id FROM servicedesk.queues WHERE key LIKE ANY($1))`,
		`DELETE FROM servicedesk.queue_grants WHERE queue_id IN (SELECT id FROM servicedesk.queues WHERE key LIKE ANY($1))`,
		`DELETE FROM servicedesk.queue_routing_rules WHERE queue_id IN (SELECT id FROM servicedesk.queues WHERE key LIKE ANY($1))`,
		`DELETE FROM servicedesk.queues WHERE key LIKE ANY($1)`,
	}
	patterns := make([]string, len(keyPrefixes))
	for i, p := range keyPrefixes {
		patterns[i] = p + "%"
	}
	for _, sql := range steps {
		var args []any
		if strings.Contains(sql, "$1") {
			args = []any{patterns}
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Errorf("purge SLA fixtures: %v", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("purge SLA fixtures: %v", err)
	}
}
