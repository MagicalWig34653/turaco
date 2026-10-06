package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Regression tests for the F9 G2 security and database review.

// window registers an approved Change with a future window and returns its id.
func (d *depEnv) window() string {
	start, end := time.Now().Add(time.Hour), time.Now().Add(72*time.Hour)
	id := d.newID()
	d.changes[id] = application.ChangeWindow{ID: id, Reference: "CHG-" + id[:4], Status: "approved", WindowStart: &start, WindowEnd: &end}
	return id
}

// windowPlan creates a draft with one ring per set, each gated by a Change window.
func (d *depEnv) windowPlan(versionID, intent string, supersede bool, sets ...application.TargetSet) application.Deployment {
	d.t.Helper()
	ctx := context.Background()
	dep, err := d.svc.CreateDeployment(ctx, d.caller(), d.planner, application.DeploymentInput{Name: "Windowed", SoftwareVersionID: versionID, Intent: intent, Supersede: supersede})
	if err != nil {
		d.t.Fatalf("create deployment: %v", err)
	}
	for _, set := range sets {
		change := d.window()
		if dep, _, err = d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "Ring", TargetSetID: set.ID,
			SuccessThresholdPercent: 90, ChangeID: &change}); err != nil {
			d.t.Fatalf("add ring: %v", err)
		}
	}
	return dep
}

// deliver hands an ApprovalDecided event to the consumer without changing the fake Approvals state.
func (d *depEnv) deliver(depID, approvalID, decision string) error {
	payload, _ := json.Marshal(map[string]string{"approvalId": approvalID, "subjectType": "deployment", "subjectId": depID, "decision": decision})
	return d.repo().InTx(context.Background(), func(tx pgx.Tx) error {
		return d.svc.OnApprovalDecided(context.Background(), tx, events.OutboxEvent{EventType: "ApprovalDecided", CorrelationID: d.corr, Payload: payload})
	})
}

func TestTargetSetHighImpactIsSemantic(t *testing.T) {
	d := newDepEnv(t)
	parent, child := d.newID(), d.newID()
	d.dir.groups[parent] = application.DirectoryGroup{ID: parent, ExternalID: "root-grp", Name: "Root"}
	d.dir.groups[child] = application.DirectoryGroup{ID: child, ExternalID: "child-grp", Name: "Child"}
	d.dir.parents[child] = []string{parent}
	cases := []struct {
		name string
		def  application.TargetDefinition
		want string
	}{
		{"every platform", application.TargetDefinition{Filters: application.TargetFilters{Platform: application.OSPlatforms}}, application.HighImpactAllDevices},
		{"every compliance with include", application.TargetDefinition{Filters: application.TargetFilters{Compliance: application.ComplianceStates},
			IncludeDeviceIDs: []string{d.newID()}}, application.HighImpactAllDevices},
		{"nested root group", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "root-grp", IncludeNested: true}}}}, application.HighImpactNestedRootGroup},
		{"nested child group", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "child-grp", IncludeNested: true}}}}, ""},
		{"direct root group", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "root-grp"}}}}, ""},
		{"two platforms", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows", "macos"}}}, ""},
	}
	for _, tc := range cases {
		set := d.set(tc.name, tc.def)
		got := ""
		if set.HighImpactReason != nil {
			got = *set.HighImpactReason
		}
		if got != tc.want || set.AllDevices != (tc.want == application.HighImpactAllDevices) {
			t.Errorf("%s: reason %q allDevices %v, want %q", tc.name, got, set.AllDevices, tc.want)
		}
	}
	// A manager without deployments.high_impact cannot put such a set in a ring.
	_, v := d.approvedProductVersion("Semantic App", hashA)
	broad := d.set("broad semantic", application.TargetDefinition{Filters: application.TargetFilters{Ownership: application.Ownerships}})
	manager := application.Principal{UserID: d.user, DeploymentsManage: true, ChangesRead: true}
	dep, err := d.svc.CreateDeployment(context.Background(), d.caller(), manager, application.DeploymentInput{Name: "x", SoftwareVersionID: v.ID, Intent: "install"})
	if err != nil {
		t.Fatal(err)
	}
	change := d.window()
	if _, _, err := d.svc.AddRing(context.Background(), d.caller(), manager, dep.ID, &dep.Version, application.RingInput{Name: "r", TargetSetID: broad.ID,
		SuccessThresholdPercent: 90, ChangeID: &change}); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("full-enum set without high impact: %v", err)
	}
}

func TestDeploymentHighImpactByTargetCount(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	d.svc.WithHighImpactThresholds(3, 0)
	_, v := d.approvedProductVersion("Count App", hashA)
	win := d.set("win", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}}})
	mac := d.set("mac", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}})
	winOK := d.set("win ok", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}, Compliance: []string{"compliant"}}})

	// One ring of 3 Devices reaches the threshold; so do two rings of 1 and 2 (the counts are summed).
	for _, sets := range [][]application.TargetSet{{win}, {mac, winOK}} {
		dep := d.windowPlan(v.ID, "install", false, sets...)
		if dep.HighImpact {
			t.Fatalf("draft snapshot must not count targets: %+v", dep)
		}
		val, err := d.svc.ValidateDeployment(ctx, d.planner, dep.ID)
		if err != nil || !val.HighImpact || val.HighImpactReason != application.HighImpactTargetCount || val.TotalTargets != 3 ||
			!slices.Contains(issueCodes(val.Issues, true), application.IssueApprovalRequired) {
			t.Fatalf("count validation: %v %+v", err, val)
		}
		if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); gateCode(err) != application.IssueApprovalRequired {
			t.Fatalf("schedule high-impact count plan from draft: %v", err)
		}
	}
	// A pilot without a window may not be high impact by its count.
	pilot, _ := d.plan(v.ID, win)
	val, err := d.svc.ValidateDeployment(ctx, d.planner, pilot.ID)
	if err != nil || !slices.Contains(issueCodes(val.Issues, true), application.IssueNoWindowHighImpact) {
		t.Fatalf("no-window pilot over the threshold: %v %+v", err, val.Issues)
	}
	// Submitting recomputes and stores high impact.
	dep := d.windowPlan(v.ID, "install", false, win)
	approver := d.approver
	if dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &approver}); err != nil || !dep.HighImpact {
		t.Fatalf("submit count plan: %v %+v", err, dep)
	}
}

func TestDeploymentApproverMustHoldApprove(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	_, v := d.approvedProductVersion("Approver App", hashA)
	mac := d.set("mac", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}})
	dep := d.windowPlan(v.ID, "update", true, mac)

	other, team, self := d.other, d.newID(), d.user
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &other}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("approver without deployments.approve: %v", err)
	}
	// A Team needs a member who holds it and did not take part (the planner is excluded even when holding it).
	d.approvers.teams[team] = []string{d.user, d.other}
	d.approvers.perms[d.user] = []string{application.PermDeploymentsApprove}
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{TeamID: &team}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("team without eligible member: %v", err)
	}
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &self}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("self approval: %v", err)
	}
	// The author of a Target Set of the plan never approves it.
	d.approvers.perms[d.other] = []string{application.PermDeploymentsApprove}
	otherCaller := application.Caller{Actor: audit.UserActor(d.other), CorrelationID: d.corr}
	authored, err := d.svc.CreateTargetSet(ctx, otherCaller, application.Principal{UserID: d.other, DeploymentsManage: true},
		application.TargetSetInput{Name: "authored " + d.suffix(), Definition: application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}}})
	if err != nil {
		t.Fatal(err)
	}
	withAuthored := d.windowPlan(v.ID, "update", true, authored)
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), d.planner, withAuthored.ID, &withAuthored.Version, application.Approver{UserID: &other}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("target set author as approver: %v", err)
	}
	if dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{TeamID: &team}); err != nil {
		t.Fatalf("team with eligible member: %v", err)
	}
	if ex := d.approvals.requested[*dep.ApprovalID]; !slices.Contains(ex, d.user) || slices.Contains(ex, d.other) {
		t.Fatalf("excluded = %v", ex)
	}

	// The decision is verified: a decider who lost deployments.approve sends the plan back to draft.
	d.approvers.perms[d.other] = nil
	if err := d.decideAs(dep.ID, *dep.ApprovalID, "approve", d.other); err != nil {
		t.Fatal(err)
	}
	got := d.get(dep.ID)
	if got.Status != application.DeploymentDraft || got.StatusReason == nil || *got.StatusReason != application.ReasonApproverNotAuthorized || got.ApprovalID != nil || got.PlanSHA256 != nil {
		t.Fatalf("unauthorized decider: %+v", got)
	}
	if n := d.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action = 'endpoints.deployment.approver_not_authorized'`, dep.ID); n != 1 {
		t.Fatalf("approver_not_authorized audits = %d", n)
	}
	// A status that differs from the event, an excluded decider and another approval id are refused alike.
	approver := d.approver
	for i, tamper := range []func(dep application.Deployment) error{
		func(dep application.Deployment) error { // the approval is still pending, the event claims approve
			return d.deliver(dep.ID, *dep.ApprovalID, "approve")
		},
		func(dep application.Deployment) error { // decided by the planner
			return d.decideAs(dep.ID, *dep.ApprovalID, "approve", d.user)
		},
		func(dep application.Deployment) error { // a different approval
			return d.deliver(dep.ID, d.newID(), "approve")
		},
	} {
		dep = d.get(dep.ID)
		if dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &approver}); err != nil {
			t.Fatalf("case %d: resubmit: %v", i, err)
		}
		if err := tamper(dep); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got := d.get(dep.ID); got.Status != application.DeploymentDraft || *got.StatusReason != application.ReasonApproverNotAuthorized {
			t.Fatalf("case %d: %+v", i, got)
		}
	}
	// A verified decision approves.
	dep = d.get(dep.ID)
	if dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &approver}); err != nil {
		t.Fatal(err)
	}
	if err := d.decide(dep, "approve"); err != nil || d.get(dep.ID).Status != application.DeploymentApproved {
		t.Fatalf("verified approval: %v %+v", err, d.get(dep.ID))
	}
}

func TestTargetSetReadsRedactAndApproverAccess(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	devs := d.fleet(d.newID())
	_, v := d.approvedProductVersion("Reader App", hashA)
	listed := d.set("listed", application.TargetDefinition{IncludeDeviceIDs: []string{devs["d1"].ID}, ExcludeDeviceIDs: []string{devs["d2"].ID}})

	// Device lists need endpoints.view to read and to author.
	got, err := d.svc.GetTargetSet(ctx, d.reader, listed.ID)
	if err != nil || !got.DeviceListsRedacted || len(got.Definition.IncludeDeviceIDs) != 0 || got.IncludeDeviceCount != 1 || got.ExcludeDeviceCount != 1 {
		t.Fatalf("redacted read: %v %+v", err, got)
	}
	if got, _ := d.svc.GetTargetSet(ctx, d.planner, listed.ID); got.DeviceListsRedacted || len(got.Definition.IncludeDeviceIDs) != 1 {
		t.Fatalf("read with endpoints.view: %+v", got)
	}
	manager := application.Principal{UserID: d.user, DeploymentsManage: true}
	for _, def := range []application.TargetDefinition{
		{IncludeDeviceIDs: []string{devs["d1"].ID}},
		{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "grp"}}}},
		{Filters: application.TargetFilters{AssetLocationIDs: []string{d.newID()}}},
	} {
		if _, err := d.svc.CreateTargetSet(ctx, d.caller(), manager, application.TargetSetInput{Name: "x " + d.suffix(), Definition: def}); !errors.Is(err, application.ErrForbidden) {
			t.Fatalf("author %+v without endpoints.view: %v", def, err)
		}
	}
	if _, err := d.svc.UpdateTargetSet(ctx, d.caller(), manager, listed.ID, &listed.Version, application.TargetSetInput{Name: listed.Name,
		Definition: application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}}}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("overwrite device lists without endpoints.view: %v", err)
	}

	// An approver Team member reads the plan's Target Set (counts only) while the Approval is pending.
	stranger := application.Principal{UserID: d.other, View: true}
	if _, err := d.svc.GetTargetSet(ctx, stranger, listed.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("stranger read: %v", err)
	}
	team := d.newID()
	d.approvers.teams[team] = []string{d.approver, d.other}
	dep := d.windowPlan(v.ID, "update", true, listed)
	if dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{TeamID: &team}); err != nil {
		t.Fatal(err)
	}
	if got, err := d.svc.GetTargetSet(ctx, stranger, listed.ID); err != nil || got.ID != listed.ID {
		t.Fatalf("approver read: %v", err)
	}
	ev, err := d.svc.EvaluateTargetSet(ctx, stranger, listed.ID)
	if err != nil || ev.Matched != 1 || len(ev.Examples) != 0 || !ev.ExamplesRedacted {
		t.Fatalf("approver evaluation: %v %+v", err, ev)
	}
	if _, err := d.svc.ExplainTargetSet(ctx, stranger, listed.ID, devs["d1"].ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("approver explain: %v", err)
	}
	if _, err := d.svc.GetDeployment(ctx, stranger, dep.ID); err != nil {
		t.Fatalf("approver team member detail: %v", err)
	}
	if _, err := d.svc.CancelDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, "plan_error"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.GetTargetSet(ctx, stranger, listed.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("read after the approval ended: %v", err)
	}
	if _, err := d.svc.GetDeployment(ctx, stranger, dep.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("team member detail after the approval ended: %v", err)
	}
}

func TestDeploymentChangeMustBeReadable(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	_, v := d.approvedProductVersion("Change App", hashA)
	set := d.set("linux", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}})
	change := d.window()
	noChanges := application.Principal{UserID: d.user, DeploymentsManage: true}
	dep, err := d.svc.CreateDeployment(ctx, d.caller(), noChanges, application.DeploymentInput{Name: "x", SoftwareVersionID: v.ID, Intent: "install"})
	if err != nil {
		t.Fatal(err)
	}
	ring := application.RingInput{Name: "r", TargetSetID: set.ID, SuccessThresholdPercent: 90, ChangeID: &change}
	if _, _, err := d.svc.AddRing(ctx, d.caller(), noChanges, dep.ID, &dep.Version, ring); gateCode(err) != application.IssueChangeWindowInvalid {
		t.Fatalf("unreadable change: %v", err)
	}
	cw := d.changes[change]
	cw.RequesterID = d.user
	d.changes[change] = cw
	if _, _, err := d.svc.AddRing(ctx, d.caller(), noChanges, dep.ID, &dep.Version, ring); err != nil {
		t.Fatalf("change of the requester: %v", err)
	}
}

func TestDeploymentPlanChangedAfterValidation(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	parent, child := d.newID(), d.newID()
	d.dir.groups[parent] = application.DirectoryGroup{ID: parent, ExternalID: "toctou-root"}
	d.dir.groups[child] = application.DirectoryGroup{ID: child, ExternalID: "toctou-child"}
	d.dir.parents[child] = []string{parent}
	_, v := d.approvedProductVersion("Toctou App", hashA)
	set := d.set("toctou", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "toctou-child", IncludeNested: true}}}})
	dep, _ := d.plan(v.ID, set)
	// The Target Set changes after the validation read it and before the scheduling transaction locks it.
	bumped := false
	d.dir.onGroups = func() {
		if !bumped {
			bumped = true
			if _, err := d.pool.Exec(ctx, `UPDATE endpoints.target_sets SET version = version + 1 WHERE id = $1`, set.ID); err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); gateCode(err) != application.CodePlanChanged {
		t.Fatalf("schedule after a concurrent change: %v", err)
	}
	d.dir.onGroups = nil
	if got, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); err != nil || got.Status != application.DeploymentScheduled {
		t.Fatalf("schedule unchanged plan: %v %+v", err, got)
	}
}

func TestEvaluationBudgetMakesPlansIncomplete(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	_, v := d.approvedProductVersion("Budget App", hashA)
	win := d.set("win", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}}})
	dep, _ := d.plan(v.ID, win)
	d.svc.WithEvaluationBudget(2, time.Minute)
	if ev := d.evaluate(d.planner, win.ID); !ev.Incomplete || ev.Scanned != 2 {
		t.Fatalf("budgeted evaluation: %+v", ev)
	}
	var plan *application.PlanInvalidError
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); !errors.As(err, &plan) ||
		!slices.Contains(issueCodes(plan.Issues, true), application.IssueEvaluationIncomplete) {
		t.Fatalf("schedule incomplete plan: %v", err)
	}
}

func TestDeploymentEditorsOverlapCapAndPermissionOrder(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	_, v := d.approvedProductVersion("Editors App", hashA)
	linux := d.set("linux", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}})

	// A full editor list refuses a new editor instead of dropping them.
	dep, _ := d.plan(v.ID, linux)
	if _, err := d.pool.Exec(ctx, `UPDATE endpoints.deployments SET editors = editors || ARRAY(SELECT uuidv7() FROM generate_series(1, 49)) WHERE id = $1`, dep.ID); err != nil {
		t.Fatal(err)
	}
	dep = d.get(dep.ID)
	otherCaller := application.Caller{Actor: audit.UserActor(d.other), CorrelationID: d.corr}
	otherManager := application.Principal{UserID: d.other, DeploymentsManage: true}
	if _, err := d.svc.UpdateDeployment(ctx, otherCaller, otherManager, dep.ID, &dep.Version, application.DeploymentInput{Name: "y", SoftwareVersionID: v.ID, Intent: "install"}); !errors.Is(err, application.ErrEditorsFull) {
		t.Fatalf("51st editor: %v", err)
	}
	if _, err := d.svc.UpdateDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.DeploymentInput{Name: "y", SoftwareVersionID: v.ID, Intent: "install"}); err != nil {
		t.Fatalf("existing editor: %v", err)
	}
	// The permission is checked before the owner lookup.
	if _, err := d.svc.UpdateDeployment(ctx, d.caller(), d.reader, dep.ID, &dep.Version, application.DeploymentInput{Name: "y", SoftwareVersionID: v.ID,
		Intent: "install", OwnerUserID: d.newID()}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("update with view: %v", err)
	}

	// The overlap check compares at most five Target Sets of other plans and says so.
	for i := 0; i < 6; i++ {
		set := d.set("overlap "+string(rune('a'+i)), application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}, Model: []string{string(rune('a' + i))}}})
		p, _ := d.plan(v.ID, set)
		if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, p.ID, &p.Version); err != nil {
			t.Fatal(err)
		}
	}
	mine, _ := d.plan(v.ID, linux)
	val, err := d.svc.ValidateDeployment(ctx, d.planner, mine.ID)
	if err != nil || !slices.Contains(issueCodes(val.Issues, false), application.IssueOverlapTruncated) {
		t.Fatalf("overlap cap: %v %+v", err, val.Issues)
	}
}

func TestDeploymentDatabaseReviewFixes(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	_, v := d.approvedProductVersion("Db App", hashA)
	set := d.set("db", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}})
	dep, ring := d.plan(v.ID, set)

	// A ring position collision surfaces at the immediate constraint check as a conflict.
	err := d.repo().InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO endpoints.deployment_rings (deployment_id, position, name, target_set_id, success_threshold_percent)
			VALUES ($1, $2, 'dup', $3, 90)`, dep.ID, ring.Position, set.ID); err != nil {
			return err
		}
		return d.repo().CheckRingPositionsTx(ctx, tx)
	})
	if !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("position collision: %v", err)
	}
	// An archived Target Set frees its name.
	archived, err := d.svc.ArchiveTargetSet(ctx, d.caller(), d.planner, d.set("reuse", application.TargetDefinition{}).ID, ptr(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.CreateTargetSet(ctx, d.caller(), d.planner, application.TargetSetInput{Name: archived.Name}); err != nil {
		t.Fatalf("reuse archived name: %v", err)
	}
	if n := d.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'endpoints' AND indexname = 'devices_live_provider_idx'`); n != 1 {
		t.Fatalf("devices_live_provider_idx missing")
	}
}
