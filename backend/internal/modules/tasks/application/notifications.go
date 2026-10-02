package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Notifier creates notifications inside the consumer's transaction
// (implemented by platform/notifications.Service).
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// PermissionResolver returns the effective permissions of a User
// (platform/authorization/roles.Evaluator).
type PermissionResolver interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// assignedSuppressWindow keeps repeated assign/unassign cycles from flooding
// a recipient: no second "assigned" notification for the same task within it.
const assignedSuppressWindow = time.Hour

// TxReader reads a task inside the consumer's transaction.
type TxReader interface {
	GetTx(ctx context.Context, tx pgx.Tx, id string) (Task, error)
}

// Consumers turn Task events into notifications. They run in the outbox
// dispatcher's claim transaction (ADR-0024), write only through tx, never call
// external systems and are idempotent: the dedupe key is the event id plus
// the recipient.
type Consumers struct {
	tasks    TxReader
	dir      Directory
	notifier Notifier
	perms    PermissionResolver
}

func NewConsumers(tasks TxReader, dir Directory, notifier Notifier, perms PermissionResolver) *Consumers {
	return &Consumers{tasks: tasks, dir: dir, notifier: notifier, perms: perms}
}

type assignedPayload struct {
	TaskID         string  `json:"taskId"`
	AssignedUserID *string `json:"assignedUserId"`
	AssignedTeamID *string `json:"assignedTeamId"`
}

// OnTaskAssigned notifies the assigned User and the current members of the
// assigned Team, except the actor who assigned the task. A stale event (the
// task has been reassigned or finished since) notifies nobody.
func (c *Consumers) OnTaskAssigned(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p assignedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode TaskAssigned payload: %w", err))
	}
	task, err := c.tasks.GetTx(ctx, tx, p.TaskID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if task.Terminal() || !equalStrPtr(task.AssignedUserID, p.AssignedUserID) || !equalStrPtr(task.AssignedTeamID, p.AssignedTeamID) {
		return nil
	}
	var candidates []string
	if p.AssignedUserID != nil {
		candidates = append(candidates, *p.AssignedUserID)
	}
	if p.AssignedTeamID != nil {
		members, err := c.dir.CurrentMemberIDs(ctx, *p.AssignedTeamID)
		if err != nil {
			return fmt.Errorf("load team members: %w", err)
		}
		candidates = append(candidates, members...)
	}
	return c.notify(ctx, tx, ev, task, "task.assigned", candidates)
}

type completedPayload struct {
	TaskID            string  `json:"taskId"`
	CompletedByUserID *string `json:"completedByUserId"`
}

// OnTaskCompleted notifies the creator of the task unless they completed it.
func (c *Consumers) OnTaskCompleted(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p completedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode TaskCompleted payload: %w", err))
	}
	task, err := c.tasks.GetTx(ctx, tx, p.TaskID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if task.CreatedByUserID == nil {
		return nil
	}
	return c.notify(ctx, tx, ev, task, "task.completed", []string{*task.CreatedByUserID})
}

// mayKnowTasks reports whether the User holds a permission that lets them see
// at least some tasks. A notification carries the task title, so it is only
// created for Users who hold task permissions, not for every Team member.
func (c *Consumers) mayKnowTasks(ctx context.Context, userID string) (bool, error) {
	perms, err := c.perms.Permissions(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load permissions: %w", err)
	}
	for _, p := range TaskPermissions {
		if _, ok := perms[p]; ok {
			return true, nil
		}
	}
	return false, nil
}

// notify creates one notification per distinct active candidate that holds a
// task permission, other than the actor of the event.
func (c *Consumers) notify(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, task Task, category string, candidates []string) error {
	seen := map[string]struct{}{}
	var ids []string
	for _, id := range candidates {
		if ev.ActorID != nil && id == *ev.ActorID {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	active, err := c.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("check recipients: %w", err)
	}
	for _, id := range ids {
		if !active[id] {
			continue
		}
		if may, err := c.mayKnowTasks(ctx, id); err != nil {
			return err
		} else if !may {
			continue
		}
		var suppress time.Duration
		if category == "task.assigned" {
			suppress = assignedSuppressWindow
		}
		_, err := c.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id,
			Category:        category,
			Params:          map[string]any{"title": task.Title},
			LinkType:        "task",
			LinkID:          task.ID,
			DedupeKey:       ev.ID + ":" + id,
			SuppressWithin:  suppress,
		})
		if err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
