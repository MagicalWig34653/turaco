package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Audit actions: requests.request.submitted, .approved, .rejected, .completed,
// .cancelled, .put_on_hold, .resumed. Answers and titles are never copied
// into the audit log; ids, statuses and (for cancel and manual completion)
// the given reason are.

// systemActor performs the changes the workflow makes by itself (approval
// decided, task finished).
var systemActor = audit.SystemActor("request-workflow")

// Service drives service requests.
type Service struct {
	store     Store
	catalog   Catalog
	approvals Approvals
	tasks     Tasks
	dir       Directory
	users     catalogpublic.UserLookup
	products  catalogpublic.ProductLookup
	now       func() time.Time
}

// NewService creates the service. users and products validate answers;
// now may be nil.
func NewService(store Store, catalog Catalog, approvals Approvals, tasks Tasks, dir Directory,
	users catalogpublic.UserLookup, products catalogpublic.ProductLookup, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, catalog: catalog, approvals: approvals, tasks: tasks, dir: dir, users: users, products: products, now: now}
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func (s *Service) record(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Request, meta map[string]any) error {
	state := func(r *Request) any {
		if r == nil {
			return nil
		}
		return map[string]any{"status": r.Status, "waitingReason": r.WaitingReason, "currentStep": r.CurrentStep, "version": r.Version}
	}
	if len(meta) == 0 {
		meta = nil
	}
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "service_request", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: state(before), After: state(after), Metadata: meta,
	})
}

func approvalsCaller(c Caller) approvalspublic.Caller {
	return approvalspublic.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}
}

func tasksCaller(c Caller) taskspublic.Caller {
	return taskspublic.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}
}

// ---- submit ----

// SubmitInput is the input of Submit.
type SubmitInput struct {
	CatalogItemID string
	// RequestedForID is the User the request is for; nil means the requester.
	RequestedForID *string
	Answers        map[string]any
}

// resolvedStep is an approval step reduced to a concrete approver.
type resolvedStep struct {
	user *string
	team *string
}

// resolveSteps reduces every approval step of the snapshot to a concrete
// approver and refuses steps nobody eligible could decide (the requester and
// the requested-for User never decide).
func (s *Service) resolveSteps(ctx context.Context, d catalogpublic.Definition, requester, requestedFor string) ([]resolvedStep, error) {
	steps := make([]resolvedStep, 0, len(d.Approvals))
	for i, st := range d.Approvals {
		switch {
		case st.ApproverTeamID != nil:
			steps = append(steps, resolvedStep{team: st.ApproverTeamID})
		case st.ApproverUserID != nil:
			if *st.ApproverUserID == requester || *st.ApproverUserID == requestedFor {
				return nil, fmt.Errorf("%w: step %d approver is the requester", ErrNoEligibleApprover, i+1)
			}
			steps = append(steps, resolvedStep{user: st.ApproverUserID})
		case st.Approver == "manager":
			m, err := s.dir.ManagerIDs(ctx, []string{requestedFor})
			if err != nil {
				return nil, fmt.Errorf("resolve manager: %w", err)
			}
			manager, ok := m[requestedFor]
			if !ok || manager == requester || manager == requestedFor {
				return nil, fmt.Errorf("%w: step %d needs a manager other than the requester", ErrNoEligibleApprover, i+1)
			}
			steps = append(steps, resolvedStep{user: &manager})
		default:
			return nil, fmt.Errorf("%w: step %d has no approver", ErrNoEligibleApprover, i+1)
		}
	}
	return steps, nil
}

// Submit creates a request for a catalog item. Every signed-in User may
// submit; the answers are validated against the definition, the definition
// and the answers are snapshotted and the first approval step (or, without
// approval steps, fulfillment) starts in the same transaction.
func (s *Service) Submit(ctx context.Context, c Caller, in SubmitInput) (Request, error) {
	if err := c.validate(); err != nil {
		return Request{}, err
	}
	requester := c.Actor.UserID
	if requester == "" {
		return Request{}, ErrForbidden
	}
	sub, err := s.catalog.ForSubmission(ctx, in.CatalogItemID)
	if err != nil {
		if errors.Is(err, catalogpublic.ErrNotFound) {
			return Request{}, ErrNotFound
		}
		return Request{}, err
	}
	if !sub.Active {
		return Request{}, ErrItemInactive
	}
	requestedFor := requester
	if in.RequestedForID != nil && !strings.EqualFold(*in.RequestedForID, requester) {
		if !sub.Definition.AllowRequestedFor {
			return Request{}, ErrRequestedForInvalid
		}
		requestedFor = strings.ToLower(*in.RequestedForID)
	}
	active, err := s.dir.ActiveUsers(ctx, []string{requester, requestedFor})
	if err != nil {
		return Request{}, fmt.Errorf("check users: %w", err)
	}
	if !active[requester] || !active[requestedFor] {
		return Request{}, ErrRequestedForInvalid
	}
	answers, refs, err := catalogpublic.ValidateAnswers(ctx, sub.Definition, in.Answers, s.users, s.products)
	if err != nil {
		return Request{}, err
	}
	steps, err := s.resolveSteps(ctx, sub.Definition, requester, requestedFor)
	if err != nil {
		return Request{}, err
	}

	var out Request
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		n := NewRequest{
			CatalogItemID: sub.ID, CatalogItemKey: sub.Key, CatalogItemTitle: sub.Title, Snapshot: sub.Snapshot,
			Answers: answers, RequesterID: requester, RequestedForID: requestedFor, Status: StatusInFulfillment,
		}
		if len(steps) > 0 {
			zero := 0
			n.Status, n.CurrentStep = StatusPendingApproval, &zero
		}
		r, err := s.store.InsertTx(ctx, tx, n)
		if err != nil {
			return err
		}
		if err := s.store.AddReferencesTx(ctx, tx, r.ID, refs); err != nil {
			return err
		}
		if err := s.record(ctx, tx, c, "requests.request.submitted", nil, &r, nil); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, "ServiceRequestSubmitted", map[string]any{"requestId": r.ID}); err != nil {
			return err
		}
		if len(steps) > 0 {
			if err := s.requestApproval(ctx, tx, c, r, 0, steps[0]); err != nil {
				return err
			}
			out = r
			return nil
		}
		out, err = s.startFulfillment(ctx, tx, c, r)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return out, nil
}

func (s *Service) requestApproval(ctx context.Context, tx pgx.Tx, c Caller, r Request, step int, st resolvedStep) error {
	requester := r.RequesterID
	_, err := s.approvals.RequestInTx(ctx, tx, approvalsCaller(c), approvalspublic.Request{
		SubjectType: SubjectType, SubjectID: r.ID, SubjectLabel: r.Label(), StepIndex: step,
		ApproverUserID: st.user, ApproverTeamID: st.team,
		ExcludedUserIDs: excludedUsers(r), RequestedBy: &requester,
	})
	return err
}

func excludedUsers(r Request) []string {
	if r.RequestedForID == r.RequesterID {
		return []string{r.RequesterID}
	}
	return []string{r.RequesterID, r.RequestedForID}
}

// startFulfillment moves an approved (or approval-free) request into
// fulfillment: it creates the template tasks and, without any, completes the
// request at once. It runs in the caller's transaction and returns the
// updated request.
func (s *Service) startFulfillment(ctx context.Context, tx pgx.Tx, c Caller, r Request) (Request, error) {
	before := r
	now := s.now().UTC()
	r.Status, r.CurrentStep, r.WaitingReason = StatusInFulfillment, nil, nil
	r.StatusReason = nil
	if err := publish(ctx, tx, c, "ServiceRequestApproved", map[string]any{"requestId": r.ID}); err != nil {
		return Request{}, err
	}
	var requested []string
	for _, t := range r.Definition.Fulfillment {
		requested = append(requested, derefs(t.AssignedUserID, t.AssignedTeamID)...)
	}
	activeUsers, err := s.dir.ActiveUsers(ctx, requested)
	if err != nil {
		return Request{}, fmt.Errorf("check task assignees: %w", err)
	}
	activeTeams, err := s.dir.ActiveTeams(ctx, requested)
	if err != nil {
		return Request{}, fmt.Errorf("check task assignees: %w", err)
	}
	taskCaller := Caller{Actor: systemActor, CorrelationID: fmt.Sprintf("request:%s:fulfillment", r.ID)}
	for i, t := range r.Definition.Fulfillment {
		in := taskspublic.CreateInput{
			Title: t.Title + " – " + r.Reference, Description: t.Description, Priority: t.Priority,
			ContextType: SubjectType, ContextID: r.ID,
		}
		// An assignee who is no longer active must not block the request: the task is created unassigned.
		if t.AssignedUserID != nil && activeUsers[*t.AssignedUserID] {
			in.AssignedUserID = t.AssignedUserID
		}
		if t.AssignedTeamID != nil && activeTeams[*t.AssignedTeamID] {
			in.AssignedTeamID = t.AssignedTeamID
		}
		if t.DueAfterHours != nil {
			due := now.Add(time.Duration(*t.DueAfterHours) * time.Hour)
			in.DueAt = &due
		}
		id, err := s.tasks.CreateInTx(ctx, tx, tasksCaller(taskCaller), in)
		if err != nil {
			return Request{}, fmt.Errorf("create fulfillment task %d: %w", i+1, err)
		}
		if err := s.store.AddTaskTx(ctx, tx, r.ID, RequestTask{TaskID: id, TemplateIndex: i, Mandatory: t.IsMandatory()}); err != nil {
			return Request{}, err
		}
	}
	if len(r.Definition.Fulfillment) == 0 {
		r.Status, r.CompletedAt = StatusCompleted, &now
	}
	out, err := s.store.UpdateTx(ctx, tx, r)
	if err != nil {
		return Request{}, err
	}
	meta := map[string]any{"tasks": len(r.Definition.Fulfillment)}
	if err := s.record(ctx, tx, Caller{Actor: systemActor, CorrelationID: c.CorrelationID}, "requests.request.approved", &before, &out, meta); err != nil {
		return Request{}, err
	}
	if out.Status == StatusCompleted {
		if err := s.record(ctx, tx, Caller{Actor: systemActor, CorrelationID: c.CorrelationID}, "requests.request.completed", &r, &out, map[string]any{"cause": "no_fulfillment_tasks"}); err != nil {
			return Request{}, err
		}
		if err := publish(ctx, tx, Caller{Actor: systemActor, CorrelationID: c.CorrelationID}, "ServiceRequestCompleted", map[string]any{"requestId": r.ID}); err != nil {
			return Request{}, err
		}
	}
	return out, nil
}

func derefs(vs ...*string) []string {
	var out []string
	for _, v := range vs {
		if v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// ---- workflow reactions (outbox consumers) ----

// OnApprovalDecided advances or ends a request when one of its approval steps
// was decided. It runs in the dispatcher's claim transaction and is
// idempotent: stale and duplicate events (the step is no longer the current
// one) change nothing.
func (s *Service) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, p ApprovalDecidedPayload) error {
	if p.SubjectType != SubjectType {
		return nil
	}
	r, err := s.store.LockTx(ctx, tx, p.SubjectID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Status != StatusPendingApproval || r.CurrentStep == nil || *r.CurrentStep != p.StepIndex {
		return nil
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	before := r
	if p.Decision == "reject" {
		r.Status, r.CurrentStep = StatusRejected, nil
		out, err := s.store.UpdateTx(ctx, tx, r)
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, c, "requests.request.rejected", &before, &out, nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "ServiceRequestRejected", map[string]any{"requestId": r.ID})
	}
	next := p.StepIndex + 1
	if next >= len(r.Definition.Approvals) {
		_, err := s.startFulfillment(ctx, tx, c, r)
		return err
	}
	steps, err := s.resolveSteps(ctx, r.Definition, r.RequesterID, r.RequestedForID)
	if err != nil {
		return events.Permanent(fmt.Errorf("resolve next approval step of %s: %w", r.Reference, err))
	}
	r.CurrentStep = &next
	out, err := s.store.UpdateTx(ctx, tx, r)
	if err != nil {
		return err
	}
	if err := s.requestApproval(ctx, tx, c, out, next, steps[next]); err != nil {
		return events.Permanent(fmt.Errorf("request next approval step of %s: %w", r.Reference, err))
	}
	return s.record(ctx, tx, c, "requests.request.approval_step_advanced", &before, &out, map[string]any{"step": next})
}

// ApprovalDecidedPayload is the payload of the ApprovalDecided event.
type ApprovalDecidedPayload struct {
	ApprovalID  string `json:"approvalId"`
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	StepIndex   int    `json:"stepIndex"`
	Decision    string `json:"decision"`
}

// OnTaskFinished completes a request when its last fulfillment task finished
// (completed, or optional tasks cancelled). Tasks of other records, stale
// events and requests that are no longer in fulfillment change nothing.
func (s *Service) OnTaskFinished(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, taskID string) error {
	requestID, err := s.store.RequestOfTaskTx(ctx, tx, taskID)
	if err != nil || requestID == "" {
		return err
	}
	r, err := s.store.LockTx(ctx, tx, requestID)
	if err != nil {
		return err
	}
	if r.Status != StatusInFulfillment && r.Status != StatusWaiting {
		return nil
	}
	done, err := s.fulfillmentDone(ctx, tx, r)
	if err != nil || !done {
		return err
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	return s.complete(ctx, tx, c, r, map[string]any{"cause": "tasks_completed"})
}

// fulfillmentDone reports whether every mandatory task is completed and every
// optional task is finished (completed or cancelled).
func (s *Service) fulfillmentDone(ctx context.Context, tx pgx.Tx, r Request) (bool, error) {
	tasks, err := s.store.TasksTx(ctx, tx, r.ID)
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	st, err := s.tasks.StatusesInTx(ctx, tx, ids)
	if err != nil {
		return false, fmt.Errorf("task statuses: %w", err)
	}
	for _, t := range tasks {
		status := st[t.TaskID]
		switch {
		case t.Mandatory && status != taskspublic.StatusCompleted:
			return false, nil
		case !t.Mandatory && status != taskspublic.StatusCompleted && status != taskspublic.StatusCancelled:
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) complete(ctx context.Context, tx pgx.Tx, c Caller, r Request, meta map[string]any) error {
	before := r
	now := s.now().UTC()
	r.Status, r.WaitingReason, r.CompletedAt = StatusCompleted, nil, &now
	out, err := s.store.UpdateTx(ctx, tx, r)
	if err != nil {
		return err
	}
	if err := s.record(ctx, tx, c, "requests.request.completed", &before, &out, meta); err != nil {
		return err
	}
	return publish(ctx, tx, c, "ServiceRequestCompleted", map[string]any{"requestId": r.ID})
}

// ---- manager and requester operations ----

func cleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxReasonLen || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("reason must be 1-%d characters without control or invisible formatting characters", maxReasonLen)
	}
	return s, nil
}

// mutate locks the request, checks the version and runs fn in one transaction.
func (s *Service) mutate(ctx context.Context, id string, expected *int, fn func(tx pgx.Tx, r Request) (Request, error)) (Request, error) {
	var out Request
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		r, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != r.Version {
			return ErrVersionConflict
		}
		out, err = fn(tx, r)
		return err
	})
	if err != nil {
		return Request{}, err
	}
	return out, nil
}

// Cancel cancels a request: its pending approvals and unfinished tasks are
// cancelled with it. The requester may cancel while approval is pending;
// requests.manage may cancel any unfinished request. A reason is required.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Request, error) {
	if err := c.validate(); err != nil {
		return Request{}, err
	}
	reason, err := cleanReason(reason)
	if err != nil {
		return Request{}, err
	}
	return s.mutate(ctx, id, expected, func(tx pgx.Tx, r Request) (Request, error) {
		own := r.RequesterID == p.UserID
		if !p.Manage && !own && !p.View {
			return Request{}, ErrNotFound
		}
		if r.Terminal() {
			return Request{}, &InvalidTransitionError{Operation: "cancel", From: r.Status}
		}
		if !p.Manage && !(own && r.Status == StatusPendingApproval) {
			return Request{}, ErrForbidden
		}
		before := r
		if _, err := s.approvals.CancelBySubjectInTx(ctx, tx, approvalsCaller(c), SubjectType, r.ID); err != nil {
			return Request{}, fmt.Errorf("cancel approvals: %w", err)
		}
		if _, err := s.tasks.CancelByContextInTx(ctx, tx, tasksCaller(c), SubjectType, r.ID, "request cancelled: "+reason); err != nil {
			return Request{}, fmt.Errorf("cancel tasks: %w", err)
		}
		r.Status, r.CurrentStep, r.WaitingReason, r.StatusReason = StatusCancelled, nil, nil, &reason
		out, err := s.store.UpdateTx(ctx, tx, r)
		if err != nil {
			return Request{}, err
		}
		if err := s.record(ctx, tx, c, "requests.request.cancelled", &before, &out, map[string]any{"reason": reason}); err != nil {
			return Request{}, err
		}
		return out, publish(ctx, tx, c, "ServiceRequestCancelled", map[string]any{"requestId": r.ID})
	})
}

// PutOnHold moves a request in fulfillment to waiting with a reason code.
// Requires requests.manage.
func (s *Service) PutOnHold(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Request, error) {
	if err := c.validate(); err != nil {
		return Request{}, err
	}
	if !p.Manage {
		return Request{}, ErrForbidden
	}
	ok := false
	for _, w := range WaitingReasons {
		ok = ok || w == reason
	}
	if !ok {
		return Request{}, invalid("waiting reason must be one of %s", strings.Join(WaitingReasons, ", "))
	}
	return s.mutate(ctx, id, expected, func(tx pgx.Tx, r Request) (Request, error) {
		if r.Status != StatusInFulfillment {
			return Request{}, &InvalidTransitionError{Operation: "put_on_hold", From: r.Status}
		}
		before := r
		r.Status, r.WaitingReason = StatusWaiting, &reason
		out, err := s.store.UpdateTx(ctx, tx, r)
		if err != nil {
			return Request{}, err
		}
		return out, s.record(ctx, tx, c, "requests.request.put_on_hold", &before, &out, map[string]any{"waitingReason": reason})
	})
}

// Resume moves a waiting request back to fulfillment. Requires requests.manage.
func (s *Service) Resume(ctx context.Context, c Caller, p Principal, id string, expected *int) (Request, error) {
	if err := c.validate(); err != nil {
		return Request{}, err
	}
	if !p.Manage {
		return Request{}, ErrForbidden
	}
	return s.mutate(ctx, id, expected, func(tx pgx.Tx, r Request) (Request, error) {
		if r.Status != StatusWaiting {
			return Request{}, &InvalidTransitionError{Operation: "resume", From: r.Status}
		}
		before := r
		r.Status, r.WaitingReason = StatusInFulfillment, nil
		out, err := s.store.UpdateTx(ctx, tx, r)
		if err != nil {
			return Request{}, err
		}
		if done, err := s.fulfillmentDone(ctx, tx, out); err != nil {
			return Request{}, err
		} else if done {
			// Tasks finished while the request waited: nothing reacted, so complete now.
			return out, s.complete(ctx, tx, Caller{Actor: systemActor, CorrelationID: c.CorrelationID}, out, map[string]any{"cause": "tasks_completed"})
		}
		return out, s.record(ctx, tx, c, "requests.request.resumed", &before, &out, nil)
	})
}

// Complete completes a request manually. Every task must be finished; when a
// mandatory task was cancelled instead of completed, a reason is required.
// Requires requests.manage.
func (s *Service) Complete(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Request, error) {
	if err := c.validate(); err != nil {
		return Request{}, err
	}
	if !p.Manage {
		return Request{}, ErrForbidden
	}
	return s.mutate(ctx, id, expected, func(tx pgx.Tx, r Request) (Request, error) {
		if r.Status != StatusInFulfillment && r.Status != StatusWaiting {
			return Request{}, &InvalidTransitionError{Operation: "complete", From: r.Status}
		}
		tasks, err := s.store.TasksTx(ctx, tx, r.ID)
		if err != nil {
			return Request{}, err
		}
		ids := make([]string, 0, len(tasks))
		for _, t := range tasks {
			ids = append(ids, t.TaskID)
		}
		st, err := s.tasks.StatusesInTx(ctx, tx, ids)
		if err != nil {
			return Request{}, fmt.Errorf("task statuses: %w", err)
		}
		needsReason := false
		for _, t := range tasks {
			status := st[t.TaskID]
			if status != taskspublic.StatusCompleted && status != taskspublic.StatusCancelled {
				return Request{}, invalid("all fulfillment tasks must be finished before the request can be completed")
			}
			if t.Mandatory && status != taskspublic.StatusCompleted {
				needsReason = true
			}
		}
		meta := map[string]any{"cause": "manual"}
		if needsReason {
			cleaned, err := cleanReason(reason)
			if err != nil {
				return Request{}, invalid("a mandatory task was cancelled: a reason is required to complete the request")
			}
			meta["reason"] = cleaned
		}
		before := r
		_ = before
		if err := s.complete(ctx, tx, c, r, meta); err != nil {
			return Request{}, err
		}
		return s.store.LockTx(ctx, tx, r.ID)
	})
}
