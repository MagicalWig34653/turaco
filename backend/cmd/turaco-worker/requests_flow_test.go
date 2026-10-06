package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	catalogapp "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	catalogrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	requestsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

var allTestEventTypes = []string{
	"AssetAssigned", "PurchaseOrderApproved", "RunbookExecutionStarted", "RunbookExecutionCompleted", "MajorIncidentDeclared", "MajorIncidentUpdated", "TicketCreated", "TicketAssigned", "TicketResolved", "TicketCommentAdded", "TaskAssigned", "TaskCompleted", "TaskCancelled", "ApprovalRequested", "ApprovalDecided",
	"ServiceRequestSubmitted", "ServiceRequestApproved", "ServiceRequestRejected", "ServiceRequestCompleted", "ServiceRequestCancelled",
	"ServiceCreated", "ChangeSubmitted", "ChangeApproved", "ChangeRejected", "ChangeScheduled", "ChangeScheduledFanOut", "ChangeStarted", "ChangeCompleted", "ChangeFailed",
	"InitiativeStatusChanged",
	"SecurityAdvisoryPublished", "SecurityAdvisoryPublishedFanOut", "VulnerabilityFindingChanged",
	"SoftwareVersionApprovalRequested", "SoftwareVersionApprovalRequestedFanOut", "SoftwareVersionApproved",
	"TargetSetChanged", "DeploymentScheduled", "DeploymentCancelled",
}

// flow is a request workflow test environment over the real modules.
type flow struct {
	*world
	svc        *requestsapp.Service
	tasksSvc   *tasksapp.Service
	requestIDs []string
	itemKeys   []string
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	w := newWorld(t)
	f := &flow{world: w, svc: wiring.Requests(w.pool)}
	f.tasksSvc = tasksapp.NewService(tasksrepository.New(w.pool), orgpublic.NewWorkDirectory(orgrepository.New(w.pool)), nil)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range f.requestIDs {
			_, _ = w.pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 OR target_id = $2`, "request:"+id+"%", id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id LIKE $1`, "request:"+id+"%")
			_, _ = w.pool.Exec(ctx, `DELETE FROM platform.tasks WHERE context_id = $1::uuid`, id)
			_, _ = w.pool.Exec(ctx, `DELETE FROM requests.service_requests WHERE id = $1::uuid`, id)
		}
		for _, k := range f.itemKeys {
			_, _ = w.pool.Exec(ctx, `DELETE FROM catalog.items WHERE key = $1`, k)
		}
	})
	return f
}

func (f *flow) item(name, definition string) string {
	f.t.Helper()
	key := fmt.Sprintf("%s-%s", name, f.corr[len(f.corr)-8:])
	f.itemKeys = append(f.itemKeys, key)
	parsed, err := catalogapp.ParseDefinition([]byte(definition))
	if err != nil {
		f.t.Fatalf("definition: %v", err)
	}
	canonical, _ := parsed.Marshal()
	it, err := catalogrepository.New(f.pool).Insert(context.Background(),
		catalogapp.Caller{Actor: audit.SystemActor("test"), CorrelationID: f.corr},
		catalogapp.NewItem{Key: key, Title: "Laptop " + name, Definition: canonical})
	if err != nil {
		f.t.Fatalf("catalog item: %v", err)
	}
	return it.ID
}

func (f *flow) submit(user, itemID string, answers map[string]any) requestsapp.Request {
	f.t.Helper()
	r, err := f.svc.Submit(context.Background(), requestsapp.Caller{Actor: audit.UserActor(user), CorrelationID: f.corr},
		requestsapp.SubmitInput{CatalogItemID: itemID, Answers: answers})
	if err != nil {
		f.t.Fatalf("submit: %v", err)
	}
	f.requestIDs = append(f.requestIDs, r.ID)
	return r
}

func (f *flow) get(user, id string, manage bool) requestsapp.Detail {
	f.t.Helper()
	d, err := f.svc.Get(context.Background(), requestsapp.Principal{UserID: user, Manage: manage}, id)
	if err != nil {
		f.t.Fatalf("get: %v", err)
	}
	return d
}

func (f *flow) decide(user, requestID string, step int, decision string) {
	f.t.Helper()
	ctx := context.Background()
	var id string
	if err := f.pool.QueryRow(ctx, `SELECT id::text FROM approvals.approvals WHERE subject_id = $1::uuid AND step_index = $2`, requestID, step).Scan(&id); err != nil {
		f.t.Fatalf("find approval: %v", err)
	}
	if err := f.decideErr(user, id, decision); err != nil {
		f.t.Fatalf("decide: %v", err)
	}
}

func (f *flow) decideErr(user, approvalID, decision string) error {
	return decideApproval(f.world, user, approvalID, decision)
}

func (f *flow) runTask(user, taskID, op string) {
	f.t.Helper()
	ctx := context.Background()
	p := tasksapp.Principal{UserID: user, Manage: true}
	c := tasksapp.Caller{Actor: audit.UserActor(user), CorrelationID: f.corr}
	var err error
	switch op {
	case "complete":
		_, err = f.tasksSvc.Transition(ctx, c, p, taskID, nil, tasksapp.OpComplete, "")
	case "cancel":
		_, err = f.tasksSvc.Transition(ctx, c, p, taskID, nil, tasksapp.OpCancel, "not needed")
	}
	if err != nil {
		f.t.Fatalf("task %s: %v", op, err)
	}
}

func (f *flow) status(id string) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM requests.service_requests WHERE id = $1::uuid`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *flow) taskIDs(requestID string) []string {
	f.t.Helper()
	d := f.get(f.creator, requestID, true)
	ids := make([]string, 0, len(d.Tasks))
	for _, tk := range d.Tasks {
		ids = append(ids, tk.Task.ID)
	}
	return ids
}

func (f *flow) def(approvals, tasks string) string {
	return fmt.Sprintf(`{"fields":[{"key":"reason","type":"text","label":"Reason","required":true}],"approvals":[%s],"fulfillment":[%s]}`, approvals, tasks)
}

func answers() map[string]any { return map[string]any{"reason": "need a notebook"} }

func TestRequestWithoutApprovalIsFulfilledByTasksAndCompletes(t *testing.T) {
	f := newFlow(t)
	item := f.item("plain", f.def("", fmt.Sprintf(`{"title":"Prepare device","priority":"high","assignedUserId":"%s","dueAfterHours":24},{"title":"Hand over","mandatory":false,"assignedTeamId":"%s"}`, f.assignee, f.team)))
	r := f.submit(f.creator, item, answers())
	if r.Status != requestsapp.StatusInFulfillment || r.Reference == "" {
		t.Fatalf("request = %+v", r)
	}
	ids := f.taskIDs(r.ID)
	if len(ids) != 2 {
		t.Fatalf("tasks = %d, want 2", len(ids))
	}
	d := f.get(f.creator, r.ID, false)
	if d.Tasks[0].Task.Title != "Prepare device – "+r.Reference || !d.Tasks[0].Mandatory || d.Tasks[1].Mandatory || d.Tasks[0].Task.DueAt == nil {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	var ctype, cid string
	if err := f.pool.QueryRow(context.Background(), `SELECT context_type, context_id::text FROM platform.tasks WHERE id = $1::uuid`, ids[0]).Scan(&ctype, &cid); err != nil || ctype != "service_request" || cid != r.ID {
		t.Errorf("task context = %q %q %v", ctype, cid, err)
	}
	f.dispatch()
	if f.notified(f.creator, "request.approved") != 1 {
		t.Error("the requester must be told the request is being fulfilled")
	}
	// The mandatory task alone does not complete the request: the optional one is still open.
	f.runTask(f.creator, ids[0], "complete")
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusInFulfillment {
		t.Errorf("status = %s, the optional task is still open", f.status(r.ID))
	}
	f.runTask(f.creator, ids[1], "complete")
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusCompleted || f.notified(f.creator, "request.completed") != 1 {
		t.Errorf("status = %s completed-notifications = %d", f.status(r.ID), f.notified(f.creator, "request.completed"))
	}
	// Re-running the events changes nothing (idempotent consumers).
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.outbox_events SET status = 'pending', available_at = now() WHERE correlation_id = $1`, f.corr); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusCompleted || f.notified(f.creator, "request.completed") != 1 {
		t.Error("redelivery must not change the outcome or duplicate notifications")
	}
}

func TestRequestWithoutTasksCompletesImmediately(t *testing.T) {
	f := newFlow(t)
	r := f.submit(f.creator, f.item("empty", f.def("", "")), answers())
	if r.Status != requestsapp.StatusCompleted || r.CompletedAt == nil {
		t.Fatalf("request = %+v", r)
	}
	f.dispatch()
	if f.notified(f.creator, "request.completed") != 1 {
		t.Error("completion must be notified")
	}
}

func TestTwoApprovalStepsThenFulfillment(t *testing.T) {
	f := newFlow(t)
	approvals := fmt.Sprintf(`{"approverUserId":"%s"},{"approverTeamId":"%s"}`, f.assignee, f.team)
	item := f.item("approved", f.def(approvals, `{"title":"Provision"}`))
	r := f.submit(f.creator, item, answers())
	if r.Status != requestsapp.StatusPendingApproval || r.CurrentStep == nil || *r.CurrentStep != 0 {
		t.Fatalf("request = %+v", r)
	}
	f.dispatch()
	if f.notified(f.assignee, "approval.requested") != 1 {
		t.Error("step 1 approver must be notified")
	}
	if f.notified(f.member, "approval.requested") != 0 {
		t.Error("step 2 has not been requested yet")
	}

	f.decide(f.assignee, r.ID, 0, "approve")
	f.dispatch()
	d := f.get(f.creator, r.ID, false)
	if f.status(r.ID) != requestsapp.StatusPendingApproval || d.Request.CurrentStep == nil || *d.Request.CurrentStep != 1 || len(d.Approvals) != 2 {
		t.Fatalf("after step 1: %s step=%v approvals=%d", f.status(r.ID), d.Request.CurrentStep, len(d.Approvals))
	}
	if f.notified(f.member, "approval.requested") != 1 || f.notified(f.creator, "approval.requested") != 0 {
		t.Error("step 2 notifies the Team members but never the requester")
	}
	// The requester is a member of the approving Team but may not approve their own request.
	var step2 string
	_ = f.pool.QueryRow(context.Background(), `SELECT id::text FROM approvals.approvals WHERE subject_id = $1::uuid AND step_index = 1`, r.ID).Scan(&step2)
	if err := f.decideErr(f.creator, step2, "approve"); err == nil {
		t.Error("the requester decided their own request")
	}
	if f.status(r.ID) != requestsapp.StatusPendingApproval {
		t.Error("a refused decision must not move the request")
	}

	f.decide(f.member, r.ID, 1, "approve")
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusInFulfillment || len(f.taskIDs(r.ID)) != 1 {
		t.Fatalf("after step 2: %s", f.status(r.ID))
	}
	if f.notified(f.creator, "request.approved") != 1 {
		t.Error("the requester is told once the last step approved")
	}
}

func TestRejectionEndsTheRequestWithoutTasks(t *testing.T) {
	f := newFlow(t)
	item := f.item("rejected", f.def(fmt.Sprintf(`{"approverUserId":"%s"}`, f.assignee), `{"title":"Provision"}`))
	r := f.submit(f.creator, item, answers())
	f.decide(f.assignee, r.ID, 0, "reject")
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusRejected || len(f.taskIDs(r.ID)) != 0 {
		t.Errorf("status = %s tasks = %d", f.status(r.ID), len(f.taskIDs(r.ID)))
	}
	if f.notified(f.creator, "request.rejected") != 1 {
		t.Error("the requester must be told about the rejection")
	}
}

func TestCancelByRequesterWhilePendingAndByManagerInFulfillment(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	c := func(u string) requestsapp.Caller {
		return requestsapp.Caller{Actor: audit.UserActor(u), CorrelationID: f.corr}
	}
	pending := f.submit(f.creator, f.item("c1", f.def(fmt.Sprintf(`{"approverUserId":"%s"}`, f.assignee), `{"title":"Provision"}`)), answers())

	if _, err := f.svc.Cancel(ctx, c(f.outsider), requestsapp.Principal{UserID: f.outsider}, pending.ID, nil, "x"); !errors.Is(err, requestsapp.ErrNotFound) {
		t.Errorf("a stranger cancelling: %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Cancel(ctx, c(f.creator), requestsapp.Principal{UserID: f.creator}, pending.ID, nil, " "); err == nil {
		t.Error("a reason is required")
	}
	got, err := f.svc.Cancel(ctx, c(f.creator), requestsapp.Principal{UserID: f.creator}, pending.ID, nil, "ordered by mistake")
	if err != nil || got.Status != requestsapp.StatusCancelled {
		t.Fatalf("cancel by requester: %+v %v", got, err)
	}
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM approvals.approvals WHERE subject_id = $1::uuid AND status = 'cancelled'`, pending.ID).Scan(&n)
	if n != 1 {
		t.Errorf("cancelled approvals = %d", n)
	}
	var decideErr = f.decideErr(f.assignee, mustApprovalID(t, f, pending.ID), "approve")
	if decideErr == nil {
		t.Error("a cancelled approval must not be decidable")
	}
	if _, err := f.svc.Cancel(ctx, c(f.creator), requestsapp.Principal{UserID: f.creator}, pending.ID, nil, "again"); err == nil {
		t.Error("cancelling twice must fail")
	}

	running := f.submit(f.creator, f.item("c2", f.def("", `{"title":"A"},{"title":"B"}`)), answers())
	if _, err := f.svc.Cancel(ctx, c(f.creator), requestsapp.Principal{UserID: f.creator}, running.ID, nil, "changed my mind"); !errors.Is(err, requestsapp.ErrForbidden) {
		t.Errorf("the requester cancelling a request in fulfillment: %v, want ErrForbidden", err)
	}
	if _, err := f.svc.Cancel(ctx, c(f.assignee), requestsapp.Principal{UserID: f.assignee, Manage: true}, running.ID, nil, "no longer needed"); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	for _, id := range f.taskIDs(running.ID) {
		var st string
		_ = f.pool.QueryRow(ctx, `SELECT status FROM platform.tasks WHERE id = $1::uuid`, id).Scan(&st)
		if st != "cancelled" {
			t.Errorf("task %s status = %s, want cancelled with the request", id, st)
		}
	}
	if f.status(running.ID) != requestsapp.StatusCancelled {
		t.Errorf("status = %s: cancelled tasks must not complete a cancelled request", f.status(running.ID))
	}
}

func mustApprovalID(t *testing.T, f *flow, requestID string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM approvals.approvals WHERE subject_id = $1::uuid ORDER BY step_index LIMIT 1`, requestID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHoldResumeAndManualCompletion(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	m := requestsapp.Principal{UserID: f.assignee, Manage: true}
	c := requestsapp.Caller{Actor: audit.UserActor(f.assignee), CorrelationID: f.corr}
	r := f.submit(f.creator, f.item("hold", f.def("", `{"title":"Order"},{"title":"Install"}`)), answers())

	if _, err := f.svc.PutOnHold(ctx, c, requestsapp.Principal{UserID: f.creator}, r.ID, nil, "stock"); !errors.Is(err, requestsapp.ErrForbidden) {
		t.Errorf("hold without requests.manage: %v", err)
	}
	if _, err := f.svc.PutOnHold(ctx, c, m, r.ID, nil, "lunch"); err == nil {
		t.Error("an unknown waiting reason must be refused")
	}
	held, err := f.svc.PutOnHold(ctx, c, m, r.ID, nil, "supplier")
	if err != nil || held.Status != requestsapp.StatusWaiting || held.WaitingReason == nil || *held.WaitingReason != "supplier" {
		t.Fatalf("hold = %+v %v", held, err)
	}
	if _, err := f.svc.PutOnHold(ctx, c, m, r.ID, nil, "stock"); err == nil {
		t.Error("holding twice must fail")
	}
	if res, err := f.svc.Resume(ctx, c, m, r.ID, nil); err != nil || res.Status != requestsapp.StatusInFulfillment || res.WaitingReason != nil {
		t.Fatalf("resume = %+v %v", res, err)
	}

	ids := f.taskIDs(r.ID)
	if _, err := f.svc.Complete(ctx, c, m, r.ID, nil, ""); err == nil {
		t.Error("completing with unfinished tasks must fail")
	}
	f.runTask(f.assignee, ids[0], "complete")
	f.runTask(f.assignee, ids[1], "cancel") // a mandatory task is cancelled
	f.dispatch()
	if f.status(r.ID) != requestsapp.StatusInFulfillment {
		t.Fatalf("a cancelled mandatory task must not complete the request automatically: %s", f.status(r.ID))
	}
	if _, err := f.svc.Complete(ctx, c, m, r.ID, nil, ""); err == nil {
		t.Error("completing despite a cancelled mandatory task needs a reason")
	}
	done, err := f.svc.Complete(ctx, c, m, r.ID, nil, "installation not needed")
	if err != nil || done.Status != requestsapp.StatusCompleted {
		t.Fatalf("manual completion = %+v %v", done, err)
	}
	var meta string
	_ = f.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'requests.request.completed'`, r.ID).Scan(&meta)
	if meta == "" {
		t.Error("manual completion must be audited with its reason")
	}
}

func TestVisibilityOfRequests(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	item := f.item("vis", f.def(fmt.Sprintf(`{"approverUserId":"%s"}`, f.assignee), `{"title":"Provision"}`))
	r := f.submit(f.creator, item, answers())
	for user, want := range map[string]bool{f.creator: true, f.assignee: true, f.outsider: false, f.member: false} {
		_, err := f.svc.Get(ctx, requestsapp.Principal{UserID: user}, r.ID)
		if (err == nil) != want {
			t.Errorf("Get as %s: err = %v, want visible=%v", user, err, want)
		}
		if !want && !errors.Is(err, requestsapp.ErrNotFound) {
			t.Errorf("a stranger must get ErrNotFound: %v", err)
		}
	}
	if _, err := f.svc.Get(ctx, requestsapp.Principal{UserID: f.outsider, View: true}, r.ID); err != nil {
		t.Errorf("requests.view sees everything: %v", err)
	}
	if _, err := f.svc.List(ctx, requestsapp.Principal{UserID: f.outsider}, true, "", requestsapp.Page{}); !errors.Is(err, requestsapp.ErrForbidden) {
		t.Errorf("listing all without requests.view: %v", err)
	}
	mine, err := f.svc.List(ctx, requestsapp.Principal{UserID: f.creator}, false, "", requestsapp.Page{Limit: 200})
	found := false
	for _, it := range mine.Items {
		found = found || it.ID == r.ID
	}
	if err != nil || !found {
		t.Errorf("the requester's own list lacks the request: %v", err)
	}
	other, _ := f.svc.List(ctx, requestsapp.Principal{UserID: f.outsider}, false, "", requestsapp.Page{Limit: 200})
	for _, it := range other.Items {
		if it.ID == r.ID {
			t.Error("a stranger's own list contains someone else's request")
		}
	}
}

func TestSubmitValidation(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	c := requestsapp.Caller{Actor: audit.UserActor(f.creator), CorrelationID: f.corr}
	item := f.item("val", f.def("", `{"title":"T"}`))
	if _, err := f.svc.Submit(ctx, c, requestsapp.SubmitInput{CatalogItemID: item, Answers: map[string]any{}}); err == nil {
		t.Error("missing required answers must be refused")
	}
	if _, err := f.svc.Submit(ctx, c, requestsapp.SubmitInput{CatalogItemID: item, Answers: answers(), RequestedForID: &f.member}); !errors.Is(err, requestsapp.ErrRequestedForInvalid) {
		t.Errorf("requesting for another user when the item does not allow it: %v", err)
	}
	if _, err := f.svc.Submit(ctx, c, requestsapp.SubmitInput{CatalogItemID: "00000000-0000-7000-8000-000000000000", Answers: answers()}); !errors.Is(err, requestsapp.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE catalog.items SET active = false WHERE id = $1::uuid`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Submit(ctx, c, requestsapp.SubmitInput{CatalogItemID: item, Answers: answers()}); !errors.Is(err, requestsapp.ErrItemInactive) {
		t.Errorf("inactive item: %v", err)
	}
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM requests.service_requests WHERE catalog_item_id = $1::uuid`, item).Scan(&n)
	if n != 0 {
		t.Errorf("%d requests were created by refused submissions", n)
	}
	// A manager step with a requester who is that manager has no eligible approver.
	mItem := f.item("mgr", f.def(`{"approver":"manager"}`, ""))
	if _, err := f.svc.Submit(ctx, c, requestsapp.SubmitInput{CatalogItemID: mItem, Answers: answers()}); !errors.Is(err, requestsapp.ErrNoEligibleApprover) {
		t.Errorf("manager approval without a manager: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET manager_user_id = $2::uuid WHERE id = $1::uuid`, f.creator, f.assignee); err != nil {
		t.Fatal(err)
	}
	r := f.submit(f.creator, mItem, answers())
	if r.Status != requestsapp.StatusPendingApproval {
		t.Errorf("manager approval: %+v", r)
	}
	var approver string
	_ = f.pool.QueryRow(ctx, `SELECT approver_user_id::text FROM approvals.approvals WHERE subject_id = $1::uuid`, r.ID).Scan(&approver)
	if approver != f.assignee {
		t.Errorf("approver = %s, want the requester's manager", approver)
	}
}

func (f *flow) userFieldDef(approvals string) string {
	return fmt.Sprintf(`{"fields":[{"key":"target","type":"user","label":"Beneficiary","required":true}],"approvals":[%s],"fulfillment":[]}`, approvals)
}

func (f *flow) submitErr(user, itemID string, a map[string]any) error {
	_, err := f.svc.Submit(context.Background(), requestsapp.Caller{Actor: audit.UserActor(user), CorrelationID: f.corr},
		requestsapp.SubmitInput{CatalogItemID: itemID, Answers: a})
	return err
}

func TestUsersNamedInAnswersNeverApprove(t *testing.T) {
	f := newFlow(t)
	// The approver named themselves as the beneficiary: nobody else could decide, so it is refused.
	own := f.item("selfaccess", f.userFieldDef(fmt.Sprintf(`{"approverUserId":"%s"}`, f.assignee)))
	if err := f.submitErr(f.creator, own, map[string]any{"target": f.assignee}); !errors.Is(err, requestsapp.ErrNoEligibleApprover) {
		t.Errorf("approver named as beneficiary: %v", err)
	}
	// A Team whose only eligible members are the requester and the named User cannot decide either.
	team := f.item("teamaccess", f.userFieldDef(fmt.Sprintf(`{"approverTeamId":"%s"}`, f.team)))
	if err := f.submitErr(f.creator, team, map[string]any{"target": f.member}); !errors.Is(err, requestsapp.ErrNoEligibleApprover) {
		t.Errorf("team without an eligible member: %v", err)
	}
	// Naming someone else who is not an approver is fine.
	if err := f.submitErr(f.creator, own, map[string]any{"target": f.outsider}); err != nil {
		t.Errorf("unrelated beneficiary: %v", err)
	}
}

func TestOnePersonCannotDecideTwoSteps(t *testing.T) {
	f := newFlow(t)
	approvals := fmt.Sprintf(`{"approverUserId":"%s"},{"approverUserId":"%s"}`, f.assignee, f.assignee)
	item := f.item("twice", f.def(approvals, `{"title":"Provision"}`))
	r := f.submit(f.creator, item, answers())
	f.decide(f.assignee, r.ID, 0, "approve")
	f.dispatch()
	// Step 2 has nobody left who may decide: the request ends rejected with a recorded cause
	// instead of hanging in pending_approval.
	d := f.get(f.creator, r.ID, false)
	if d.Request.Status != requestsapp.StatusRejected || d.Request.StatusReason == nil || *d.Request.StatusReason != "no_eligible_approver" {
		t.Fatalf("request = %+v", d.Request)
	}
	if f.notified(f.creator, "request.rejected") != 1 {
		t.Error("the requester must be told")
	}
	var open int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM approvals.approvals WHERE subject_id = $1::uuid AND status = 'pending'`, r.ID).Scan(&open)
	if open != 0 {
		t.Errorf("%d approvals still pending", open)
	}
}

func TestLongCatalogTitleDoesNotBreakSubmission(t *testing.T) {
	f := newFlow(t)
	item := f.item("long", f.def(fmt.Sprintf(`{"approverUserId":"%s"}`, f.assignee), ""))
	long := make([]rune, 200)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE catalog.items SET title = $2 WHERE id = $1::uuid`, item, string(long)); err != nil {
		t.Fatal(err)
	}
	r := f.submit(f.creator, item, answers())
	if r.Status != requestsapp.StatusPendingApproval {
		t.Errorf("status = %s", r.Status)
	}
}
