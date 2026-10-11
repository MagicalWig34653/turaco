package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestParseFirstAdminArgs(t *testing.T) {
	const g1, g2 = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	if got, err := parseFirstAdminArgs([]string{"--user", "a", "--force"}); err != nil || got.user != "a" || !got.force {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, err := parseFirstAdminArgs([]string{"--user", "a", "--tenant", g1, "--object", g2}); err != nil || got.tenant != g1 {
		t.Fatalf("entra got %+v, %v", got, err)
	}
	for _, args := range [][]string{
		nil, {"--force"}, {"--user", "a", "--tenant", g1}, {"--user", "a", "--tenant", "x", "--object", "y"}, {"--user", "a", "extra"},
	} {
		if _, err := parseFirstAdminArgs(args); !errors.Is(err, errUsage) {
			t.Fatalf("%v: err = %v, want usage error", args, err)
		}
	}
	if err := runAdmin(context.Background(), env{}, "other", nil); !errors.Is(err, errUsage) {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestCreateFirstAdmin(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	rb := make([]byte, 5)
	_, _ = rand.Read(rb)
	pfx := "zt-fa-" + hex.EncodeToString(rb)
	out := &bytes.Buffer{}
	e := env{pool: pool, stdout: out, actor: []byte(`{"actor":"cli","osUser":"tester"}`)}

	insertUser := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO organization.users(display_name, primary_email) VALUES ($1,$2) RETURNING id::text`,
			name, name+"@example.test").Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	dirUser, secondUser, localUser := insertUser(pfx+"-dir"), insertUser(pfx+"-second"), insertUser(pfx+"-local")
	ids := []string{dirUser, secondUser, localUser}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM platform.audit_events WHERE correlation_id IN (SELECT correlation_id FROM platform.audit_events WHERE metadata->>'userId' = ANY($1) OR target_id IN (SELECT id::text FROM platform.role_assignments WHERE subject_id::text = ANY($1)))`,
			`DELETE FROM platform.role_assignments WHERE subject_id::text = ANY($1)`,
			`DELETE FROM platform.local_credentials WHERE user_id::text = ANY($1)`,
			`DELETE FROM organization.users WHERE id::text = ANY($1)`,
		} {
			if _, err := pool.Exec(ctx, q, ids); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO platform.local_credentials(user_id, login_name, kind, enabled) VALUES ($1::uuid, $2, 'local', false)`,
		localUser, pfx+"-l"); err != nil {
		t.Fatal(err)
	}

	// Local accounts are refused even with --force.
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: localUser, force: true}); err == nil || !strings.Contains(err.Error(), "local accounts") {
		t.Fatalf("local account err = %v", err)
	}
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: pfx + "-nobody", force: true}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown user err = %v", err)
	}

	// --force makes the test independent of administrators that exist in a shared database.
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: dirUser, force: true}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if !strings.Contains(out.String(), "now a platform administrator") {
		t.Fatalf("output = %q", out.String())
	}
	// An administrator exists now, so a plain run is refused and changes nothing.
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: secondUser}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second without --force err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments WHERE subject_id = $1::uuid AND revoked_at IS NULL`, secondUser).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused run granted %d assignments, %v", n, err)
	}
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: dirUser, force: true}); err == nil || !strings.Contains(err.Error(), "already a platform administrator") {
		t.Fatalf("duplicate err = %v", err)
	}
	if err := createFirstAdmin(ctx, e, firstAdminArgs{user: secondUser, force: true}); err != nil {
		t.Fatalf("forced second: %v", err)
	}

	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'authorization.first_administrator.created'
		AND metadata->>'actor' = 'cli' AND metadata->>'userId' = ANY($1)`, ids).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("first-administrator audit rows = %d, %v", audited, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'authorization.role.assigned' AND actor_id IS NULL
		AND target_id IN (SELECT id::text FROM platform.role_assignments WHERE subject_id = ANY($1::uuid[]))`, ids).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("assignment audit rows = %d, %v", audited, err)
	}
}
