package main

import (
	"context"
	"errors"
	"testing"

	planningapp "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// An Initiative goes through the real Approvals and Notifications modules:
// the proposal requests an approval for the chosen approver (never the owner,
// the creator or an editor), the decision is turned into the Initiative's
// status by the outbox consumer and the owner is told about every status change.
func TestInitiativeApprovalRoundTrip(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.Planning(w.pool)
	var ids []string
	t.Cleanup(func() {
		conn, err := w.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		for _, id := range ids {
			_, _ = conn.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, id)
			_, _ = conn.Exec(ctx, `DELETE FROM planning.initiative_transitions WHERE initiative_id = $1::uuid`, id)
			_, _ = conn.Exec(ctx, `DELETE FROM planning.initiatives WHERE id = $1::uuid`, id)
		}
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	mgr := planningapp.Principal{UserID: w.creator, Manage: true}
	cc := planningapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr}
	get := func(id string) planningapp.Initiative {
		t.Helper()
		d, err := svc.Get(ctx, mgr, id)
		if err != nil {
			t.Fatal(err)
		}
		return d.Initiative
	}
	propose := func(approver string) (planningapp.Initiative, error) {
		i, err := svc.Create(ctx, cc, mgr, planningapp.NewInitiative{Title: "Datacenter move", OwnerUserID: w.assignee})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, i.ID)
		if i, err = svc.StartPlanning(ctx, cc, mgr, i.ID, &i.Version); err != nil {
			t.Fatal(err)
		}
		return svc.Propose(ctx, cc, mgr, i.ID, &i.Version, planningapp.Approver{UserID: &approver})
	}
	approvalOf := func(id string) string {
		var a string
		if err := w.pool.QueryRow(ctx, `SELECT id::text FROM approvals.approvals WHERE subject_type = 'initiative' AND subject_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, id).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}

	// The owner and the creator (the proposer) can never approve.
	for _, u := range []string{w.assignee, w.creator} {
		if _, err := propose(u); !errors.Is(err, planningapp.ErrNoEligibleApprover) {
			t.Errorf("excluded approver: %v", err)
		}
	}
	i, err := propose(w.member)
	if err != nil || i.Status != "proposed" {
		t.Fatalf("propose = %+v %v", i, err)
	}
	w.dispatch()
	if w.notified(w.member, "approval.requested") != 1 {
		t.Error("the approver must be notified of the request")
	}
	before := w.notified(w.assignee, "initiative.state")
	if before == 0 {
		t.Error("the owner must be told about the status changes")
	}
	if err := decideApproval(w, w.creator, approvalOf(i.ID), "approve"); err == nil {
		t.Error("the proposer decided their own initiative")
	}
	if err := decideApproval(w, w.member, approvalOf(i.ID), "approve"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if got := get(i.ID); got.Status != "approved" || got.ApprovedAt == nil {
		t.Fatalf("after approval = %+v", got)
	}
	if w.notified(w.assignee, "initiative.state") != before+1 {
		t.Errorf("owner notifications = %d, want %d", w.notified(w.assignee, "initiative.state"), before+1)
	}

	// A rejection returns the Initiative to planning.
	r, err := propose(w.member)
	if err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, approvalOf(r.ID), "reject"); err != nil {
		t.Fatal(err)
	}
	w.dispatch()
	if got := get(r.ID); got.Status != "planning" || got.StatusReason == nil || *got.StatusReason != planningapp.ReasonApprovalRejected {
		t.Fatalf("after rejection = %+v", got)
	}

	// Cancelling a proposal cancels its approval.
	c, err := propose(w.member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, cc, mgr, c.ID, &c.Version, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := decideApproval(w, w.member, approvalOf(c.ID), "approve"); err == nil {
		t.Error("a cancelled initiative's approval could still be decided")
	}
	w.dispatch()
	if w.pendingEvents() != 0 {
		t.Errorf("%d events left unprocessed", w.pendingEvents())
	}
}
