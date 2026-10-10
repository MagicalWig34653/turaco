package audit

import (
	"context"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestPurgeBeforeClampsToOneYear(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var pfx string
	if err := pool.QueryRow(ctx, `SELECT 'zp' || substr(uuidv7()::text, 25, 8)`).Scan(&pfx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE action LIKE $1 || '%'`, pfx) })
	ins := func(name string, age time.Duration) {
		if _, err := pool.Exec(ctx, `INSERT INTO platform.audit_events (id, occurred_at, action, target_type, target_id, correlation_id)
			VALUES (uuidv7(), $1, $2, 'x', 'x', 'c')`, time.Now().Add(-age), pfx+"."+name); err != nil {
			t.Fatal(err)
		}
	}
	ins("two_years", 2*365*24*time.Hour)
	ins("hundred_days", 100*24*time.Hour)
	ins("fresh", time.Hour)
	reader := NewReader(pool)
	// The caller asks for a 30 day cutoff; the database function clamps it to 365 days.
	var deleted int64
	for i := 0; i < 5; i++ {
		res, err := reader.PurgeBefore(ctx, time.Now().Add(-30*24*time.Hour), 5000, "purge-test")
		if err != nil {
			t.Fatal(err)
		}
		if res.EffectiveCutoff.After(time.Now().Add(-364 * 24 * time.Hour)) {
			t.Fatalf("effective cutoff not clamped: %v", res.EffectiveCutoff)
		}
		deleted += res.Deleted
		if res.Deleted == 0 {
			break
		}
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action LIKE $1 || '.%'`, pfx).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining = %d (deleted %d), want the 100 day and the fresh event", remaining, deleted)
	}
	var purged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = $1 AND correlation_id = 'purge-test'`, ActionPurged).Scan(&purged); err != nil || purged < 1 {
		t.Fatalf("purge must be audited: %d %v", purged, err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = 'purge-test'`) })
	// A bad batch size is refused by the function.
	if _, err := reader.PurgeBefore(ctx, time.Now(), 0, "x"); err == nil {
		t.Fatal("batch size 0 must fail")
	}
}
