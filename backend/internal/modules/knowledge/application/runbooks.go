package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Runbook execution statuses.
const (
	ExecRunning   = "running"
	ExecCompleted = "completed"
	ExecCancelled = "cancelled"

	maxSteps     = 30
	maxStepTitle = 150
	maxStepDesc  = 2000
	// ContextTicket is the only record kind an execution can belong to.
	ContextTicket = "ticket"
)

// Step is one step of a runbook; executing the runbook creates one Task per step.
type Step struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// TeamID is the Team the step's task is assigned to (optional).
	TeamID string `json:"teamId,omitempty"`
}

// Runbook is a reusable procedure.
type Runbook struct {
	ID          string
	Reference   string
	Title       string
	Description string
	Steps       []Step
	Active      bool
	CreatedBy   *string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Execution is one run of a runbook.
type Execution struct {
	ID           string
	RunbookID    string
	RunbookTitle string
	Steps        []Step
	ContextType  *string
	ContextID    *string
	Status       string
	StartedBy    *string
	FinishedAt   *time.Time
	TaskIDs      []string
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RunbookStore is the persistence port of runbooks and their executions.
type RunbookStore interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	InsertRunbookTx(ctx context.Context, tx pgx.Tx, r Runbook) (Runbook, error)
	LockRunbookTx(ctx context.Context, tx pgx.Tx, id string) (Runbook, error)
	UpdateRunbookTx(ctx context.Context, tx pgx.Tx, r Runbook) (Runbook, error)
	GetRunbook(ctx context.Context, id string) (Runbook, error)
	ListRunbooks(ctx context.Context, activeOnly bool, page Page) (RunbookResult, error)

	InsertExecutionTx(ctx context.Context, tx pgx.Tx, e Execution) (Execution, error)
	LockExecutionTx(ctx context.Context, tx pgx.Tx, id string) (Execution, error)
	UpdateExecutionTx(ctx context.Context, tx pgx.Tx, e Execution) (Execution, error)
	AddExecutionTaskTx(ctx context.Context, tx pgx.Tx, executionID, taskID string, step int) error
	ExecutionTasksTx(ctx context.Context, tx pgx.Tx, executionID string) ([]string, error)
	ExecutionOfTaskTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error)
	GetExecution(ctx context.Context, id string) (Execution, error)
	ListExecutions(ctx context.Context, runbookID, contextID string, page Page) (ExecutionResult, error)
}

// RunbookResult and ExecutionResult are pages.
type (
	RunbookResult struct {
		Items      []Runbook
		NextCursor string
	}
	ExecutionResult struct {
		Items      []Execution
		NextCursor string
	}
)

// TaskPort is the Tasks contract runbook executions use (tasks/public adapter).
type TaskPort interface {
	// CreateInTx creates an open task of the execution and returns its id; an inactive team
	// leaves the task unassigned.
	CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in TaskInput) (string, error)
	CancelByContextInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, contextID, reason string) (int, error)
	StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error)
}

// TaskInput describes a task of an execution.
type TaskInput struct {
	Title, Description, TeamID, ContextID string
}

// Contexts checks that the record an execution belongs to exists and is readable by staff.
type Contexts interface {
	TicketExists(ctx context.Context, id string) (bool, error)
}

// TeamDirectory answers whether Teams are active.
type TeamDirectory interface {
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
}

// RunbookPrincipal is the caller's runbook authority: View (knowledge.view) reads definitions,
// Manage (knowledge.manage) writes them, Execute (runbooks.execute) starts and cancels runs.
type RunbookPrincipal struct {
	UserID  string
	View    bool
	Manage  bool
	Execute bool
}

func (p RunbookPrincipal) canRead() bool { return p.View || p.Manage || p.Execute }

// RunbookService performs Runbook operations. Audit actions: knowledge.runbook.created,
// .updated, .activated, .deactivated and knowledge.runbook_execution.started, .completed,
// .cancelled. Step text is not copied into audit.
type RunbookService struct {
	store    RunbookStore
	tasks    TaskPort
	contexts Contexts
	teams    TeamDirectory
}

func NewRunbookService(store RunbookStore, tasks TaskPort, contexts Contexts, teams TeamDirectory) *RunbookService {
	return &RunbookService{store: store, tasks: tasks, contexts: contexts, teams: teams}
}

var systemActor = audit.SystemActor("runbook-execution")

func rbRecord(ctx context.Context, tx pgx.Tx, c Caller, action, target, id string, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: target, TargetID: id, Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta})
}

func rbPublish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		actor = &u
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

// RunbookInput is the editable content of a runbook.
type RunbookInput struct {
	Title       string
	Description string
	Steps       []Step
}

func (s *RunbookService) clean(ctx context.Context, in RunbookInput) (RunbookInput, error) {
	var err error
	if in.Title, err = clean(in.Title, maxTitle, true, false, "title"); err != nil {
		return in, err
	}
	if in.Description, err = clean(in.Description, 2000, false, true, "description"); err != nil {
		return in, err
	}
	if len(in.Steps) == 0 || len(in.Steps) > maxSteps {
		return in, invalid("a runbook needs between 1 and %d steps", maxSteps)
	}
	var teams []string
	steps := make([]Step, len(in.Steps))
	for i, st := range in.Steps {
		t, err := clean(st.Title, maxStepTitle, true, false, fmt.Sprintf("title of step %d", i+1))
		if err != nil {
			return in, err
		}
		d, err := clean(st.Description, maxStepDesc, false, true, fmt.Sprintf("description of step %d", i+1))
		if err != nil {
			return in, err
		}
		team := strings.ToLower(strings.TrimSpace(st.TeamID))
		if team != "" {
			if len(team) != 36 {
				return in, invalid("the team of step %d is not a valid id", i+1)
			}
			teams = append(teams, team)
		}
		steps[i] = Step{Title: t, Description: d, TeamID: team}
	}
	if len(teams) > 0 {
		active, err := s.teams.ActiveTeams(ctx, teams)
		if err != nil {
			return in, fmt.Errorf("check teams: %w", err)
		}
		for _, id := range teams {
			if !active[id] {
				return in, invalid("a step refers to a team that does not exist or is not active")
			}
		}
	}
	in.Steps = steps
	return in, nil
}

// Create defines a runbook. Requires knowledge.manage.
func (s *RunbookService) Create(ctx context.Context, c Caller, p RunbookPrincipal, in RunbookInput) (Runbook, error) {
	if err := c.validate(); err != nil {
		return Runbook{}, err
	}
	if !p.Manage {
		return Runbook{}, ErrForbidden
	}
	in, err := s.clean(ctx, in)
	if err != nil {
		return Runbook{}, err
	}
	r := Runbook{Title: in.Title, Description: in.Description, Steps: in.Steps, Active: true}
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		r.CreatedBy = &u
	}
	var out Runbook
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertRunbookTx(ctx, tx, r)
		if err != nil {
			return err
		}
		return rbRecord(ctx, tx, c, "knowledge.runbook.created", "runbook", out.ID, map[string]any{"steps": len(out.Steps)})
	})
	return out, err
}

// Update changes a runbook. Executions already started keep the steps they started with.
// Requires knowledge.manage.
func (s *RunbookService) Update(ctx context.Context, c Caller, p RunbookPrincipal, id string, expected int, in RunbookInput) (Runbook, error) {
	if err := c.validate(); err != nil {
		return Runbook{}, err
	}
	if !p.Manage {
		return Runbook{}, ErrForbidden
	}
	in, err := s.clean(ctx, in)
	if err != nil {
		return Runbook{}, err
	}
	var out Runbook
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRunbookTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		next := cur
		next.Title, next.Description, next.Steps = in.Title, in.Description, in.Steps
		out, err = s.store.UpdateRunbookTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return rbRecord(ctx, tx, c, "knowledge.runbook.updated", "runbook", id, map[string]any{"steps": len(out.Steps)})
	})
	return out, err
}

// SetActive activates or deactivates a runbook (inactive ones cannot be started).
// Requires knowledge.manage.
func (s *RunbookService) SetActive(ctx context.Context, c Caller, p RunbookPrincipal, id string, expected *int, active bool) (Runbook, error) {
	if err := c.validate(); err != nil {
		return Runbook{}, err
	}
	if !p.Manage {
		return Runbook{}, ErrForbidden
	}
	action := "knowledge.runbook.deactivated"
	if active {
		action = "knowledge.runbook.activated"
	}
	var out Runbook
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockRunbookTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Active == active {
			out = cur
			return nil
		}
		next := cur
		next.Active = active
		out, err = s.store.UpdateRunbookTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return rbRecord(ctx, tx, c, action, "runbook", id, nil)
	})
	return out, err
}

// Start runs an active runbook: one Task per step is created (assigned to the step's Team when
// it is still active), optionally on behalf of a ticket. Requires runbooks.execute.
func (s *RunbookService) Start(ctx context.Context, c Caller, p RunbookPrincipal, runbookID, ticketID string) (Execution, error) {
	if err := c.validate(); err != nil {
		return Execution{}, err
	}
	if !p.Execute {
		return Execution{}, ErrForbidden
	}
	rb, err := s.store.GetRunbook(ctx, runbookID)
	if err != nil {
		return Execution{}, err
	}
	if !rb.Active {
		return Execution{}, &InvalidTransitionError{Operation: "start", From: "inactive"}
	}
	e := Execution{RunbookID: rb.ID, RunbookTitle: rb.Title, Steps: rb.Steps, Status: ExecRunning}
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		e.StartedBy = &u
	}
	if ticketID != "" {
		ok, err := s.contexts.TicketExists(ctx, ticketID)
		if err != nil {
			return Execution{}, fmt.Errorf("check ticket: %w", err)
		}
		if !ok {
			return Execution{}, invalid("the ticket does not exist")
		}
		kind, id := ContextTicket, strings.ToLower(ticketID)
		e.ContextType, e.ContextID = &kind, &id
	}
	var teams []string
	for _, st := range rb.Steps {
		if st.TeamID != "" {
			teams = append(teams, st.TeamID)
		}
	}
	active := map[string]bool{}
	if len(teams) > 0 {
		if active, err = s.teams.ActiveTeams(ctx, teams); err != nil {
			return Execution{}, fmt.Errorf("check teams: %w", err)
		}
	}
	var out Execution
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertExecutionTx(ctx, tx, e)
		if err != nil {
			return err
		}
		taskCaller := Caller{Actor: systemActor, CorrelationID: "runbook:" + out.ID}
		for i, st := range rb.Steps {
			in := TaskInput{Title: fmt.Sprintf("%s – %d/%d: %s", rb.Reference, i+1, len(rb.Steps), st.Title), Description: st.Description, ContextID: out.ID}
			if st.TeamID != "" && active[st.TeamID] {
				in.TeamID = st.TeamID
			}
			id, err := s.tasks.CreateInTx(ctx, tx, taskCaller.Actor, taskCaller.CorrelationID, in)
			if err != nil {
				return fmt.Errorf("create task for step %d: %w", i+1, err)
			}
			if err := s.store.AddExecutionTaskTx(ctx, tx, out.ID, id, i); err != nil {
				return err
			}
			out.TaskIDs = append(out.TaskIDs, id)
		}
		meta := map[string]any{"runbookId": rb.ID, "tasks": len(out.TaskIDs)}
		if out.ContextID != nil {
			meta["ticketId"] = *out.ContextID
		}
		if err := rbRecord(ctx, tx, c, "knowledge.runbook_execution.started", "runbook_execution", out.ID, meta); err != nil {
			return err
		}
		return rbPublish(ctx, tx, c, "RunbookExecutionStarted", map[string]any{"executionId": out.ID, "runbookId": rb.ID})
	})
	return out, err
}

// Cancel stops a running execution and cancels its unfinished tasks. Requires runbooks.execute.
func (s *RunbookService) Cancel(ctx context.Context, c Caller, p RunbookPrincipal, id string, reason string) (Execution, error) {
	if err := c.validate(); err != nil {
		return Execution{}, err
	}
	if !p.Execute {
		return Execution{}, ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 500 || safetext.ContainsUnsafe(reason, false) {
		return Execution{}, invalid("a reason of 1-500 characters without control characters is required")
	}
	var out Execution
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockExecutionTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status != ExecRunning {
			return &InvalidTransitionError{Operation: "cancel", From: cur.Status}
		}
		if _, err := s.tasks.CancelByContextInTx(ctx, tx, systemActor, "runbook:"+cur.ID, cur.ID, "runbook execution cancelled: "+reason); err != nil {
			return fmt.Errorf("cancel tasks: %w", err)
		}
		now := time.Now().UTC()
		next := cur
		next.Status, next.FinishedAt = ExecCancelled, &now
		out, err = s.store.UpdateExecutionTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return rbRecord(ctx, tx, c, "knowledge.runbook_execution.cancelled", "runbook_execution", id, map[string]any{"reason": reason})
	})
	return out, err
}

// OnTaskEvent is the outbox consumer of TaskCompleted and TaskCancelled.
func (s *RunbookService) OnTaskEvent(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	return s.OnTaskFinished(ctx, tx, ev, p.TaskID)
}

// OnTaskFinished completes an execution when none of its tasks is open any more (a consumer of
// TaskCompleted and TaskCancelled). Tasks of other records, stale events and executions that are
// not running change nothing.
func (s *RunbookService) OnTaskFinished(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, taskID string) error {
	execID, err := s.store.ExecutionOfTaskTx(ctx, tx, taskID)
	if err != nil || execID == "" {
		return err
	}
	cur, err := s.store.LockExecutionTx(ctx, tx, execID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != ExecRunning {
		return nil
	}
	ids, err := s.store.ExecutionTasksTx(ctx, tx, execID)
	if err != nil {
		return err
	}
	st, err := s.tasks.StatusesInTx(ctx, tx, ids)
	if err != nil {
		return fmt.Errorf("task statuses: %w", err)
	}
	for _, id := range ids {
		if !slices.Contains([]string{"completed", "cancelled"}, st[id]) {
			return nil
		}
	}
	now := time.Now().UTC()
	next := cur
	next.Status, next.FinishedAt = ExecCompleted, &now
	out, err := s.store.UpdateExecutionTx(ctx, tx, next)
	if err != nil {
		return err
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	if err := rbRecord(ctx, tx, c, "knowledge.runbook_execution.completed", "runbook_execution", execID, nil); err != nil {
		return err
	}
	return rbPublish(ctx, tx, c, "RunbookExecutionCompleted", map[string]any{"executionId": out.ID, "runbookId": out.RunbookID})
}

// Reads. A runbook definition needs knowledge.view, knowledge.manage or runbooks.execute.

func (s *RunbookService) Get(ctx context.Context, p RunbookPrincipal, id string) (Runbook, error) {
	if !p.canRead() {
		return Runbook{}, ErrNotFound
	}
	return s.store.GetRunbook(ctx, id)
}

func (s *RunbookService) List(ctx context.Context, p RunbookPrincipal, activeOnly bool, page Page) (RunbookResult, error) {
	if !p.canRead() {
		return RunbookResult{}, ErrForbidden
	}
	return s.store.ListRunbooks(ctx, activeOnly, page.Normalize())
}

func (s *RunbookService) GetExecution(ctx context.Context, p RunbookPrincipal, id string) (Execution, error) {
	if !p.canRead() {
		return Execution{}, ErrNotFound
	}
	return s.store.GetExecution(ctx, id)
}

func (s *RunbookService) ListExecutions(ctx context.Context, p RunbookPrincipal, runbookID, ticketID string, page Page) (ExecutionResult, error) {
	if !p.canRead() {
		return ExecutionResult{}, ErrForbidden
	}
	return s.store.ListExecutions(ctx, runbookID, ticketID, page.Normalize())
}
