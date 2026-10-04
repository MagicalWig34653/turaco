package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Maintenance calendar bounds (docs/product/f7-infrastructure-change-design.md, slice 4).
const (
	// MaxCalendarRange is the longest range one calendar read covers.
	MaxCalendarRange = 92 * 24 * time.Hour
	// MaxCalendarEntries bounds the Changes of one calendar read.
	MaxCalendarEntries = 500
	// calendarLinkChunk keeps one AFFECTS batch read within relationships.MaxSources (200 x 25).
	calendarLinkChunk = 200
	// MaxLookupIDs bounds one Lookup.
	MaxLookupIDs = 500
)

// CalendarStatuses are the statuses whose maintenance windows the calendar
// shows: approved (planned), scheduled and in progress.
var CalendarStatuses = []string{StatusApproved, StatusScheduled, StatusInProgress}

// CalendarEntry is a Change whose maintenance window lies in the calendar range,
// with the resources it affects (current AFFECTS links, at most MaxAffected).
type CalendarEntry struct {
	Change   Change
	Affected []relationships.Node
}

// CalendarPage is one calendar read; Truncated reports that more Changes than
// the limit overlap the range.
type CalendarPage struct {
	Entries   []CalendarEntry
	Truncated bool
}

// Calendar lists the approved, scheduled and in-progress Changes whose
// maintenance window overlaps [from, to) (a window ending exactly at from or
// starting exactly at to does not overlap), ordered by window start, at most
// limit (1 to MaxCalendarEntries). The range must not be longer than
// MaxCalendarRange. It performs no permission check: it is the contract for
// read models of other modules (Planning's maintenance calendar, the
// briefing), which authorize and redact for their own caller.
func (s *Service) Calendar(ctx context.Context, from, to time.Time, limit int) (CalendarPage, error) {
	if !to.After(from) {
		return CalendarPage{}, invalid("the calendar range must end after it starts")
	}
	if to.Sub(from) > MaxCalendarRange {
		return CalendarPage{}, invalid("the calendar range may cover at most 92 days")
	}
	if limit <= 0 || limit > MaxCalendarEntries {
		limit = MaxCalendarEntries
	}
	list, err := s.store.InWindow(ctx, CalendarStatuses, from.UTC(), to.UTC(), limit+1)
	if err != nil {
		return CalendarPage{}, err
	}
	page := CalendarPage{Entries: make([]CalendarEntry, 0, len(list))}
	if len(list) > limit {
		list, page.Truncated = list[:limit], true
	}
	affected := map[string][]relationships.Node{}
	for start := 0; start < len(list); start += calendarLinkChunk {
		end := min(start+calendarLinkChunk, len(list))
		ids := make([]string, 0, end-start)
		for _, c := range list[start:end] {
			ids = append(ids, c.ID)
		}
		links, _, err := s.graph.OutgoingFrom(ctx, s.store.Q(), NodeChange, ids, []string{RelAffects}, calendarLinkChunk*MaxAffected)
		if err != nil {
			return CalendarPage{}, fmt.Errorf("list affected resources: %w", err)
		}
		for _, r := range links {
			affected[r.Source.ID] = append(affected[r.Source.ID], r.Target)
		}
	}
	for _, c := range list {
		page.Entries = append(page.Entries, CalendarEntry{Change: c, Affected: affected[c.ID]})
	}
	return page, nil
}

// Lookup returns id -> Change for the existing Changes among ids (at most
// MaxLookupIDs). It performs no permission check: it is the contract other
// modules use after authorizing their own caller (Planning validates and shows
// the Changes an Initiative includes).
func (s *Service) Lookup(ctx context.Context, ids []string) (map[string]Change, error) {
	if len(ids) > MaxLookupIDs {
		return nil, invalid("at most %d changes can be looked up at once", MaxLookupIDs)
	}
	norm := make([]string, 0, len(ids))
	for _, id := range ids {
		norm = append(norm, strings.ToLower(id))
	}
	found, err := s.store.ByIDs(ctx, norm)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Change, len(found))
	for _, c := range found {
		out[c.ID] = c
	}
	return out, nil
}
