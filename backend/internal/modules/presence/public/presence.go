// Package public is the contract other modules (Service Desk, Changes, Briefing, My Work) use to read Operational
// Availability and Team Coverage. Every call takes the viewer and applies the viewer's scope; a module that is
// switched off answers Disabled with unknown values and no data. Nobody reads presence tables.
package public

import (
	"context"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
)

// Viewer is who asks and which Presence permissions the caller holds.
type Viewer struct {
	UserID           string
	ViewAvailability bool
	ViewEntries      bool
}

func (v Viewer) principal() application.Principal {
	return application.Principal{UserID: v.UserID, ViewAvailability: v.ViewAvailability, ViewEntries: v.ViewEntries}
}

// Operational Availability values.
const (
	Available   = application.Available
	Limited     = application.Limited
	Unavailable = application.Unavailable
	Unknown     = application.Unknown
)

// Availability is the derived value of one User. Disabled is true when the module is off (Value is unknown).
type Availability struct {
	UserID      string
	Value       string
	Explanation string
	LimitedBy   string
	Until       *time.Time
	Sources     []application.SourceInfo
	// MayBeUnavailable is the neutral hint for a User outside the viewer's scope who is unavailable now.
	MayBeUnavailable bool
	Disabled         bool
}

// Presence reads the Presence module.
type Presence struct{ svc *application.Service }

func New(svc *application.Service) *Presence { return &Presence{svc: svc} }

// Need says whether the question needs the User on site.
type Need = application.Need

// Availability answers for each User at the instant. A disabled module returns unknown/Disabled for every id.
func (p *Presence) Availability(ctx context.Context, viewer Viewer, userIDs []string, at time.Time, need Need) ([]Availability, error) {
	res, err := p.svc.Availability(ctx, viewer.principal(), userIDs, at, need)
	if errors.Is(err, application.ErrDisabled) {
		out := make([]Availability, 0, len(userIDs))
		for _, id := range userIDs {
			out = append(out, Availability{UserID: id, Value: Unknown, Explanation: application.ExplainDisabled, Disabled: true})
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Availability, 0, len(res))
	for _, r := range res {
		out = append(out, Availability{UserID: r.UserID, Value: r.Value, Explanation: r.Explanation, LimitedBy: r.LimitedBy,
			Until: r.Until, Sources: r.Sources, MayBeUnavailable: r.MayBeUnavailable})
	}
	return out, nil
}

// WindowResult is the availability of one User across a window (at most 31 days).
type WindowResult struct {
	Disabled bool
	Segments []application.Segment
}

// AvailabilityWindow answers for one User over [from, to).
func (p *Presence) AvailabilityWindow(ctx context.Context, viewer Viewer, userID string, from, to time.Time, need Need) (WindowResult, error) {
	segs, err := p.svc.AvailabilityWindow(ctx, viewer.principal(), userID, from, to, need)
	if errors.Is(err, application.ErrDisabled) {
		return WindowResult{Disabled: true}, nil
	}
	return WindowResult{Segments: segs}, err
}

// CoverageResult is the Team Coverage of a window.
type CoverageResult struct {
	Disabled bool
	application.CoverageResult
}

// TeamCoverage counts a Team's availability per day. Names are never returned through this contract.
func (p *Presence) TeamCoverage(ctx context.Context, viewer Viewer, teamID string, from, to time.Time, timezone string) (CoverageResult, error) {
	v := viewer
	v.ViewEntries = false
	res, err := p.svc.TeamCoverage(ctx, application.Caller{}, v.principal(), teamID, from, to, timezone)
	if errors.Is(err, application.ErrDisabled) {
		return CoverageResult{Disabled: true, CoverageResult: application.CoverageResult{TeamID: teamID, State: application.CoverageUnknown}}, nil
	}
	return CoverageResult{CoverageResult: res}, err
}
