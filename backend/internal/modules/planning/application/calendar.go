package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// CalendarAffected is an affected Service or Location of a calendar entry as the
// caller may see it (Hidden: placeholder id, no name; numbered per row).
type CalendarAffected struct {
	Type      string
	ID        string
	Name      *string
	Reference *string
	Missing   bool
	Hidden    bool
}

// InitiativeRef names an Initiative that includes a Change.
type InitiativeRef struct {
	ID        string
	Reference string
	Title     string
}

// CalendarItem is one Change in the maintenance calendar. Title is nil when
// the caller may not read the Change (changes.view|manage|execute, its
// requester or its owner); Kind and Risk follow the same rule.
// Initiatives lists only Initiatives the caller may read.
type CalendarItem struct {
	ChangeID    string
	Reference   string
	Title       *string
	Kind        *string
	Risk        *string
	Status      string
	WindowStart time.Time
	WindowEnd   time.Time
	Affected    []CalendarAffected
	Initiatives []InitiativeRef
}

// Calendar is one maintenance calendar read; Truncated reports more Changes than MaxCalendarEntries.
type Calendar struct {
	From      time.Time
	To        time.Time
	Items     []CalendarItem
	Truncated bool
}

// mayReadCalendar reports that the caller may open the maintenance calendar:
// planning.view|manage or changes.view|manage|execute.
func (p Principal) mayReadCalendar() bool { return p.readsAll() || p.ChangesView }

func checkRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() {
		return invalid("from and to are required")
	}
	if !to.After(from) {
		return invalid("the calendar range must end after it starts")
	}
	if to.Sub(from) > MaxCalendarRange {
		return invalid("the calendar range may cover at most 92 days")
	}
	return nil
}

// RawCalendar is the unredacted calendar (for the public contract): every
// approved, scheduled and in-progress Change whose window overlaps [from, to)
// with its affected records and the ids of the Initiatives that include it.
func (s *Service) RawCalendar(ctx context.Context, from, to time.Time) ([]ChangeCalendarEntry, map[string][]string, bool, error) {
	if err := checkRange(from, to); err != nil {
		return nil, nil, false, err
	}
	entries, truncated, err := s.changes.Calendar(ctx, from.UTC(), to.UTC(), MaxCalendarEntries)
	if errors.Is(err, ErrInvalidRange) {
		return nil, nil, false, invalid("invalid calendar range")
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("load maintenance windows: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.Change.ID)
	}
	byChange := map[string][]string{}
	if len(ids) > 0 {
		links, linksTruncated, err := s.graph.IncomingTo(ctx, s.store.Q(), NodeChange, ids, NodeInitiative, []string{RelIncludes}, 0)
		if err != nil {
			return nil, nil, false, fmt.Errorf("list including initiatives: %w", err)
		}
		truncated = truncated || linksTruncated
		for _, r := range links {
			byChange[r.Target.ID] = append(byChange[r.Target.ID], r.Source.ID)
		}
	}
	return entries, byChange, truncated, nil
}

// MaintenanceCalendar lists the approved, scheduled and in-progress Changes
// whose maintenance window overlaps [from, to) (at most 92 days, at most
// MaxCalendarEntries Changes, ordered by window start). It needs planning.view|manage
// or changes.view|manage|execute. Titles appear only for Changes the caller may
// read; affected Services need services.view and Locations infrastructure.view
// to be named (otherwise hidden placeholders); other affected record types are
// not listed. Initiatives that include a Change are listed when the caller may read them.
func (s *Service) MaintenanceCalendar(ctx context.Context, p Principal, from, to time.Time) (Calendar, error) {
	if p.UserID == "" || !p.mayReadCalendar() {
		return Calendar{}, ErrForbidden
	}
	entries, byChange, truncated, err := s.RawCalendar(ctx, from, to)
	if err != nil {
		return Calendar{}, err
	}
	out := Calendar{From: from.UTC(), To: to.UTC(), Truncated: truncated, Items: make([]CalendarItem, 0, len(entries))}
	var serviceIDs, locationIDs, initiativeIDs []string
	for _, e := range entries {
		for _, n := range e.Affected {
			switch {
			case n.Type == NodeService && !p.hides(NodeService):
				serviceIDs = append(serviceIDs, n.ID)
			case n.Type == NodeLocation && !p.hides(NodeLocation):
				locationIDs = append(locationIDs, n.ID)
			}
		}
		initiativeIDs = append(initiativeIDs, byChange[e.Change.ID]...)
	}
	services := map[string]ServiceInfo{}
	serviceIDs = dedupe(serviceIDs)
	for start := 0; start < len(serviceIDs); start += 500 {
		found, lookupErr := s.services.Lookup(ctx, serviceIDs[start:min(start+500, len(serviceIDs))])
		if lookupErr != nil {
			return Calendar{}, fmt.Errorf("load services: %w", lookupErr)
		}
		for id, info := range found {
			services[id] = info
		}
	}
	locations := map[string]string{}
	locationIDs = dedupe(locationIDs)
	for start := 0; start < len(locationIDs); start += 500 {
		found, lookupErr := s.dir.LocationNames(ctx, locationIDs[start:min(start+500, len(locationIDs))])
		if lookupErr != nil {
			return Calendar{}, fmt.Errorf("load locations: %w", lookupErr)
		}
		for id, name := range found {
			locations[id] = name
		}
	}
	initiatives := map[string]Initiative{}
	if len(initiativeIDs) > 0 {
		list, err := s.store.ByIDs(ctx, dedupe(initiativeIDs))
		if err != nil {
			return Calendar{}, err
		}
		for _, i := range list {
			if p.readsAll() || i.OwnerID == p.UserID {
				initiatives[i.ID] = i
			}
		}
	}
	for _, e := range entries {
		var m masker
		c := e.Change
		if c.WindowStart == nil || c.WindowEnd == nil {
			continue
		}
		item := CalendarItem{ChangeID: c.ID, Reference: c.Reference, Status: c.Status,
			WindowStart: *c.WindowStart, WindowEnd: *c.WindowEnd, Affected: []CalendarAffected{}, Initiatives: []InitiativeRef{}}
		if p.ChangesView || c.RequesterID == p.UserID || c.OwnerID != nil && *c.OwnerID == p.UserID {
			title := c.Title
			item.Title = &title
			item.Kind = &c.Kind
			item.Risk = &c.Risk
		}
		for _, n := range e.Affected {
			if n.Type != NodeService && n.Type != NodeLocation {
				continue
			}
			a := CalendarAffected{Type: n.Type, ID: n.ID}
			switch {
			case p.hides(n.Type):
				a.Hidden, a.ID = true, m.id(n.ID)
			case n.Type == NodeService:
				if v, ok := services[n.ID]; ok {
					a.Name, a.Reference = &v.Name, &v.Reference
				} else {
					a.Missing = true
				}
			default:
				if name, ok := locations[n.ID]; ok {
					a.Name = &name
				} else {
					a.Missing = true
				}
			}
			item.Affected = append(item.Affected, a)
		}
		for _, id := range byChange[c.ID] {
			if i, ok := initiatives[id]; ok {
				item.Initiatives = append(item.Initiatives, InitiativeRef{ID: i.ID, Reference: i.Reference, Title: i.Title})
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// DueMilestones lists open Milestones due in [from, to] (dates) of Initiatives
// that are approved, active or on hold, by due date, at most MaxDueMilestones.
// It performs no permission check: it is the contract for other modules' read
// models (the briefing), which authorize for their own caller.
func (s *Service) DueMilestones(ctx context.Context, from, to time.Time, ownerID string) ([]Milestone, error) {
	from, to = day(from), day(to)
	if to.Before(from) || to.Sub(from) > MaxCalendarRange {
		return nil, invalid("the range must not be reversed or longer than 92 days")
	}
	return s.store.DueMilestones(ctx, from, to, []string{StatusApproved, StatusActive, StatusOnHold}, ownerID, MaxDueMilestones)
}

// ByIDs returns the Initiatives among ids (for the public contract; no permission check).
func (s *Service) ByIDs(ctx context.Context, ids []string) ([]Initiative, error) {
	if len(ids) > MaxCalendarEntries {
		return nil, invalid("at most %d initiatives can be looked up at once", MaxCalendarEntries)
	}
	return s.store.ByIDs(ctx, ids)
}

func dedupe(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
