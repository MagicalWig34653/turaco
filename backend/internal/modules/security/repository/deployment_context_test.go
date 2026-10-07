package repository_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
)

// The security context of a Deployment (F9 G4) is scoped by product and Device ids and redacted without details.
func TestDeploymentContextScopeAndRedaction(t *testing.T) {
	pool, svc, _, _ := securityEnv(t)
	ctx := context.Background()
	product := uuid.NewString()
	devIn1, devIn2, devOut := uuid.NewString(), uuid.NewString(), uuid.NewString()
	var advisory string
	if err := pool.QueryRow(ctx, `INSERT INTO security.advisories (source, title, severity, status, applicable_at)
		VALUES ('manual', 'Deployment context advisory', 'critical', 'applicable', now()) RETURNING id::text`).Scan(&advisory); err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, advisory)
	var closed string
	if err := pool.QueryRow(ctx, `INSERT INTO security.advisories (source, title, severity, status, applicable_at, resolved_at)
		VALUES ('manual', 'Resolved advisory', 'high', 'resolved', now(), now()) RETURNING id::text`).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, closed)
	insert := func(adv, dev, prod, status string) {
		t.Helper()
		reason := ""
		if status == "remediated" || status == "false_positive" {
			reason = "test"
		}
		remediated := "NULL"
		if status == "remediated" {
			remediated = "now()"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO security.vulnerability_findings (advisory_id, device_id, software_product_id, installed_version, confidence, status, status_reason, first_seen_at, last_seen_at, remediated_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, '1.0', 'probable', $4, NULLIF($5, ''), now(), now(), `+remediated+`)`, adv, dev, prod, status, reason); err != nil {
			t.Fatal(err)
		}
	}
	insert(advisory, devIn1, product, "open")
	insert(advisory, devIn2, product, "remediating")
	insert(advisory, devOut, product, "open")                    // not a target Device
	insert(advisory, uuid.NewString(), uuid.NewString(), "open") // another product
	insert(closed, devIn1, product, "open")                      // the Advisory is resolved
	remediated := uuid.NewString()
	insert(advisory, remediated, product, "remediated") // the Finding is fixed
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM security.vulnerability_findings WHERE software_product_id = $1::uuid OR advisory_id IN ($2::uuid, $3::uuid)`, product, advisory, closed)
	})

	adv := public.NewAdvisories(svc)
	scope := []string{devIn1, devIn2, remediated}
	got, err := adv.DeploymentContext(ctx, product, scope, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.AdvisoryCount != 1 || got.OpenFindings != 2 || len(got.Advisories) != 1 || got.Advisories[0].Reference == "" ||
		got.Advisories[0].OpenFindings != 2 || got.Advisories[0].AffectedDevices != 2 {
		t.Fatalf("detailed context = %+v", got)
	}
	redacted, err := adv.DeploymentContext(ctx, product, scope, false)
	if err != nil {
		t.Fatal(err)
	}
	if redacted.AdvisoryCount != 1 || redacted.OpenFindings != 2 || len(redacted.Advisories) != 0 {
		t.Fatalf("redacted context = %+v", redacted)
	}
	// An empty scope reads nothing; a malformed product reads nothing.
	if empty, err := adv.DeploymentContext(ctx, product, nil, true); err != nil || empty.AdvisoryCount != 0 {
		t.Fatalf("empty scope: %+v %v", empty, err)
	}
	if bad, err := adv.DeploymentContext(ctx, "x'; --", scope, true); err != nil || bad.AdvisoryCount != 0 {
		t.Fatalf("bad product: %+v %v", bad, err)
	}
}
