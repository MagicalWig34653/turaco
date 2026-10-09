package public

import (
	"context"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
)

// ChangeInfo is what other modules may know about a Change. It carries the
// requester and owner so the caller can apply the Changes read rule
// (changes.view|manage|execute, or being the requester or owner) before it
// shows the title.
type ChangeInfo struct {
	ID          string
	Reference   string
	Title       string
	Kind        string
	Risk        string
	Status      string
	RequesterID string
	OwnerID     *string
	WindowStart *time.Time
	WindowEnd   *time.Time
}

// Terminal reports that the Change can no longer be carried out.
func (c ChangeInfo) Terminal() bool {
	switch c.Status {
	case application.StatusClosed, application.StatusCancelled, application.StatusRejected:
		return true
	}
	return false
}

// Node is a record a Change affects (type service, vm, asset or location).
type Node struct {
	Type string
	ID   string
}

// CalendarEntry is a Change in the maintenance calendar with its affected resources.
type CalendarEntry struct {
	Change   ChangeInfo
	Affected []Node
}

// CalendarPage is one calendar read; Truncated reports more Changes than the limit.
type CalendarPage struct {
	Entries   []CalendarEntry
	Truncated bool
}

// ReadScope selects fields the caller is authorized to receive. Its zero value
// returns only a Change's id, reference, status and maintenance window.
type ReadScope struct {
	IncludeDetails     bool
	IncludeAffectedIDs bool
}

// Calendar bounds.
const (
	MaxCalendarRange   = application.MaxCalendarRange
	MaxCalendarEntries = application.MaxCalendarEntries
)

// ErrInvalidRange refuses a calendar range that is empty, reversed or longer than MaxCalendarRange.
var ErrInvalidRange = errors.New("changes: invalid calendar range")

// Changes is the Changes module's public read service for other modules.
type Changes struct{ svc *application.Service }

func NewChanges(svc *application.Service) *Changes { return &Changes{svc: svc} }

func info(c application.Change) ChangeInfo {
	return ChangeInfo{ID: c.ID, Reference: c.Reference, Title: c.Title, Kind: c.Kind, Risk: c.Risk, Status: c.Status,
		RequesterID: c.RequesterID, OwnerID: c.OwnerID, WindowStart: c.WindowStart, WindowEnd: c.WindowEnd}
}

func scopedInfo(c application.Change, scope ReadScope) ChangeInfo {
	i := info(c)
	if !scope.IncludeDetails {
		i.Title, i.Kind, i.Risk, i.RequesterID, i.OwnerID = "", "", "", "", nil
	}
	return i
}

// Lookup returns id -> Change for the existing Changes among ids (at most 500).
// The caller must authorize details; the zero scope returns reference and state only.
func (x *Changes) Lookup(ctx context.Context, ids []string, scope ReadScope) (map[string]ChangeInfo, error) {
	found, err := x.svc.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ChangeInfo, len(found))
	for id, c := range found {
		out[id] = scopedInfo(c, scope)
	}
	return out, nil
}

// Calendar lists the approved, scheduled and in-progress Changes whose
// maintenance window overlaps [from, to) (at most MaxCalendarRange), ordered by
// window start, at most limit (1 to MaxCalendarEntries). It performs no
// permission check. The caller must authorize details and affected ids before
// requesting them; the zero scope returns only reference and state metadata.
func (x *Changes) Calendar(ctx context.Context, from, to time.Time, limit int, scope ReadScope) (CalendarPage, error) {
	res, err := x.svc.Calendar(ctx, from, to, limit)
	return x.page(res, err, scope)
}

// CalendarWithProposed is Calendar plus the submitted Changes (in assessment or pending approval) with their
// proposed window; the status of an entry tells them apart.
func (x *Changes) CalendarWithProposed(ctx context.Context, from, to time.Time, limit int, scope ReadScope) (CalendarPage, error) {
	res, err := x.svc.CalendarWithProposed(ctx, from, to, limit)
	return x.page(res, err, scope)
}

func (x *Changes) page(res application.CalendarPage, err error, scope ReadScope) (CalendarPage, error) {
	var inv *application.InvalidInputError
	if errors.As(err, &inv) {
		return CalendarPage{}, ErrInvalidRange
	}
	if err != nil {
		return CalendarPage{}, err
	}
	out := CalendarPage{Truncated: res.Truncated, Entries: make([]CalendarEntry, 0, len(res.Entries))}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, scopedCalendarEntry(e, scope))
	}
	return out, nil
}

func scopedCalendarEntry(e application.CalendarEntry, scope ReadScope) CalendarEntry {
	entry := CalendarEntry{Change: scopedInfo(e.Change, scope)}
	if scope.IncludeAffectedIDs {
		entry.Affected = make([]Node, 0, len(e.Affected))
		for _, n := range e.Affected {
			entry.Affected = append(entry.Affected, Node{Type: n.Type, ID: n.ID})
		}
	}
	return entry
}
