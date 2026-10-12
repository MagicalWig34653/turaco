package repository

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// The Location move guard (ADR-0040, F16 design L10, slice L-C): a move that changes what active Location-scoped role
// assignments cover needs a platform administrator or the CLI, the dry run reports the count first, the move is
// audited with the count and announced to the other administrators, scoped assignments in flight block a move, and
// access follows the new tree on the next evaluation.
func TestLocationMoveGuard(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	cli := f.caller("")
	// Registered first, so it runs after the user and role cleanups have removed the assignments.
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE payload->>'locationId' IN (SELECT id::text FROM organization.locations WHERE name LIKE $1 || '%')`, f.pfx)
		for i := 0; i < 5; i++ {
			_, _ = f.pool.Exec(ctx, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL)`, f.pfx)
		}
	})
	loc := func(name string, parent *application.Location) application.Location {
		t.Helper()
		in := application.NewLocationInput{Kind: application.LocationSite, Name: f.pfx + " " + name}
		if parent != nil {
			in.Kind, in.ParentID = application.LocationArea, &parent.ID
		}
		l, err := f.repo.CreateLocation(ctx, cli, in)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return l
	}
	reload := func(l application.Location) application.Location {
		t.Helper()
		cur, err := f.repo.GetLocation(ctx, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cur
	}
	siteA := loc("Site A", nil)
	a1 := loc("A1", &siteA)
	a2 := loc("A2", &a1)
	siteB := loc("Site B", nil)
	b1 := loc("B1", &siteB)
	b1a := loc("B1a", &b1)
	siteC := loc("Site C", nil)

	manager := f.local("Location manager", "organization.locations.manage")
	holder := f.local("Site lead")
	admin := f.admin("Admin")
	other := f.admin("Other admin")
	role, err := f.roles.CreateRole(ctx, f.cli, "corr-"+f.pfx, roles.CreateRoleInput{Key: f.pfx + "-site-lead", Name: f.pfx + " Site lead",
		Permissions: []string{"tickets.view"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.role_assignments WHERE role_id = $1`, role.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.roles WHERE id = $1`, role.ID)
	})
	for _, at := range []string{siteA.ID, b1a.ID} {
		if _, err := f.roles.AssignRole(ctx, f.cli, "corr-"+f.pfx, roles.AssignInput{RoleID: role.ID, SubjectType: roles.SubjectUser,
			SubjectID: holder.ID, ScopeLocationID: at}); err != nil {
			t.Fatalf("scoped assignment at %s: %v", at, err)
		}
	}
	mgr := f.caller(manager.ID)

	// Pulling Site B's area below Site A, where a scoped assignment exists, needs an administrator; the dry run says
	// so first (the assignment at Site A gains B1, the one at B1a moves with it).
	preview, err := f.repo.PreviewLocationMove(ctx, mgr, b1.ID, b1.Version, &siteA.ID)
	if err != nil || preview != (application.LocationMovePreview{ScopedAssignmentsAffected: 2, AdministratorRequired: true, Allowed: false}) {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := f.repo.MoveLocation(ctx, mgr, b1.ID, b1.Version, &siteA.ID); !errors.Is(err, application.ErrLocationMoveRequiresAdministrator) {
		t.Fatalf("manager pulls another Site's area below a scoped Site: %v", err)
	}
	if cur := reload(b1); cur.Version != b1.Version || *cur.ParentID != siteB.ID {
		t.Fatalf("the refused move changed the Location: %+v", cur)
	}
	// Moving a subtree that carries a scoped assignment to a Site without any is guarded as well.
	if _, err := f.repo.MoveLocation(ctx, mgr, b1.ID, b1.Version, &siteC.ID); !errors.Is(err, application.ErrLocationMoveRequiresAdministrator) {
		t.Fatalf("manager moves a subtree with a scoped assignment: %v", err)
	}
	// A move below a common ancestor changes nothing for the assignment there: allowed, and audited with zero.
	if p, err := f.repo.PreviewLocationMove(ctx, mgr, a2.ID, a2.Version, &siteA.ID); err != nil || p.ScopedAssignmentsAffected != 0 || p.AdministratorRequired || !p.Allowed {
		t.Fatalf("preview within the Site: %+v %v", p, err)
	}
	if _, err := f.repo.MoveLocation(ctx, mgr, a2.ID, a2.Version, &siteA.ID); err != nil {
		t.Fatalf("move within the scoped Site: %v", err)
	}
	if got := f.movedMeta(a2.ID); got != "0" {
		t.Errorf("audit of an unguarded move: scopedAssignmentsAffected = %q", got)
	}

	// Access follows the tree on the next evaluation.
	eval := roles.NewEvaluator(f.pool, orgpublic.NewAuthorizationSubjects(New(f.pool)))
	covers := func(id string) bool {
		t.Helper()
		grants, err := eval.RoleGrants(ctx, holder.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range grants {
			if g.RoleID == role.ID && slices.Contains(g.Locations, id) {
				return true
			}
		}
		return false
	}
	if covers(b1.ID) {
		t.Fatal("B1 is covered before the move")
	}

	// A scoped assignment in flight holds the tree lock: the move waits for it (and then counts it).
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path, err := orgpublic.NewAuthorizationSubjects(New(f.pool)).LockLocationPath(ctx, tx, siteA.ID)
	if err != nil || len(path) != 1 || path[0].ID != siteA.ID {
		t.Fatalf("lock path: %+v %v", path, err)
	}
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err = f.repo.MoveLocation(short, f.caller(admin.ID), b1.ID, b1.Version, &siteA.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the move did not wait for the scoped assignment transaction: %v", err)
	}
	_ = tx.Rollback(ctx)

	moved, err := f.repo.MoveLocation(ctx, f.caller(admin.ID), b1.ID, b1.Version, &siteA.ID)
	if err != nil || *moved.ParentID != siteA.ID {
		t.Fatalf("administrator move: %+v %v", moved, err)
	}
	if !covers(b1.ID) {
		t.Error("B1 is not covered by the Site A assignment after the move")
	}
	if got := f.movedMeta(b1.ID); got != "2" {
		t.Errorf("audit: scopedAssignmentsAffected = %q", got)
	}
	var ev events.OutboxEvent
	if err := f.pool.QueryRow(ctx, `SELECT id::text, event_type, actor_id::text, payload FROM platform.outbox_events
		WHERE event_type = $1 AND payload->>'locationId' = $2`, application.EventScopedLocationMoved, b1.ID).
		Scan(&ev.ID, &ev.EventType, &ev.ActorID, &ev.Payload); err != nil {
		t.Fatalf("ScopedLocationMoved: %v", err)
	}

	// The other administrators are notified; the actor is not.
	n := &recordingNotifier{}
	consumers := application.NewLocationMoveConsumers(f.repo, roles.NewGuards(f.pool, orgpublic.NewAuthorizationSubjects(New(f.pool))), n)
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return consumers.OnScopedLocationMoved(ctx, tx, ev) }); err != nil {
		t.Fatal(err)
	}
	var toOther, toActor bool
	for _, in := range n.intents {
		if in.Category != application.NotificationScopedLocationMoved || in.LinkID != b1.ID || in.Params["count"] != 2 || in.Params["title"] != b1.Name {
			t.Errorf("intent: %+v", in)
		}
		toOther = toOther || in.RecipientUserID == other.ID
		toActor = toActor || in.RecipientUserID == admin.ID
	}
	if !toOther || toActor {
		t.Errorf("recipients: other administrator %v, actor %v", toOther, toActor)
	}

	// The CLI is the operator and may make a guarded move.
	if _, err := f.repo.MoveLocation(ctx, cli, b1.ID, moved.Version, &siteB.ID); err != nil {
		t.Fatalf("CLI move: %v", err)
	}
	if covers(b1.ID) {
		t.Error("B1 is still covered by the Site A assignment after it moved back")
	}
	// Without guards the move fails closed.
	if _, err := New(f.pool).MoveLocation(ctx, cli, b1.ID, reload(b1).Version, &siteC.ID); !errors.Is(err, application.ErrNoGuards) {
		t.Errorf("move without guards: %v", err)
	}
}

// movedMeta returns scopedAssignmentsAffected of the latest organization.location.moved audit record of the Location.
func (f *peopleFix) movedMeta(locationID string) string {
	f.t.Helper()
	var v *string
	if err := f.pool.QueryRow(context.Background(), `SELECT metadata->>'scopedAssignmentsAffected' FROM platform.audit_events
		WHERE target_id = $1 AND action = 'organization.location.moved' ORDER BY id DESC LIMIT 1`, locationID).Scan(&v); err != nil {
		f.t.Fatalf("moved audit: %v", err)
	}
	if v == nil {
		return ""
	}
	return *v
}
