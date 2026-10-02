package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Recurring Task Definitions generate real Tasks on a schedule
// (docs/domain/core-data-model.md). They are managed with the permission
// tasks.recurrence.manage and executed by the scheduled job GenerateJobType.
//
// Audit actions: tasks.recurrence.created, .updated, .paused, .resumed,
// .deleted. Generated tasks are audited as tasks.task.created by the system
// actor "recurrence" with the definition id and the scheduled run.

const (
	// GenerateJobType is the scheduled job that creates the due Tasks.
	GenerateJobType = "tasks.recurrence.generate"
	// GenerateInterval is how often due definitions are looked up.
	GenerateInterval = time.Minute
	// GenerateJobTimeout bounds one generation run.
	GenerateJobTimeout = 5 * time.Minute
	generatePerRun     = 200
	maxDueAfterHours   = 8760
)

// Definition is a Recurring Task Definition.
type Definition struct {
	ID             string
	Title          string
	Description    *string
	Priority       string
	AssignedUserID *string
	AssignedTeamID *string
	// DueAfterHours sets the generated task's due date to the scheduled run
	// plus this many hours; nil means no due date.
	DueAfterHours *int
	Rule          Rule
	// Active is false while the definition is paused.
	Active bool
	// NextRunAt is the next scheduled run; nil exactly while paused.
	NextRunAt       *time.Time
	LastGeneratedAt *time.Time
	CreatedByUserID *string
	// Version starts at 1 and increases with every change.
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewDefinition is the input of DefinitionStore.Insert; the definition starts active.
type NewDefinition struct {
	Title          string
	Description    *string
	Priority       string
	AssignedUserID *string
	AssignedTeamID *string
	DueAfterHours  *int
	Rule           Rule
	NextRunAt      time.Time
	CreatedBy      *string
}

// DefinitionChange is the outcome of a decision made inside the store's
// transaction, like Change for tasks.
type DefinitionChange struct {
	Next     Definition
	NoChange bool
	Action   string
	Metadata map[string]any
}

// Generation is the plan for one due definition, decided inside the store's
// transaction: who the generated task is assigned to and the next run.
type Generation struct {
	AssignedUserID *string
	AssignedTeamID *string
	// AssigneeDropped records that a configured assignee was no longer active.
	AssigneeDropped bool
	NextRunAt       time.Time
}

// DefinitionStore is the persistence port of definitions.
type DefinitionStore interface {
	Insert(ctx context.Context, c Caller, n NewDefinition) (Definition, error)
	Get(ctx context.Context, id string) (Definition, error)
	List(ctx context.Context, p Page) (Result[Definition], error)
	// Change locks the definition and applies decide like Store.Change.
	Change(ctx context.Context, c Caller, id string, decide func(Definition) (DefinitionChange, error)) (Definition, error)
	// Delete removes the definition (audited); expected, when set, must equal its version.
	Delete(ctx context.Context, c Caller, id string, expected *int) error
	// GenerateDue claims one definition whose next run is due (skipping rows
	// other workers hold), asks plan for the assignees and next run, creates
	// the task for the scheduled run idempotently, advances the definition and
	// audits the task, all in one transaction. found is false when nothing is due.
	GenerateDue(ctx context.Context, now time.Time, plan func(Definition) (Generation, error)) (found, generated bool, err error)
}

// RecurrenceService manages and executes Recurring Task Definitions.
type RecurrenceService struct {
	store DefinitionStore
	dir   Directory
	now   func() time.Time
}

// NewRecurrenceService creates the service. now may be nil.
func NewRecurrenceService(store DefinitionStore, dir Directory, now func() time.Time) *RecurrenceService {
	if now == nil {
		now = time.Now
	}
	return &RecurrenceService{store: store, dir: dir, now: now}
}

func (s *RecurrenceService) authorize(p Principal) error {
	if !p.RecurrenceManage {
		return ErrForbidden
	}
	return nil
}

func validDueAfter(h *int) error {
	if h != nil && (*h < 1 || *h > maxDueAfterHours) {
		return invalid("due after must be 1 to %d hours", maxDueAfterHours)
	}
	return nil
}

// DefinitionInput is the input of CreateDefinition.
type DefinitionInput struct {
	Title          string
	Description    string
	Priority       string // empty means normal
	AssignedUserID *string
	AssignedTeamID *string
	DueAfterHours  *int
	Rule           Rule
}

// CreateDefinition creates an active definition and schedules its first run.
func (s *RecurrenceService) CreateDefinition(ctx context.Context, c Caller, p Principal, in DefinitionInput) (Definition, error) {
	if err := c.validate(); err != nil {
		return Definition{}, err
	}
	if err := s.authorize(p); err != nil {
		return Definition{}, err
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Definition{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return Definition{}, err
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityNormal
	}
	if err := validPriority(priority); err != nil {
		return Definition{}, err
	}
	if err := validDueAfter(in.DueAfterHours); err != nil {
		return Definition{}, err
	}
	first, err := in.Rule.NextAfter(s.now())
	if err != nil {
		return Definition{}, err
	}
	if err := checkAssignees(ctx, s.dir, in.AssignedUserID, in.AssignedTeamID); err != nil {
		return Definition{}, err
	}
	var createdBy *string
	if c.Actor.UserID != "" {
		createdBy = &c.Actor.UserID
	}
	return s.store.Insert(ctx, c, NewDefinition{
		Title: title, Description: desc, Priority: priority, AssignedUserID: in.AssignedUserID,
		AssignedTeamID: in.AssignedTeamID, DueAfterHours: in.DueAfterHours, Rule: in.Rule,
		NextRunAt: first, CreatedBy: createdBy,
	})
}

// Get returns a definition.
func (s *RecurrenceService) Get(ctx context.Context, p Principal, id string) (Definition, error) {
	if err := s.authorize(p); err != nil {
		return Definition{}, err
	}
	return s.store.Get(ctx, id)
}

// List returns definitions oldest first.
func (s *RecurrenceService) List(ctx context.Context, p Principal, page Page) (Result[Definition], error) {
	if err := s.authorize(p); err != nil {
		return Result[Definition]{}, err
	}
	return s.store.List(ctx, page.Normalize())
}

// UpdateDefinitionInput changes a definition; nil fields stay unchanged.
type UpdateDefinitionInput struct {
	Title          *string
	Description    *string // empty clears
	Priority       *string
	AssignedUserID *string
	AssignedTeamID *string
	// ClearAssignment removes both assignees; it excludes the two fields above.
	ClearAssignment bool
	DueAfterHours   *int
	ClearDueAfter   bool
	Rule            *Rule
}

func (in UpdateDefinitionInput) empty() bool {
	return in.Title == nil && in.Description == nil && in.Priority == nil && in.AssignedUserID == nil &&
		in.AssignedTeamID == nil && !in.ClearAssignment && in.DueAfterHours == nil && !in.ClearDueAfter && in.Rule == nil
}

// UpdateDefinition changes fields of a definition. Changing the rule
// reschedules an active definition from now.
func (s *RecurrenceService) UpdateDefinition(ctx context.Context, c Caller, p Principal, id string, expected *int, in UpdateDefinitionInput) (Definition, error) {
	if err := c.validate(); err != nil {
		return Definition{}, err
	}
	if err := s.authorize(p); err != nil {
		return Definition{}, err
	}
	if in.empty() {
		return Definition{}, invalid("at least one field to change is required")
	}
	if in.ClearAssignment && (in.AssignedUserID != nil || in.AssignedTeamID != nil) {
		return Definition{}, invalid("clearing the assignment excludes assigning a user or team")
	}
	if in.ClearDueAfter && in.DueAfterHours != nil {
		return Definition{}, invalid("clearing the due offset excludes setting it")
	}
	var title *string
	if in.Title != nil {
		t, err := cleanTitle(*in.Title)
		if err != nil {
			return Definition{}, err
		}
		title = &t
	}
	var desc *string
	if in.Description != nil {
		d, err := cleanDescription(*in.Description)
		if err != nil {
			return Definition{}, err
		}
		desc = d
	}
	if in.Priority != nil {
		if err := validPriority(*in.Priority); err != nil {
			return Definition{}, err
		}
	}
	if err := validDueAfter(in.DueAfterHours); err != nil {
		return Definition{}, err
	}
	if in.Rule != nil {
		if err := in.Rule.Validate(); err != nil {
			return Definition{}, err
		}
	}
	if err := checkAssignees(ctx, s.dir, in.AssignedUserID, in.AssignedTeamID); err != nil {
		return Definition{}, err
	}
	now := s.now()
	return s.store.Change(ctx, c, id, func(cur Definition) (DefinitionChange, error) {
		if expected != nil && *expected != cur.Version {
			return DefinitionChange{}, ErrVersionConflict
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
		switch {
		case in.ClearAssignment && (cur.AssignedUserID != nil || cur.AssignedTeamID != nil):
			next.AssignedUserID, next.AssignedTeamID = nil, nil
			changed = append(changed, "assignment")
		case in.AssignedUserID != nil || in.AssignedTeamID != nil:
			if !equalStrPtr(in.AssignedUserID, cur.AssignedUserID) || !equalStrPtr(in.AssignedTeamID, cur.AssignedTeamID) {
				next.AssignedUserID, next.AssignedTeamID = in.AssignedUserID, in.AssignedTeamID
				changed = append(changed, "assignment")
			}
		}
		switch {
		case in.ClearDueAfter && cur.DueAfterHours != nil:
			next.DueAfterHours = nil
			changed = append(changed, "dueAfterHours")
		case in.DueAfterHours != nil && !equalIntPtr(in.DueAfterHours, cur.DueAfterHours):
			next.DueAfterHours = in.DueAfterHours
			changed = append(changed, "dueAfterHours")
		}
		if in.Rule != nil && *in.Rule != cur.Rule {
			next.Rule = *in.Rule
			changed = append(changed, "rule")
			if cur.Active {
				run, err := in.Rule.NextAfter(now)
				if err != nil {
					return DefinitionChange{}, err
				}
				next.NextRunAt = &run
			}
		}
		if len(changed) == 0 {
			return DefinitionChange{NoChange: true, Next: cur}, nil
		}
		return DefinitionChange{Next: next, Action: "tasks.recurrence.updated", Metadata: map[string]any{"changedFields": changed}}, nil
	})
}

// Pause stops generation until Resume. Pausing a paused definition is a no-op.
func (s *RecurrenceService) Pause(ctx context.Context, c Caller, p Principal, id string, expected *int) (Definition, error) {
	if err := c.validate(); err != nil {
		return Definition{}, err
	}
	if err := s.authorize(p); err != nil {
		return Definition{}, err
	}
	return s.store.Change(ctx, c, id, func(cur Definition) (DefinitionChange, error) {
		if expected != nil && *expected != cur.Version {
			return DefinitionChange{}, ErrVersionConflict
		}
		if !cur.Active {
			return DefinitionChange{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.Active, next.NextRunAt = false, nil
		return DefinitionChange{Next: next, Action: "tasks.recurrence.paused"}, nil
	})
}

// Resume continues generation from the next scheduled run after now; runs
// missed while paused are not generated. Resuming an active definition is a no-op.
func (s *RecurrenceService) Resume(ctx context.Context, c Caller, p Principal, id string, expected *int) (Definition, error) {
	if err := c.validate(); err != nil {
		return Definition{}, err
	}
	if err := s.authorize(p); err != nil {
		return Definition{}, err
	}
	now := s.now()
	return s.store.Change(ctx, c, id, func(cur Definition) (DefinitionChange, error) {
		if expected != nil && *expected != cur.Version {
			return DefinitionChange{}, ErrVersionConflict
		}
		if cur.Active {
			return DefinitionChange{NoChange: true, Next: cur}, nil
		}
		run, err := cur.Rule.NextAfter(now)
		if err != nil {
			return DefinitionChange{}, err
		}
		next := cur
		next.Active, next.NextRunAt = true, &run
		return DefinitionChange{Next: next, Action: "tasks.recurrence.resumed"}, nil
	})
}

// DeleteDefinition removes a definition; the tasks it generated stay.
func (s *RecurrenceService) DeleteDefinition(ctx context.Context, c Caller, p Principal, id string, expected *int) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := s.authorize(p); err != nil {
		return err
	}
	return s.store.Delete(ctx, c, id, expected)
}

// GenerateDue creates the tasks of all due definitions, at most generatePerRun
// per call, and reports how many were created. For each definition it creates
// at most one task, for its oldest due run, and moves the schedule to the
// first run after now: runs missed during an outage are not caught up.
func (s *RecurrenceService) GenerateDue(ctx context.Context) (int, error) {
	now := s.now().UTC()
	generated := 0
	for i := 0; i < generatePerRun && ctx.Err() == nil; i++ {
		found, created, err := s.store.GenerateDue(ctx, now, func(d Definition) (Generation, error) {
			return s.plan(ctx, d, now)
		})
		if err != nil {
			return generated, err
		}
		if !found {
			break
		}
		if created {
			generated++
		}
	}
	return generated, ctx.Err()
}

func (s *RecurrenceService) plan(ctx context.Context, d Definition, now time.Time) (Generation, error) {
	next, err := d.Rule.NextAfter(now)
	if err != nil {
		return Generation{}, err
	}
	g := Generation{AssignedUserID: d.AssignedUserID, AssignedTeamID: d.AssignedTeamID, NextRunAt: next}
	// An assignee who is no longer active must not receive work; the task is
	// still created, unassigned for that side, so the work is not lost.
	if g.AssignedUserID != nil {
		active, err := s.dir.ActiveUsers(ctx, []string{*g.AssignedUserID})
		if err != nil {
			return Generation{}, fmt.Errorf("check assignee: %w", err)
		}
		if !active[*g.AssignedUserID] {
			g.AssignedUserID, g.AssigneeDropped = nil, true
		}
	}
	if g.AssignedTeamID != nil {
		active, err := s.dir.ActiveTeams(ctx, []string{*g.AssignedTeamID})
		if err != nil {
			return Generation{}, fmt.Errorf("check assignee: %w", err)
		}
		if !active[*g.AssignedTeamID] {
			g.AssignedTeamID, g.AssigneeDropped = nil, true
		}
	}
	return g, nil
}

// HandleGenerate is the job handler of GenerateJobType.
func (s *RecurrenceService) HandleGenerate(ctx context.Context, _ jobs.Job) error {
	n, err := s.GenerateDue(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	_ = n
	return nil
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
