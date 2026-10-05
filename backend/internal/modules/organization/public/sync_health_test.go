package public_test

import (
	"context"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"testing"
	"time"
)

func TestDirectorySyncStatusHidesError(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	provider := "f8c_test_provider"
	now := time.Now().UTC().Truncate(time.Microsecond)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization.directory_sync_runs WHERE provider_key=$1`, provider)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO organization.directory_sync_runs(provider_key,trigger,started_at,finished_at,outcome,error) VALUES ($1,'manual',$2,$3,'failed','private diagnostic')`, provider, now.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	health := orgpublic.NewSyncHealth(repository.New(pool))
	list, err := health.DirectorySyncStatus(ctx, orgpublic.SyncScope{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range list {
		if v.Provider == provider {
			found = true
			if v.LastErrorCode != "sync_failed" || v.LastErrorSummary != "" {
				t.Fatalf("status = %+v", v)
			}
		}
	}
	if !found {
		t.Fatal("provider absent")
	}
	full, err := health.DirectorySyncStatus(ctx, orgpublic.SyncScope{IncludeErrorSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range full {
		if v.Provider == provider && v.LastErrorSummary != "private diagnostic" {
			t.Fatalf("diagnostic missing: %+v", v)
		}
	}
}
