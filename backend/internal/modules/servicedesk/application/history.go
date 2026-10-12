package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Ticket history: the staff-visible timeline of a Ticket's state changes, derived from the audit events of the
// Ticket (no second record exists). Every audited mutation that changes the assignee, the routing Team, the
// status, the priority or the Queue becomes entries with the actor, the time and (where one was given) the reason.
// Comments are not part of it; they have their own list.

// History kinds.
const (
	HistoryCreated           = "created"
	HistoryAssigned          = "assigned"
	HistoryUnassigned        = "unassigned"
	HistoryReassigned        = "reassigned"
	HistoryTeamRouted        = "team_routed"
	HistoryStatusChanged     = "status_changed"
	HistoryPriorityChanged   = "priority_changed"
	HistoryQueueMoved        = "queue_moved"
	HistoryMarkedDuplicate   = "marked_duplicate"
	HistoryLocationChanged   = "location_changed"
	HistoryDeviceChanged     = "device_changed"
	HistoryCallbackScheduled = "callback_scheduled"
	HistorySLA               = "sla"
)

// HistoryEntry is one change. From/To fields are set only for the kinds they apply to. Via names the operation
// that caused it (assign, start, queue_move, ...), which explains an assignment nobody made by hand.
type HistoryEntry struct {
	ID         string
	At         time.Time
	Kind       string
	ActorID    string
	Via        string
	Reason     string
	ExpectedAt string
	FromUserID string
	ToUserID   string
	FromTeamID string
	ToTeamID   string
	FromStatus string
	ToStatus   string
	FromPrio   string
	ToPrio     string
	// DuplicateOfID is set on HistoryMarkedDuplicate.
	DuplicateOfID string
	// FromLocationID and ToLocationID are set on HistoryLocationChanged (empty: no location).
	FromLocationID string
	ToLocationID   string
	// FromDevice and ToDevice are the device references on HistoryDeviceChanged (empty: no device).
	FromDevice string
	ToDevice   string
	// TaskID, DueAt and AssigneeID are set on HistoryCallbackScheduled (the Task of the callback).
	TaskID       string
	DueAt        string
	AssigneeID   string
	SLAKind      string
	SLAOperation string
}

// HistoryStore reads the audit events of a Ticket, oldest first. The repository implements it.
type HistoryStore interface {
	TicketEvents(ctx context.Context, ticketID string, limit int) ([]audit.Event, error)
}

type auditState struct {
	Status   string  `json:"status"`
	Priority string  `json:"priority"`
	Assignee *string `json:"assignee"`
	Queue    *string `json:"queue"`
	QueueID  string  `json:"queueId"`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// buildHistory turns audit events (oldest first) into entries.
func buildHistory(events []audit.Event) []HistoryEntry {
	out := []HistoryEntry{}
	for _, ev := range events {
		if !strings.HasPrefix(ev.Action, "servicedesk.ticket.") {
			continue
		}
		if ev.Action == "servicedesk.ticket.callback_scheduled" {
			var m struct {
				TaskID     string `json:"taskId"`
				DueAt      string `json:"dueAt"`
				AssigneeID string `json:"assigneeId"`
			}
			if len(ev.Metadata) > 0 {
				_ = json.Unmarshal(ev.Metadata, &m)
			}
			out = append(out, HistoryEntry{ID: ev.ID, At: ev.OccurredAt, Kind: HistoryCallbackScheduled, ActorID: deref(ev.ActorID),
				Via: "callback_scheduled", TaskID: m.TaskID, DueAt: m.DueAt, AssigneeID: m.AssigneeID})
			continue
		}
		var before, after auditState
		hasBefore := len(ev.Before) > 0 && string(ev.Before) != "null"
		if hasBefore {
			_ = json.Unmarshal(ev.Before, &before)
		}
		if len(ev.After) == 0 || string(ev.After) == "null" {
			continue
		}
		_ = json.Unmarshal(ev.After, &after)
		var meta struct {
			Reason      string `json:"reason"`
			ExpectedAt  string `json:"expectedAt"`
			ReasonCode  string `json:"reasonCode"`
			Operation   string `json:"operation"`
			DuplicateOf string `json:"duplicateOfId"`
			FromLoc     string `json:"fromLocationId"`
			ToLoc       string `json:"toLocationId"`
			FromDevice  string `json:"fromDevice"`
			ToDevice    string `json:"toDevice"`
			SLA         []struct {
				Kind      string `json:"kind"`
				Operation string `json:"operation"`
			} `json:"sla"`
		}
		if len(ev.Metadata) > 0 {
			_ = json.Unmarshal(ev.Metadata, &meta)
		}
		reason := meta.Reason
		if reason == "" {
			reason = meta.ReasonCode
		}
		via := strings.TrimPrefix(ev.Action, "servicedesk.ticket.")
		base := HistoryEntry{ID: ev.ID, At: ev.OccurredAt, ActorID: deref(ev.ActorID), Via: via, Reason: reason, ExpectedAt: meta.ExpectedAt}
		n := 0
		add := func(kind string, f func(*HistoryEntry)) {
			e := base
			e.Kind = kind
			// One audit event can yield several entries; every entry gets a unique id (first keeps the event id).
			if n++; n > 1 {
				e.ID = fmt.Sprintf("%s:%d", ev.ID, n)
			}
			f(&e)
			out = append(out, e)
		}
		if ev.Action == "servicedesk.ticket.device_changed" {
			add(HistoryDeviceChanged, func(e *HistoryEntry) { e.FromDevice, e.ToDevice = meta.FromDevice, meta.ToDevice })
			continue
		}
		if ev.Action == "servicedesk.ticket.location_changed" {
			add(HistoryLocationChanged, func(e *HistoryEntry) { e.FromLocationID, e.ToLocationID = meta.FromLoc, meta.ToLoc })
			continue
		}
		if !hasBefore {
			add(HistoryCreated, func(e *HistoryEntry) {
				e.ToStatus, e.ToPrio, e.ToUserID, e.ToTeamID = after.Status, after.Priority, deref(after.Assignee), deref(after.Queue)
			})
			for _, c := range meta.SLA {
				kind, operation := c.Kind, c.Operation
				add(HistorySLA, func(e *HistoryEntry) { e.SLAKind = kind; e.SLAOperation = operation })
			}
			continue
		}
		if deref(before.Assignee) != deref(after.Assignee) {
			kind := HistoryReassigned
			switch {
			case before.Assignee == nil:
				kind = HistoryAssigned
			case after.Assignee == nil:
				kind = HistoryUnassigned
			}
			add(kind, func(e *HistoryEntry) { e.FromUserID, e.ToUserID = deref(before.Assignee), deref(after.Assignee) })
		}
		if deref(before.Queue) != deref(after.Queue) {
			add(HistoryTeamRouted, func(e *HistoryEntry) { e.FromTeamID, e.ToTeamID = deref(before.Queue), deref(after.Queue) })
		}
		if meta.DuplicateOf != "" {
			add(HistoryMarkedDuplicate, func(e *HistoryEntry) {
				e.FromStatus, e.ToStatus, e.DuplicateOfID = before.Status, after.Status, meta.DuplicateOf
			})
		} else if before.Status != after.Status {
			add(HistoryStatusChanged, func(e *HistoryEntry) { e.FromStatus, e.ToStatus = before.Status, after.Status })
		}
		if before.Priority != after.Priority {
			add(HistoryPriorityChanged, func(e *HistoryEntry) { e.FromPrio, e.ToPrio = before.Priority, after.Priority })
		}
		if before.QueueID != after.QueueID && after.QueueID != "" {
			add(HistoryQueueMoved, func(e *HistoryEntry) {})
		}
		for _, c := range meta.SLA {
			kind, operation := c.Kind, c.Operation
			add(HistorySLA, func(e *HistoryEntry) { e.SLAKind = kind; e.SLAOperation = operation })
		}
	}
	return out
}

// History returns the change history of a Ticket for people who work it (view access in its Queue). Reporters and
// affected Users who cannot work the Ticket get a 404 here, like for any staff-only sub-resource.
func (s *Service) History(ctx context.Context, p Principal, id string) ([]HistoryEntry, map[string]string, error) {
	hs, ok := s.store.(HistoryStore)
	if !ok {
		return nil, nil, fmt.Errorf("servicedesk: store does not support the history")
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !a.effT(p, t).staff() {
		return nil, nil, ErrNotFound
	}
	events, err := hs.TicketEvents(ctx, t.ID, 500)
	if err != nil {
		return nil, nil, err
	}
	entries := buildHistory(events)
	on, err := s.slaOn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !on || s.isOwner(t, p) {
		entries = slices.DeleteFunc(entries, func(e HistoryEntry) bool { return e.Kind == HistorySLA })
	}
	var users, teams []string
	for _, e := range entries {
		users = append(users, e.ActorID, e.FromUserID, e.ToUserID, e.AssigneeID)
		teams = append(teams, e.FromTeamID, e.ToTeamID)
	}
	names, err := s.dir.UserNames(ctx, nonEmpty(users))
	if err != nil {
		return nil, nil, fmt.Errorf("load names: %w", err)
	}
	if tn := nonEmpty(teams); len(tn) > 0 {
		tm, err := s.dir.TeamNames(ctx, tn)
		if err != nil {
			return nil, nil, fmt.Errorf("load names: %w", err)
		}
		for k, v := range tm {
			names[k] = v
		}
	}
	return entries, names, nil
}

func nonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
