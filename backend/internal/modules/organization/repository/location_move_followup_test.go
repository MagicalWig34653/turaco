package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
)

func cleanupMoveLocations(f *peopleFix) {
	f.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE payload->>'locationId' IN (SELECT id::text FROM organization.locations WHERE name LIKE $1 || '%')`, f.pfx)
		for i := 0; i < 5; i++ {
			_, _ = f.pool.Exec(ctx, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL)`, f.pfx)
		}
	})
}

// A dry run takes the tree lock in shared mode only: it neither waits for nor blocks a scoped role assignment in
// flight (which holds the shared lock too).
func TestLocationMovePreviewDoesNotBlockScopedAssignments(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	cli := f.caller("")
	cleanupMoveLocations(f)
	siteA, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " Site A"})
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " Site B"})
	if err != nil {
		t.Fatal(err)
	}
	area, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationArea, ParentID: &siteB.ID, Name: f.pfx + " Area"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := f.caller(f.local("Manager").ID)
	subjects := orgpublic.NewAuthorizationSubjects(New(f.pool))

	// An assignment in flight does not delay the preview.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := subjects.LockLocationPath(ctx, tx, siteA.ID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	p, err := f.repo.PreviewLocationMove(short, mgr, area.ID, area.Version, &siteA.ID)
	cancel()
	if err != nil || p.ScopedAssignmentsAffected != nil || !p.Allowed {
		t.Fatalf("preview while an assignment is in flight: %+v %v", p, err)
	}

	// An open dry run does not delay an assignment.
	pv, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pv.Rollback(ctx) }()
	if _, err := f.repo.planLocationMove(ctx, pv, mgr, area.ID, area.Version, &siteA.ID, true); err != nil {
		t.Fatal(err)
	}
	other, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	short, cancel = context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := subjects.LockLocationPath(short, other, siteB.ID); err != nil {
		t.Fatalf("a scoped assignment waited for a dry run: %v", err)
	}
}

// A move that touches more Locations than the guard counts in one call is refused with a typed error, not a 500.
func TestLocationMoveTooLarge(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	cli := f.caller("")
	cleanupMoveLocations(f)
	siteA, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " Site A"})
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " Site B"})
	if err != nil {
		t.Fatal(err)
	}
	big, err := f.repo.CreateLocation(ctx, cli, application.NewLocationInput{Kind: application.LocationArea, ParentID: &siteB.ID, Name: f.pfx + " Big"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO organization.locations (name, kind, parent_location_id)
		SELECT $1 || ' leaf ' || g, 'area', $2::uuid FROM generate_series(1, $3::int) g`, f.pfx, big.ID, application.MaxMoveAffectedLocations); err != nil {
		t.Fatalf("seed leaves: %v", err)
	}
	if _, err := f.repo.PreviewLocationMove(ctx, cli, big.ID, big.Version, &siteA.ID); !errors.Is(err, application.ErrLocationMoveTooLarge) {
		t.Fatalf("preview of a huge move: %v", err)
	}
	if _, err := f.repo.MoveLocation(ctx, cli, big.ID, big.Version, &siteA.ID); !errors.Is(err, application.ErrLocationMoveTooLarge) {
		t.Fatalf("huge move: %v", err)
	}
	if cur, err := f.repo.GetLocation(ctx, big.ID); err != nil || *cur.ParentID != siteB.ID {
		t.Fatalf("the refused move changed the Location: %+v %v", cur, err)
	}
}

// A Location import takes the tree lock before any Location row lock (lock order is tree, then row), so it cannot
// deadlock with a move or a (de)activation that holds the tree lock and then locks a row. The import updates one
// Location (row lock) and then creates another (tree lock); a transaction holding the tree lock and wanting that
// row would deadlock with the old order.
func TestLocationImportTakesTreeLockBeforeRowLocks(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	admin := f.admin("Importer")
	c := f.batchCaller(admin.ID, true, application.PermLocationsManage, application.PermImport)
	cleanupMoveLocations(f)
	existing := f.pfx + "-EX"
	loc, err := f.repo.CreateLocation(ctx, f.caller(""), application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " Existing", Code: &existing})
	if err != nil {
		t.Fatal(err)
	}
	csv := "code,name,parent_code\n" + existing + "," + f.pfx + " Renamed,\n" + f.pfx + "-NEW," + f.pfx + " New,\n"
	p := f.previewCSV(c, application.ImportLocations, "", application.ModeUpsert, csv)
	if p.Batch.Counts["update"] != 1 || p.Batch.Counts["create"] != 1 {
		t.Fatalf("counts = %v, rows %+v", p.Batch.Counts, p.Rows)
	}

	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if err := lockTree(ctx, holder, treeLockLocations); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.repo.ApplyBatch(ctx, c, p.Batch.ID, p.Batch.PreviewHash, 0)
		done <- err
	}()
	// The import must wait for the tree lock without having locked the Location row.
	select {
	case err := <-done:
		t.Fatalf("the import did not wait for the tree lock: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	if _, err := lockLocation(ctx, holder, loc.ID); err != nil {
		t.Fatalf("the tree lock holder could not lock the row (the import holds it): %v", err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("import: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the import did not finish")
	}
}
