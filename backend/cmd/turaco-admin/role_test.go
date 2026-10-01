package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestParseGrantArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want grantArgs
		ok   bool
	}{
		{"user", []string{"--role", "admin", "--user", "alice"}, grantArgs{role: "admin", user: "alice"}, true},
		{"group", []string{"--role=r", "--group", "00000000-0000-0000-0000-000000000001"}, grantArgs{role: "r", group: "00000000-0000-0000-0000-000000000001"}, true},
		{"no role", []string{"--user", "a"}, grantArgs{}, false},
		{"no subject", []string{"--role", "r"}, grantArgs{}, false},
		{"both subjects", []string{"--role", "r", "--user", "a", "--group", "g"}, grantArgs{}, false},
		{"unknown flag", []string{"--role", "r", "--user", "a", "--force"}, grantArgs{}, false},
		{"positional", []string{"--role", "r", "--user", "a", "extra"}, grantArgs{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGrantArgs(tc.args)
			if tc.ok {
				if err != nil || got != tc.want {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, errUsage) {
				t.Fatalf("err = %v, want usage error", err)
			}
		})
	}
}

func TestParseRevokeArgs(t *testing.T) {
	if got, err := parseRevokeArgs([]string{"--assignment", "x"}); err != nil || got.assignment != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, args := range [][]string{nil, {"--assignment"}, {"--assignment", "x", "y"}, {"--bogus", "x"}} {
		if _, err := parseRevokeArgs(args); !errors.Is(err, errUsage) {
			t.Fatalf("%v: err = %v, want usage error", args, err)
		}
	}
}

func TestRunRoleUnknownCommandIsUsageError(t *testing.T) {
	pool := dbtest.Pool(t)
	err := runRole(context.Background(), env{pool: pool, stdout: &bytes.Buffer{}}, "frobnicate", nil)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v", err)
	}
	if err := runRole(context.Background(), env{pool: pool, stdout: &bytes.Buffer{}}, "list", []string{"x"}); !errors.Is(err, errUsage) {
		t.Fatalf("list with args err = %v", err)
	}
}

func TestRoleListGrantRevoke(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	rb := make([]byte, 5)
	_, _ = rand.Read(rb)
	pfx := "zt-" + hex.EncodeToString(rb)
	out := &bytes.Buffer{}
	e := env{pool: pool, stdout: out, actor: []byte(`{"actor":"cli","osUser":"tester"}`)}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM platform.audit_events WHERE correlation_id IN (SELECT correlation_id FROM platform.audit_events WHERE target_id IN (SELECT id::text FROM platform.roles WHERE key LIKE $1 || '%') OR target_id IN (SELECT a.id::text FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id WHERE r.key LIKE $1 || '%'))`,
			`DELETE FROM platform.role_assignments WHERE role_id IN (SELECT id FROM platform.roles WHERE key LIKE $1 || '%')`,
			`DELETE FROM platform.roles WHERE key LIKE $1 || '%'`,
			`DELETE FROM organization.directory_groups WHERE external_id LIKE $1 || '%'`,
			`DELETE FROM organization.users WHERE display_name LIKE $1 || '%'`,
		} {
			if _, err := pool.Exec(ctx, q, pfx); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})

	var userID, groupID string
	email := pfx + "@example.test"
	if err := pool.QueryRow(ctx, `INSERT INTO organization.users(display_name, primary_email) VALUES ($1,$2) RETURNING id::text`, pfx+" User", email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := pool.QueryRow(ctx, `INSERT INTO organization.directory_groups(provider_key, external_id, display_name, first_observed_at, last_observed_at)
		VALUES ('test',$1,$1,$2,$2) RETURNING id::text`, pfx+"-group", now).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	svc := roles.NewService(pool, orgpublic.NewAuthorizationSubjects(orgrepo.New(pool)))
	role, err := svc.CreateRole(ctx, e.auditActor(), e.correlationID(), roles.CreateRoleInput{Key: pfx + "-ops", Name: "Ops", Permissions: []string{"tasks.view"}})
	if err != nil {
		t.Fatal(err)
	}

	run := func(command string, args ...string) error {
		out.Reset()
		return runRole(ctx, e, command, args)
	}

	if err := run("list"); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "KEY") || !strings.Contains(s, roles.AdministratorRoleKey) || !strings.Contains(s, pfx+"-ops") {
		t.Fatalf("list output = %q", s)
	}

	// Grant by e-mail (case-insensitive) and by group uuid.
	if err := run("grant", "--role", role.Key, "--user", strings.ToUpper(email)); err != nil {
		t.Fatalf("grant user: %v", err)
	}
	if !strings.Contains(out.String(), "granted role "+role.Key) {
		t.Fatalf("grant output = %q", out.String())
	}
	if err := run("grant", "--role", role.Key, "--group", groupID); err != nil {
		t.Fatalf("grant group: %v", err)
	}
	if err := run("grant", "--role", role.Key, "--user", userID); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate grant err = %v", err)
	}
	if err := run("grant", "--role", pfx+"-missing", "--user", userID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown role err = %v", err)
	}
	if err := run("grant", "--role", role.Key, "--user", pfx+"-nobody"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown user err = %v", err)
	}
	if err := run("grant", "--role", role.Key, "--group", "00000000-0000-7000-8000-000000000001"); err == nil {
		t.Fatal("unknown group must fail")
	}

	page, err := svc.ListAssignments(ctx, roles.AssignmentFilter{RoleID: role.ID})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("assignments = %+v, %v", page, err)
	}
	var cliAudit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'authorization.role.assigned' AND actor_id IS NULL AND metadata->>'actor' = 'cli' AND target_id = ANY($1)`,
		[]string{page.Items[0].ID, page.Items[1].ID}).Scan(&cliAudit); err != nil || cliAudit != 2 {
		t.Fatalf("cli audit rows = %d, %v", cliAudit, err)
	}

	if err := run("revoke", "--assignment", page.Items[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "revoked assignment") || strings.Contains(s, "warning") {
		t.Fatalf("revoke output = %q", s)
	}
	if err := run("revoke", "--assignment", page.Items[0].ID); err != nil || !strings.Contains(out.String(), "already revoked") {
		t.Fatalf("second revoke = %v %q", err, out.String())
	}
	if err := run("revoke", "--assignment", "00000000-0000-7000-8000-000000000001"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown assignment err = %v", err)
	}
}
