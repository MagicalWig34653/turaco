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

// Lookup returns id -> Change for the existing Changes among ids (at most 500).
// It performs no permission check.
func (x *Changes) Lookup(ctx context.Context, ids []string) (map[string]ChangeInfo, error) {
	found, err := x.svc.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ChangeInfo, len(found))
	for id, c := range found {
		out[id] = info(c)
	}
	return out, nil
}

// Calendar lists the approved, scheduled and in-progress Changes whose
// maintenance window overlaps [from, to) (at most MaxCalendarRange), ordered by
// window start, at most limit (1 to MaxCalendarEntries). It performs no
// permission check: the caller redacts titles and affected resources for its
// own user.
func (x *Changes) Calendar(ctx context.Context, from, to time.Time, limit int) (CalendarPage, error) {
	res, err := x.svc.Calendar(ctx, from, to, limit)
	var inv *application.InvalidInputError
	if errors.As(err, &inv) {
		return CalendarPage{}, ErrInvalidRange
	}
	if err != nil {
		return CalendarPage{}, err
	}
	out := CalendarPage{Truncated: res.Truncated, Entries: make([]CalendarEntry, 0, len(res.Entries))}
	for _, e := range res.Entries {
		entry := CalendarEntry{Change: info(e.Change), Affected: make([]Node, 0, len(e.Affected))}
		for _, n := range e.Affected {
			entry.Affected = append(entry.Affected, Node{Type: n.Type, ID: n.ID})
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}
