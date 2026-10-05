package public

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"testing"
	"time"
)

func TestSyncHealthReadsCompletion(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	provider := "f8c_test_provider"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM endpoints.provider_sync_state WHERE provider=$1`, provider) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO endpoints.provider_sync_state(provider,last_completed_at) VALUES ($1,$2) ON CONFLICT(provider) DO UPDATE SET last_completed_at=$2`, provider, now); err != nil {
		t.Fatal(err)
	}
	list, err := NewHealth(pool).SyncHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range list {
		if v.Provider == provider {
			found = true
			if v.LastCompletedAt == nil || !v.LastCompletedAt.Equal(now) {
				t.Fatalf("completion = %v", v.LastCompletedAt)
			}
		}
	}
	if !found {
		t.Fatal("provider absent")
	}
}
