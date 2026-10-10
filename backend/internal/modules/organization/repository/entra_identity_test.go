package repository

import (
	"context"
	"errors"
	"testing"
)

const (
	testEntraTenant = "11111111-2222-3333-4444-555555555555"
	testEntraObject = "0f0f0f0f-0000-1111-2222-333333333333"
)

func TestEntraIdentityLinkFindUnlink(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	u := f.local("Ana")
	other := f.local("Ben")
	provider := "entra:" + testEntraTenant
	object := testEntraObject
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.external_identities WHERE provider_key = $1`, provider)
	})

	if _, found, err := f.repo.FindUserByExternalIdentity(ctx, provider, object); err != nil || found {
		t.Fatalf("before link: %v %v", found, err)
	}
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "corr-"+f.pfx, u.ID, provider, object, "cli"); err != nil {
		t.Fatal(err)
	}
	if id, found, err := f.repo.FindUserByExternalIdentity(ctx, provider, object); err != nil || !found || id != u.ID {
		t.Fatalf("after link: %s %v %v", id, found, err)
	}
	// The same identity cannot be linked twice; the same user cannot get a second identity of the tenant.
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "corr-"+f.pfx, other.ID, provider, object, "cli"); !errors.Is(err, ErrEntraIdentityTaken) {
		t.Fatalf("taken: %v", err)
	}
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "corr-"+f.pfx, u.ID, provider, "aaaaaaaa-0000-1111-2222-333333333333", "cli"); !errors.Is(err, ErrEntraTenantAlreadyUsed) {
		t.Fatalf("second identity of tenant: %v", err)
	}
	var linked int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'organization.external_identity.linked' AND target_id = $1`, u.ID).Scan(&linked)
	if linked != 1 {
		t.Fatalf("link audit events: %d", linked)
	}
	if _, found, _ := f.repo.FindUserByExternalIdentity(ctx, "entra:"+"99999999-2222-3333-4444-555555555555", object); found {
		t.Fatal("the same object id in another tenant must not match")
	}

	// A session of the entra method is revoked on unlink, other methods are not.
	var entraSession, otherSession string
	_ = f.pool.QueryRow(ctx, `INSERT INTO platform.sessions (user_id, token_hash, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, sha256(random()::text::bytea), 'entra', now(), now(), now() + interval '1 hour', now() + interval '2 hours') RETURNING id::text`, u.ID).Scan(&entraSession)
	_ = f.pool.QueryRow(ctx, `INSERT INTO platform.sessions (user_id, token_hash, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1::uuid, sha256(random()::text::bytea), 'ldap', now(), now(), now() + interval '1 hour', now() + interval '2 hours') RETURNING id::text`, u.ID).Scan(&otherSession)
	f.t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM platform.sessions WHERE user_id = $1::uuid`, u.ID) })
	if err := f.repo.UnlinkExternalIdentity(ctx, f.cli, "corr-"+f.pfx, provider, object, "cli"); err != nil {
		t.Fatal(err)
	}
	var revoked, kept bool
	_ = f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM platform.sessions WHERE id = $1::uuid`, entraSession).Scan(&revoked)
	_ = f.pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM platform.sessions WHERE id = $1::uuid`, otherSession).Scan(&kept)
	if !revoked || !kept {
		t.Fatalf("entra session revoked=%v, ldap session untouched=%v", revoked, kept)
	}
	if _, found, _ := f.repo.FindUserByExternalIdentity(ctx, provider, object); found {
		t.Fatal("still found after unlink")
	}
	if err := f.repo.UnlinkExternalIdentity(ctx, f.cli, "corr-"+f.pfx, provider, object, "cli"); !errors.Is(err, ErrEntraNotLinked) {
		t.Fatalf("second unlink: %v", err)
	}
}

func TestEntraLinkRefusals(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	provider := "entra:" + testEntraTenant
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.external_identities WHERE provider_key = $1`, provider)
	})
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "c", "00000000-0000-7000-8000-000000000999", provider, testEntraObject, "cli"); !errors.Is(err, ErrEntraUserNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	// A user with a local credential must not be linked (the atomic replacement is a separate operation).
	u := f.local("Cara")
	if _, err := f.repo.IssueCredentialLink(ctx, f.caller(""), u.ID, "invitation"); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "c", u.ID, provider, testEntraObject, "cli"); !errors.Is(err, ErrEntraLocalCredential) {
		t.Errorf("local credential: %v", err)
	}
	// Inactive users are refused.
	v := f.local("Dan")
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'inactive' WHERE id = $1::uuid`, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.LinkExternalIdentity(ctx, f.cli, "c", v.ID, provider, testEntraObject, "cli"); !errors.Is(err, ErrEntraUserNotLinkable) {
		t.Errorf("inactive user: %v", err)
	}
}
