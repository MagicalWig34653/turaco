package public

import (
	"context"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
)

// UpcomingMaintenance is one Change of the maintenance calendar as other
// modules (the F8 briefing) see it. Details and related ids are available
// only when explicitly requested by an authorized caller.
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

// MaintenanceScope selects fields authorized for the recipient. Its zero
// value exposes only the Change id, reference, status and window.
type MaintenanceScope struct {
	IncludeChangeDetails bool
	IncludeAffectedIDs   bool
	IncludeInitiativeIDs bool
}

// DueMilestoneScope requires an explicit owner or an explicit all-owner read.
// The caller must authorize AllOwners before using it.
type DueMilestoneScope struct {
	OwnerID   string
	AllOwners bool
	Limit     int
}

// Bounds of the contract.
const (
	MaxRange   = application.MaxCalendarRange
	MaxEntries = application.MaxCalendarEntries
)

// ErrInvalidRange refuses a range that is empty, reversed or longer than MaxRange.
var ErrInvalidRange = errors.New("planning: invalid range")
var ErrInvalidScope = errors.New("planning: invalid scope")

// Planning is the module's public read service for other modules. It
// performs no permission check: the caller authorizes the requested scope.
type Planning struct{ svc *application.Service }

func New(svc *application.Service) *Planning { return &Planning{svc: svc} }

// UpcomingMaintenance lists the approved, scheduled and in-progress Changes
// whose maintenance window overlaps [from, to) (at most MaxRange), ordered by
// window start, at most MaxEntries; truncated reports more.
func (x *Planning) UpcomingMaintenance(ctx context.Context, from, to time.Time, scope MaintenanceScope) (items []UpcomingMaintenance, truncated bool, err error) {
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
		u := scopedMaintenance(e, byChange[c.ID], scope)
		out = append(out, u)
	}
	return out, truncated, nil
}

func scopedMaintenance(e application.ChangeCalendarEntry, initiativeIDs []string, scope MaintenanceScope) UpcomingMaintenance {
	c := e.Change
	u := UpcomingMaintenance{ChangeID: c.ID, Reference: c.Reference, Status: c.Status,
		WindowStart: *c.WindowStart, WindowEnd: *c.WindowEnd}
	if scope.IncludeChangeDetails {
		u.Title, u.Kind, u.Risk, u.RequesterID, u.OwnerID = c.Title, c.Kind, c.Risk, c.RequesterID, c.OwnerID
	}
	if scope.IncludeAffectedIDs {
		u.Affected = make([]Node, 0, len(e.Affected))
		for _, n := range e.Affected {
			u.Affected = append(u.Affected, Node{Type: n.Type, ID: n.ID})
		}
	}
	if scope.IncludeInitiativeIDs {
		u.InitiativeIDs = append([]string{}, initiativeIDs...)
	}
	return u
}

// DueMilestones lists the open Milestones due in [from, to] (dates, at most
// MaxRange apart) of approved, active and on-hold Initiatives, by due date, at
// most 200. A scope must select exactly one owner or AllOwners.
func (x *Planning) DueMilestones(ctx context.Context, from, to time.Time, scope DueMilestoneScope) ([]DueMilestone, error) {
	if (scope.OwnerID == "") == !scope.AllOwners {
		return nil, ErrInvalidScope
	}
	list, err := x.svc.DueMilestones(ctx, from, to, scope.OwnerID, scope.Limit)
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
