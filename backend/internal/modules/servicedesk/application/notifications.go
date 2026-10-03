package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories the Service Desk creates.
func NotificationCategories() []notifications.Category {
	cat := func(name, enSubject, enIntro, deSubject, deIntro string) notifications.Category {
		return notifications.Category{
			Name: name, Owner: "servicedesk", LinkType: "ticket", LinkPath: "/support/{id}",
			Email: map[string]notifications.EmailText{
				"en": {Subject: enSubject, Intro: enIntro, Action: "Open ticket"},
				"de": {Subject: deSubject, Intro: deIntro, Action: "Ticket öffnen"},
			},
		}
	}
	return []notifications.Category{
		cat("ticket.assigned", "Ticket assigned: %s", "A ticket was assigned to you:", "Ticket zugewiesen: %s", "Dir wurde ein Ticket zugewiesen:"),
		cat("ticket.comment", "New reply on ticket: %s", "There is a new reply on a ticket:", "Neue Antwort im Ticket: %s", "Es gibt eine neue Antwort in einem Ticket:"),
		majorCategory(),
		cat("ticket.resolved", "Ticket resolved: %s", "Your ticket was resolved:", "Ticket gelöst: %s", "Dein Ticket wurde gelöst:"),
	}
}

// Notifier creates notifications inside the consumer's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// Consumers turn ticket events into notifications. They run in the outbox
// dispatcher's claim transaction, write only through tx and are idempotent.
type Consumers struct {
	store    Store
	dir      Directory
	notifier Notifier
}

func NewConsumers(store Store, dir Directory, notifier Notifier) *Consumers {
	return &Consumers{store: store, dir: dir, notifier: notifier}
}

func decodePayload(ev events.OutboxEvent, dst any) error {
	if err := json.Unmarshal(ev.Payload, dst); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	return nil
}

// notify tells the recipients, skipping the actor, inactive Users and duplicates.
func (c *Consumers) notify(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, category string, t Ticket, recipients []string) error {
	var ids []string
	for _, id := range recipients {
		if id == "" || slices.Contains(ids, id) || (ev.ActorID != nil && *ev.ActorID == id) {
			continue
		}
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
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: category, Params: map[string]any{"title": t.Reference + " · " + t.Title},
			LinkType: "ticket", LinkID: t.ID, DedupeKey: ev.ID + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}

func (c *Consumers) load(ctx context.Context, id string) (Ticket, bool, error) {
	t, err := c.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Ticket{}, false, nil
	}
	return t, err == nil, err
}

// OnTicketAssigned tells the assignee. A stale event (reassigned meanwhile) notifies nobody.
func (c *Consumers) OnTicketAssigned(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		TicketID   string `json:"ticketId"`
		AssigneeID string `json:"assigneeId"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	t, ok, err := c.load(ctx, p.TicketID)
	if err != nil || !ok || t.AssigneeID == nil || *t.AssigneeID != p.AssigneeID {
		return err
	}
	return c.notify(ctx, tx, ev, "ticket.assigned", t, []string{p.AssigneeID})
}

// OnTicketResolved tells the reporter and the affected User.
func (c *Consumers) OnTicketResolved(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		TicketID string `json:"ticketId"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	t, ok, err := c.load(ctx, p.TicketID)
	if err != nil || !ok {
		return err
	}
	return c.notify(ctx, tx, ev, "ticket.resolved", t, []string{t.ReporterID, t.AffectedUserID})
}

// OnCommentAdded tells the reporter and affected User about a public reply from staff,
// and the assignee about a reply from the reporter or affected User. Internal comments
// notify nobody.
func (c *Consumers) OnCommentAdded(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		TicketID string `json:"ticketId"`
		Internal bool   `json:"internal"`
		AuthorID string `json:"authorId"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if p.Internal {
		return nil
	}
	t, ok, err := c.load(ctx, p.TicketID)
	if err != nil || !ok {
		return err
	}
	if p.AuthorID == t.ReporterID || p.AuthorID == t.AffectedUserID {
		if t.AssigneeID == nil {
			return nil
		}
		return c.notify(ctx, tx, ev, "ticket.comment", t, []string{*t.AssigneeID})
	}
	return c.notify(ctx, tx, ev, "ticket.comment", t, []string{t.ReporterID, t.AffectedUserID})
}

// MajorCategory is the notification category for incident progress.
func majorCategory() notifications.Category {
	return notifications.Category{
		Name: "majorincident.update", Owner: "servicedesk", LinkType: "major_incident", LinkPath: "/incidents/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Incident update: %s", Intro: "There is news on an incident you follow:", Action: "Open incident"},
			"de": {Subject: "Störungsmeldung aktualisiert: %s", Intro: "Es gibt Neuigkeiten zu einer Störung, der du folgst:", Action: "Störung öffnen"},
		},
	}
}

// MajorConsumers notify the subscribers of a Major Incident about its progress.
type MajorConsumers struct {
	store    MajorStore
	dir      Directory
	notifier Notifier
}

func NewMajorConsumers(store MajorStore, dir Directory, notifier Notifier) *MajorConsumers {
	return &MajorConsumers{store: store, dir: dir, notifier: notifier}
}

// OnMajorIncidentUpdated tells every active subscriber except the author. The text is the
// incident's current title; the latest message is read in the app.
func (c *MajorConsumers) OnMajorIncidentUpdated(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ID string `json:"majorIncidentId"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if !validEventID(p.ID) {
		return events.Permanent(fmt.Errorf("invalid incident id in %s", ev.EventType))
	}
	subs, err := c.store.Subscribers(ctx, tx, p.ID)
	if err != nil || len(subs) == 0 {
		return err
	}
	var title string
	if err := tx.QueryRow(ctx, `SELECT reference || ' · ' || title FROM servicedesk.major_incidents WHERE id = $1::uuid`, p.ID).Scan(&title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load incident title: %w", err)
	}
	var ids []string
	for _, id := range subs {
		if ev.ActorID == nil || *ev.ActorID != id {
			ids = append(ids, id)
		}
	}
	active, err := c.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("check recipients: %w", err)
	}
	for _, id := range ids {
		if !active[id] {
			continue
		}
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: "majorincident.update", Params: map[string]any{"title": title},
			LinkType: "major_incident", LinkID: p.ID, DedupeKey: ev.ID + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}

func validEventID(s string) bool { return len(s) == 36 }
