package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Manager overview (simulation backlog, ADR-0033): a read-only summary for team leads of the open Tickets in the
// Queues the caller may view. It is a breakdown the filter engine cannot express (counts grouped by Queue and by
// assignee), so it is one explicit read operation instead of a parallel reporting system. Every number maps to a
// filter over the tickets catalog (status open, queue, assignee), so the frontend links each count to the ordinary
// list.
//
// Visibility is exactly the Queue view scope of the list endpoints, narrowed to Queues the caller can view: Tickets
// the caller sees only because they reported them, or because they are affected, are not management information
// and are never counted. A caller without any Queue view gets ErrForbidden, the same as the "all" ticket list.

// Overview limits keep the response bounded regardless of the data.
const (
	OverviewMaxAssignees = 50
	OverviewMaxOldest    = 10
)

// OverviewQuery is the store's input.
type OverviewQuery struct {
	// Global means every Queue is viewable; otherwise QueueIDs lists the viewable ones.
	Global        bool
	QueueIDs      []string
	AssigneeLimit int
	OldestLimit   int
	// SLA is true while the sla switch is on: the store then also counts at-risk and breached Tickets per Queue and
	// lists the Tickets next to breach. There is deliberately no per-assignee SLA count (ADR-0041 D5).
	SLA bool
}

// OverviewQueueCounts are the open Ticket counts of one Queue.
type OverviewQueueCounts struct {
	QueueID    string
	Open       int
	Unassigned int
	Waiting    int
	// AtRisk and Breached count open Tickets by sla_state (see SLAStateSQL); zero while the sla switch is off.
	AtRisk   int
	Breached int
	// OldestOpenAt is the creation time of the oldest open Ticket, if any.
	OldestOpenAt *time.Time
}

// OverviewAssigneeCount is the open Ticket count of one assignee over the viewable Queues.
type OverviewAssigneeCount struct {
	UserID  string
	Open    int
	Waiting int
}

// OverviewTicket is one row of the "oldest" lists: only what a list row shows.
type OverviewTicket struct {
	ID         string
	Reference  string
	Title      string
	Priority   string
	Status     string
	QueueID    string
	AssigneeID *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// SLADueAt is the next due time still ahead (next-to-breach list only).
	SLADueAt *time.Time
}

// OverviewData is what the store reads.
type OverviewData struct {
	Queues             []OverviewQueueCounts
	Assignees          []OverviewAssigneeCount
	AssigneesTruncated bool
	// OldestUnassigned are open Tickets nobody owns, oldest first; OldestWaiting are Tickets in status waiting,
	// least recently changed first.
	OldestUnassigned []OverviewTicket
	OldestWaiting    []OverviewTicket
	// NextToBreach are open Tickets with the nearest future due time of a running clock (only while SLA is on).
	NextToBreach []OverviewTicket
}

// OverviewStore is the persistence port of the manager overview. The repository implements it next to Store.
type OverviewStore interface {
	TicketOverview(ctx context.Context, q OverviewQuery) (OverviewData, error)
}

// OverviewQueue is one Queue row of the result, with the Queue as the caller may know it.
type OverviewQueue struct {
	Queue QueueRef
	// Level is the caller's grant level in the Queue (view, work or manage).
	Level string
	OverviewQueueCounts
}

// OverviewAssignee is one assignee row with the display name.
type OverviewAssignee struct {
	OverviewAssigneeCount
	Name string
}

// OverviewTicketRow is an OverviewTicket with the Queue and assignee display names.
type OverviewTicketRow struct {
	OverviewTicket
	Queue        QueueRef
	AssigneeName string
}

// Overview is the manager overview of one caller.
type Overview struct {
	GeneratedAt time.Time
	Totals      OverviewTotals
	Queues      []OverviewQueue
	Assignees   []OverviewAssignee
	// AssigneesTruncated is true when more assignees exist than listed (the busiest are listed).
	AssigneesTruncated bool
	OldestUnassigned   []OverviewTicketRow
	OldestWaiting      []OverviewTicketRow
	// SLA is true while the sla switch is on; NextToBreach is then the Tickets that breach soonest.
	SLA          bool
	NextToBreach []OverviewTicketRow
}

// OverviewTotals sum the Queue rows.
type OverviewTotals struct {
	Open       int
	Unassigned int
	Waiting    int
	AtRisk     int
	Breached   int
}

// TicketOverview returns the manager overview for the caller.
func (s *Service) TicketOverview(ctx context.Context, p Principal) (Overview, error) {
	if p.UserID == "" {
		return Overview{}, ErrForbidden
	}
	os, ok := s.store.(OverviewStore)
	if !ok {
		return Overview{}, fmt.Errorf("servicedesk: store does not support the overview")
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return Overview{}, err
	}
	if !a.anyView() {
		return Overview{}, ErrForbidden
	}
	sla, err := s.slaOn(ctx)
	if err != nil {
		return Overview{}, err
	}
	data, err := os.TicketOverview(ctx, OverviewQuery{Global: a.global(), QueueIDs: a.viewIDs(),
		AssigneeLimit: OverviewMaxAssignees, OldestLimit: OverviewMaxOldest, SLA: sla})
	if err != nil {
		return Overview{}, fmt.Errorf("ticket overview: %w", err)
	}
	out := Overview{GeneratedAt: time.Now().UTC(), AssigneesTruncated: data.AssigneesTruncated, SLA: sla}
	ref := func(id string) QueueRef {
		r := a.queues[id].Queue
		return QueueRef{ID: r.ID, Key: r.Key, Prefix: r.Prefix, Name: r.Name}
	}
	for _, c := range data.Queues {
		// Defensive: the store already filtered, but a Queue the caller may not view never appears.
		if !a.canView(c.QueueID) {
			continue
		}
		out.Queues = append(out.Queues, OverviewQueue{Queue: ref(c.QueueID), Level: levelName(a.levelOf(c.QueueID)), OverviewQueueCounts: c})
		out.Totals.Open += c.Open
		out.Totals.Unassigned += c.Unassigned
		out.Totals.Waiting += c.Waiting
		out.Totals.AtRisk += c.AtRisk
		out.Totals.Breached += c.Breached
	}
	sort.SliceStable(out.Queues, func(i, j int) bool {
		x, y := strings.ToLower(out.Queues[i].Queue.Name), strings.ToLower(out.Queues[j].Queue.Name)
		if x != y {
			return x < y
		}
		return out.Queues[i].Queue.ID < out.Queues[j].Queue.ID
	})
	ids := []string{}
	for _, c := range data.Assignees {
		ids = append(ids, c.UserID)
	}
	for _, list := range [][]OverviewTicket{data.OldestUnassigned, data.OldestWaiting, data.NextToBreach} {
		for _, t := range list {
			if t.AssigneeID != nil {
				ids = append(ids, *t.AssigneeID)
			}
		}
	}
	names := map[string]string{}
	if len(ids) > 0 && s.dir != nil {
		if names, err = s.dir.UserNames(ctx, ids); err != nil {
			return Overview{}, fmt.Errorf("overview names: %w", err)
		}
	}
	for _, c := range data.Assignees {
		out.Assignees = append(out.Assignees, OverviewAssignee{OverviewAssigneeCount: c, Name: names[c.UserID]})
	}
	rows := func(in []OverviewTicket) []OverviewTicketRow {
		res := make([]OverviewTicketRow, 0, len(in))
		for _, t := range in {
			if !a.canView(t.QueueID) {
				continue
			}
			row := OverviewTicketRow{OverviewTicket: t, Queue: ref(t.QueueID)}
			if t.AssigneeID != nil {
				row.AssigneeName = names[*t.AssigneeID]
			}
			res = append(res, row)
		}
		return res
	}
	out.OldestUnassigned, out.OldestWaiting = rows(data.OldestUnassigned), rows(data.OldestWaiting)
	if sla {
		out.NextToBreach = rows(data.NextToBreach)
	}
	return out, nil
}
