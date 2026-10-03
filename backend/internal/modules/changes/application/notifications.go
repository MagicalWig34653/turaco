package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// NotificationCategories are the notification categories Changes creates:
// change.scheduled (a Change affecting a Service you own or support was
// scheduled), change.reminder (such a Change starts soon) and change.state (a
// Change you requested or own was approved, rejected or failed).
func NotificationCategories() []notifications.Category {
	cat := func(name, enSubject, enIntro, deSubject, deIntro string) notifications.Category {
		return notifications.Category{
			Name: name, Owner: "changes", LinkType: "change", LinkPath: "/changes/{id}",
			Email: map[string]notifications.EmailText{
				"en": {Subject: enSubject, Intro: enIntro, Action: "Open change"},
				"de": {Subject: deSubject, Intro: deIntro, Action: "Änderung öffnen"},
			},
		}
	}
	return []notifications.Category{
		cat("change.scheduled", "Change scheduled: %s", "A change that affects a service you own or support was scheduled:",
			"Änderung geplant: %s", "Eine Änderung, die einen von dir betreuten Service betrifft, wurde eingeplant:"),
		cat("change.reminder", "Change starts soon: %s", "A change that affects a service you own or support starts soon:",
			"Änderung beginnt bald: %s", "Eine Änderung, die einen von dir betreuten Service betrifft, beginnt bald:"),
		cat("change.state", "Change status updated: %s", "The status of a change you requested or own changed:",
			"Status einer Änderung aktualisiert: %s", "Der Status einer Änderung, die du beantragt hast oder verantwortest, hat sich geändert:"),
	}
}

const (
	// notifyChunk is how many recipients one consumer run notifies; the rest follows in a continuation event.
	notifyChunk = 500
	// ReminderJobType is the job that sends the "starts soon" reminders.
	ReminderJobType = "changes.reminders"
	// ReminderJobTimeout bounds one reminder run.
	ReminderJobTimeout = 5 * time.Minute
	// ReminderInterval is how often the reminder job is scheduled.
	ReminderInterval = 5 * time.Minute
	// ReminderLead is how long before the window start the reminder is sent.
	ReminderLead  = time.Hour
	reminderBatch = 100
)

// Notifications turns Change events into notifications and runs the reminder
// job. Consumers run in the outbox dispatcher's claim transaction, write only
// through tx and are idempotent (notification dedupe keys per event or window).
type Notifications struct {
	store    Store
	graph    *relationships.Graph
	dir      Directory
	services Services
	notifier Notifier
	now      func() time.Time
	chunk    int
}

func NewNotifications(store Store, graph *relationships.Graph, dir Directory, services Services, notifier Notifier) *Notifications {
	return &Notifications{store: store, graph: graph, dir: dir, services: services, notifier: notifier,
		now: func() time.Time { return time.Now().UTC() }, chunk: notifyChunk}
}

// WithChunk overrides the number of recipients per consumer run (tests).
func (n *Notifications) WithChunk(chunk int) *Notifications {
	n.chunk = chunk
	return n
}

// WithClock replaces the clock (tests).
func (n *Notifications) WithClock(now func() time.Time) *Notifications {
	n.now = now
	return n
}

func title(c Change) string { return c.Reference + " · " + c.Title }

// audience returns the Users to tell about a Change: the owner Users and the
// current members of the owner and support Teams of the Services it affects
// directly (not their dependents), sorted and without duplicates.
func (n *Notifications) audience(ctx context.Context, changeID string) ([]string, error) {
	page, err := n.graph.Outgoing(ctx, n.store.Q(), relationships.Node{Type: NodeChange, ID: changeID}, []string{RelAffects}, "", MaxAffected)
	if err != nil {
		return nil, fmt.Errorf("list affected resources: %w", err)
	}
	var serviceIDs []string
	for _, r := range page.Items {
		if r.Target.Type == NodeService {
			serviceIDs = append(serviceIDs, r.Target.ID)
		}
	}
	if len(serviceIDs) == 0 {
		return nil, nil
	}
	found, err := n.services.Lookup(ctx, serviceIDs)
	if err != nil {
		return nil, fmt.Errorf("load affected services: %w", err)
	}
	users, teams := map[string]bool{}, map[string]bool{}
	for _, s := range found {
		if s.Status == "retired" {
			continue
		}
		if s.OwnerUserID != nil {
			users[*s.OwnerUserID] = true
		}
		for _, t := range []*string{s.OwnerTeamID, s.SupportTeamID} {
			if t != nil {
				teams[*t] = true
			}
		}
	}
	for team := range teams {
		members, err := n.dir.CurrentMemberIDs(ctx, team)
		if err != nil {
			return nil, fmt.Errorf("load team members: %w", err)
		}
		for _, m := range members {
			users[m] = true
		}
	}
	out := make([]string, 0, len(users))
	for u := range users {
		out = append(out, u)
	}
	slices.Sort(out)
	return out, nil
}

// send creates the notifications for the recipients that are active, skipping skip.
func (n *Notifications) send(ctx context.Context, tx pgx.Tx, category string, c Change, recipients []string, skip *string, dedupe func(user string) string) error {
	var ids []string
	for _, id := range recipients {
		if id != "" && !slices.Contains(ids, id) && (skip == nil || *skip != id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	active, err := n.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("check recipients: %w", err)
	}
	for _, id := range ids {
		if !active[id] {
			continue
		}
		if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: category, Params: map[string]any{"title": title(c)},
			LinkType: "change", LinkID: c.ID, DedupeKey: dedupe(id),
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}

func decodePayload(ev events.OutboxEvent, dst any) error {
	if err := json.Unmarshal(ev.Payload, dst); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	return nil
}

// OnChangeScheduled tells the owners and support Teams of the affected Services
// that a Change was scheduled. Large audiences are notified in chunks: the rest
// follows in a continuation event (payload field "after"), so one claim
// transaction stays short and nobody is silently left out. A stale event (the
// Change was cancelled meanwhile) notifies nobody.
func (n *Notifications) OnChangeScheduled(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ChangeID string `json:"changeId"`
		After    string `json:"after"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if !uuidPattern.MatchString(p.ChangeID) {
		return events.Permanent(fmt.Errorf("invalid change id in %s", ev.EventType))
	}
	c, err := n.store.Get(ctx, strings.ToLower(p.ChangeID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Status != StatusScheduled && c.Status != StatusInProgress {
		return nil
	}
	all, err := n.audience(ctx, c.ID)
	if err != nil {
		return err
	}
	rest := all
	if p.After != "" {
		i, _ := slices.BinarySearch(all, p.After)
		if i < len(all) && all[i] == p.After {
			i++
		}
		rest = all[i:]
	}
	if len(rest) > n.chunk {
		rest = rest[:n.chunk]
		if err := events.Publish(ctx, tx, events.Publication{Type: "ChangeScheduled", ActorID: ev.ActorID, CorrelationID: ev.CorrelationID,
			Payload: map[string]any{"changeId": c.ID, "after": rest[len(rest)-1]}}); err != nil {
			return fmt.Errorf("continue fan-out: %w", err)
		}
	}
	return n.send(ctx, tx, "change.scheduled", c, rest, ev.ActorID, func(u string) string { return ev.ID + ":" + u })
}

// OnChangeState tells the requester and the owner that their Change was
// approved, rejected or failed. The person who caused the event is not told.
func (n *Notifications) OnChangeState(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ChangeID string `json:"changeId"`
	}
	if err := decodePayload(ev, &p); err != nil {
		return err
	}
	if !uuidPattern.MatchString(p.ChangeID) {
		return events.Permanent(fmt.Errorf("invalid change id in %s", ev.EventType))
	}
	c, err := n.store.Get(ctx, strings.ToLower(p.ChangeID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	recipients := []string{c.RequesterID}
	if c.OwnerID != nil {
		recipients = append(recipients, *c.OwnerID)
	}
	return n.send(ctx, tx, "change.state", c, recipients, ev.ActorID, func(u string) string { return ev.ID + ":" + u })
}

// HandleReminders is the job handler of ReminderJobType: for every scheduled
// Change whose window starts within the next hour it tells the owners and
// support Teams of the affected Services once per window. Each Change is
// handled in its own transaction; the window start that was reminded is stored
// on the Change and every notification has a dedupe key of Change, window and
// recipient, so a retry or a second worker sends nothing twice, while a
// rescheduled window is reminded again.
func (n *Notifications) HandleReminders(ctx context.Context, job jobs.Job) error {
	after := ""
	for {
		now := n.now()
		ids, err := n.store.DueReminderIDs(ctx, now, now.Add(ReminderLead), after, reminderBatch)
		if err != nil {
			return fmt.Errorf("list due reminders: %w", err)
		}
		for _, id := range ids {
			if err := n.remind(ctx, id); err != nil {
				return fmt.Errorf("remind change %s: %w", id, err)
			}
		}
		if len(ids) < reminderBatch {
			return nil
		}
		after = ids[len(ids)-1]
	}
}

func (n *Notifications) remind(ctx context.Context, id string) error {
	return n.store.InTx(ctx, func(tx pgx.Tx) error {
		c, err := n.store.LockTx(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := n.now()
		if c.Status != StatusScheduled || c.WindowStart == nil || !c.WindowStart.After(now) || c.WindowStart.After(now.Add(ReminderLead)) ||
			c.RemindedFor != nil && c.RemindedFor.Equal(*c.WindowStart) {
			return nil
		}
		all, err := n.audience(ctx, c.ID)
		if err != nil {
			return err
		}
		window := strconv.FormatInt(c.WindowStart.Unix(), 10)
		if err := n.send(ctx, tx, "change.reminder", c, all, nil, func(u string) string {
			return "reminder:" + c.ID + ":" + window + ":" + u
		}); err != nil {
			return err
		}
		return n.store.MarkRemindedTx(ctx, tx, c.ID, *c.WindowStart)
	})
}
