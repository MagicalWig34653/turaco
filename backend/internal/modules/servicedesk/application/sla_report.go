package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SLA report minimum (F17 S-E): how many finished SLA clocks met, breached or were exempted in a period, grouped by
// Queue, Priority or month. It reads the clock rows for Queues the caller may view, exactly the manager overview's
// scope, and adds no permission. There is deliberately no grouping or filter by assignee, Team or any person
// (ADR-0041 D5): the report measures the desk, not the people.

// Report groupings.
const (
	SLAGroupQueue    = "queue"
	SLAGroupPriority = "priority"
	SLAGroupMonth    = "month"
)

// SLAReportMaxDays bounds the period so one request reads a bounded amount of clock rows.
const SLAReportMaxDays = 366

// SLAReportQuery is the store's input. To is exclusive.
type SLAReportQuery struct {
	From, To time.Time
	GroupBy  string
	// Global means every Queue is viewable; otherwise QueueIDs lists the viewable ones.
	Global   bool
	QueueIDs []string
}

// SLAReportRow is one group and clock kind as the store aggregates it.
type SLAReportRow struct {
	Group    string
	Kind     string
	Measured int
	Met      int
	Breached int
	Exempted int
	// MedianSeconds and P90Seconds are percentiles of the business time the completed clocks needed (nil when none).
	MedianSeconds *float64
	P90Seconds    *float64
}

// SLAReportStore is the persistence port of the report.
type SLAReportStore interface {
	SLAReport(ctx context.Context, q SLAReportQuery) ([]SLAReportRow, error)
}

// SLAReportKind is the figures of one clock kind in one group.
type SLAReportKind struct {
	Kind     string
	Measured int
	Met      int
	Breached int
	Exempted int
	// MetRate is Met/Measured (nil when nothing was measured).
	MetRate       *float64
	MedianSeconds *int64
	P90Seconds    *int64
}

// SLAReportGroup is one row of the report. Queue is set for groupBy=queue.
type SLAReportGroup struct {
	Key   string
	Queue *QueueRef
	Kinds []SLAReportKind
}

// SLAReport is the report of one caller.
type SLAReport struct {
	From, To time.Time
	GroupBy  string
	Groups   []SLAReportGroup
}

// SLAReport returns the report for clocks completed (or exempted) between from and to, both inclusive days in UTC.
func (s *Service) SLAReport(ctx context.Context, p Principal, from, to time.Time, groupBy, queueID string) (SLAReport, error) {
	if p.UserID == "" || p.External {
		return SLAReport{}, ErrForbidden
	}
	switch groupBy {
	case SLAGroupQueue, SLAGroupPriority, SLAGroupMonth:
	default:
		return SLAReport{}, invalid("groupBy must be queue, priority or month")
	}
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	if to.Before(from) {
		return SLAReport{}, invalid("from must not be after to")
	}
	if int(to.Sub(from).Hours()/24)+1 > SLAReportMaxDays {
		return SLAReport{}, invalid("the period must not exceed %d days", SLAReportMaxDays)
	}
	if queueID != "" && !uuidPattern.MatchString(queueID) {
		return SLAReport{}, invalid("queueId must be a Queue id")
	}
	rs, ok := s.store.(SLAReportStore)
	if !ok {
		return SLAReport{}, fmt.Errorf("servicedesk: store does not support the SLA report")
	}
	on, err := s.slaOn(ctx)
	if err != nil {
		return SLAReport{}, err
	}
	if !on {
		return SLAReport{}, ErrForbidden
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return SLAReport{}, err
	}
	if !a.anyView() {
		return SLAReport{}, ErrForbidden
	}
	q := SLAReportQuery{From: from, To: to.AddDate(0, 0, 1), GroupBy: groupBy, Global: a.global(), QueueIDs: a.viewIDs()}
	if queueID != "" {
		// A Queue the caller does not view yields an empty report, the same as an empty Queue.
		q.Global, q.QueueIDs = false, nil
		if a.canView(strings.ToLower(queueID)) {
			q.QueueIDs = []string{strings.ToLower(queueID)}
		}
	}
	rows := []SLAReportRow{}
	if q.Global || len(q.QueueIDs) > 0 {
		if rows, err = rs.SLAReport(ctx, q); err != nil {
			return SLAReport{}, fmt.Errorf("sla report: %w", err)
		}
	}
	out := SLAReport{From: from, To: to, GroupBy: groupBy, Groups: []SLAReportGroup{}}
	index := map[string]int{}
	for _, r := range rows {
		if groupBy == SLAGroupQueue && !a.canView(r.Group) {
			continue // defensive: the store already filtered
		}
		i, seen := index[r.Group]
		if !seen {
			g := SLAReportGroup{Key: r.Group}
			if groupBy == SLAGroupQueue {
				ref := a.queues[r.Group].Queue
				g.Queue = &QueueRef{ID: ref.ID, Key: ref.Key, Prefix: ref.Prefix, Name: ref.Name}
			}
			out.Groups = append(out.Groups, g)
			i = len(out.Groups) - 1
			index[r.Group] = i
		}
		k := SLAReportKind{Kind: r.Kind, Measured: r.Measured, Met: r.Met, Breached: r.Breached, Exempted: r.Exempted,
			MedianSeconds: roundSeconds(r.MedianSeconds), P90Seconds: roundSeconds(r.P90Seconds)}
		if r.Measured > 0 {
			rate := float64(r.Met) / float64(r.Measured)
			k.MetRate = &rate
		}
		out.Groups[i].Kinds = append(out.Groups[i].Kinds, k)
	}
	sortReportGroups(out.Groups, groupBy)
	return out, nil
}

func roundSeconds(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v + 0.5)
	return &n
}

func sortReportGroups(gs []SLAReportGroup, groupBy string) {
	rank := map[string]int{"urgent": 0, "high": 1, "normal": 2, "low": 3}
	sort.SliceStable(gs, func(i, j int) bool {
		switch groupBy {
		case SLAGroupQueue:
			x, y := strings.ToLower(gs[i].Queue.Name), strings.ToLower(gs[j].Queue.Name)
			if x != y {
				return x < y
			}
		case SLAGroupPriority:
			return rank[gs[i].Key] < rank[gs[j].Key]
		}
		return gs[i].Key < gs[j].Key
	})
	for i := range gs {
		sort.SliceStable(gs[i].Kinds, func(a, b int) bool { return gs[i].Kinds[a].Kind < gs[i].Kinds[b].Kind })
	}
}
