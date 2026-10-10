package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// entraFix wraps the people fixture with the credential remover the replacement of a local credential needs.
type entraFix struct {
	*peopleFix
	admin  application.User
	caller application.Caller
}

func newEntraFix(t *testing.T) *entraFix {
	f := newPeopleFix(t)
	f.repo = f.repo.WithCredentialRemover(testRemover{})
	adm := f.admin("Admin")
	c := f.caller(adm.ID)
	c.PlatformAdmin = true
	return &entraFix{peopleFix: f, admin: adm, caller: c}
}

func (f *entraFix) oid() string {
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// cleanIdentities removes the identities of a user before the user itself (registered after the user cleanup, so it runs first).
func (f *entraFix) cleanIdentities(userID string) {
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization.external_identities WHERE user_id = $1`, userID)
	})
}

func (f *entraFix) target(name string, perms ...string) application.User {
	u := f.local(name, perms...)
	f.cleanIdentities(u.ID)
	return u
}

func (f *entraFix) identityOf(userID string) (id, via, subject string) {
	f.t.Helper()
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text, coalesce(linked_via,''), external_subject FROM organization.external_identities
		WHERE user_id = $1 AND provider_key LIKE 'entra:%'`, userID).Scan(&id, &via, &subject); err != nil {
		f.t.Fatalf("entra identity of %s: %v", userID, err)
	}
	return id, via, subject
}

func (f *entraFix) entraCount(userID string) int {
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.external_identities WHERE user_id = $1 AND provider_key LIKE 'entra:%'`, userID).Scan(&n)
	return n
}

func TestAdministratorLinksAndUnlinksAnEntraIdentity(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	u := f.target("Ana")
	oid := f.oid()
	res, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version, testEntraTenant, oid)
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Version != u.Version+1 || res.CredentialDeleted {
		t.Fatalf("result %+v", res)
	}
	id, via, subject := f.identityOf(u.ID)
	if via != "administrator" || subject != oid {
		t.Fatalf("via %q subject %q", via, subject)
	}
	if got, found, _ := f.repo.FindUserByExternalIdentity(ctx, "entra:"+testEntraTenant, oid); !found || got != u.ID {
		t.Fatalf("sign-in lookup %q %v", got, found)
	}
	// The list shows provider, linked-at, via and only the last four characters of the subject.
	list, err := f.repo.ListUserExternalIdentities(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].LinkedVia != "administrator" || list[0].SubjectSuffix != oid[len(oid)-4:] || list[0].CreatedAt.IsZero() {
		t.Fatalf("list %+v %v", list, err)
	}
	if !hasAction(f.actions(u.ID), "organization.external_identity.linked") {
		t.Fatalf("audit %v", f.actions(u.ID))
	}
	var meta string
	_ = f.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.external_identity.linked'`, u.ID).Scan(&meta)
	if !strings.Contains(meta, `administrator`) || strings.Contains(meta, oid) {
		t.Fatalf("audit metadata must name the route and never the whole subject: %s", meta)
	}
	// A linked user can no longer be given a password.
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller, u.ID, application.CredentialInvitation); !errors.Is(err, application.ErrDirectoryUser) {
		t.Fatalf("invitation after link: %v", err)
	}

	// Unlink revokes the Entra sessions only.
	var entraSession, ldapSession string
	_ = f.pool.QueryRow(ctx, `INSERT INTO platform.sessions (user_id, token_hash, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, sha256(random()::text::bytea), 'entra', now(), now(), now() + interval '1 hour', now() + interval '2 hours') RETURNING id::text`, u.ID).Scan(&entraSession)
	_ = f.pool.QueryRow(ctx, `INSERT INTO platform.sessions (user_id, token_hash, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, sha256(random()::text::bytea), 'ldap', now(), now(), now() + interval '1 hour', now() + interval '2 hours') RETURNING id::text`, u.ID).Scan(&ldapSession)
	un, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, u.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if un.User.Version != res.User.Version+1 || f.entraCount(u.ID) != 0 {
		t.Fatalf("after unlink: version %d identities %d", un.User.Version, f.entraCount(u.ID))
	}
	var revoked, kept bool
	_ = f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM platform.sessions WHERE id = $1::uuid`, entraSession).Scan(&revoked)
	_ = f.pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM platform.sessions WHERE id = $1::uuid`, ldapSession).Scan(&kept)
	if !revoked || !kept {
		t.Fatalf("entra session revoked=%v other session kept=%v", revoked, kept)
	}
	if !hasAction(f.actions(u.ID), "organization.external_identity.unlinked") {
		t.Fatalf("audit %v", f.actions(u.ID))
	}
	if _, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, u.ID, id); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("second unlink: %v", err)
	}
}

func TestLinkEntraIdentityRefusals(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	link := func(c application.Caller, u application.User) error {
		_, err := f.repo.LinkEntraIdentity(ctx, c, u.ID, u.Version, testEntraTenant, f.oid())
		return err
	}
	t.Run("not a platform administrator", func(t *testing.T) {
		u := f.target("NoAdmin")
		c := f.caller
		c.PlatformAdmin = false
		if err := link(c, u); !errors.Is(err, application.ErrAdminRequired) {
			t.Fatal(err)
		}
	})
	t.Run("never on oneself", func(t *testing.T) {
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, f.admin.ID, f.admin.Version, testEntraTenant, f.oid()); !errors.Is(err, application.ErrSelfOperation) {
			t.Fatal(err)
		}
		if f.entraCount(f.admin.ID) != 0 {
			t.Fatal("a refused self-link created an identity")
		}
	})
	t.Run("inactive target", func(t *testing.T) {
		u := f.target("Inactive")
		if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'inactive' WHERE id = $1`, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := link(f.caller, f.reload(u.ID)); !errors.Is(err, application.ErrWrongState) {
			t.Fatal(err)
		}
	})
	t.Run("emergency account", func(t *testing.T) {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO organization.users (display_name, origin) VALUES ($1, 'emergency') RETURNING id::text`, f.pfx+" Emergency").Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.cleanupUser(id)
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, id, 1, testEntraTenant, f.oid()); !errors.Is(err, application.ErrEmergencyAccount) {
			t.Fatal(err)
		}
	})
	t.Run("stale version", func(t *testing.T) {
		u := f.target("Stale")
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version+3, testEntraTenant, f.oid()); !errors.Is(err, application.ErrVersionConflict) {
			t.Fatal(err)
		}
	})
	t.Run("unknown user", func(t *testing.T) {
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, "00000000-0000-7000-8000-000000000999", 1, testEntraTenant, f.oid()); !errors.Is(err, application.ErrNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("dominance", func(t *testing.T) {
		// The actor holds no role, so it does not hold the target's permissions (review rule R1).
		strong := f.target("Strong", "tickets.view")
		weak := f.local("Weak")
		c := f.caller
		c.Actor = audit.UserActor(weak.ID)
		if err := link(c, strong); !errors.Is(err, application.ErrDominanceRequired) {
			t.Fatal(err)
		}
		if f.entraCount(strong.ID) != 0 {
			t.Fatal("a refused link created an identity")
		}
	})
	t.Run("identity already linked", func(t *testing.T) {
		a, b := f.target("TakenA"), f.target("TakenB")
		oid := f.oid()
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, a.ID, a.Version, testEntraTenant, oid); err != nil {
			t.Fatal(err)
		}
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, b.ID, b.Version, testEntraTenant, oid); !errors.Is(err, application.ErrEntraIdentityTaken) {
			t.Fatal(err)
		}
		// The same object id in another tenant is another identity.
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, b.ID, b.Version, "99999999-2222-3333-4444-555555555555", oid); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("second identity of the same tenant", func(t *testing.T) {
		u := f.target("Twice")
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version, testEntraTenant, f.oid()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version+1, testEntraTenant, f.oid()); !errors.Is(err, application.ErrEntraTenantAlreadyUsed) {
			t.Fatal(err)
		}
	})
	t.Run("local credential and roles", func(t *testing.T) {
		u := f.target("RoleHolder", "tickets.view")
		if _, err := f.repo.IssueCredentialLink(ctx, f.caller, u.ID, application.CredentialInvitation); err != nil {
			t.Fatal(err)
		}
		if err := link(f.caller, u); !errors.Is(err, application.ErrDirectoryLinkRoles) {
			t.Fatal(err)
		}
		var creds int
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.local_credentials WHERE user_id = $1`, u.ID).Scan(&creds)
		if creds != 1 || f.entraCount(u.ID) != 0 {
			t.Fatalf("a refused link must change nothing: credentials %d identities %d", creds, f.entraCount(u.ID))
		}
	})
	t.Run("local credential without a remover fails closed", func(t *testing.T) {
		u := f.target("NoRemover")
		if _, err := f.repo.IssueCredentialLink(ctx, f.caller, u.ID, application.CredentialInvitation); err != nil {
			t.Fatal(err)
		}
		bare := *f.repo
		bare.removers = nil
		if _, err := bare.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version, testEntraTenant, f.oid()); !errors.Is(err, application.ErrNoGuards) {
			t.Fatal(err)
		}
	})
}

func TestLinkingAnEntraIdentityReplacesTheLocalCredentialAtomically(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	f.mailer.mail = false
	u := f.target("Replace")
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller, u.ID, application.CredentialInvitation); err != nil {
		t.Fatal(err)
	}
	var sess string
	_ = f.pool.QueryRow(ctx, `INSERT INTO platform.sessions (user_id, token_hash, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, sha256(random()::text::bytea), 'local', now(), now(), now() + interval '1 hour', now() + interval '2 hours') RETURNING id::text`, u.ID).Scan(&sess)
	res, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version, testEntraTenant, f.oid())
	if err != nil {
		t.Fatal(err)
	}
	var creds, open int
	var revoked bool
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.local_credentials WHERE user_id = $1`, u.ID).Scan(&creds)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.credential_tokens WHERE user_id = $1 AND used_at IS NULL`, u.ID).Scan(&open)
	_ = f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM platform.sessions WHERE id = $1::uuid`, sess).Scan(&revoked)
	if !res.CredentialDeleted || creds != 0 || open != 0 || !revoked {
		t.Fatalf("deleted=%v credentials=%d open tokens=%d session revoked=%v", res.CredentialDeleted, creds, open, revoked)
	}
	// A pending invitation of a user without a credential row ends too.
	v := f.target("Pending")
	if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, v.ID, v.Version, testEntraTenant, f.oid()); err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkEntraIdentityRefusals(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	u := f.target("Unlink")
	if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, u.ID, u.Version, testEntraTenant, f.oid()); err != nil {
		t.Fatal(err)
	}
	id, _, _ := f.identityOf(u.ID)
	other := f.target("Other")
	otherCaller := f.caller
	otherCaller.PlatformAdmin = false
	if _, err := f.repo.UnlinkEntraIdentity(ctx, otherCaller, u.ID, id); !errors.Is(err, application.ErrAdminRequired) {
		t.Errorf("not administrator: %v", err)
	}
	if _, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, other.ID, id); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("identity of another user: %v", err)
	}
	if _, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, u.ID, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
	// A directory identity is never removed through this operation.
	var dirID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject) VALUES ($1, $2, $3) RETURNING id::text`,
		u.ID, f.pfx+"-ad", f.oid()).Scan(&dirID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, u.ID, dirID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("directory identity: %v", err)
	}
	if _, err := f.repo.UnlinkEntraIdentity(ctx, f.caller, f.admin.ID, id); !errors.Is(err, application.ErrSelfOperation) {
		t.Errorf("self: %v", err)
	}
	strong := f.target("StrongUnlink", "tickets.view")
	if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, strong.ID, strong.Version, testEntraTenant, f.oid()); err != nil {
		t.Fatal(err)
	}
	sid, _, _ := f.identityOf(strong.ID)
	weak := f.local("WeakUnlink")
	c := f.caller
	c.Actor = audit.UserActor(weak.ID)
	if _, err := f.repo.UnlinkEntraIdentity(ctx, c, strong.ID, sid); !errors.Is(err, application.ErrDominanceRequired) {
		t.Errorf("dominance: %v", err)
	}
	if f.entraCount(strong.ID) != 1 || f.entraCount(u.ID) != 1 {
		t.Fatal("a refused unlink removed an identity")
	}
}

// ---- sign-in time: hybrid source anchor ----

// directoryUser creates a directory-owned employee with a directory identity of provider key.
func (f *entraFix) directoryUser(name, providerKey, subject string) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO organization.users (display_name, primary_email, origin) VALUES ($1, $2, 'directory') RETURNING id::text`,
		f.pfx+" "+name, strings.ToLower(f.pfx+"-"+name)+"@example.test").Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.cleanupUser(id)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO organization.external_identities (user_id, provider_key, external_subject, username) VALUES ($1, $2, $3, $4)`,
		id, providerKey, subject, f.pfx+name); err != nil {
		f.t.Fatal(err)
	}
	f.cleanIdentities(id)
	return id
}

func TestSourceAnchorMatchLinksTheDirectoryUser(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	dirKey := f.pfx + "-ad"
	anchor := f.oid()
	uid := f.directoryUser("Hybrid", dirKey, anchor)
	oid := f.oid()
	got, err := f.repo.LinkEntraBySourceAnchor(ctx, dirKey, testEntraTenant, oid, anchor, "corr-"+f.pfx)
	if err != nil || got != uid {
		t.Fatalf("%q %v", got, err)
	}
	if _, via, subject := f.identityOf(uid); via != "source_anchor" || subject != oid {
		t.Fatalf("via %q subject %q", via, subject)
	}
	var meta string
	_ = f.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.external_identity.linked'`, uid).Scan(&meta)
	if !strings.Contains(meta, "source_anchor") {
		t.Fatalf("audit %s", meta)
	}
	// A second sign-in of the same Entra identity (it is linked now) and a second Entra account for the same person are refused.
	if _, err := f.repo.LinkEntraBySourceAnchor(ctx, dirKey, testEntraTenant, f.oid(), anchor, "corr-"+f.pfx); !errors.Is(err, application.ErrAnchorRefused) {
		t.Fatalf("second entra account for the same directory user: %v", err)
	}
}

func TestSourceAnchorMatchRefusals(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	dirKey := f.pfx + "-ad"
	match := func(anchor string) error {
		_, err := f.repo.LinkEntraBySourceAnchor(ctx, dirKey, testEntraTenant, f.oid(), anchor, "corr-"+f.pfx)
		return err
	}
	if err := match(f.oid()); !errors.Is(err, application.ErrAnchorNoMatch) {
		t.Errorf("no user: %v", err)
	}
	// The anchor is matched against the configured directory provider only.
	other := f.oid()
	f.directoryUser("OtherProvider", f.pfx+"-other", other)
	if err := match(other); !errors.Is(err, application.ErrAnchorNoMatch) {
		t.Errorf("identity of another provider: %v", err)
	}
	for name, mutate := range map[string]string{
		"inactive user":     `UPDATE organization.users SET status = 'inactive' WHERE id = $1`,
		"disabled identity": `UPDATE organization.external_identities SET enabled = false WHERE user_id = $1`,
	} {
		anchor := f.oid()
		id := f.directoryUser(strings.ReplaceAll(name, " ", ""), dirKey, anchor)
		if _, err := f.pool.Exec(ctx, mutate, id); err != nil {
			t.Fatal(err)
		}
		if err := match(anchor); !errors.Is(err, application.ErrAnchorRefused) {
			t.Errorf("%s: %v", name, err)
		}
		if f.entraCount(id) != 0 {
			t.Errorf("%s: an identity was linked", name)
		}
	}
	// A local account that happens to carry the identity row is not directory-owned.
	anchor := f.oid()
	local := f.target("LocalWithRow")
	if _, err := f.pool.Exec(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject) VALUES ($1, $2, $3)`, local.ID, dirKey, anchor); err != nil {
		t.Fatal(err)
	}
	if err := match(anchor); !errors.Is(err, application.ErrAnchorRefused) {
		t.Errorf("local account: %v", err)
	}
	// An Entra object id that is already linked elsewhere cannot be linked again.
	taken := f.oid()
	holder := f.target("Holder")
	if _, err := f.repo.LinkEntraIdentity(ctx, f.caller, holder.ID, holder.Version, testEntraTenant, taken); err != nil {
		t.Fatal(err)
	}
	anchor = f.oid()
	f.directoryUser("Victim", dirKey, anchor)
	if _, err := f.repo.LinkEntraBySourceAnchor(ctx, dirKey, testEntraTenant, taken, anchor, "corr-"+f.pfx); !errors.Is(err, application.ErrAnchorRefused) {
		t.Errorf("object id already linked: %v", err)
	}
}

// ---- sign-in time: provisioning ----

func (f *entraFix) provisioned(id string) {
	f.cleanupUser(id)
	f.cleanIdentities(id)
}

func TestProvisioningCreatesAnEmployeeWithoutRolesOrTeams(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	oid := f.oid()
	email := strings.ToLower(f.pfx) + "-new@example.test"
	id, err := f.repo.ProvisionEntraEmployee(ctx, testEntraTenant, oid, f.pfx+" Nora New", email, "corr-"+f.pfx)
	if err != nil {
		t.Fatal(err)
	}
	f.provisioned(id)
	u := f.reload(id)
	if u.AccountKind != "employee" || u.Origin != "directory" || u.Status != "active" || u.PrimaryEmail == nil || *u.PrimaryEmail != email {
		t.Fatalf("user %+v", u)
	}
	var roleCount, teamCount int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments WHERE subject_id = $1`, id).Scan(&roleCount)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM organization.team_memberships WHERE user_id = $1`, id).Scan(&teamCount)
	if roleCount != 0 || teamCount != 0 {
		t.Fatalf("roles %d teams %d", roleCount, teamCount)
	}
	if _, via, subject := f.identityOf(id); via != "provisioning" || subject != oid {
		t.Fatalf("via %q subject %q", via, subject)
	}
	acts := f.actions(id)
	if !hasAction(acts, "organization.user.provisioned") || !hasAction(acts, "organization.external_identity.linked") {
		t.Fatalf("audit %v", acts)
	}
	var meta string
	_ = f.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.user.provisioned'`, id).Scan(&meta)
	if !strings.Contains(meta, `entra`) {
		t.Fatalf("audit metadata %s", meta)
	}
	// The same person signing in again (or concurrently) gets the same user, not a second one.
	again, err := f.repo.ProvisionEntraEmployee(ctx, testEntraTenant, oid, f.pfx+" Nora New", email, "corr-"+f.pfx)
	if err != nil || again != id {
		t.Fatalf("second provisioning %q %v", again, err)
	}
}

func TestProvisioningRefusesAnEmailCollisionAndNeverLinksByEmail(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	existing := f.target("Existing")
	var email string
	_ = f.pool.QueryRow(ctx, `SELECT primary_email FROM organization.users WHERE id = $1`, existing.ID).Scan(&email)
	oid := f.oid()
	for _, addr := range []string{email, strings.ToUpper(email)} {
		if _, err := f.repo.ProvisionEntraEmployee(ctx, testEntraTenant, oid, f.pfx+" Mallory", addr, "corr-"+f.pfx); !errors.Is(err, application.ErrProvisionEmailConflict) {
			t.Fatalf("collision with %q: %v", addr, err)
		}
	}
	if f.entraCount(existing.ID) != 0 {
		t.Fatal("an email collision linked the existing user")
	}
	var users int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM organization.users WHERE display_name = $1`, f.pfx+" Mallory").Scan(&users)
	if users != 0 {
		t.Fatalf("a refused provisioning created %d users", users)
	}
	if !hasAction(f.actions(existing.ID), "organization.user.provisioning_refused") {
		t.Fatalf("no trace for administrators: %v", f.actions(existing.ID))
	}
	if _, found, _ := f.repo.FindUserByExternalIdentity(ctx, "entra:"+testEntraTenant, oid); found {
		t.Fatal("the refused identity must stay unlinked")
	}
}

func TestProvisioningRefusesUnusableProfiles(t *testing.T) {
	f := newEntraFix(t)
	ctx := context.Background()
	for name, c := range map[string]struct{ name, email string }{
		"no email":     {"Pat Doe", ""},
		"bad email":    {"Pat Doe", "not-an-address"},
		"no name":      {"", "pat@example.test"},
		"control name": {"Pat\x00Doe", "pat@example.test"},
	} {
		_, err := f.repo.ProvisionEntraEmployee(ctx, testEntraTenant, f.oid(), c.name, c.email, "corr-"+f.pfx)
		var inv *application.InvalidInputError
		if !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
