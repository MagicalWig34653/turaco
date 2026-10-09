package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

func TestPeopleLookupFindsAnyNameTokenOfActiveInternalEmployeesOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	family := "Zyx" + f.pfx
	caller := f.user("Caller "+family+"c", "active")
	target := f.insert(`INSERT INTO organization.users(display_name, given_name, family_name, status) VALUES ($1, 'Birgit', $2, 'active') RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, "Dr. Birgit "+family, family)
	f.user("Gone "+family, "inactive")
	f.insert(`INSERT INTO organization.users(display_name, status, account_kind, access_expires_at) VALUES ($1, 'active', 'external', now() + interval '30 days') RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, "Vendor "+family)
	f.insert(`INSERT INTO organization.users(display_name, status, origin) VALUES ($1, 'active', 'emergency') RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, "Breakglass "+family)
	svc := application.NewPeopleLookup(f.repo)
	actor := audit.UserActor(caller)

	// Surname (not the start of the display name) finds the colleague; the other kinds of account never appear.
	res, err := svc.Find(ctx, actor, "corr-"+f.pfx, strings.ToLower(family))
	if err != nil || len(res) != 2 {
		t.Fatalf("lookup = %+v %v", res, err)
	}
	found := false
	for _, p := range res {
		if p.ID == target {
			found = true
		}
		if strings.Contains(p.DisplayName, "Vendor") || strings.Contains(p.DisplayName, "Gone") || strings.Contains(p.DisplayName, "Breakglass") {
			t.Errorf("must not be listed: %+v", p)
		}
	}
	if !found {
		t.Errorf("target missing: %+v", res)
	}
	// Too short and wildcard-only texts.
	if _, err := svc.Find(ctx, actor, "corr", "ab"); err != application.ErrLookupQueryTooShort {
		t.Errorf("short text: %v", err)
	}
	if res, _ := svc.Find(ctx, actor, "corr-"+f.pfx, "%%%"); len(res) != 0 {
		t.Errorf("wildcards must be literal: %+v", res)
	}
	// An external account that calls gets nothing, even for a matching text.
	ext := f.insert(`INSERT INTO organization.users(display_name, status, account_kind, access_expires_at) VALUES ($1, 'active', 'external', now() + interval '30 days') RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, "Ext caller "+family+"e")
	if res, err := svc.Find(ctx, audit.UserActor(ext), "corr-"+f.pfx, family); err != nil || len(res) != 0 {
		t.Errorf("external caller = %+v %v", res, err)
	}
	// The use is audited without the search text or any names.
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'organization.people.lookup' AND correlation_id = $1 AND metadata::text NOT LIKE '%'||$2||'%'`, "corr-"+f.pfx, family).Scan(&n); err != nil || n < 2 {
		t.Errorf("audit events = %d %v", n, err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, "corr-"+f.pfx)
	})
}
