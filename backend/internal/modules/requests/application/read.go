package application

import (
	"context"
	"fmt"
	"sort"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
)

// TaskView is a fulfillment task with its current state.
type TaskView struct {
	RequestTask
	Task taskspublic.Task
}

// Detail is a request with everything its viewers may see. Names resolves the
// ids of Users and Teams that appear in it.
type Detail struct {
	Request    Request
	Approvals  []approvalspublic.Approval
	Tasks      []TaskView
	References []catalogpublic.Reference
	Names      map[string]string
	// Actions lists the operations the caller may perform now (cancel,
	// put_on_hold, resume, complete), so clients need no copy of the rules.
	Actions []string
}

func (s *Service) canView(ctx context.Context, p Principal, r Request) (bool, error) {
	if p.View || p.Manage || r.RequesterID == p.UserID || r.RequestedForID == p.UserID {
		return true, nil
	}
	return s.approvals.CanView(ctx, SubjectType, r.ID, p.UserID)
}

// actions are the operations p may perform on r now; the operations check
// again when they run.
func actions(p Principal, r Request) []string {
	var out []string
	if r.Terminal() {
		return out
	}
	if p.Manage || (r.RequesterID == p.UserID && r.Status == StatusPendingApproval) {
		out = append(out, "cancel")
	}
	if p.Manage {
		switch r.Status {
		case StatusInFulfillment:
			out = append(out, "put_on_hold", "complete")
		case StatusWaiting:
			out = append(out, "resume", "complete")
		}
	}
	return out
}

// Get returns a request the caller may see: the requester, the requested-for
// User, an approver of one of its steps, or holders of requests.view/manage.
// Everybody else gets ErrNotFound.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	ok, err := s.canView(ctx, p, r)
	if err != nil {
		return Detail{}, fmt.Errorf("check access: %w", err)
	}
	if !ok {
		return Detail{}, ErrNotFound
	}
	d := Detail{Request: r, Actions: actions(p, r), Names: map[string]string{}}
	if d.Approvals, err = s.approvals.ForSubject(ctx, SubjectType, r.ID); err != nil {
		return Detail{}, fmt.Errorf("load approvals: %w", err)
	}
	if d.References, err = s.store.References(ctx, r.ID); err != nil {
		return Detail{}, err
	}
	rts, err := s.store.Tasks(ctx, r.ID)
	if err != nil {
		return Detail{}, err
	}
	ids := make([]string, 0, len(rts))
	for _, t := range rts {
		ids = append(ids, t.TaskID)
	}
	tasks, err := s.tasks.Tasks(ctx, ids)
	if err != nil {
		return Detail{}, fmt.Errorf("load tasks: %w", err)
	}
	byID := map[string]taskspublic.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	for _, rt := range rts {
		if t, ok := byID[rt.TaskID]; ok {
			// A result note marked for the requester is for the people who asked for the work and the request's
			// staff; approvers who can merely view the request do not get it.
			if !mayReadRequesterNotes(p, r) {
				t.ResultNote = nil
			}
			d.Tasks = append(d.Tasks, TaskView{RequestTask: rt, Task: t})
		}
	}
	return d, s.resolveNames(ctx, &d)
}

// mayReadRequesterNotes reports whether the caller gets the task result notes marked for the requester: the
// requester, the requested-for User and the request's staff. Approvers who only view the request do not.
func mayReadRequesterNotes(p Principal, r Request) bool {
	return p.View || p.Manage || r.RequesterID == p.UserID || r.RequestedForID == p.UserID
}

func (s *Service) resolveNames(ctx context.Context, d *Detail) error {
	users := map[string]struct{}{d.Request.RequesterID: {}, d.Request.RequestedForID: {}}
	teams := map[string]struct{}{}
	addU := func(v *string) {
		if v != nil {
			users[*v] = struct{}{}
		}
	}
	addT := func(v *string) {
		if v != nil {
			teams[*v] = struct{}{}
		}
	}
	for _, a := range d.Approvals {
		addU(a.ApproverUserID)
		addT(a.ApproverTeamID)
		addU(a.DecidedByUserID)
	}
	for _, t := range d.Tasks {
		addU(t.Task.AssignedUserID)
		addT(t.Task.AssignedTeamID)
	}
	for _, ref := range d.References {
		if ref.Type == "user" {
			users[ref.ID] = struct{}{}
		}
	}
	keys := func(m map[string]struct{}) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	un, err := s.dir.UserNames(ctx, keys(users))
	if err != nil {
		return fmt.Errorf("resolve user names: %w", err)
	}
	tn, err := s.dir.TeamNames(ctx, keys(teams))
	if err != nil {
		return fmt.Errorf("resolve team names: %w", err)
	}
	for k, v := range un {
		d.Names[k] = v
	}
	for k, v := range tn {
		d.Names[k] = v
	}
	return nil
}

// List returns the caller's own requests (as requester or requested-for
// User), newest first; all=true lists every request and needs requests.view
// or requests.manage.
func (s *Service) List(ctx context.Context, p Principal, all bool, status string, page Page) (Result, error) {
	if all && !p.View && !p.Manage {
		return Result{}, ErrForbidden
	}
	if status != "" {
		switch status {
		case StatusPendingApproval, StatusInFulfillment, StatusWaiting, StatusCompleted, StatusRejected, StatusCancelled:
		default:
			return Result{}, invalid("status must be one of pending_approval, in_fulfillment, waiting, completed, rejected, cancelled")
		}
	}
	return s.store.List(ctx, ListQuery{UserID: p.UserID, All: all, Status: status, Page: page.Normalize()})
}
