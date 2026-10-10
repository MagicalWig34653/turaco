package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
// Change you requested or own was approved, rejected or failed). Only Users
// who may read the Change are notified: holders of changes.view, manage or
// execute, its requester, its owner and its approvers; anybody else in the
// audience gets nothing, so the title never reaches someone who cannot open it.
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
	scheduled := cat("change.scheduled", "Change scheduled: %s", "A change that affects a service you own or support was scheduled:",
		"Änderung geplant: %s", "Eine Änderung, die einen von dir betreuten Service betrifft, wurde eingeplant:")
	// A scheduled Change may be posted to a Teams channel: generic wording and the reference number only.
	scheduled.Broadcast = &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{
		notifications.PostKindScheduled: {
			"en": {Headline: "A change was scheduled", Action: "Open in Turaco"},
			"de": {Headline: "Eine Änderung wurde eingeplant", Action: "In Turaco öffnen"},
		},
	}}
	return []notifications.Category{
		scheduled,
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

	// FanOutEventType is the internal continuation event of the change.scheduled
	// fan-out; ChangeScheduled itself is published once per scheduling.
	FanOutEventType = "ChangeScheduledFanOut"
)

// errStale stops a reminder whose Change changed between its chunks.
var errStale = errors.New("changes: reminder no longer due")

// Notifications turns Change events into notifications and runs the reminder
// job. Consumers run in the outbox dispatcher's claim transaction, write only
// through tx and are idempotent (notification dedupe keys per event or window).
type Notifications struct {
	store     Store
	graph     *relationships.Graph
	dir       Directory
	services  Services
	notifier  Notifier
	perms     PermissionResolver
	approvals ApprovalViewer
	logger    *slog.Logger
	now       func() time.Time
	chunk     int
	channels  ChannelPoster
}

// ChannelPoster posts about a record to the routed channel destinations (the Notification service).
type ChannelPoster interface {
	PostToChannels(ctx context.Context, tx pgx.Tx, in notifications.ChannelPost) (int, error)
}

// WithChannelPosts enables channel posts for scheduled Changes.
func (n *Notifications) WithChannelPosts(p ChannelPoster) *Notifications {
	n.channels = p
	return n
}

// PostChangeScheduled posts a scheduled Change to the routed channels, reference-only (the reference number and a
// link, never the title). It consumes ChangeScheduled only, not the fan-out continuation, so one scheduling is one
// post per destination; the event id is the idempotency key. A stale event (cancelled meanwhile) posts nothing.
func (n *Notifications) PostChangeScheduled(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	if n.channels == nil {
		return nil
	}
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
	if c.Status != StatusScheduled && c.Status != StatusInProgress {
		return nil
	}
	if _, err := n.channels.PostToChannels(ctx, tx, notifications.ChannelPost{
		Category: "change.scheduled", Kind: notifications.PostKindScheduled, Reference: c.Reference,
		LinkType: "change", LinkID: c.ID, DedupeKey: ev.ID,
	}); err != nil {
		return fmt.Errorf("post to channels: %w", err)
	}
	return nil
}

func NewNotifications(store Store, graph *relationships.Graph, dir Directory, services Services, notifier Notifier,
	perms PermissionResolver, approvals ApprovalViewer) *Notifications {
	return &Notifications{store: store, graph: graph, dir: dir, services: services, notifier: notifier, perms: perms, approvals: approvals,
		logger: slog.Default(), now: func() time.Time { return time.Now().UTC() }, chunk: notifyChunk}
}

// WithLogger sets the logger the reminder job reports per-Change failures to.
func (n *Notifications) WithLogger(l *slog.Logger) *Notifications {
	n.logger = l
	return n
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

// mayRead reports whether the User may read the Change (the read rule of the
// API): requester, owner, a holder of changes.view|manage|execute or an approver.
func (n *Notifications) mayRead(ctx context.Context, c Change, user string) (bool, error) {
	if user == c.RequesterID || c.OwnerID != nil && *c.OwnerID == user {
		return true, nil
	}
	perms, err := n.perms.Permissions(ctx, user)
	if err != nil {
		return false, fmt.Errorf("load recipient permissions: %w", err)
	}
	for _, p := range []string{PermView, PermManage, PermExecute} {
		if _, ok := perms[p]; ok {
			return true, nil
		}
	}
	ok, err := n.approvals.CanView(ctx, c.ID, user)
	if err != nil {
		return false, fmt.Errorf("check recipient approver: %w", err)
	}
	return ok, nil
}

// send creates the notifications for the recipients that are active and may
// read the Change, skipping skip.
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
		ok, err := n.mayRead(ctx, c, id)
		if err != nil {
			return err
		}
		if !ok {
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
// that a Change was scheduled; it consumes ChangeScheduled and its internal
// continuation ChangeScheduledFanOut. Large audiences are notified in chunks:
// the rest follows in a ChangeScheduledFanOut event (payload field "after"), so
// one claim transaction stays short, nobody is silently left out and
// ChangeScheduled stays one event per scheduling. A stale event (the Change was
// cancelled meanwhile) notifies nobody.
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
		if err := events.Publish(ctx, tx, events.Publication{Type: FanOutEventType, ActorID: ev.ActorID, CorrelationID: ev.CorrelationID,
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
// handled on its own, in chunks of recipients with one transaction each; the
// window start that was reminded is stored with the last chunk and every
// notification has a dedupe key of Change, window and recipient, so a retry or
// a second worker sends nothing twice, while a rescheduled window is reminded
// again. A failing Change is logged and skipped; the job reports all failures
// together at the end (and is retried), so one broken Change never blocks the
// reminders of the others.
func (n *Notifications) HandleReminders(ctx context.Context, job jobs.Job) error {
	after := ""
	var failed []error
	for {
		now := n.now()
		ids, err := n.store.DueReminderIDs(ctx, now, now.Add(ReminderLead), after, reminderBatch)
		if err != nil {
			return errors.Join(append(failed, fmt.Errorf("list due reminders: %w", err))...)
		}
		for _, id := range ids {
			if err := n.remind(ctx, id); err != nil {
				n.logger.ErrorContext(ctx, "change reminder failed", "job_id", job.ID, "change_id", id, "error", err)
				failed = append(failed, fmt.Errorf("remind change %s: %w", id, err))
			}
		}
		if len(ids) < reminderBatch {
			return errors.Join(failed...)
		}
		after = ids[len(ids)-1]
	}
}

// due reports that the Change's window starts within the reminder lead and was not reminded yet.
func (n *Notifications) due(c Change) bool {
	now := n.now()
	return c.Status == StatusScheduled && c.WindowStart != nil && c.WindowStart.After(now) && !c.WindowStart.After(now.Add(ReminderLead)) &&
		(c.RemindedFor == nil || !c.RemindedFor.Equal(*c.WindowStart))
}

func (n *Notifications) remind(ctx context.Context, id string) error {
	c, err := n.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !n.due(c) {
		return nil
	}
	all, err := n.audience(ctx, c.ID)
	if err != nil {
		return err
	}
	window := *c.WindowStart
	key := strconv.FormatInt(window.Unix(), 10)
	parts := [][]string{}
	for len(all) > n.chunk {
		parts, all = append(parts, all[:n.chunk]), all[n.chunk:]
	}
	parts = append(parts, all)
	for i, part := range parts {
		last := i == len(parts)-1
		err := n.store.InTx(ctx, func(tx pgx.Tx) error {
			cur, err := n.store.LockTx(ctx, tx, id)
			if errors.Is(err, ErrNotFound) {
				return errStale
			}
			if err != nil {
				return err
			}
			if !n.due(cur) || !cur.WindowStart.Equal(window) {
				return errStale
			}
			if err := n.send(ctx, tx, "change.reminder", cur, part, nil, func(u string) string {
				return "reminder:" + cur.ID + ":" + key + ":" + u
			}); err != nil {
				return err
			}
			if !last {
				return nil
			}
			return n.store.MarkRemindedTx(ctx, tx, cur.ID, window)
		})
		if errors.Is(err, errStale) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}
