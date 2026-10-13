package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// A scheduled callback is an ordinary Task of the Tasks module whose context is the Ticket (context type
// "ticket"); it shows in My Work like any other Task. The Service Desk adds no callback table: the audit event
// servicedesk.ticket.callback_scheduled is the Ticket history entry.

// TaskContextType is the Tasks context type of Tasks that belong to a Ticket.
const TaskContextType = "ticket"

// MaxCallbackHorizon bounds how far ahead a callback can be scheduled.
const MaxCallbackHorizon = 365 * 24 * time.Hour

// CallbackTaskInput describes the Task created for a callback.
type CallbackTaskInput struct {
	TicketID       string
	Title          string
	DueAt          time.Time
	AssignedUserID string
	// LocationID is the Ticket's affected Location; the Task is placed there (Location-scoped access).
	LocationID *string
}

// CallbackTasks is the Tasks public contract as the Service Desk uses it (servicedesk/public adapter). It creates
// the Task in the caller's transaction so the Task exists exactly when the audit entry does.
type CallbackTasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in CallbackTaskInput) (string, error)
	ByContext(ctx context.Context, ticketID string, limit int) ([]CallbackTask, error)
}

// ErrAssigneeInvalid means the chosen assignee does not exist or is not active.
var ErrAssigneeInvalid = errors.New("servicedesk: callback assignee is not an active user")

// WithCallbackTasks enables scheduling callbacks; without it the operation is unavailable.
func (s *Service) WithCallbackTasks(t CallbackTasks) *Service {
	s.callbacks = t
	return s
}

// Callback is a scheduled callback as created.
type Callback struct {
	TaskID         string
	DueAt          time.Time
	AssignedUserID string
}

// ScheduleCallback creates a Task for the Ticket, due at dueAt (in the future, at most a year ahead) and assigned
// to assigneeID (empty: the caller). It needs work access in the Ticket's Queue and an open Ticket
// (Abilities.ScheduleCallback). The Ticket itself does not change. Audited as servicedesk.ticket.callback_scheduled
// with the Task id, the due time and the assignee; the ticket title is not copied.
func (s *Service) ScheduleCallback(ctx context.Context, c Caller, p Principal, id string, dueAt time.Time, assigneeID string, title string) (Callback, error) {
	if err := c.validate(); err != nil {
		return Callback{}, err
	}
	if s.callbacks == nil {
		return Callback{}, errors.New("servicedesk: callbacks are not configured")
	}
	dueAt = dueAt.UTC()
	now := time.Now().UTC()
	if dueAt.IsZero() || !dueAt.After(now) || dueAt.After(now.Add(MaxCallbackHorizon)) {
		return Callback{}, invalid("dueAt must be in the future and at most one year ahead")
	}
	title, err := cleanText(title, 200, false, "title")
	if err != nil {
		return Callback{}, err
	}
	assigneeID = lower(assigneeID)
	if assigneeID == "" {
		assigneeID = p.UserID
	}
	if !uuidPattern.MatchString(assigneeID) {
		return Callback{}, invalid("assigneeId must be a user id")
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Callback{}, err
	}
	var out Callback
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		if !AbilitiesOf(cur, ep, s.queues != nil).ScheduleCallback {
			if !ep.Manage {
				return ErrForbidden
			}
			return &InvalidTransitionError{Operation: "schedule_callback", From: cur.Status}
		}
		if title == "" {
			title = "Callback " + cur.Reference
		}
		taskID, err := s.callbacks.CreateInTx(ctx, tx, c.Actor, c.CorrelationID, CallbackTaskInput{
			TicketID: cur.ID, Title: title, DueAt: dueAt, AssignedUserID: assigneeID, LocationID: cur.AffectedLocationID})
		if err != nil {
			return err
		}
		out = Callback{TaskID: taskID, DueAt: dueAt, AssignedUserID: assigneeID}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.callback_scheduled", TargetType: "ticket", TargetID: cur.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID,
			Metadata: map[string]any{"taskId": taskID, "dueAt": dueAt.Format(time.RFC3339), "assigneeId": assigneeID}})
	})
	return out, err
}

// CallbackTask is a live Task projection for staff on the Ticket detail.
type CallbackTask struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	DueAt          *time.Time `json:"dueAt"`
	AssignedUserID *string    `json:"assignedUserId"`
	AssigneeName   *string    `json:"assigneeName"`
}

// CallbackTasksForTicket returns bounded live Tasks to staff with access to the Ticket's Queue.
func (s *Service) CallbackTasksForTicket(ctx context.Context, p Principal, id string) ([]CallbackTask, error) {
	if p.External {
		return nil, ErrNotFound
	}
	if s.callbacks == nil {
		return nil, errors.New("servicedesk: callbacks are not configured")
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !a.effT(p, t).staff() {
		return nil, ErrNotFound
	}
	tasks, err := s.callbacks.ByContext(ctx, t.ID, 100)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.AssignedUserID != nil {
			ids = append(ids, *task.AssignedUserID)
		}
	}
	if len(ids) > 0 {
		names, err := s.dir.UserNames(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			if tasks[i].AssignedUserID != nil {
				if name := names[*tasks[i].AssignedUserID]; name != "" {
					tasks[i].AssigneeName = &name
				}
			}
		}
	}
	return tasks, nil
}
