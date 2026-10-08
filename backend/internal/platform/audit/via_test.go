package audit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// F12 A15: audit entries carry the tenant and, for AI-assisted actions, the channel and proposal.
func TestRecordViaTenantAndProposal(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	corr := "via-test-" + t.Name()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr) })
	user := "0190a000-0000-7000-8000-0000000000bb"
	proposal := "0190a000-0000-7000-8000-0000000000cc"
	base := Change{Action: "test.via", TargetType: "t", TargetID: "1", Actor: UserActor(user), CorrelationID: corr}
	rec := func(c Change) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return Record(ctx, tx, c) })
	}

	bad := map[string]Change{}
	c := base
	c.Via = "ai"
	bad["via without tenant"] = c
	c = base
	c.Via, c.TenantID = "robot", "t1"
	bad["unknown via"] = c
	c = base
	c.ProposalID, c.TenantID = proposal, "t1"
	bad["proposal without via"] = c
	for name, ch := range bad {
		if err := rec(ch); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	good := base
	good.Via, good.TenantID, good.ProposalID = "ai", "tenant-1", proposal
	if err := rec(good); err != nil {
		t.Fatal(err)
	}
	plain := base
	plain.Action = "test.plain"
	if err := rec(plain); err != nil {
		t.Fatal(err)
	}
	var via, tenant, prop *string
	if err := pool.QueryRow(ctx, `SELECT via, tenant_id, ai_proposal_id::text FROM platform.audit_events WHERE correlation_id=$1 AND action='test.via'`, corr).Scan(&via, &tenant, &prop); err != nil {
		t.Fatal(err)
	}
	if via == nil || *via != "ai" || tenant == nil || *tenant != "tenant-1" || prop == nil || *prop != proposal {
		t.Fatalf("stored %v %v %v", via, tenant, prop)
	}
	if err := pool.QueryRow(ctx, `SELECT via, tenant_id, ai_proposal_id::text FROM platform.audit_events WHERE correlation_id=$1 AND action='test.plain'`, corr).Scan(&via, &tenant, &prop); err != nil || via != nil || tenant != nil || prop != nil {
		t.Fatalf("existing writers must stay NULL: %v %v %v %v", via, tenant, prop, err)
	}
	// The database refuses an unknown channel even if a writer bypasses Record.
	if _, err := pool.Exec(ctx, `INSERT INTO platform.audit_events (action, target_type, target_id, correlation_id, via) VALUES ('x','t','1',$1,'robot')`, corr); err == nil {
		t.Error("database accepted via=robot")
	}
}
