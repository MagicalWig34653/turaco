package application

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Task mutations. Each runs in one transaction with its audit event and
// outbox events (Store). Authorization is decided here on the locked, current
// task so a concurrent reassignment cannot be raced.
//
// Audit actions: tasks.task.created, .details_updated, .assigned,
// .unassigned, .started, .blocked, .unblocked, .completed, .cancelled,
// .reopened. Audit states carry status, priority, assignees, due date and
// version; titles and descriptions are never copied into the audit log.

// CreateInput is the input of Create.
type CreateInput struct {
	Title          string
	Description    string
	Priority       string // empty means normal
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
}

// Create creates an open task. Requires tasks.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in CreateInput) (TaskView, error) {
	if err := c.validate(); err != nil {
		return TaskView{}, err
	}
	if !p.Manage {
		return TaskView{}, ErrForbidden
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return TaskView{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return TaskView{}, err
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityNormal
	}
	if err := validPriority(priority); err != nil {
		return TaskView{}, err
	}
	if err := s.checkAssignees(ctx, in.AssignedUserID, in.AssignedTeamID); err != nil {
		return TaskView{}, err
	}
	// System actors (recurrence generation) create tasks without a creating User.
	var createdBy *string
	if c.Actor.UserID != "" {
		createdBy = &c.Actor.UserID
	}
	t, err := s.store.Insert(ctx, c, NewTask{
		Title: title, Description: desc, Priority: priority, DueAt: utc(in.DueAt),
		AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID, CreatedBy: createdBy,
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.viewOne(ctx, t)
}

// UpdateInput changes details; nil fields stay unchanged.
type UpdateInput struct {
	Title       *string
	Description *string // empty clears
	Priority    *string
	DueAt       *time.Time
	ClearDueAt  bool
}

func (in UpdateInput) empty() bool {
	return in.Title == nil && in.Description == nil && in.Priority == nil && in.DueAt == nil && !in.ClearDueAt
}

// UpdateDetails changes title, description, priority and/or due date of an
// unfinished task. Requires tasks.manage.
func (s *Service) UpdateDetails(ctx context.Context, c Caller, p Principal, id string, expected *int, in UpdateInput) (TaskView, error) {
	if err := c.validate(); err != nil {
		return TaskView{}, err
	}
	if in.empty() {
		return TaskView{}, invalid("at least one field to change is required")
	}
	var title *string
	if in.Title != nil {
		t, err := cleanTitle(*in.Title)
		if err != nil {
			return TaskView{}, err
		}
		title = &t
	}
	var desc *string
	if in.Description != nil {
		d, err := cleanDescription(*in.Description)
		if err != nil {
			return TaskView{}, err
		}
		desc = d
	}
	if in.Priority != nil {
		if err := validPriority(*in.Priority); err != nil {
			return TaskView{}, err
		}
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return TaskView{}, err
	}
	t, err := s.store.Change(ctx, c, id, func(cur Task) (Change, error) {
		if err := authorizeChange(a, cur, expected, true); err != nil {
			return Change{}, err
		}
		if cur.Terminal() {
			return Change{}, &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		next := cur
		var changed []string
		if title != nil && *title != cur.Title {
			next.Title = *title
			changed = append(changed, "title")
		}
		if in.Description != nil && !equalStrPtr(desc, cur.Description) {
			next.Description = desc
			changed = append(changed, "description")
		}
		if in.Priority != nil && *in.Priority != cur.Priority {
			next.Priority = *in.Priority
			changed = append(changed, "priority")
		}
		if in.ClearDueAt && cur.DueAt != nil {
			next.DueAt = nil
			changed = append(changed, "dueAt")
		} else if in.DueAt != nil && !equalTimePtr(utc(in.DueAt), cur.DueAt) {
			next.DueAt = utc(in.DueAt)
			changed = append(changed, "dueAt")
		}
		if len(changed) == 0 {
			return Change{NoChange: true, Next: cur}, nil
		}
		return Change{Next: next, Action: "tasks.task.details_updated", Metadata: map[string]any{"changedFields": changed}}, nil
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.viewOne(ctx, t)
}

// Assign sets the assigned User and/or Team (nil clears that side); at least
// one must be set, use Unassign to clear both. Requires tasks.manage.
func (s *Service) Assign(ctx context.Context, c Caller, p Principal, id string, expected *int, userID, teamID *string) (TaskView, error) {
	if err := c.validate(); err != nil {
		return TaskView{}, err
	}
	if userID == nil && teamID == nil {
		return TaskView{}, invalid("a user or a team is required; use unassign to clear the assignment")
	}
	if err := s.checkAssignees(ctx, userID, teamID); err != nil {
		return TaskView{}, err
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return TaskView{}, err
	}
	t, err := s.store.Change(ctx, c, id, func(cur Task) (Change, error) {
		if err := authorizeChange(a, cur, expected, true); err != nil {
			return Change{}, err
		}
		if cur.Terminal() {
			return Change{}, &InvalidTransitionError{Operation: "assign", From: cur.Status}
		}
		if equalStrPtr(userID, cur.AssignedUserID) && equalStrPtr(teamID, cur.AssignedTeamID) {
			return Change{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.AssignedUserID, next.AssignedTeamID = userID, teamID
		return Change{Next: next, Action: "tasks.task.assigned", Events: []Event{assignedEvent(cur, next)}}, nil
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.viewOne(ctx, t)
}

// Unassign clears both assignments. Requires tasks.manage.
func (s *Service) Unassign(ctx context.Context, c Caller, p Principal, id string, expected *int) (TaskView, error) {
	if err := c.validate(); err != nil {
		return TaskView{}, err
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return TaskView{}, err
	}
	t, err := s.store.Change(ctx, c, id, func(cur Task) (Change, error) {
		if err := authorizeChange(a, cur, expected, true); err != nil {
			return Change{}, err
		}
		if cur.Terminal() {
			return Change{}, &InvalidTransitionError{Operation: "unassign", From: cur.Status}
		}
		if cur.AssignedUserID == nil && cur.AssignedTeamID == nil {
			return Change{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.AssignedUserID, next.AssignedTeamID = nil, nil
		return Change{Next: next, Action: "tasks.task.unassigned"}, nil
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.viewOne(ctx, t)
}

// Transition performs a lifecycle operation. reason is required for block,
// cancel and reopen and ignored otherwise. start, block, unblock and complete
// need tasks.manage or tasks.work on a task assigned to the caller or its
// Teams; cancel and reopen need tasks.manage.
func (s *Service) Transition(ctx context.Context, c Caller, p Principal, id string, expected *int, op Operation, reason string) (TaskView, error) {
	return s.transition(ctx, c, p, id, expected, op, reason, "", false)
}

// Complete finishes a task like Transition with OpComplete and stores the optional result note (at most 1000
// characters of plain text without control or invisible formatting characters) as its closing comment. The note
// is shown with the task and kept only while the task is completed; it is not copied into audit events or
// notifications (they carry only whether a note exists).
func (s *Service) Complete(ctx context.Context, c Caller, p Principal, id string, expected *int, resultNote string) (TaskView, error) {
	return s.transition(ctx, c, p, id, expected, OpComplete, "", resultNote, false)
}

// CompleteWithVisibility is Complete with the choice to show the result note to the requester of the request the
// task belongs to (it needs a note). The default (Complete) keeps the note internal.
func (s *Service) CompleteWithVisibility(ctx context.Context, c Caller, p Principal, id string, expected *int, resultNote string, forRequester bool) (TaskView, error) {
	return s.transition(ctx, c, p, id, expected, OpComplete, "", resultNote, forRequester)
}

func (s *Service) transition(ctx context.Context, c Caller, p Principal, id string, expected *int, op Operation, reason, resultNote string, forRequester bool) (TaskView, error) {
	if err := c.validate(); err != nil {
		return TaskView{}, err
	}
	var note *string
	if strings.TrimSpace(resultNote) != "" {
		if op != OpComplete {
			return TaskView{}, invalid("a result note is only valid when completing a task")
		}
		n, err := cleanResultNote(resultNote)
		if err != nil {
			return TaskView{}, err
		}
		note = &n
	}
	if forRequester && note == nil {
		return TaskView{}, invalid("a result note is required to show it to the requester")
	}
	tr, ok := transitions[op]
	if !ok {
		return TaskView{}, fmt.Errorf("tasks: unknown operation %q", op)
	}
	var cleaned string
	if tr.reason {
		r, err := cleanReason(reason)
		if err != nil {
			return TaskView{}, err
		}
		cleaned = r
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return TaskView{}, err
	}
	t, err := s.store.Change(ctx, c, id, func(cur Task) (Change, error) {
		if err := authorizeChange(a, cur, expected, !tr.workable); err != nil {
			return Change{}, err
		}
		to, err := next(op, cur.Status)
		if err != nil {
			return Change{}, err
		}
		nx := cur
		nx.Status = to
		nx.StatusReason, nx.CompletedAt, nx.CompletedByUserID, nx.ResultNote, nx.ResultNoteForRequester = nil, nil, nil, nil, false
		meta := map[string]any{}
		var events []Event
		switch op {
		case OpBlock, OpCancel:
			nx.StatusReason = &cleaned
			meta["reason"] = cleaned
		case OpReopen:
			meta["reason"] = cleaned
		case OpComplete:
			now := s.now().UTC().Truncate(time.Microsecond)
			by := c.Actor.UserID
			nx.CompletedAt = &now
			if by != "" {
				nx.CompletedByUserID = &by
			}
			nx.ResultNote, nx.ResultNoteForRequester = note, forRequester && note != nil
			meta["hasResultNote"] = note != nil
			meta["noteForRequester"] = nx.ResultNoteForRequester
			events = append(events, Event{Type: "TaskCompleted", Payload: map[string]any{
				"taskId": cur.ID, "completedByUserId": nx.CompletedByUserID,
			}})
		}
		if op == OpCancel {
			events = append(events, Event{Type: "TaskCancelled", Payload: map[string]any{"taskId": cur.ID}})
		}
		return Change{Next: nx, Action: "tasks.task." + auditSuffix[op], Metadata: meta, Events: events}, nil
	})
	if err != nil {
		return TaskView{}, err
	}
	return s.viewOne(ctx, t)
}

var auditSuffix = map[Operation]string{
	OpStart: "started", OpBlock: "blocked", OpUnblock: "unblocked",
	OpComplete: "completed", OpCancel: "cancelled", OpReopen: "reopened",
}

// authorizeChange checks visibility, the required authority and the optional
// expected version, in that order: a task the caller cannot see does not
// exist for it.
func authorizeChange(a access, cur Task, expected *int, managerOnly bool) error {
	if !a.canSee(cur) {
		return ErrNotFound
	}
	if managerOnly && !a.p.Manage {
		return ErrForbidden
	}
	if !managerOnly && !a.canWork(cur) {
		return ErrForbidden
	}
	if expected != nil && *expected != cur.Version {
		return ErrVersionConflict
	}
	return nil
}

func assignedEvent(before, after Task) Event {
	return Event{Type: "TaskAssigned", Payload: map[string]any{
		"taskId":         after.ID,
		"assignedUserId": after.AssignedUserID,
		"assignedTeamId": after.AssignedTeamID,
		"previousUserId": before.AssignedUserID,
		"previousTeamId": before.AssignedTeamID,
	}}
}

func equalStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// TaskDraft is the unvalidated input of NormalizeNewTask.
type TaskDraft struct {
	Title          string
	Description    string
	Priority       string // empty means normal
	DueAt          *time.Time
	AssignedUserID *string
	AssignedTeamID *string
}

// NormalizeNewTask validates and normalizes a task draft (title, description,
// priority, due date in UTC); it does not check assignees.
func NormalizeNewTask(d TaskDraft) (NewTask, error) {
	title, err := cleanTitle(d.Title)
	if err != nil {
		return NewTask{}, err
	}
	desc, err := cleanDescription(d.Description)
	if err != nil {
		return NewTask{}, err
	}
	priority := d.Priority
	if priority == "" {
		priority = PriorityNormal
	}
	if err := validPriority(priority); err != nil {
		return NewTask{}, err
	}
	return NewTask{
		Title: title, Description: desc, Priority: priority, DueAt: utc(d.DueAt),
		AssignedUserID: d.AssignedUserID, AssignedTeamID: d.AssignedTeamID,
	}, nil
}

// CheckAssignees verifies that the referenced User and Team are active.
func CheckAssignees(ctx context.Context, dir Directory, userID, teamID *string) error {
	return checkAssignees(ctx, dir, userID, teamID)
}
