package main

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// A high-impact Deployment plan goes through the real Approvals module: the submission requests the plan Approval
// for the chosen approver (never the owner, creator or editors), the decision is turned into the plan's status by
// the outbox consumer, and only an approved plan can be scheduled.
func TestDeploymentPlanApprovalRoundTrip(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.Endpoints(w.pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false)
	var productID string
	t.Cleanup(func() {
		conn, err := w.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_type = 'deployment' AND subject_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, w.creator)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_transitions WHERE deployment_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, w.creator)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_rings WHERE deployment_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, w.creator)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployments WHERE created_by = $1`, w.creator)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.target_sets WHERE created_by = $1`, w.creator)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.software_version_approvals WHERE software_version_id IN (SELECT id FROM endpoints.software_versions WHERE software_product_id = $1::uuid)`, productID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.software_versions WHERE software_product_id = $1::uuid`, productID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.software_product_transitions WHERE software_product_id = $1::uuid`, productID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.software_aliases WHERE software_product_id = $1::uuid`, productID)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.software_products WHERE id = $1::uuid`, productID)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	planner := endpointsapp.Principal{UserID: w.creator, Manage: true, SoftwarePackage: true, DeploymentsManage: true, DeploymentsHighImpact: true}
	approverP := endpointsapp.Principal{UserID: w.member, SoftwareApprove: true}
	cc := endpointsapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}
	ac := endpointsapp.Caller{Actor: audit.UserActor(w.member), CorrelationID: w.corr}

	// An approved version of an approved product.
	info, _, err := svc.RegisterSoftwareProduct(ctx, cc, planner, w.corr+" Deploy App", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	productID = info.ID
	one := 1
	if _, err := svc.ApproveProduct(ctx, ac, approverP, info.ID, &one); err != nil {
		t.Fatal(err)
	}
	v, _, err := svc.RegisterVersion(ctx, cc, planner, endpointsapp.NewSoftwareVersion{ProductID: info.ID, ProductVersion: "2.0",
		InstallerSHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", InstallerURL: "https://downloads.example.com/app-2.0.msi",
		InstallCommand: "msiexec /i app-2.0.msi /qn", DetectionRule: "version >= 2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err = svc.RequestVersionApproval(ctx, cc, planner, v.ID, &v.Version); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.ApproveVersion(ctx, ac, approverP, v.ID, &v.Version); err != nil {
		t.Fatal(err)
	}

	// A superseding rollout is high impact. Its pilot targets one explicit (unknown) Device.
	set, err := svc.CreateTargetSet(ctx, cc, planner, endpointsapp.TargetSetInput{Name: w.corr + " pilot",
		Definition: endpointsapp.TargetDefinition{IncludeDeviceIDs: []string{"00000000-0000-7000-8000-0000000000aa"}}})
	if err != nil {
		t.Fatal(err)
	}
	plan := func() endpointsapp.Deployment {
		t.Helper()
		d, err := svc.CreateDeployment(ctx, cc, planner, endpointsapp.DeploymentInput{Name: "Upgrade", SoftwareVersionID: v.ID, Intent: "update", Supersede: true})
		if err != nil {
			t.Fatal(err)
		}
		d, _, err = svc.AddRing(ctx, cc, planner, d.ID, &d.Version, endpointsapp.RingInput{Name: "Pilot", TargetSetID: set.ID, SuccessThresholdPercent: 90, NoWindowRequired: true})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	get := func(id string) endpointsapp.Deployment {
		t.Helper()
		d, err := svc.GetDeployment(ctx, planner, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.Deployment
	}

	d := plan()
	if !d.HighImpact {
		t.Fatalf("supersede must be high impact: %+v", d)
	}
	// The planner (owner, creator, editor) can never approve.
	self := w.creator
	if _, err := svc.SubmitDeployment(ctx, cc, planner, d.ID, &d.Version, endpointsapp.Approver{UserID: &self}); !errors.Is(err, endpointsapp.ErrNoEligibleApprover) {
		t.Fatalf("self approver: %v", err)
	}
	member := w.member
	if d, err = svc.SubmitDeployment(ctx, cc, planner, d.ID, &d.Version, endpointsapp.Approver{UserID: &member}); err != nil || d.Status != "pending_approval" {
		t.Fatalf("submit = %+v %v", d, err)
	}
	w.dispatch()
	if w.notified(w.member, "approval.requested") != 1 {
		t.Error("the approver must be notified of the request")
	}
	if err := decideApproval(w, w.creator, *d.ApprovalID, "approve"); err == nil {
		t.Error("the planner decided their own plan")
	}
	// Approving a pending plan's approval twice or for a scheduled plan is refused by the Approvals module itself.
	if err := decideApproval(w, w.member, *d.ApprovalID, "approve"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if got := get(d.ID); got.Status != "approved" || got.ApprovedAt == nil {
		t.Fatalf("after approval = %+v", got)
	}
	d = get(d.ID)
	if d, err = svc.ScheduleDeployment(ctx, cc, planner, d.ID, &d.Version); err != nil || d.Status != "scheduled" {
		t.Fatalf("schedule = %+v %v", d, err)
	}

	// A rejection returns the plan to draft.
	r := plan()
	if r, err = svc.SubmitDeployment(ctx, cc, planner, r.ID, &r.Version, endpointsapp.Approver{UserID: &member}); err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, *r.ApprovalID, "reject"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if got := get(r.ID); got.Status != "draft" || got.StatusReason == nil || *got.StatusReason != endpointsapp.ReasonApprovalRejected {
		t.Fatalf("after rejection = %+v", got)
	}

	// Cancelling a submitted plan cancels its approval.
	c := plan()
	if c, err = svc.SubmitDeployment(ctx, cc, planner, c.ID, &c.Version, endpointsapp.Approver{UserID: &member}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelDeployment(ctx, cc, planner, c.ID, &c.Version, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, *c.ApprovalID, "approve"); err == nil {
		t.Error("a cancelled plan's approval could still be decided")
	}
	w.dispatch()
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}
