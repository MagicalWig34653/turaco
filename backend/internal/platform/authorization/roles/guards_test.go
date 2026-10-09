package roles

import (
	"context"
	"errors"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// assign gives a role to a user through the CLI actor (no guards relative to an actor).
func (f *fixture) assign(role Role, userID string) Assignment {
	f.t.Helper()
	a, err := f.svc.AssignRole(context.Background(), f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: userID})
	if err != nil {
		f.t.Fatalf("assign %s: %v", role.Key, err)
	}
	return a
}

// delegate is a non-administrator who manages roles: it holds the high-risk permission platform.roles.manage and
// the operational permissions tickets.view and tasks.view (through two roles, so "holding a role" and "holding the
// permissions" can be told apart).
type delegate struct {
	id        string
	mgr, ops  Role
	opsViaCli Assignment
}

func (f *fixture) delegate() delegate {
	d := delegate{id: f.user("Delegate")}
	d.mgr = f.role("mgr", "platform.roles.manage")
	d.ops = f.role("ops", "tickets.view", "tasks.view")
	f.assign(d.mgr, d.id)
	d.opsViaCli = f.assign(d.ops, d.id)
	return d
}

func TestEscalationThroughRoleCreationAndEditing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d := f.delegate()
	a := f.actor(d.id)

	// Granting what the actor does not hold, and any high-risk permission, is refused.
	if _, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-esc1", Name: "x", Permissions: []string{"tickets.manage"}}); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("create with unheld permission err = %v", err)
	}
	if _, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-esc2", Name: "x", Permissions: []string{"platform.roles.manage"}}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("create with high-risk permission err = %v", err)
	}
	// What the actor holds may be delegated.
	own, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-own", Name: "x", Permissions: []string{"tickets.view"}})
	if err != nil {
		t.Fatalf("create with held permission: %v", err)
	}
	// Editing: adding an unheld permission, adding a high-risk one.
	if _, err := f.svc.SetRolePermissions(ctx, a, f.corr, own.ID, SetRolePermissionsInput{Permissions: []string{"tickets.view", "tickets.manage"}, ExpectedVersion: own.Version}); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("edit adding unheld permission err = %v", err)
	}
	if _, err := f.svc.SetRolePermissions(ctx, a, f.corr, own.ID, SetRolePermissionsInput{Permissions: []string{"platform.admin"}, ExpectedVersion: own.Version}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("edit adding high-risk permission err = %v", err)
	}
	// Editing a role the actor holds so that it gains a permission is the self-escalation route: refused even for
	// a permission the actor holds through another role.
	f.assign(own, d.id)
	if _, err := f.svc.SetRolePermissions(ctx, a, f.corr, own.ID, SetRolePermissionsInput{Permissions: []string{"tickets.view", "tasks.view"}, ExpectedVersion: own.Version}); !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("self edit err = %v", err)
	}
	// Cloning and templates run through the same ceiling.
	if _, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-clone", Name: "x", CloneFromRoleID: d.mgr.ID}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("clone of a high-risk role err = %v", err)
	}
	if _, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-tmpl", Name: "x", TemplateKey: "it-specialist"}); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("template beyond the actor err = %v", err)
	}
	if _, err := f.svc.CreateRole(ctx, a, f.corr, CreateRoleInput{Key: f.pfx + "-remote", Name: "x", TemplateKey: "remote-support-attended"}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("remote support template by a delegate err = %v", err)
	}
	// An administrator may.
	admin := f.user("Admin")
	f.makeAdmin(admin)
	r, err := f.svc.CreateRole(ctx, f.actor(admin), f.corr, CreateRoleInput{Key: f.pfx + "-remote", Name: "Remote support", TemplateKey: "remote-support-attended"})
	if err != nil || r.TemplateKey != "remote-support-attended" || r.TemplateVersion != 1 {
		t.Fatalf("administrator creates the opt-in template: %+v %v", r, err)
	}
	f.wantAudit(r.ID, "authorization.role.created", "authorization.role.created_from_template")
}

func TestAssignmentRulesRoleHoldingSelfAndRemoval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d := f.delegate()
	a := f.actor(d.id)
	alice := f.user("Alice")
	other := f.role("other", "tickets.view")

	// The delegate holds "ops": it may assign it, but not a role it does not hold, whatever the permissions.
	as, err := f.svc.AssignRole(ctx, a, f.corr, AssignInput{RoleID: d.ops.ID, SubjectType: SubjectUser, SubjectID: alice})
	if err != nil {
		t.Fatalf("assign a held role: %v", err)
	}
	if _, err := f.svc.AssignRole(ctx, a, f.corr, AssignInput{RoleID: other.ID, SubjectType: SubjectUser, SubjectID: alice}); !errors.Is(err, ErrRoleNotHeld) {
		t.Fatalf("assign a role that is not held (permissions are held) err = %v", err)
	}
	// A high-risk role is held but cannot be handed on.
	if _, err := f.svc.AssignRole(ctx, a, f.corr, AssignInput{RoleID: d.mgr.ID, SubjectType: SubjectUser, SubjectID: alice}); !errors.Is(err, ErrHighRiskNeedsAdministrator) {
		t.Fatalf("assign a high-risk role err = %v", err)
	}
	// Nobody changes their own assignments, and a group they belong to is their own as well.
	self := f.role("selfrole", "tickets.view")
	f.assign(self, d.id)
	if _, err := f.svc.AssignRole(ctx, a, f.corr, AssignInput{RoleID: self.ID, SubjectType: SubjectUser, SubjectID: d.id}); !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("assign to self err = %v", err)
	}
	g := f.group("Delegate's group")
	f.dir.groupsOf[d.id] = []string{g}
	if _, err := f.svc.AssignRole(ctx, a, f.corr, AssignInput{RoleID: d.ops.ID, SubjectType: SubjectDirectoryGroup, SubjectID: g}); !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("assign to own group err = %v", err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, a, f.corr, d.opsViaCli.ID, false); !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("revoke own assignment err = %v", err)
	}
	// Even a platform administrator does not assign to themselves (a second administrator or the CLI does).
	admin := f.user("Admin")
	f.makeAdmin(admin)
	if _, err := f.svc.AssignRole(ctx, f.actor(admin), f.corr, AssignInput{RoleID: other.ID, SubjectType: SubjectUser, SubjectID: admin}); !errors.Is(err, ErrSelfAssignment) {
		t.Fatalf("administrator assigns to self err = %v", err)
	}
	// Removal ceiling: revoking a role whose permissions the actor lacks is refused, one it holds is allowed.
	heavy := f.role("heavy", "tickets.manage")
	bob := f.user("Bob")
	heavyAssign := f.assign(heavy, bob)
	if _, _, err := f.svc.RevokeAssignment(ctx, a, f.corr, heavyAssign.ID, false); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("revoke beyond the ceiling err = %v", err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, a, f.corr, as.ID, false); err != nil {
		t.Fatalf("revoke within the ceiling: %v", err)
	}
	if _, _, err := f.svc.RevokeAssignment(ctx, f.actor(admin), f.corr, heavyAssign.ID, false); err != nil {
		t.Fatalf("administrator revokes: %v", err)
	}
	// Stripping permissions from a role with holders needs the permissions as well.
	two := f.role("two", "tickets.view", "tickets.manage")
	f.assign(two, bob)
	if _, err := f.svc.SetRolePermissions(ctx, a, f.corr, two.ID, SetRolePermissionsInput{Permissions: []string{"tickets.view"}, ExpectedVersion: two.Version}); !errors.Is(err, ErrGrantExceedsHolder) {
		t.Fatalf("strip beyond the ceiling err = %v", err)
	}
}

func TestAdministratorAssignmentsNeverExpire(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	builtIn, err := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	u := f.user("Future admin")
	soon := time.Now().Add(time.Hour)
	// Not even the CLI.
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: builtIn.ID, SubjectType: SubjectUser, SubjectID: u, ExpiresAt: &soon}); !errors.Is(err, ErrAdminNoExpiry) {
		t.Fatalf("expiring administrator assignment err = %v", err)
	}
	// And the database refuses it as well.
	if _, err := f.pool.Exec(ctx, `INSERT INTO platform.role_assignments(role_id, subject_type, subject_id, created_by, expires_at)
		VALUES ($1, 'user', $2, '{"actor":"x"}', now() + interval '1 hour')`, builtIn.ID, u); err == nil {
		t.Fatal("the database accepted an expiring administrator assignment")
	}
	// A past or too distant expiry is invalid.
	role := f.role("exp", "tickets.view")
	past := time.Now().Add(-time.Minute)
	var inv *InvalidError
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u, ExpiresAt: &past}); !errors.As(err, &inv) {
		t.Fatalf("past expiry err = %v", err)
	}
	risky := f.role("riskyexp", "changes.approve")
	far := time.Now().AddDate(0, 0, 400)
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: risky.ID, SubjectType: SubjectUser, SubjectID: u, ExpiresAt: &far}); !errors.As(err, &inv) {
		t.Fatalf("too distant expiry for a high-risk role err = %v", err)
	}
	near := time.Now().AddDate(0, 0, 30)
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: risky.ID, SubjectType: SubjectUser, SubjectID: u, ExpiresAt: &near}); err != nil {
		t.Fatalf("30 days for a high-risk role: %v", err)
	}
}

func TestExpiredAssignmentsGrantNothingAndAreRevokedByTheJob(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role := f.role("temp", "tickets.view")
	u := f.user("Temp")
	soon := time.Now().Add(time.Hour)
	a, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u, ExpiresAt: &soon})
	if err != nil || a.ExpiresAt == nil {
		t.Fatalf("assign with expiry: %+v %v", a, err)
	}
	eval := NewEvaluator(f.pool, f.dir)
	if p, _ := eval.Permissions(ctx, u); len(p) != 1 {
		t.Fatalf("permissions before expiry: %v", p)
	}
	// Let the assignment lapse (the check constraint wants expires_at after created_at).
	if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := eval.Permissions(ctx, u); len(p) != 0 {
		t.Fatalf("an expired assignment still grants: %v", p)
	}
	page, err := f.svc.ListAssignments(ctx, AssignmentFilter{SubjectID: u})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("active list shows an expired assignment: %+v %v", page, err)
	}
	if got, _ := f.svc.GetRole(ctx, role.ID); got.ActiveAssignments != 0 {
		t.Fatalf("expired assignment counted as active: %d", got.ActiveAssignments)
	}
	n, err := ExpireAssignments(ctx, f.pool, time.Now())
	if err != nil || n < 1 {
		t.Fatalf("expire job: %d %v", n, err)
	}
	rows := f.auditRows(a.ID)
	if len(rows) != 2 || rows[1].action != "authorization.role.assignment_expired" || rows[1].actor != nil || rows[1].metadata["actor"] != "access-expiry" {
		t.Fatalf("audit of the expiry: %+v", rows)
	}
	// Idempotent, and a new assignment is possible.
	if n, err := ExpireAssignments(ctx, f.pool, time.Now()); err != nil {
		t.Fatalf("second run: %d %v", n, err)
	}
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u}); err != nil {
		t.Fatalf("assign again: %v", err)
	}
	// A lapsed but not yet revoked assignment does not block a new one either.
	u2 := f.user("Temp2")
	b, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u2, ExpiresAt: &soon})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectUser, SubjectID: u2}); err != nil {
		t.Fatalf("assign over a lapsed assignment: %v", err)
	}
}

func TestLocalAccountsNeverHoldHighRiskPermissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	local := f.user("Local")
	if _, err := f.pool.Exec(ctx, `INSERT INTO platform.local_credentials(user_id, login_name, kind, enabled) VALUES ($1, $2, 'local', false)`,
		local, "local."+f.pfx); err != nil {
		t.Fatal(err)
	}
	risky := f.role("high", "changes.approve", "tickets.view")
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: risky.ID, SubjectType: SubjectUser, SubjectID: local}); !errors.Is(err, ErrLocalAccountHighRisk) {
		t.Fatalf("high-risk role to a local account (CLI) err = %v", err)
	}
	builtIn, _ := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: builtIn.ID, SubjectType: SubjectUser, SubjectID: local}); !errors.Is(err, ErrLocalAccountHighRisk) {
		t.Fatalf("a local account can never be a platform administrator: %v", err)
	}
	safe := f.role("safe", "tickets.view")
	f.assign(safe, local)
	// Adding a high-risk permission to a role a local account holds is refused.
	if _, err := f.svc.SetRolePermissions(ctx, f.cli, f.corr, safe.ID, SetRolePermissionsInput{Permissions: []string{"tickets.view", "changes.approve"}, ExpectedVersion: safe.Version}); !errors.Is(err, ErrLocalAccountHighRisk) {
		t.Fatalf("high-risk permission into a local account's role err = %v", err)
	}
	// Second line: a high-risk role that was assigned before the credential existed stops granting it.
	later := f.user("Later local")
	f.assign(risky, later)
	eval := NewEvaluator(f.pool, f.dir)
	before, _ := eval.Permissions(ctx, later)
	if _, ok := before["changes.approve"]; !ok {
		t.Fatalf("setup: the directory account should hold changes.approve: %v", before)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO platform.local_credentials(user_id, login_name, kind, enabled) VALUES ($1, $2, 'local', false)`,
		later, "local2."+f.pfx); err != nil {
		t.Fatal(err)
	}
	after, _ := eval.Permissions(ctx, later)
	if _, ok := after["changes.approve"]; ok {
		t.Fatal("a local account holds a high-risk permission")
	}
	if _, ok := after["tickets.view"]; !ok {
		t.Fatalf("normal permissions must stay: %v", after)
	}
	ex, _ := eval.Explain(ctx, later)
	if !ex.LocalAccount || !slices.Contains(ex.Excluded, "changes.approve") {
		t.Fatalf("explanation: %+v", ex)
	}
}

func TestSeparationOfDutiesNeedsAcknowledgement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in := CreateRoleInput{Key: f.pfx + "-sod", Name: "Both", Permissions: []string{"security.manage", "security.accept_risk"}}
	_, err := f.svc.CreateRole(ctx, f.cli, f.corr, in)
	var sod *SoDRequiredError
	if !errors.Is(err, ErrSoDAcknowledgementRequired) || !errors.As(err, &sod) || !slices.Equal(sod.Rules, []string{"security_manage_accept_risk"}) {
		t.Fatalf("without acknowledgement: %v", err)
	}
	in.Acknowledgement = Acknowledgement{Rules: []string{"security_manage_accept_risk"}}
	if _, err := f.svc.CreateRole(ctx, f.cli, f.corr, in); !errors.Is(err, ErrSoDAcknowledgementRequired) {
		t.Fatalf("without a reason: %v", err)
	}
	in.Acknowledgement = Acknowledgement{Rules: []string{"changes_manage_approve"}, Reason: "wrong rule"}
	if _, err := f.svc.CreateRole(ctx, f.cli, f.corr, in); !errors.Is(err, ErrSoDAcknowledgementRequired) {
		t.Fatalf("with another rule: %v", err)
	}
	in.Acknowledgement = Acknowledgement{Rules: []string{"security_manage_accept_risk"}, Reason: "small team, reviewed monthly"}
	r, err := f.svc.CreateRole(ctx, f.cli, f.corr, in)
	if err != nil {
		t.Fatalf("with acknowledgement: %v", err)
	}
	var acked int
	for _, row := range f.auditRows("pending") {
		if row.action == "authorization.role.sod_acknowledged" && row.correlationID == f.corr {
			acked++
		}
	}
	if acked == 0 {
		t.Fatal("the acknowledgement was not audited")
	}
	// Assignment: combining roles creates a conflict the assignment must acknowledge.
	u := f.user("Duo")
	f.assign(f.role("a", "changes.manage"), u)
	approve := f.role("b", "changes.approve")
	_, err = f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: approve.ID, SubjectType: SubjectUser, SubjectID: u})
	if !errors.Is(err, ErrSoDAcknowledgementRequired) {
		t.Fatalf("assignment conflict: %v", err)
	}
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: approve.ID, SubjectType: SubjectUser, SubjectID: u,
		Acknowledgement: Acknowledgement{Rules: []string{"changes_manage_approve"}, Reason: "temporary cover"}}); err != nil {
		t.Fatalf("acknowledged assignment: %v", err)
	}
	ep, err := f.svc.EffectivePermissionsOf(ctx, u)
	if err != nil || len(ep.Warnings) != 1 || ep.Warnings[0].Key != "changes_manage_approve" {
		t.Fatalf("effective warnings: %+v %v", ep.Warnings, err)
	}
	_ = r
}

func TestTemplatesRegistry(t *testing.T) {
	if problems := validateTemplates(); len(problems) > 0 {
		t.Fatalf("template registry: %v", problems)
	}
	for _, key := range []string{"first-level-support", "it-specialist", "team-lead", "security-analyst", "infrastructure-engineer",
		"vendor-restricted", "employee-plus", "remote-support-attended"} {
		if _, ok := TemplateByKey(key); !ok {
			t.Errorf("template %s is missing", key)
		}
	}
	// The registry test would catch a bad template: prove that on a broken copy.
	saved := templates
	t.Cleanup(func() { templates = saved })
	templates = append(slices.Clone(saved), Template{Key: "bad", Version: 1, Permissions: []string{"platform.roles.manage", "no.such", "changes.approve"}})
	if len(validateTemplates()) < 3 {
		t.Fatal("the registry test does not detect broken templates")
	}
}

func TestVersionsAreRequiredAndChecked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.role("ver", "tickets.view")
	name := "Renamed"
	var inv *InvalidError
	if _, err := f.svc.UpdateRole(ctx, f.cli, f.corr, r.ID, UpdateRoleInput{Name: &name}); !errors.As(err, &inv) {
		t.Fatalf("missing expectedVersion: %v", err)
	}
	if _, err := f.svc.UpdateRole(ctx, f.cli, f.corr, r.ID, UpdateRoleInput{Name: &name, ExpectedVersion: r.Version + 1}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	u, err := f.svc.UpdateRole(ctx, f.cli, f.corr, r.ID, UpdateRoleInput{Name: &name, ExpectedVersion: r.Version})
	if err != nil || u.Version != r.Version+1 {
		t.Fatalf("update: %+v %v", u, err)
	}
	if _, err := f.svc.SetRolePermissions(ctx, f.cli, f.corr, r.ID, SetRolePermissionsInput{Permissions: []string{}, ExpectedVersion: r.Version}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale permissions version: %v", err)
	}
	if err := f.svc.DeleteRole(ctx, f.cli, f.corr, r.ID, r.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := f.svc.DeleteRole(ctx, f.cli, f.corr, r.ID, 0); !errors.As(err, &inv) {
		t.Fatalf("missing delete version: %v", err)
	}
}

func TestPermissionsChangedAuditCarriesTheDiff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r, err := f.svc.CreateRole(ctx, f.cli, f.corr, CreateRoleInput{Key: f.pfx + "-diff", Name: "Diff", TemplateKey: "vendor-restricted"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Permissions, []string{"tasks.work"}) || r.TemplateKey != "vendor-restricted" {
		t.Fatalf("from template: %+v", r)
	}
	r2, err := f.svc.SetRolePermissions(ctx, f.cli, f.corr, r.ID, SetRolePermissionsInput{Permissions: []string{"tasks.view", "changes.approve"}, ExpectedVersion: r.Version})
	if err != nil {
		t.Fatal(err)
	}
	rows := f.auditRows(r.ID)
	last := rows[len(rows)-1]
	if last.action != "authorization.role.permissions_changed" || last.metadata["templateKey"] != "vendor-restricted" {
		t.Fatalf("audit: %+v", last)
	}
	added, _ := last.metadata["added"].([]any)
	removed, _ := last.metadata["removed"].([]any)
	byRisk, _ := last.metadata["addedByRisk"].(map[string]any)
	if len(added) != 2 || len(removed) != 1 || byRisk["high"] != float64(1) {
		t.Fatalf("diff metadata: %+v", last.metadata)
	}
	// The template never updates the role; the template view shows the drift.
	views, err := f.svc.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range views {
		for _, tr := range v.Roles {
			if tr.RoleID == r2.ID {
				found = true
				if !slices.Equal(tr.Missing, []string{"tasks.work"}) || len(tr.Extra) != 2 {
					t.Fatalf("drift: %+v", tr)
				}
			}
		}
	}
	if !found {
		t.Fatal("role not listed under its template")
	}
}

// Explain and Permissions must agree for every combination of direct and group roles, expiry and local flag.
func TestExplainAgreesWithEvaluate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(7))
	pool := []string{"tickets.view", "tickets.manage", "tasks.view", "tasks.work", "changes.approve", "changes.manage", "assets.view",
		"platform.roles.manage", "remote_access.start_attended", "knowledge.view", "security.manage", "nope.unregistered"}
	eval := NewEvaluator(f.pool, f.dir)
	builtIn, _ := f.svc.GetRoleByKey(ctx, AdministratorRoleKey)
	for round := 0; round < 25; round++ {
		u := f.user("U")
		grp := f.group("G")
		f.dir.groupsOf[u] = []string{grp}
		for i := 0; i < 1+rng.Intn(3); i++ {
			var perms []string
			for _, p := range pool {
				if rng.Intn(3) == 0 {
					perms = append(perms, p)
				}
			}
			// Unregistered names cannot be stored through the service; insert the row directly like an old release.
			var clean []string
			for _, p := range perms {
				if p != "nope.unregistered" {
					clean = append(clean, p)
				}
			}
			r := f.role("x"+f.newID()[24:], clean...)
			if slices.Contains(perms, "nope.unregistered") {
				if _, err := f.pool.Exec(ctx, `INSERT INTO platform.role_permissions(role_id, permission) VALUES ($1, 'nope.unregistered')`, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			subject, typ := u, SubjectUser
			if rng.Intn(2) == 0 {
				subject, typ = grp, SubjectDirectoryGroup
			}
			a, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: r.ID, SubjectType: typ, SubjectID: subject,
				Acknowledgement: Acknowledgement{Rules: allSoDKeys(), Reason: "test"}})
			if err != nil && !errors.Is(err, ErrDuplicateAssignment) {
				t.Fatalf("assign: %v", err)
			}
			if err == nil && rng.Intn(4) == 0 {
				// Lapsed assignment: must be invisible to both.
				if _, err := f.pool.Exec(ctx, `UPDATE platform.role_assignments SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute' WHERE id = $1`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		if rng.Intn(5) == 0 {
			f.assign(Role{ID: builtIn.ID, Key: AdministratorRoleKey}, u)
		}
		if rng.Intn(3) == 0 {
			if _, err := f.pool.Exec(ctx, `INSERT INTO platform.local_credentials(user_id, login_name, kind) VALUES ($1, $2, 'local')`, u, "local."+f.newID()[:20]); err != nil {
				t.Fatal(err)
			}
		}
		set, err := eval.Permissions(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		ex, err := eval.Explain(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range ex.Permissions {
			got = append(got, p.Name)
			if len(p.GrantedBy) == 0 {
				t.Fatalf("permission %s without a grant path", p.Name)
			}
		}
		var want []string
		for p := range set {
			want = append(want, p)
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("round %d: explain %v != evaluate %v", round, got, want)
		}
	}
}

func TestHoldersAndMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	role := f.role("holders", "knowledge.manage")
	direct := f.user("Direct")
	grouped := f.user("Grouped")
	grp := f.group("Staff")
	f.dir.groupsOf[grouped] = []string{grp}
	f.assign(role, direct)
	if _, err := f.svc.AssignRole(ctx, f.cli, f.corr, AssignInput{RoleID: role.ID, SubjectType: SubjectDirectoryGroup, SubjectID: grp}); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.Holders(ctx, "knowledge.manage", Page{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, h := range page.Items {
		if h.RoleID == role.ID {
			subjects = append(subjects, h.SubjectID)
		}
	}
	slices.Sort(subjects)
	want := []string{direct, grp}
	slices.Sort(want)
	if !slices.Equal(subjects, want) {
		t.Fatalf("holders %v, want %v", subjects, want)
	}
	if _, err := f.svc.Holders(ctx, "no.such", Page{}); !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("unknown permission: %v", err)
	}
	m, err := f.svc.MembersOf(ctx, role.ID)
	if err != nil || len(m.Items) != 2 || m.Capped {
		t.Fatalf("members: %+v %v", m, err)
	}
	sources := map[string]string{}
	for _, x := range m.Items {
		sources[x.UserID] = x.Source
	}
	if sources[direct] != "direct" || sources[grouped] != SubjectDirectoryGroup {
		t.Fatalf("sources: %v", sources)
	}
	if _, err := f.svc.MembersOf(ctx, f.newID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role: %v", err)
	}
}

// Guards used by the Organization module: dominance and the last administrator.
func TestDominanceAndLastAdministratorGuards(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	guards := NewGuards(f.pool, f.dir)
	lead := f.user("Lead")
	staff := f.user("Staff")
	admin := f.user("Admin")
	f.assign(f.role("lead", "tickets.manage", "tasks.manage", "organization.users.manage"), lead)
	f.assign(f.role("staff", "tasks.manage"), staff)
	f.makeAdmin(admin)
	privileged := f.user("Privileged")
	f.assign(f.role("priv", "changes.approve"), privileged)

	check := func(actor, target string) error {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		return guards.RequireDominance(ctx, tx, f.actor(actor), target)
	}
	if err := check(lead, staff); err != nil {
		t.Fatalf("lead over staff: %v", err)
	}
	if err := check(lead, privileged); !errors.Is(err, ErrDominanceRequired) {
		t.Fatalf("lead over a more privileged account: %v", err)
	}
	if err := check(lead, admin); !errors.Is(err, ErrDominanceRequired) {
		t.Fatalf("lead over an administrator: %v", err)
	}
	if err := check(admin, privileged); err != nil {
		t.Fatalf("administrator over everyone: %v", err)
	}
	if err := check(staff, lead); !errors.Is(err, ErrDominanceRequired) {
		t.Fatalf("staff over lead: %v", err)
	}
	tx, _ := f.pool.Begin(ctx)
	defer tx.Rollback(ctx)
	if err := guards.RequireDominance(ctx, tx, f.cli, admin); err != nil {
		t.Fatalf("the operator (CLI): %v", err)
	}
}

// Team leads work with saved views: every working template may share a view with named people, Teams and roles
// (views.share, low risk, never grants data access); publishing to everyone (views.publish) stays out of templates.
func TestTemplatesViewSharing(t *testing.T) {
	for _, key := range []string{"first-level-support", "it-specialist", "team-lead"} {
		tpl, ok := TemplateByKey(key)
		if !ok {
			t.Fatalf("template %s missing", key)
		}
		if !slices.Contains(tpl.Permissions, "views.share") {
			t.Errorf("%s lacks views.share", key)
		}
	}
	for _, tpl := range Templates() {
		if slices.Contains(tpl.Permissions, "views.publish") {
			t.Errorf("%s contains views.publish", tpl.Key)
		}
	}
}
