package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

type fakeMailer struct {
	mu     sync.Mutex
	mail   bool
	sent   []string // links
	failed bool
}

func (m *fakeMailer) BaseURLConfigured() bool { return true }
func (m *fakeMailer) MailConfigured() bool    { return m.mail }
func (m *fakeMailer) Link(token, purpose string) string {
	return "https://turaco.example.test/set-password#token=" + token + "&purpose=" + purpose
}
func (m *fakeMailer) Send(_ context.Context, _, _, _, link string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failed {
		return errors.New("relay down")
	}
	m.sent = append(m.sent, link)
	return nil
}

type peopleFix struct {
	*fixture
	repo   *Repository
	roles  *roles.Service
	mailer *fakeMailer
	cli    audit.Actor
	n      int
}

func newPeopleFix(t *testing.T) *peopleFix {
	f := newFixture(t)
	subjects := orgpublic.NewAuthorizationSubjects(New(f.pool))
	mailer := &fakeMailer{}
	repo := New(f.pool).WithGuards(roles.NewGuards(f.pool, subjects), authentication.SessionRevoker{}).
		WithCredentials(authentication.NewLocalCredentials(nil), mailer)
	return &peopleFix{fixture: f, repo: repo, roles: roles.NewService(f.pool, subjects), mailer: mailer, cli: audit.CLIActor("test")}
}

func (f *peopleFix) caller(userID string) application.Caller {
	a := f.cli
	if userID != "" {
		a = audit.UserActor(userID)
	}
	// The CLI actor stands for an administrator: it may be shown an unmailed invitation link.
	return application.Caller{Actor: a, CorrelationID: "corr-" + f.pfx, PlatformAdmin: userID == ""}
}

func (f *peopleFix) cleanupUser(id string) {
	f.t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`UPDATE organization.users SET manager_user_id = NULL WHERE manager_user_id = $1`,
			`DELETE FROM platform.role_assignments WHERE subject_id = $1`,
			`DELETE FROM platform.credential_tokens WHERE user_id = $1`,
			`DELETE FROM platform.local_credentials WHERE user_id = $1`,
			`DELETE FROM platform.sessions WHERE user_id = $1`,
			`DELETE FROM organization.team_memberships WHERE user_id = $1`,
			`DELETE FROM organization.users WHERE id = $1`,
		} {
			if _, err := f.pool.Exec(ctx, q, id); err != nil {
				f.t.Errorf("cleanup user: %v", err)
			}
		}
	})
}

// local creates a local User; perms (when given) become one custom role assigned to the User.
func (f *peopleFix) local(name string, perms ...string) application.User {
	f.t.Helper()
	f.n++
	email := fmt.Sprintf("%s-%d@example.test", f.pfx, f.n)
	u, err := f.repo.CreateLocalUser(context.Background(), f.caller(""), application.NewUserInput{DisplayName: f.pfx + " " + name, PrimaryEmail: &email, AccountKind: "employee"})
	if err != nil {
		f.t.Fatalf("create local user: %v", err)
	}
	f.cleanupUser(u.ID)
	if len(perms) > 0 {
		role, err := f.roles.CreateRole(context.Background(), f.cli, "corr-"+f.pfx, roles.CreateRoleInput{Key: fmt.Sprintf("%s-r%d", f.pfx, f.n), Name: name, Permissions: perms,
			Acknowledgement: roles.Acknowledgement{Rules: sodKeys(), Reason: "test"}})
		if err != nil {
			f.t.Fatalf("create role: %v", err)
		}
		f.t.Cleanup(func() {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.role_assignments WHERE role_id = $1`, role.ID)
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.roles WHERE id = $1`, role.ID)
		})
		if _, err := f.roles.AssignRole(context.Background(), f.cli, "corr-"+f.pfx, roles.AssignInput{RoleID: role.ID, SubjectType: roles.SubjectUser, SubjectID: u.ID,
			Acknowledgement: roles.Acknowledgement{Rules: sodKeys(), Reason: "test"}}); err != nil {
			f.t.Fatalf("assign role: %v", err)
		}
	}
	return u
}

func sodKeys() []string {
	var keys []string
	for _, r := range roles.SoDRules() {
		keys = append(keys, r.Key)
	}
	return keys
}

func (f *peopleFix) admin(name string) application.User {
	f.t.Helper()
	u := f.local(name)
	built, err := f.roles.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.roles.AssignRole(context.Background(), f.cli, "corr-"+f.pfx, roles.AssignInput{RoleID: built.ID, SubjectType: roles.SubjectUser, SubjectID: u.ID}); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *peopleFix) reload(id string) application.User {
	f.t.Helper()
	u, err := f.repo.GetUser(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *peopleFix) actions(targetID string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT action FROM platform.audit_events WHERE target_id = $1 ORDER BY id`, targetID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func hasAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

func str(s string) *string { return &s }

func TestCreateLocalUserAndKindConfusion(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	u := f.local("Alice")
	if u.Origin != "local" || u.AccountKind != "employee" || u.Status != "active" || u.StatusSource != "platform" || u.Version != 1 {
		t.Fatalf("local user: %+v", u)
	}
	if !hasAction(f.actions(u.ID), "organization.user.created_local") {
		t.Errorf("audit: %v", f.actions(u.ID))
	}
	// The email is unique.
	if _, err := f.repo.CreateLocalUser(ctx, f.caller(""), application.NewUserInput{DisplayName: "Dup", PrimaryEmail: u.PrimaryEmail, AccountKind: "employee"}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate email: %v", err)
	}
	// account_kind and origin never change: not even by hand (review rule R4).
	for _, col := range []string{"account_kind = 'external', access_expires_at = now() + interval '1 day'", "origin = 'directory'", "origin = 'emergency'"} {
		_, err := f.pool.Exec(ctx, `UPDATE organization.users SET `+col+` WHERE id = $1`, u.ID)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "ORG03" {
			t.Errorf("UPDATE ... SET %s: %v, want ORG03", col, err)
		}
	}
	// An external account needs an expiry; an employee never has one.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET access_expires_at = now() + interval '1 day' WHERE id = $1`, u.ID); err == nil {
		t.Error("an employee with an expiry must be refused by the constraint")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO organization.users(display_name, account_kind) VALUES ('x', 'external')`); err == nil {
		t.Error("an external account without expiry must be refused by the constraint")
	}
}

func TestProfileOwnershipDirectoryAndEmergency(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	// A directory-origin user (the column default) keeps directory-owned attributes read-only.
	dir := f.user(f.pfx+"-dir", "active")
	f.cleanupUser(dir)
	u := f.reload(dir)
	_, err := f.repo.UpdateProfile(ctx, f.caller(""), dir, application.ProfileChange{ExpectedVersion: u.Version,
		GivenName: application.OptString{Set: true, Value: str("X")}, PrimaryEmail: application.OptString{Set: true, Value: str("x@example.test")}})
	var owned *application.FieldDirectoryOwnedError
	if !errors.As(err, &owned) || len(owned.Fields) != 2 {
		t.Fatalf("directory-owned: %v", err)
	}
	if _, err := f.repo.SetManager(ctx, f.caller(""), dir, u.Version, nil); !errors.As(err, &owned) {
		t.Errorf("manager of a directory user is directory-owned: %v", err)
	}
	// Platform-owned attributes stay editable.
	dept, err := f.repo.CreateDepartment(ctx, f.caller(""), application.NewDepartmentInput{Name: f.pfx + " dept"})
	if err != nil {
		t.Fatal(err)
	}
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `UPDATE organization.users SET department_id = NULL WHERE department_id = $1`, dept.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.departments WHERE id = $1`, dept.ID)
	})
	if got, err := f.repo.SetDepartment(ctx, f.caller(""), dir, u.Version, &dept.ID); err != nil || got.DepartmentID == nil || got.Version != u.Version+1 {
		t.Fatalf("set department: %+v %v", got, err)
	}
	// Stale versions change nothing.
	if _, err := f.repo.SetDepartment(ctx, f.caller(""), dir, u.Version, nil); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	// The emergency account is CLI-only.
	em := f.insert(`INSERT INTO organization.users(display_name, origin) VALUES ($1, 'emergency') RETURNING id::text`, `DELETE FROM organization.users WHERE id = $1`, f.pfx+"-em")
	if _, err := f.repo.UpdateProfile(ctx, f.caller(""), em, application.ProfileChange{ExpectedVersion: 1, GivenName: application.OptString{Set: true, Value: str("X")}}); !errors.Is(err, application.ErrEmergencyAccount) {
		t.Errorf("emergency profile: %v", err)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), em, 1, application.OpDeactivate, "no_longer_needed"); !errors.Is(err, application.ErrEmergencyAccount) {
		t.Errorf("emergency deactivate: %v", err)
	}
	f.mailer.mail = true
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), em, application.CredentialReset); !errors.Is(err, application.ErrEmergencyAccount) {
		t.Errorf("emergency reset: %v", err)
	}
	f.mailer.mail = false
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), dir, application.CredentialInvitation); !errors.Is(err, application.ErrDirectoryUser) {
		t.Errorf("directory user invitation: %v", err)
	}
}

func TestDominanceRuleBlocksTakeover(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	f.mailer.mail = true
	delegate := f.local("Delegate", "organization.users.manage", "tickets.view")
	staff := f.local("Staff", "tickets.view")
	powerful := f.local("Powerful", "tickets.view", "changes.approve")
	admin := f.admin("Admin")

	// Reset, invitation-like, email change, deactivation and reactivation: refused on a more privileged target.
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(delegate.ID), powerful.ID, application.CredentialReset); !errors.Is(err, application.ErrDominanceRequired) {
		t.Errorf("reset of a more privileged account: %v", err)
	}
	if _, err := f.repo.UpdateProfile(ctx, f.caller(delegate.ID), powerful.ID, application.ProfileChange{ExpectedVersion: powerful.Version,
		PrimaryEmail: application.OptString{Set: true, Value: str(f.pfx + "-evil@example.test")}}); !errors.Is(err, application.ErrDominanceRequired) {
		t.Errorf("email change of a more privileged account: %v", err)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(delegate.ID), powerful.ID, powerful.Version, application.OpDeactivate, "no_longer_needed"); !errors.Is(err, application.ErrDominanceRequired) {
		t.Errorf("deactivate of a more privileged account: %v", err)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(delegate.ID), admin.ID, admin.Version, application.OpDeactivate, "no_longer_needed"); !errors.Is(err, application.ErrDominanceRequired) {
		t.Errorf("deactivate of an administrator: %v", err)
	}
	if got := f.reload(powerful.ID); got.Status != "active" || got.PrimaryEmail == nil || strings.Contains(*got.PrimaryEmail, "evil") {
		t.Errorf("a refused operation changed the account: %+v", got)
	}
	// Over an equal or lower account the delegate may act; an administrator may act on everyone.
	staffReset, err := f.repo.IssueCredentialLink(ctx, f.caller(delegate.ID), staff.ID, application.CredentialInvitation)
	if err != nil || !staffReset.Mailed || staffReset.Link != "" {
		t.Errorf("invitation over a lower account: %+v %v", staffReset, err)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(admin.ID), powerful.ID, powerful.Version, application.OpDeactivate, "no_longer_needed"); err != nil {
		t.Errorf("administrator deactivates: %v", err)
	}
	// Nobody deactivates themselves.
	if _, err := f.repo.ChangeStatus(ctx, f.caller(admin.ID), admin.ID, admin.Version, application.OpDeactivate, "no_longer_needed"); !errors.Is(err, application.ErrSelfOperation) {
		t.Errorf("self deactivation: %v", err)
	}
}

func TestStatusLifecycleRevokesSessionsAndTokens(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	u := f.local("Bob")
	sessions := authentication.NewService(f.pool, authentication.Config{IdleTimeout: time.Hour, AbsoluteTimeout: time.Hour}, nil)
	if _, _, err := sessions.Create(ctx, u.ID, "ldap", "corr-"+f.pfx); err != nil {
		t.Fatal(err)
	}
	link, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialInvitation)
	if err != nil || link.Link == "" || link.Mailed {
		t.Fatalf("invitation without a mail channel is shown to the administrator once: %+v %v", link, err)
	}
	var open int
	count := func() {
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.credential_tokens WHERE user_id = $1 AND used_at IS NULL`, u.ID).Scan(&open)
	}
	count()
	if open != 1 {
		t.Fatalf("open tokens = %d", open)
	}
	// A reason code is part of the contract.
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), u.ID, u.Version, "explode", "x"); err == nil {
		t.Error("unknown operation")
	}
	off, err := f.repo.ChangeStatus(ctx, f.caller(""), u.ID, u.Version, application.OpDeactivate, "extended_leave")
	if err != nil || off.Status != "inactive" || off.StatusSource != "platform" || off.Version != u.Version+1 {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	var live int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.sessions WHERE user_id = $1 AND revoked_at IS NULL`, u.ID).Scan(&live)
	count()
	if live != 0 || open != 0 {
		t.Errorf("deactivation left %d sessions and %d tokens", live, open)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), u.ID, off.Version, application.OpDeactivate, "extended_leave"); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("deactivate twice: %v", err)
	}
	// A deactivated user gets no invitation (no token for a previously deactivated account).
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialInvitation); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("invitation for an inactive account: %v", err)
	}
	dep, err := f.repo.ChangeStatus(ctx, f.caller(""), u.ID, off.Version, application.OpMarkDeparted, "left_organization")
	if err != nil || dep.Status != "departed" {
		t.Fatalf("depart: %+v %v", dep, err)
	}
	back, err := f.repo.ChangeStatus(ctx, f.caller(""), u.ID, dep.Version, application.OpReactivate, "returned")
	if err != nil || back.Status != "active" {
		t.Fatalf("reactivate: %+v %v", back, err)
	}
	want := []string{"organization.user.created_local", "organization.user.deactivated", "organization.user.departed", "organization.user.reactivated"}
	got := f.actions(u.ID)
	for _, w := range want {
		if !hasAction(got, w) {
			t.Errorf("audit lacks %s: %v", w, got)
		}
	}
	// The directory only re-enables what it disabled: a directory user whose identity is disabled stays inactive.
	dir := f.user(f.pfx+"-dirdis", "active")
	f.cleanupUser(dir)
	f.exec(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, enabled) VALUES ($1::uuid, 'test', $1::uuid::text, false)`,
		`DELETE FROM organization.external_identities WHERE user_id = $1`, dir)
	du := f.reload(dir)
	d1, err := f.repo.ChangeStatus(ctx, f.caller(""), dir, du.Version, application.OpDeactivate, "no_longer_needed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), dir, d1.Version, application.OpReactivate, "mistake"); !errors.Is(err, application.ErrDirectoryIdentityDisabled) {
		t.Errorf("reactivate while the directory identity is disabled: %v", err)
	}
}

func TestLastAdministratorCannotBeDeactivated(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	a1, a2 := f.admin("Admin1"), f.admin("Admin2")
	var others int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM platform.role_assignments a
		JOIN platform.roles r ON r.id = a.role_id AND r.built_in
		JOIN organization.users u ON u.id = a.subject_id
		WHERE a.subject_type = 'user' AND a.revoked_at IS NULL AND u.status = 'active' AND a.subject_id NOT IN ($1::uuid, $2::uuid)`, a1.ID, a2.ID).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others > 0 {
		t.Skip("the database has other active administrators; the guard cannot be exercised deterministically")
	}
	// A second operator (a user who is no administrator but dominates, or the CLI) deactivates; the actor is the CLI.
	off, err := f.repo.ChangeStatus(ctx, f.caller(""), a1.ID, a1.Version, application.OpDeactivate, "no_longer_needed")
	if err != nil || off.Status != "inactive" {
		t.Fatalf("deactivate with a second administrator left: %+v %v", off, err)
	}
	for _, op := range []string{application.OpDeactivate, application.OpMarkDeparted} {
		if _, err := f.repo.ChangeStatus(ctx, f.caller(""), a2.ID, a2.Version, op, map[string]string{application.OpDeactivate: "no_longer_needed", application.OpMarkDeparted: "left_organization"}[op]); !errors.Is(err, application.ErrLastAdministrator) {
			t.Errorf("%s of the last administrator: %v", op, err)
		}
	}
	if got := f.reload(a2.ID); got.Status != "active" {
		t.Errorf("last administrator was changed: %+v", got)
	}
	// Two concurrent deactivations of the last two administrators: exactly one succeeds.
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), a1.ID, off.Version, application.OpReactivate, "returned"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range []string{a1.ID, a2.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := f.reload(id)
			_, results[i] = f.repo.ChangeStatus(ctx, f.caller(""), id, u.Version, application.OpDeactivate, "no_longer_needed")
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range results {
		if e == nil {
			ok++
		} else if !errors.Is(e, application.ErrLastAdministrator) {
			t.Errorf("unexpected error: %v", e)
		}
	}
	if ok != 1 {
		t.Errorf("%d of 2 concurrent deactivations succeeded, want exactly 1", ok)
	}
}

func TestEmailChangeRevokesOpenTokens(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	u := f.local("Carol")
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialInvitation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.UpdateProfile(ctx, f.caller(""), u.ID, application.ProfileChange{ExpectedVersion: u.Version,
		PrimaryEmail: application.OptString{Set: true, Value: str(f.pfx + "-new@example.test")}}); err != nil {
		t.Fatal(err)
	}
	var open int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.credential_tokens WHERE user_id = $1 AND used_at IS NULL`, u.ID).Scan(&open)
	if open != 0 {
		t.Errorf("an email change must revoke open tokens, %d left", open)
	}
	// A reset needs working mail and is never shown to the administrator; the invitation of an activated account is refused.
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialReset); !errors.Is(err, application.ErrMailNotConfigured) {
		t.Errorf("reset without mail: %v", err)
	}
	f.mailer.mail = true
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialReset); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("reset of a never-activated account (use the invitation): %v", err)
	}
	f.mailer.failed = true
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialInvitation); !errors.Is(err, application.ErrMailFailed) {
		t.Errorf("mail failure: %v", err)
	}
}

func TestManagerCycleAndReferences(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	a, b, c := f.local("A"), f.local("B"), f.local("C")
	a1, err := f.repo.SetManager(ctx, f.caller(""), a.ID, a.Version, &b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := f.repo.SetManager(ctx, f.caller(""), b.ID, b.Version, &c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.SetManager(ctx, f.caller(""), c.ID, c.Version, &a.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("manager cycle: %v", err)
	}
	if _, err := f.repo.SetManager(ctx, f.caller(""), c.ID, c.Version, &c.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("own manager: %v", err)
	}
	_ = a1
	_ = b1
	// An inactive manager is refused.
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), c.ID, c.Version, application.OpDeactivate, "no_longer_needed"); err != nil {
		t.Fatal(err)
	}
	d := f.local("D")
	if _, err := f.repo.SetManager(ctx, f.caller(""), d.ID, d.Version, &c.ID); !errors.Is(err, application.ErrTargetInactive) {
		t.Errorf("inactive manager: %v", err)
	}
	if _, err := f.repo.SetDepartment(ctx, f.caller(""), d.ID, d.Version, str(missingID)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown department: %v", err)
	}
}

func TestLocationTreeRules(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	c := f.caller("")
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `UPDATE organization.users SET primary_location_id = NULL WHERE primary_location_id IN (SELECT id FROM organization.locations WHERE name LIKE $1 || '%')`, f.pfx)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND kind = 'area' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL AND name LIKE $1 || '%')`, f.pfx)
		for i := 0; i < 5; i++ {
			_, _ = f.pool.Exec(ctx, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL AND name LIKE $1 || '%')`, f.pfx)
		}
	})
	site, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "site", Name: f.pfx + " Site", Code: str(f.pfx + "-S")})
	if err != nil || site.Kind != "site" || site.ParentID != nil {
		t.Fatalf("site: %+v %v", site, err)
	}
	if _, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "site", Name: f.pfx + " Other", Code: str(strings.ToLower(f.pfx) + "-s")}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("code is unique (case-insensitive) among active locations: %v", err)
	}
	a1, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &site.ID, Name: f.pfx + " A1"})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &a1.ID, Name: f.pfx + " A2"})
	a3, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &a2.ID, Name: f.pfx + " A3"})
	if err != nil {
		t.Fatalf("fourth level: %v", err)
	}
	if _, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &a3.ID, Name: f.pfx + " A4"}); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("fifth level: %v", err)
	}
	// A move may not create a cycle, exceed the depth with a deep subtree, or change the kind.
	if _, err := f.repo.MoveLocation(ctx, c, a1.ID, a1.Version, &a3.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("move under a descendant: %v", err)
	}
	other, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "site", Name: f.pfx + " Site2"})
	b1, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &other.ID, Name: f.pfx + " B1"})
	b2, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &b1.ID, Name: f.pfx + " B2"})
	if _, err := f.repo.MoveLocation(ctx, c, a1.ID, a1.Version, &b2.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("a subtree of 3 levels under depth 3: %v", err)
	}
	if _, err := f.repo.MoveLocation(ctx, c, site.ID, site.Version, &other.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("a site has no parent: %v", err)
	}
	if _, err := f.repo.MoveLocation(ctx, c, a1.ID, a1.Version, nil); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("an area needs a parent: %v", err)
	}
	moved, err := f.repo.MoveLocation(ctx, c, b2.ID, b2.Version, &site.ID)
	if err != nil || moved.ParentID == nil || *moved.ParentID != site.ID || moved.Version != b2.Version+1 {
		t.Fatalf("legal move: %+v %v", moved, err)
	}
	if _, err := f.repo.MoveLocation(ctx, c, b2.ID, b2.Version, &other.ID); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale move: %v", err)
	}
	// The database refuses a cycle and a fifth level even without the operation.
	var pg *pgconn.PgError
	if _, err := f.pool.Exec(ctx, `UPDATE organization.locations SET parent_location_id = $2 WHERE id = $1`, a1.ID, a3.ID); !errors.As(err, &pg) || pg.Code != "ORG01" {
		t.Errorf("trigger cycle: %v", err)
	}
	// Deactivation names what still points at the location.
	user := f.local("Resident")
	if _, err := f.repo.SetPrimaryLocation(ctx, c, user.ID, user.Version, &a1.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := f.repo.GetLocation(ctx, a1.ID)
	var impact *application.ImpactError
	if _, err := f.repo.SetLocationActive(ctx, c, a1.ID, cur.Version, false, false); !errors.As(err, &impact) || impact.Counts["users"] != 1 || impact.Counts["areas"] != 1 {
		t.Fatalf("impact: %v", err)
	}
	off, err := f.repo.SetLocationActive(ctx, c, a1.ID, cur.Version, false, true)
	if err != nil || off.Active {
		t.Fatalf("confirmed deactivation: %+v %v", off, err)
	}
	// A deactivated location is not offered any more.
	if _, err := f.repo.SetPrimaryLocation(ctx, c, user.ID, f.reload(user.ID).Version, &a1.ID); !errors.Is(err, application.ErrTargetInactive) {
		t.Errorf("inactive location as target: %v", err)
	}
	if _, err := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &a1.ID, Name: f.pfx + " Z"}); !errors.Is(err, application.ErrTargetInactive) {
		t.Errorf("inactive parent: %v", err)
	}
	if on, err := f.repo.SetLocationActive(ctx, c, a1.ID, off.Version, true, false); err != nil || !on.Active {
		t.Errorf("activate: %+v %v", on, err)
	}
	// Concurrent opposite moves never produce a cycle.
	x, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &other.ID, Name: f.pfx + " X"})
	y, _ := f.repo.CreateLocation(ctx, c, application.NewLocationInput{Kind: "area", ParentID: &other.ID, Name: f.pfx + " Y"})
	var wg sync.WaitGroup
	res := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, res[0] = f.repo.MoveLocation(ctx, c, x.ID, x.Version, &y.ID) }()
	go func() { defer wg.Done(); _, res[1] = f.repo.MoveLocation(ctx, c, y.ID, y.Version, &x.ID) }()
	wg.Wait()
	if res[0] == nil && res[1] == nil {
		t.Errorf("two opposite moves both succeeded: a cycle")
	}
	if !hasAction(f.actions(a1.ID), "organization.location.deactivated") {
		t.Errorf("audit: %v", f.actions(a1.ID))
	}
}

func TestDepartmentTreeAndImpact(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	c := f.caller("")
	mk := func(name string, parent *string) application.Department {
		d, err := f.repo.CreateDepartment(ctx, c, application.NewDepartmentInput{Name: f.pfx + name, Code: str(f.pfx + name), ParentID: parent})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	root := mk("R", nil)
	child := mk("C", &root.ID)
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `UPDATE organization.users SET department_id = NULL WHERE department_id IN ($1, $2)`, root.ID, child.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.departments WHERE id = $1`, child.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.departments WHERE id = $1`, root.ID)
	})
	if _, err := f.repo.MoveDepartment(ctx, c, root.ID, root.Version, &child.ID); !errors.Is(err, application.ErrHierarchy) {
		t.Errorf("department cycle: %v", err)
	}
	if _, err := f.repo.CreateDepartment(ctx, c, application.NewDepartmentInput{Name: "dup", Code: str(strings.ToLower(f.pfx) + "r")}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("department code unique: %v", err)
	}
	u := f.local("Member")
	if _, err := f.repo.SetDepartment(ctx, c, u.ID, u.Version, &child.ID); err != nil {
		t.Fatal(err)
	}
	var impact *application.ImpactError
	if _, err := f.repo.SetDepartmentActive(ctx, c, child.ID, child.Version, false, false); !errors.As(err, &impact) || impact.Counts["users"] != 1 {
		t.Errorf("impact: %v", err)
	}
	if _, err := f.repo.UpdateDepartment(ctx, c, child.ID, application.DepartmentChange{ExpectedVersion: child.Version + 3, Name: str("x")}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
}

func TestTeamMembershipRules(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	team := f.createTeam(f.pfx + " Team")
	lead := f.local("Lead", "organization.teams.manage")
	member := f.local("Member")

	// Nobody changes their own membership, not even an administrator; the CLI may.
	if _, err := f.repo.AddTeamMember(ctx, f.caller(lead.ID), team.ID, lead.ID, nil); !errors.Is(err, application.ErrTeamMembershipSelf) {
		t.Errorf("add self: %v", err)
	}
	admin := f.admin("Admin")
	if _, err := f.repo.AddTeamMember(ctx, f.caller(admin.ID), team.ID, admin.ID, nil); !errors.Is(err, application.ErrTeamMembershipSelf) {
		t.Errorf("administrator adds self: %v", err)
	}
	if _, err := f.repo.AddTeamMember(ctx, f.caller(lead.ID), team.ID, member.ID, nil); err != nil {
		t.Fatalf("add other: %v", err)
	}
	if err := f.repo.RemoveTeamMember(ctx, f.caller(member.ID), team.ID, member.ID); !errors.Is(err, application.ErrTeamMembershipSelf) {
		t.Errorf("remove self: %v", err)
	}
	if _, err := f.repo.SetMemberRole(ctx, f.caller(member.ID), team.ID, member.ID, "lead"); !errors.Is(err, application.ErrTeamMembershipSelf) {
		t.Errorf("promote self: %v", err)
	}
	got, err := f.repo.SetMemberRole(ctx, f.caller(lead.ID), team.ID, member.ID, "lead")
	if err != nil || got.Role == nil || *got.Role != "lead" {
		t.Fatalf("set lead: %+v %v", got, err)
	}
	leads, err := f.repo.ListTeamLeads(ctx, team.ID)
	if err != nil || len(leads) != 1 || leads[0].UserID != member.ID {
		t.Errorf("leads: %+v %v", leads, err)
	}
	if _, err := f.repo.SetMemberRole(ctx, f.caller(lead.ID), team.ID, member.ID, "owner"); err == nil {
		t.Error("an unknown role must fail (database constraint)")
	}
	// An external account is a restricted principal: it enters a Team only with the external parties permission.
	ext := f.insert(`INSERT INTO organization.users(display_name, account_kind, access_expires_at, origin) VALUES ($1, 'external', now() + interval '30 days', 'local') RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, f.pfx+"-ext")
	f.t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM organization.team_memberships WHERE user_id = $1`, ext) })
	if _, err := f.repo.AddTeamMember(ctx, f.caller(lead.ID), team.ID, ext, nil); !errors.Is(err, application.ErrExternalNeedsPermission) {
		t.Errorf("external account without the permission: %v", err)
	}
	c := f.caller(lead.ID)
	c.ExternalPartiesManage = true
	if _, err := f.repo.AddTeamMember(ctx, c, team.ID, ext, nil); err != nil {
		t.Errorf("external account with the permission: %v", err)
	}
	// Descriptions are versioned.
	d, err := f.repo.SetTeamDescription(ctx, f.caller(lead.ID), team.ID, f.reloadTeam(team.ID).Version, "Handles tickets")
	if err != nil || d.Description != "Handles tickets" {
		t.Fatalf("description: %+v %v", d, err)
	}
	if _, err := f.repo.SetTeamDescription(ctx, f.caller(lead.ID), team.ID, d.Version-1, "stale"); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale description: %v", err)
	}
}

func (f *peopleFix) reloadTeam(id string) application.Team {
	tm, err := f.repo.GetTeam(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return tm
}

// Without a mail channel the invitation link is shown only to a platform administrator (ADR-0034): a delegated
// manager gets a refusal and no token is issued.
func TestInvitationLinkWithoutMailIsAdministratorOnly(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	u := f.local("Carla")
	delegate := f.local("Delegate")
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(delegate.ID), u.ID, application.CredentialInvitation); !errors.Is(err, application.ErrMailNotConfigured) {
		t.Fatalf("delegate without mail: %v", err)
	}
	var tokens int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.credential_tokens WHERE user_id = $1`, u.ID).Scan(&tokens)
	if tokens != 0 {
		t.Fatalf("a refused invitation issued %d tokens", tokens)
	}
	if link, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, application.CredentialInvitation); err != nil || link.Link == "" {
		t.Fatalf("administrator: %+v %v", link, err)
	}
}
