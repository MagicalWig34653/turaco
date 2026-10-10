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
		cat("ticket.mention", "You were mentioned on ticket: %s", "You were mentioned in an internal note on a ticket:", "Du wurdest im Ticket erwähnt: %s", "Du wurdest in einer internen Notiz zu einem Ticket erwähnt:"),
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
	// references resolves the number a recipient may know (Service.ReferenceFor); nil keeps the current number.
	references func(ctx context.Context, userID string, t Ticket) (string, error)
}

// WithReferences sets the per-recipient display number resolver. A notification must not name a Queue number the
// recipient may not know (a requester whose Ticket moved to an internal Queue).
func (c *Consumers) WithReferences(fn func(ctx context.Context, userID string, t Ticket) (string, error)) *Consumers {
	c.references = fn
	return c
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
		ref := t.Reference
		if c.references != nil {
			r, err := c.references(ctx, id, t)
			if err != nil {
				return fmt.Errorf("resolve reference of recipient: %w", err)
			}
			ref = r
		}
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: category, Params: map[string]any{"title": ref + " · " + t.Title},
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
	if err != nil || !ok || (t.Status != StatusResolved && t.Status != StatusClosed) {
		return err // reopened meanwhile: the news is stale
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
		// MentionedUserIDs were checked against the Queue when the note was added.
		MentionedUserIDs []string `json:"mentionedUserIds"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if p.Internal {
		if len(p.MentionedUserIDs) == 0 {
			return nil
		}
		t, ok, err := c.load(ctx, p.TicketID)
		if err != nil || !ok {
			return err
		}
		return c.notify(ctx, tx, ev, "ticket.mention", t, p.MentionedUserIDs)
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
		// Incident progress may be posted to a Teams channel: generic wording and the reference number only.
		Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{
			notifications.PostKindDeclared: {
				"en": {Headline: "A major incident was declared", Action: "Open in Turaco"},
				"de": {Headline: "Eine Großstörung wurde ausgerufen", Action: "In Turaco öffnen"},
			},
			notifications.PostKindUpdated: {
				"en": {Headline: "A major incident was updated", Action: "Open in Turaco"},
				"de": {Headline: "Eine Großstörung wurde aktualisiert", Action: "In Turaco öffnen"},
			},
		}},
	}
}

// ChannelPoster posts about a record to the routed channel destinations (the Notification service). It is separate
// from Notifier because only some installations route channels.
type ChannelPoster interface {
	PostToChannels(ctx context.Context, tx pgx.Tx, in notifications.ChannelPost) (int, error)
}

// MajorConsumers notify the subscribers of a Major Incident about its progress and, when a ChannelPoster is set,
// post the declaration and every update to the routed channels.
type MajorConsumers struct {
	store    MajorStore
	dir      Directory
	notifier Notifier
	channels ChannelPoster
}

// WithChannelPosts enables channel posts for declared and updated Major Incidents.
func (c *MajorConsumers) WithChannelPosts(p ChannelPoster) *MajorConsumers {
	c.channels = p
	return c
}

// postToChannels posts the incident reference-only: the reference number and a link, never the title or the
// messages. Exercises (drills) are never posted. The event id is the idempotency key per destination.
func (c *MajorConsumers) postToChannels(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, kind, id string, exercise bool) error {
	if c.channels == nil || exercise {
		return nil
	}
	reference, err := c.store.MajorReference(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load incident reference: %w", err)
	}
	if _, err := c.channels.PostToChannels(ctx, tx, notifications.ChannelPost{
		Category: "majorincident.update", Kind: kind, Reference: reference,
		LinkType: "major_incident", LinkID: id, DedupeKey: ev.ID,
	}); err != nil {
		return fmt.Errorf("post to channels: %w", err)
	}
	return nil
}

// OnMajorIncidentDeclared posts the declaration to the routed channels. It notifies nobody else: subscribers
// follow an incident only after it exists.
func (c *MajorConsumers) OnMajorIncidentDeclared(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ID         string `json:"majorIncidentId"`
		IsExercise bool   `json:"isExercise"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if !validEventID(p.ID) {
		return events.Permanent(fmt.Errorf("invalid incident id in %s", ev.EventType))
	}
	return c.postToChannels(ctx, tx, ev, notifications.PostKindDeclared, p.ID, p.IsExercise)
}

func NewMajorConsumers(store MajorStore, dir Directory, notifier Notifier) *MajorConsumers {
	return &MajorConsumers{store: store, dir: dir, notifier: notifier}
}

// OnMajorIncidentUpdated tells every active subscriber except the author. The text is the
// incident's current title; the latest message is read in the app.
func (c *MajorConsumers) OnMajorIncidentUpdated(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ID         string `json:"majorIncidentId"`
		Status     string `json:"status"`
		After      string `json:"after"`
		IsExercise bool   `json:"isExercise"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if !validEventID(p.ID) {
		return events.Permanent(fmt.Errorf("invalid incident id in %s", ev.EventType))
	}
	// The channel post belongs to the update itself, not to a fan-out continuation (After is set there).
	if p.After == "" {
		if err := c.postToChannels(ctx, tx, ev, notifications.PostKindUpdated, p.ID, p.IsExercise); err != nil {
			return err
		}
	}
	subs, err := c.store.Subscribers(ctx, tx, p.ID, p.After, majorChunk+1)
	if err != nil || len(subs) == 0 {
		return err
	}
	title, err := c.store.MajorTitle(ctx, tx, p.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load incident title: %w", err)
	}
	// Large audiences are notified in chunks: the rest follows in a new event, so one claim
	// transaction stays short and nobody is silently left out.
	if len(subs) > majorChunk {
		subs = subs[:majorChunk]
		var actor *string
		if ev.ActorID != nil {
			a := *ev.ActorID
			actor = &a
		}
		if err := events.Publish(ctx, tx, events.Publication{Type: "MajorIncidentUpdated", ActorID: actor, CorrelationID: ev.CorrelationID,
			Payload: map[string]any{"majorIncidentId": p.ID, "status": p.Status, "after": subs[len(subs)-1]}}); err != nil {
			return fmt.Errorf("continue fan-out: %w", err)
		}
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

// majorChunk is how many subscribers one consumer run notifies.
const majorChunk = 500

func validEventID(s string) bool { return len(s) == 36 }
