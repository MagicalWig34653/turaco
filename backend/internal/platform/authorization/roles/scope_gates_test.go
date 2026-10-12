package roles

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// The binding gates of the F16 L-B security review (docs/product/f16-location-scoped-access-design.md), implemented
// with slice L-C. Gate 6 (unexported Principal scopes) is enforced by the compiler and archcheck.

// Gate 1: a scoped assignment and a scoped revocation lock the scope Location's path (the Location tree in shared
// mode) before the guards expand any scoped holding; a global assignment locks nothing.
func TestScopedOperationsLockTheLocationBeforeTheGuards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	ops := f.role("ops", "tickets.view")
	lead := f.user("Site lead")
	f.assignAt(ops, lead, l.siteA)
	target := f.user("Target")

	f.dir.calls, f.dir.locked = nil, nil
	a, err := f.svc.AssignRole(ctx, f.actor(lead), f.corr, AssignInput{RoleID: ops.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: l.areaA1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.dir.locked, []string{l.areaA1}) || len(f.dir.calls) < 2 || f.dir.calls[0] != "lock" {
		t.Fatalf("assign: locked %v, calls %v (the lock must come first)", f.dir.locked, f.dir.calls)
	}
	f.dir.calls, f.dir.locked = nil, nil
	if _, _, err := f.svc.RevokeAssignment(ctx, f.actor(lead), f.corr, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.dir.locked, []string{l.areaA1}) || f.dir.calls[0] != "lock" {
		t.Fatalf("revoke: locked %v, calls %v", f.dir.locked, f.dir.calls)
	}
	f.dir.locked = nil
	f.assign(ops, target)
	if len(f.dir.locked) != 0 {
		t.Fatalf("a global assignment locked %v", f.dir.locked)
	}
}

// Gate 2: an assignment that would give the subject more than MaxScopedRoots scope Locations is refused, for a User
// (over every assignment that reaches the User) and for a Directory Group; Explain lists only surviving roots.
func TestScopedRootCapIsRefusedAtAssignment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("many", "tickets.view")
	other := f.role("other", "tickets.view")
	u := f.user("Many Locations")
	g := f.group("Many Locations group")
	for _, subject := range []struct{ typ, id string }{{SubjectUser, u}, {SubjectDirectoryGroup, g}} {
		// MaxScopedRoots - 1 assignments of one role and one of another: the subject is at the cap.
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO platform.role_assignments (role_id, subject_type, subject_id, scope, scope_location_id, created_by)
			SELECT $1, $2, $3, 'location', uuidv7(), '{"actor":"cli"}' FROM generate_series(1, $4)`, role.ID, subject.typ, subject.id, MaxScopedRoots-1); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: other.ID, SubjectType: subject.typ, SubjectID: subject.id, ScopeLocationID: l.siteA}); err != nil {
			t.Fatalf("%s: the assignment that reaches the cap: %v", subject.typ, err)
		}
		_, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: other.ID, SubjectType: subject.typ, SubjectID: subject.id, ScopeLocationID: l.siteB})
		if !errors.Is(err, ErrScopedRootsExceeded) {
			t.Fatalf("%s: the assignment beyond the cap: %v", subject.typ, err)
		}
		// Another role at a Location the subject already has adds no root.
		if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: subject.typ, SubjectID: subject.id, ScopeLocationID: l.siteA}); err != nil {
			t.Fatalf("%s: a known Location: %v", subject.typ, err)
		}
		// A global assignment is never counted.
		if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: f.role("global-"+strings.ReplaceAll(subject.typ, "_", "-"), "tickets.manage").ID, SubjectType: subject.typ, SubjectID: subject.id}); err != nil {
			t.Fatalf("%s: global assignment: %v", subject.typ, err)
		}
	}

	var grants []grant
	for i := 0; i < MaxScopedRoots+5; i++ {
		grants = append(grants, grant{roleID: "r", perms: []string{"tickets.view"}, assignmentID: string(rune('a' + i%26)), scope: ScopeLocation,
			scopeLocationID: f.newID()})
	}
	out, r := explain(grants, false, false, scopableForTest)
	if len(r.scoped["tickets.view"]) != MaxScopedRoots+5 || len(out.Scoped) != 1 || len(out.Scoped[0].Roots) != MaxScopedRoots {
		t.Fatalf("explained roots = %d, want %d", len(out.Scoped[0].Roots), MaxScopedRoots)
	}
	if out.Scoped[0].Roots[MaxScopedRoots-1].RootLocationID != r.scoped["tickets.view"][MaxScopedRoots-1] {
		t.Fatal("Explain must list the roots expand keeps (the first ones in id order)")
	}
}

// The cap check of one subject is serialized by a transaction advisory lock, so two concurrent assignments of
// different roles at different Locations cannot both pass at one root below the cap. The test holds the lock and
// shows that an assignment waits for it, then proceeds.
func TestScopedRootCapCheckIsSerializedPerSubject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("serial", "tickets.view")
	u := f.user("Serialized")
	other := f.user("Not serialized")

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "roles.scoped_roots:"+SubjectUser+":"+strings.ToLower(u)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u, ScopeLocationID: l.siteA})
		done <- err
	}()
	// Another subject is not delayed.
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: other, ScopeLocationID: l.siteA}); err != nil {
		t.Fatalf("another subject: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("the assignment did not wait for the subject's cap lock: %v", err)
	case <-time.After(700 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("assignment after the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the assignment did not finish")
	}
}

// Gate 3: dominance treats the target's inert permissions (global-only permissions reached through a scoped
// assignment) like checkGrant does: the actor must hold them globally.
func TestDominanceCoversInertPermissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	g := NewGuards(f.pool, f.dir)
	g.eval.scopable = scopableForTest
	dominance := func(actor, target string) error {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		return g.RequireDominance(ctx, tx, f.actor(actor), target)
	}
	mixed := f.role("mixed", "tickets.view", "organization.view")
	target := f.user("Target with inert permission")
	f.assignAt(mixed, target, l.siteA)

	ops := f.role("ops", "tickets.view")
	scopedOnly := f.user("Scoped actor")
	f.assignAt(ops, scopedOnly, l.siteA)
	if err := dominance(scopedOnly, target); !errors.Is(err, ErrDominanceRequired) {
		t.Fatalf("actor without the inert permission: %v", err)
	}
	inertToo := f.user("Scoped actor with the same inert permission")
	f.assignAt(mixed, inertToo, l.siteA)
	if err := dominance(inertToo, target); !errors.Is(err, ErrDominanceRequired) {
		t.Fatalf("an inert holding of the actor is not a global one: %v", err)
	}
	withGlobal := f.user("Scoped actor with global organization.view")
	f.assignAt(ops, withGlobal, l.siteA)
	f.assign(f.role("orgview", "organization.view"), withGlobal)
	if err := dominance(withGlobal, target); err != nil {
		t.Fatalf("actor holding the inert permission globally: %v", err)
	}
}

// Gate 4: the actor guards run before the scope Location is validated, so an actor who may not assign there learns
// nothing about Location ids (403, never 422); an actor who covers the Location gets 422 for an inactive one.
func TestScopeLocationValidationAfterTheGuards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	ops := f.role("ops", "tickets.view")
	lead := f.user("Site lead")
	f.assignAt(ops, lead, l.siteA)
	target := f.user("Target")
	assign := func(loc string) error {
		_, err := f.svc.AssignRole(ctx, f.actor(lead), f.corr, AssignInput{RoleID: ops.ID, SubjectType: SubjectUser, SubjectID: target, ScopeLocationID: loc})
		return err
	}
	for name, loc := range map[string]string{"unknown": f.newID(), "other Site": l.siteB} {
		if err := assign(loc); errors.Is(err, ErrScopeLocationInvalid) || !(errors.Is(err, ErrRoleNotHeld) || errors.Is(err, ErrGrantExceedsHolder)) {
			t.Errorf("%s Location: %v", name, err)
		}
	}
	f.dir.inactiveLocations[l.areaA1] = true
	if err := assign(l.areaA1); !errors.Is(err, ErrScopeLocationInvalid) {
		t.Errorf("inactive Location inside the actor's scope: %v", err)
	}
}

// Gate 5: scoped assignment audit records snapshot the Location name and path.
func TestScopedAuditSnapshotsTheLocation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.locations()
	role := f.role("snap", "tickets.view")
	u := f.user("Subject")
	a := f.assignAt(role, u, l.areaA1)
	if _, _, err := f.svc.RevokeAssignment(ctx, f.cli, f.corr, a.ID, false); err != nil {
		t.Fatal(err)
	}
	rows := f.wantAudit(a.ID, "authorization.role.assigned", "authorization.role.assignment_revoked")
	want := []any{"loc-" + l.siteA, "loc-" + l.areaA1}
	for i, raw := range []json.RawMessage{rows[0].after, rows[1].before, rows[1].after} {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		path, _ := m["scopeLocationPath"].([]any)
		if m["scopeLocationName"] != "loc-"+l.areaA1 || !slices.Equal(path, want) {
			t.Errorf("record %d: %v", i, m)
		}
	}
	global := f.assign(role, u)
	var after map[string]any
	_ = json.Unmarshal(f.wantAudit(global.ID, "authorization.role.assigned")[0].after, &after)
	if _, named := after["scopeLocationName"]; named {
		t.Errorf("a global assignment has no Location snapshot: %v", after)
	}
}
