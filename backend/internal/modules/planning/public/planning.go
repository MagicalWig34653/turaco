package public

import (
	"context"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
)

// UpcomingMaintenance is one Change of the maintenance calendar as other
// modules (the F8 briefing) see it. It carries the requester and owner so the
// caller can apply the Changes read rule before it shows the title, and the
// ids of the Initiatives that include the Change. Affected holds every affected
// record (type service, vm, asset or location); the caller redacts them.
type UpcomingMaintenance struct {
	ChangeID      string
	Reference     string
	Title         string
	Kind          string
	Risk          string
	Status        string
	RequesterID   string
	OwnerID       *string
	WindowStart   time.Time
	WindowEnd     time.Time
	Affected      []Node
	InitiativeIDs []string
}

// Node is a record a Change affects.
type Node struct {
	Type string
	ID   string
}

// DueMilestone is an open Milestone of an approved, active or on-hold Initiative.
type DueMilestone struct {
	ID                  string
	Title               string
	DueDate             time.Time
	InitiativeID        string
	InitiativeReference string
	InitiativeTitle     string
	InitiativeOwnerID   string
}

// Bounds of the contract.
const (
	MaxRange   = application.MaxCalendarRange
	MaxEntries = application.MaxCalendarEntries
)

// ErrInvalidRange refuses a range that is empty, reversed or longer than MaxRange.
var ErrInvalidRange = errors.New("planning: invalid range")

// Planning is the module's public read service for other modules. It
// performs no permission check: the caller authorizes and redacts for its own
// user (planning.view|manage or the Initiative's owner for Initiatives and
// Milestones; the Changes read rule for Change titles).
type Planning struct{ svc *application.Service }

func New(svc *application.Service) *Planning { return &Planning{svc: svc} }

// UpcomingMaintenance lists the approved, scheduled and in-progress Changes
// whose maintenance window overlaps [from, to) (at most MaxRange), ordered by
// window start, at most MaxEntries; truncated reports more.
func (x *Planning) UpcomingMaintenance(ctx context.Context, from, to time.Time) (items []UpcomingMaintenance, truncated bool, err error) {
	entries, byChange, truncated, err := x.svc.RawCalendar(ctx, from, to)
	var inv *application.InvalidInputError
	if errors.As(err, &inv) {
		return nil, false, ErrInvalidRange
	}
	if err != nil {
		return nil, false, err
	}
	out := make([]UpcomingMaintenance, 0, len(entries))
	for _, e := range entries {
		c := e.Change
		if c.WindowStart == nil || c.WindowEnd == nil {
			continue
		}
		u := UpcomingMaintenance{ChangeID: c.ID, Reference: c.Reference, Title: c.Title, Kind: c.Kind, Risk: c.Risk, Status: c.Status,
			RequesterID: c.RequesterID, OwnerID: c.OwnerID, WindowStart: *c.WindowStart, WindowEnd: *c.WindowEnd,
			Affected: make([]Node, 0, len(e.Affected)), InitiativeIDs: append([]string{}, byChange[c.ID]...)}
		for _, n := range e.Affected {
			u.Affected = append(u.Affected, Node{Type: n.Type, ID: n.ID})
		}
		out = append(out, u)
	}
	return out, truncated, nil
}

// DueMilestones lists the open Milestones due in [from, to] (dates, at most
// MaxRange apart) of approved, active and on-hold Initiatives, by due date, at
// most 200. A non-empty ownerID restricts them to Initiatives that User owns.
func (x *Planning) DueMilestones(ctx context.Context, from, to time.Time, ownerID string) ([]DueMilestone, error) {
	list, err := x.svc.DueMilestones(ctx, from, to, ownerID)
	var inv *application.InvalidInputError
	if errors.As(err, &inv) {
		return nil, ErrInvalidRange
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.InitiativeID)
	}
	inits, err := x.svc.ByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]application.Initiative, len(inits))
	for _, i := range inits {
		byID[i.ID] = i
	}
	out := make([]DueMilestone, 0, len(list))
	for _, m := range list {
		i := byID[m.InitiativeID]
		out = append(out, DueMilestone{ID: m.ID, Title: m.Title, DueDate: m.DueDate, InitiativeID: m.InitiativeID,
			InitiativeReference: i.Reference, InitiativeTitle: i.Title, InitiativeOwnerID: i.OwnerID})
	}
	return out, nil
}
