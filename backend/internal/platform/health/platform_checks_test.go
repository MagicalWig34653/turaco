package health

import (
	"context"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestPlatformChecksAgainstDatabase(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	r := NewRegistry()
	if err := RegisterPlatformChecks(r, pool); err != nil {
		t.Fatal(err)
	}
	entries := map[string]Entry{}
	for _, e := range r.All(ctx) {
		entries[e.Key] = e
	}
	for _, k := range []string{"database", "migrations", "worker", "jobs", "outbox"} {
		if _, ok := entries[k]; !ok {
			t.Fatalf("missing check %s", k)
		}
	}
	if entries["database"].Status != StatusOK || entries["migrations"].Status != StatusOK {
		t.Fatalf("database/migrations: %+v %+v", entries["database"], entries["migrations"])
	}
	if entries["migrations"].Detail["latestVersion"].(int64) < 71 {
		t.Errorf("latest migration: %+v", entries["migrations"].Detail)
	}
}

func TestWorkerCheckFollowsHeartbeats(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	id := "test-worker-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.worker_heartbeats WHERE instance_id = $1`, id) })
	check := func() Result {
		r := NewRegistry()
		_ = RegisterPlatformChecks(r, pool)
		e, _ := r.Get(ctx, "worker")
		return e.Result
	}
	// A stale heartbeat of this instance only matters when no other worker is running; assert on this row's effect.
	if _, err := pool.Exec(ctx, `INSERT INTO platform.worker_heartbeats (instance_id, version, started_at, last_seen_at) VALUES ($1, 'test', now(), now())`, id); err != nil {
		t.Fatal(err)
	}
	if got := check(); got.Status != StatusOK || got.Counts["workers"] < 1 {
		t.Fatalf("fresh heartbeat: %+v", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform.worker_heartbeats SET last_seen_at = now() - interval '5 minutes'`); err != nil {
		t.Fatal(err)
	}
	if got := check(); got.Status != StatusFailing || got.ErrorCode != "worker_not_running" {
		t.Fatalf("old heartbeats: %+v", got)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM platform.worker_heartbeats`); err != nil {
		t.Fatal(err)
	}
	if got := check(); got.Status != StatusUnknown || got.ErrorCode != "no_heartbeat" {
		t.Fatalf("no heartbeat: %+v", got)
	}
}
