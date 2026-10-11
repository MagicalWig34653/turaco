package health

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestExplicitProbeIsStoredAuditedAndRateLimited(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var userID, correlationID string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text, uuidv7()::text`).Scan(&userID, &correlationID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := NewProber(pool, map[string]func(context.Context) error{"zz_probe_test": func(context.Context) error { calls++; return errors.New("secret must not appear") }})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.health_probes WHERE check_key = 'zz_probe_test'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE target_type = 'health_check' AND target_id = 'zz_probe_test'`)
	})
	if _, err := p.Run(ctx, "unknown", userID, correlationID); !errors.Is(err, ErrProbeUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
	result, err := p.Run(ctx, "zz_probe_test", userID, correlationID)
	if err != nil || result.Status != StatusFailing || result.ErrorCode != "connection_failed" {
		t.Fatalf("probe: %+v %v", result, err)
	}
	if _, err := p.Run(ctx, "zz_probe_test", userID, correlationID); !errors.Is(err, ErrProbeRateLimited) || calls != 1 {
		t.Fatalf("rate limit: calls=%d err=%v", calls, err)
	}
	snapshot, err := p.Snapshot(ctx)
	if err != nil || snapshot["zz_probe_test"].Status != StatusFailing {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'platform.health.probe_run' AND target_type = 'health_check' AND target_id = 'zz_probe_test' AND metadata::text NOT LIKE '%secret%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit: %d %v", count, err)
	}
}
