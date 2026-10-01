package authorization

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

type fakeDirectory struct {
	users  map[string]string
	groups map[string]string
	// groupsOf is what the GroupResolver returns per user.
	groupsOf map[string][]string
}

func (f *fakeDirectory) UserExists(_ context.Context, id string) (bool, error) {
	_, ok := f.users[id]
	return ok, nil
}
func (f *fakeDirectory) DirectoryGroupObserved(_ context.Context, id string) (bool, error) {
	_, ok := f.groups[id]
	return ok, nil
}
func (f *fakeDirectory) DisplayNames(_ context.Context, u, g []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range u {
		if n, ok := f.users[id]; ok {
			out[id] = n
		}
	}
	for _, id := range g {
		if n, ok := f.groups[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}
func (f *fakeDirectory) FindUser(_ context.Context, ref string) (string, bool, error) {
	_, ok := f.users[ref]
	return ref, ok, nil
}
func (f *fakeDirectory) GroupIDsOfUser(_ context.Context, userID string) ([]string, error) {
	return f.groupsOf[userID], nil
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	dir  *fakeDirectory
	svc  *Service
	pfx  string
	cli  Actor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, pfx: "zt-" + hex.EncodeToString(b)}
	f.dir = &fakeDirectory{users: map[string]string{}, groups: map[string]string{}, groupsOf: map[string][]string{}}
	f.svc = NewService(pool, f.dir)
	f.cli = Actor{CLI: json.RawMessage(`{"actor":"cli","osUser":"tester"}`), CorrelationID: f.pfx + "-cli"}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform.role_assignments WHERE role_id IN (SELECT id FROM platform.roles WHERE key LIKE $1 || '%')`,
		`DELETE FROM platform.roles WHERE key LIKE $1 || '%'`,
		`DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`,
	} {
		if _, err := f.pool.Exec(ctx, q, f.pfx); err != nil {
			f.t.Errorf("cleanup: %v", err)
		}
	}
	// Assignments of generated subjects to the built-in role.
	for id := range f.dir.users {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.role_assignments WHERE subject_id = $1`, id)
	}
	for id := range f.dir.groups {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.role_assignments WHERE subject_id = $1`, id)
	}
}

func (f *fixture) newID() string {
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) user(name string) string {
	id := f.newID()
	f.dir.users[id] = name
	return id
}

func (f *fixture) group(name string) string {
	id := f.newID()
	f.dir.groups[id] = name
	return id
}

func (f *fixture) actor(userID, suffix string) Actor {
	return UserActor(userID, f.pfx+"-"+suffix)
}

func (f *fixture) role(suffix string, perms ...string) Role {
	f.t.Helper()
	r, err := f.svc.CreateRole(context.Background(), f.cli, CreateRoleInput{Key: f.pfx + "-" + suffix, Name: "Role " + suffix, Permissions: perms})
	if err != nil {
		f.t.Fatalf("create role: %v", err)
	}
	return r
}

type auditRow struct {
	actor         *string
	action        string
	targetType    string
	targetID      string
	correlationID string
	before, after json.RawMessage
	metadata      map[string]any
}

func (f *fixture) auditRows(targetID string) []auditRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT actor_id::text, action, target_type, target_id, correlation_id, before_data, after_data, metadata
		FROM platform.audit_events WHERE target_id = $1 ORDER BY id`, targetID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var meta []byte
		if err := rows.Scan(&r.actor, &r.action, &r.targetType, &r.targetID, &r.correlationID, &r.before, &r.after, &meta); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal(meta, &r.metadata); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (f *fixture) wantAudit(targetID string, actions ...string) []auditRow {
	f.t.Helper()
	rows := f.auditRows(targetID)
	var got []string
	for _, r := range rows {
		got = append(got, r.action)
		if r.correlationID == "" {
			f.t.Errorf("audit row %s without correlation id", r.action)
		}
	}
	if len(got) != len(actions) {
		f.t.Fatalf("audit actions for %s = %v, want %v", targetID, got, actions)
	}
	for i := range got {
		if got[i] != actions[i] {
			f.t.Fatalf("audit actions for %s = %v, want %v", targetID, got, actions)
		}
	}
	return rows
}

func TestCreateUpdateSetPermissionsDeleteRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.user("Admin")
	actor := f.actor(admin, "a")

	created, err := f.svc.CreateRole(ctx, actor, CreateRoleInput{Key: f.pfx + "-helpdesk", Name: " Helpdesk ", Description: "d", Permissions: []string{"tasks.view", "organization.view", "tasks.view"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Helpdesk" || created.BuiltIn || created.ActiveAssignments != 0 {
		t.Fatalf("created = %+v", created)
	}
	if got := created.Permissions; len(got) != 2 || got[0] != "organization.view" || got[1] != "tasks.view" {
		t.Fatalf("permissions = %v", got)
	}

	if _, err := f.svc.CreateRole(ctx, actor, CreateRoleInput{Key: created.Key, Name: "x"}); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("duplicate key err = %v", err)
	}
	var inv *InvalidError
	for _, in := range []CreateRoleInput{
		{Key: "A", Name: "x"}, {Key: f.pfx + "-n", Name: " "}, {Key: f.pfx + "-n", Name: "x", Description: "a\x00"},
	} {
		if _, err := f.svc.CreateRole(ctx, actor, in); !errors.As(err, &inv) {
			t.Fatalf("CreateRole(%+v) err = %v, want invalid", in, err)
		}
	}
	var unk *UnknownPermissionError
	if _, err := f.svc.CreateRole(ctx, actor, CreateRoleInput{Key: f.pfx + "-bad", Name: "x", Permissions: []string{"no.such"}}); !errors.Is(err, ErrUnknownPermission) || !errors.As(err, &unk) {
		t.Fatalf("unknown permission err = %v", err)
	}

	name := "Service desk"
	updated, err := f.svc.UpdateRole(ctx, actor, created.ID, UpdateRoleInput{Name: &name})
	if err != nil || updated.Name != name || updated.Description != "d" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := f.svc.UpdateRole(ctx, actor, created.ID, UpdateRoleInput{}); !errors.As(err, &inv) {
		t.Fatalf("empty update err = %v", err)
	}

	set, err := f.svc.SetRolePermissions(ctx, actor, created.ID, []string{"tickets.view"})
	if err != nil || len(set.Permissions) != 1 || set.Permissions[0] != "tickets.view" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if _, err := f.svc.SetRolePermissions(ctx, actor, created.ID, []string{"nope"}); !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("set unknown err = %v", err)
	}
	cleared, err := f.svc.SetRolePermissions(ctx, actor, created.ID, nil)
	if err != nil || len(cleared.Permissions) != 0 {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}

	// Deletion is refused while an assignment is active, allowed after revoke.
	u := f.user("Alice")
	as, err := f.svc.AssignRole(ctx, actor, AssignInput{RoleID: created.ID, SubjectType: SubjectUser, SubjectID: u})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteRole(ctx, actor, created.ID); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("delete in use err = %v", err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, actor, as.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteRole(ctx, actor, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.svc.GetRole(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted err = %v", err)
	}
	if err := f.svc.DeleteRole(ctx, actor, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice err = %v", err)
	}
	if _, err := f.svc.GetRole(ctx, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get malformed err = %v", err)
	}

	rows := f.wantAudit(created.ID, "authorization.role.created", "authorization.role.updated", "authorization.role.permissions_changed",
		"authorization.role.permissions_changed", "authorization.role.deleted")
	for _, r := range rows {
		if r.actor == nil || *r.actor != admin || r.targetType != "role" {
			t.Fatalf("audit actor/target = %+v", r)
		}
	}
	if rows[0].before != nil || rows[0].after == nil {
		t.Fatalf("created before/after = %s / %s", rows[0].before, rows[0].after)
	}
	var b, a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rows[1].before, &b)
	_ = json.Unmarshal(rows[1].after, &a)
	if b.Name != "Helpdesk" || a.Name != name {
		t.Fatalf("update before/after = %s / %s", rows[1].before, rows[1].after)
	}
	var pb, pa struct {
		Permissions []string `json:"permissions"`
	}
	_ = json.Unmarshal(rows[2].before, &pb)
	_ = json.Unmarshal(rows[2].after, &pa)
	if len(pb.Permissions) != 2 || len(pa.Permissions) != 1 {
		t.Fatalf("permissions before/after = %s / %s", rows[2].before, rows[2].after)
	}
	if rows[4].before == nil || rows[4].after != nil {
		t.Fatalf("deleted before/after = %s / %s", rows[4].before, rows[4].after)
	}
}

func TestBuiltInRoleIsImmutable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin, err := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	if !admin.BuiltIn || len(admin.Permissions) != len(permissions.Registry) || !sort.StringsAreSorted(admin.Permissions) {
		t.Fatalf("admin role = %+v", admin)
	}
	name := "x"
	if _, err := f.svc.UpdateRole(ctx, f.cli, admin.ID, UpdateRoleInput{Name: &name}); !errors.Is(err, ErrBuiltInRole) {
		t.Fatalf("update err = %v", err)
	}
	if _, err := f.svc.SetRolePermissions(ctx, f.cli, admin.ID, []string{"tasks.view"}); !errors.Is(err, ErrBuiltInRole) {
		t.Fatalf("set permissions err = %v", err)
	}
	if err := f.svc.DeleteRole(ctx, f.cli, admin.ID); !errors.Is(err, ErrBuiltInRole) {
		t.Fatalf("delete err = %v", err)
	}
	if got := f.auditRows(admin.ID); len(got) != 0 {
		t.Fatalf("refused operations wrote audit rows: %d", len(got))
	}
}

func TestAssignAndRevokeAssignment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role := f.role("r1", "tasks.view")
	u := f.user("Alice")
	g := f.group("Helpdesk group")
	admin := f.user("Admin")
	session := f.actor(admin, "s")

	a1, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u})
	if err != nil {
		t.Fatal(err)
	}
	if a1.RoleKey != role.Key || a1.Scope != "global" || a1.SubjectDisplayName != "Alice" || a1.CreatedBy != admin || a1.RevokedAt != nil {
		t.Fatalf("assignment = %+v", a1)
	}
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u}); !errors.Is(err, ErrDuplicateAssignment) {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: f.newID()}); !errors.Is(err, ErrSubjectNotFound) {
		t.Fatalf("unknown user err = %v", err)
	}
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: SubjectDirectoryGroup, SubjectID: u}); !errors.Is(err, ErrSubjectNotFound) {
		t.Fatalf("user id as group err = %v", err)
	}
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: f.newID(), SubjectType: SubjectUser, SubjectID: u}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role err = %v", err)
	}
	var inv *InvalidError
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: "team", SubjectID: u}); !errors.As(err, &inv) {
		t.Fatalf("bad subject type err = %v", err)
	}
	// The CLI may assign too; its audit row has no actor id.
	a2, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: role.ID, SubjectType: SubjectDirectoryGroup, SubjectID: g})
	if err != nil || a2.CreatedBy != "" || a2.SubjectDisplayName != "Helpdesk group" {
		t.Fatalf("cli assignment = %+v, %v", a2, err)
	}
	var createdBy string
	if err := f.pool.QueryRow(ctx, `SELECT created_by->>'actor' FROM platform.role_assignments WHERE id = $1`, a2.ID).Scan(&createdBy); err != nil || createdBy != "cli" {
		t.Fatalf("created_by actor = %q, %v", createdBy, err)
	}

	got, err := f.svc.GetRole(ctx, role.ID)
	if err != nil || got.ActiveAssignments != 2 {
		t.Fatalf("role = %+v, %v", got, err)
	}

	revoked, outcome, err := f.svc.RevokeAssignment(ctx, session, a1.ID, false)
	if err != nil || revoked.RevokedAt == nil || revoked.RevokedBy != admin || outcome.AlreadyRevoked {
		t.Fatalf("revoke = %+v %+v %v", revoked, outcome, err)
	}
	again, outcome, err := f.svc.RevokeAssignment(ctx, session, a1.ID, false)
	if err != nil || !outcome.AlreadyRevoked || again.RevokedAt == nil {
		t.Fatalf("second revoke = %+v %+v %v", again, outcome, err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, session, f.newID(), false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown err = %v", err)
	}
	// Revoked assignments can be assigned again.
	if _, err := f.svc.AssignRole(ctx, session, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u}); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	rows := f.wantAudit(a1.ID, "authorization.role.assigned", "authorization.role.assignment_revoked")
	for _, r := range rows {
		if r.actor == nil || *r.actor != admin || r.targetType != "role_assignment" {
			t.Fatalf("audit row = %+v", r)
		}
	}
	if rows[0].before != nil || rows[0].after == nil || rows[1].before == nil || rows[1].after == nil {
		t.Fatalf("before/after = %s %s / %s %s", rows[0].before, rows[0].after, rows[1].before, rows[1].after)
	}
	cliRows := f.wantAudit(a2.ID, "authorization.role.assigned")
	if cliRows[0].actor != nil || cliRows[0].metadata["actor"] != "cli" || cliRows[0].metadata["osUser"] != "tester" {
		t.Fatalf("cli audit row = %+v", cliRows[0])
	}
}

func TestListAssignments(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role := f.role("lst")
	var ids []string
	for i := 0; i < 3; i++ {
		a, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: f.user("U")})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, f.cli, ids[0], false); err != nil {
		t.Fatal(err)
	}
	active, err := f.svc.ListAssignments(ctx, AssignmentFilter{RoleID: role.ID})
	if err != nil || len(active.Items) != 2 || active.Items[0].ID != ids[2] || active.Items[1].ID != ids[1] || active.Items[0].SubjectDisplayName != "U" {
		t.Fatalf("active = %+v, %v", active, err)
	}
	p1, err := f.svc.ListAssignments(ctx, AssignmentFilter{RoleID: role.ID, IncludeRevoked: true, Page: Page{Limit: 2}})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor != ids[1] {
		t.Fatalf("page1 = %+v, %v", p1, err)
	}
	p2, err := f.svc.ListAssignments(ctx, AssignmentFilter{RoleID: role.ID, IncludeRevoked: true, Page: Page{Limit: 2, Cursor: p1.NextCursor}})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].ID != ids[0] || p2.NextCursor != "" || p2.Items[0].RevokedAt == nil {
		t.Fatalf("page2 = %+v, %v", p2, err)
	}
	if _, err := f.svc.ListAssignments(ctx, AssignmentFilter{Page: Page{Cursor: "x"}}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("bad cursor err = %v", err)
	}
	var inv *InvalidError
	if _, err := f.svc.ListAssignments(ctx, AssignmentFilter{RoleID: "x"}); !errors.As(err, &inv) {
		t.Fatalf("bad role id err = %v", err)
	}
	byUser, err := f.svc.ListAssignments(ctx, AssignmentFilter{SubjectType: SubjectUser, SubjectID: active.Items[0].SubjectID})
	if err != nil || len(byUser.Items) != 1 {
		t.Fatalf("by subject = %+v, %v", byUser, err)
	}
}

func TestLastAdministratorGuard(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin, err := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	var existing int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments WHERE role_id = $1 AND subject_type = 'user' AND revoked_at IS NULL`, admin.ID).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing > 0 {
		t.Skip("database already has active administrator assignments; the guard cannot be exercised deterministically")
	}
	u1, u2, grp := f.user("A1"), f.user("A2"), f.group("Admins")
	a1, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: admin.ID, SubjectType: SubjectUser, SubjectID: u1})
	if err != nil {
		t.Fatal(err)
	}
	// Group assignments are not counted.
	ag, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: admin.ID, SubjectType: SubjectDirectoryGroup, SubjectID: grp})
	if err != nil {
		t.Fatal(err)
	}
	session := f.actor(u1, "g")
	if _, _, err := f.svc.RevokeAssignment(ctx, session, a1.ID, false); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last admin err = %v", err)
	}
	// Revoking the group assignment is always fine.
	if _, _, err := f.svc.RevokeAssignment(ctx, session, ag.ID, false); err != nil {
		t.Fatalf("revoke group: %v", err)
	}
	a2, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: admin.ID, SubjectType: SubjectUser, SubjectID: u2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, session, a1.ID, false); err != nil {
		t.Fatalf("revoke with second admin: %v", err)
	}
	// a2 is last again; only the CLI bypass may revoke it.
	if _, _, err := f.svc.RevokeAssignment(ctx, session, a2.ID, false); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last admin err = %v", err)
	}
	_, outcome, err := f.svc.RevokeAssignment(ctx, f.cli, a2.ID, true)
	if err != nil || !outcome.LastAdministratorBypassed {
		t.Fatalf("bypass = %+v, %v", outcome, err)
	}
	rows := f.wantAudit(a2.ID, "authorization.role.assigned", "authorization.role.assignment_revoked")
	if rows[1].metadata["lastAdministratorGuardBypassed"] != true {
		t.Fatalf("bypass not audited: %+v", rows[1].metadata)
	}
}

func TestRolePermissionsEvaluation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	eval := NewRolePermissions(f.pool, f.dir)

	direct := f.role("direct", "tasks.view", "organization.view")
	viaGroup := f.role("viagroup", "tickets.view")
	legacy := f.role("legacy", "assets.view")
	if _, err := f.pool.Exec(ctx, `INSERT INTO platform.role_permissions(role_id, permission) VALUES ($1, 'retired.permission')`, legacy.ID); err != nil {
		t.Fatal(err)
	}

	u := f.user("Alice")
	child, parent := f.group("Child"), f.group("Parent")
	// The resolver reports the transitive closure; the assignment is on the parent.
	f.dir.groupsOf[u] = []string{child, parent}

	has := func(want ...string) {
		t.Helper()
		got, err := eval.Permissions(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("permissions = %v, want %v", got, want)
		}
		for _, w := range want {
			if _, ok := got[w]; !ok {
				t.Fatalf("permissions = %v, want %v", got, want)
			}
		}
	}

	has()
	direct1, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: direct.ID, SubjectType: SubjectUser, SubjectID: u})
	if err != nil {
		t.Fatal(err)
	}
	has("tasks.view", "organization.view")

	gAssign, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: viaGroup.ID, SubjectType: SubjectDirectoryGroup, SubjectID: parent})
	if err != nil {
		t.Fatal(err)
	}
	has("tasks.view", "organization.view", "tickets.view")

	// Unregistered stored permissions are ignored.
	lAssign, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: legacy.ID, SubjectType: SubjectUser, SubjectID: u})
	if err != nil {
		t.Fatal(err)
	}
	has("tasks.view", "organization.view", "tickets.view", "assets.view")

	// Revoked assignments grant nothing.
	for _, id := range []string{direct1.ID, gAssign.ID, lAssign.ID} {
		if _, _, err := f.svc.RevokeAssignment(ctx, f.cli, id, false); err != nil {
			t.Fatal(err)
		}
	}
	has()

	// Losing the group membership removes the group's permissions immediately.
	if _, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: viaGroup.ID, SubjectType: SubjectDirectoryGroup, SubjectID: parent}); err != nil {
		t.Fatal(err)
	}
	has("tickets.view")
	f.dir.groupsOf[u] = nil
	has()

	if got, err := eval.Permissions(ctx, "not-a-uuid"); err != nil || len(got) != 0 {
		t.Fatalf("malformed user = %v, %v", got, err)
	}
}

func TestAdministratorEvaluationGrantsEveryRegisteredPermission(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin, err := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	u, g := f.user("Root"), f.group("Admin group")
	f.dir.groupsOf[u] = []string{g}
	if _, err := f.svc.AssignRole(ctx, f.cli, AssignInput{RoleID: admin.ID, SubjectType: SubjectDirectoryGroup, SubjectID: g}); err != nil {
		t.Fatal(err)
	}
	got, err := NewRolePermissions(f.pool, f.dir).Permissions(ctx, u)
	if err != nil || len(got) != len(permissions.Registry) {
		t.Fatalf("permissions = %d, %v", len(got), err)
	}
	for _, p := range permissions.Registry {
		if _, ok := got[p.Name]; !ok {
			t.Fatalf("missing %s", p.Name)
		}
	}
}
