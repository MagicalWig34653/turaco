package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories the Requests module creates.
func NotificationCategories() []notifications.Category {
	cat := func(name, enSubject, enIntro, deSubject, deIntro string) notifications.Category {
		return notifications.Category{
			Name: name, Owner: "requests", LinkType: "service_request", LinkPath: "/requests/{id}",
			Email: map[string]notifications.EmailText{
				"en": {Subject: enSubject, Intro: enIntro, Action: "Open request"},
				"de": {Subject: deSubject, Intro: deIntro, Action: "Antrag öffnen"},
			},
		}
	}
	return []notifications.Category{
		cat("request.approved", "Request approved: %s", "Your request was approved and is being fulfilled:", "Antrag genehmigt: %s", "Dein Antrag wurde genehmigt und wird bearbeitet:"),
		cat("request.rejected", "Request rejected: %s", "Your request was rejected:", "Antrag abgelehnt: %s", "Dein Antrag wurde abgelehnt:"),
		cat("request.completed", "Request completed: %s", "Your request was completed:", "Antrag abgeschlossen: %s", "Dein Antrag wurde abgeschlossen:"),
	}
}

// Notifier creates notifications inside the consumer's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// Consumers are the outbox consumers of the Requests module. They run in the
// dispatcher's claim transaction, write only through tx, call no external
// system and are idempotent.
type Consumers struct {
	svc      *Service
	notifier Notifier
}

func NewConsumers(svc *Service, notifier Notifier) *Consumers {
	return &Consumers{svc: svc, notifier: notifier}
}

func decode(ev events.OutboxEvent, name string, dst any) error {
	if err := json.Unmarshal(ev.Payload, dst); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", name, err))
	}
	return nil
}

// OnApprovalDecided is the consumer of ApprovalDecided.
func (c *Consumers) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p ApprovalDecidedPayload
	if err := decode(ev, "ApprovalDecided", &p); err != nil {
		return err
	}
	return c.svc.OnApprovalDecided(ctx, tx, ev, p)
}

// OnTaskFinished is the consumer of TaskCompleted and TaskCancelled.
func (c *Consumers) OnTaskFinished(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := decode(ev, ev.EventType, &p); err != nil {
		return err
	}
	return c.svc.OnTaskFinished(ctx, tx, ev, p.TaskID)
}

func (c *Consumers) notifyOwners(category string) func(context.Context, pgx.Tx, events.OutboxEvent) error {
	return func(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
		var p struct {
			RequestID string `json:"requestId"`
		}
		if err := decode(ev, ev.EventType, &p); err != nil {
			return err
		}
		r, err := c.svc.store.LockTx(ctx, tx, p.RequestID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ids := []string{r.RequesterID}
		if r.RequestedForID != r.RequesterID {
			ids = append(ids, r.RequestedForID)
		}
		active, err := c.svc.dir.ActiveUsers(ctx, ids)
		if err != nil {
			return fmt.Errorf("check recipients: %w", err)
		}
		for _, id := range ids {
			if !active[id] {
				continue
			}
			if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
				RecipientUserID: id, Category: category, Params: map[string]any{"title": r.Label()},
				LinkType: "service_request", LinkID: r.ID, DedupeKey: ev.ID + ":" + id,
			}); err != nil {
				return fmt.Errorf("create notification: %w", err)
			}
		}
		return nil
	}
}

// OnRequestApproved, OnRequestRejected and OnRequestCompleted notify the
// requester and the requested-for User.
func (c *Consumers) OnRequestApproved(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	return c.notifyOwners("request.approved")(ctx, tx, ev)
}
func (c *Consumers) OnRequestRejected(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	return c.notifyOwners("request.rejected")(ctx, tx, ev)
}
func (c *Consumers) OnRequestCompleted(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	return c.notifyOwners("request.completed")(ctx, tx, ev)
}
