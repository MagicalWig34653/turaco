package application

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// EntryView is an entry with its occurrences inside the requested window.
type EntryView struct {
	Entry       Entry
	Occurrences []Interval
}

func views(entries []Entry, from, to time.Time) []EntryView {
	out := make([]EntryView, 0, len(entries))
	for _, e := range entries {
		if occ := e.Occurrences(from, to); len(occ) > 0 {
			out = append(out, EntryView{Entry: e, Occurrences: occ})
		}
	}
	return out
}

// MyEntries lists the viewer's own entries with occurrences in the window.
func (s *Service) MyEntries(ctx context.Context, p Principal, from, to time.Time) ([]EntryView, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	if p.UserID == "" || !(p.ManageOwn || p.ManageEntries || p.ViewAvailability) {
		return nil, ErrForbidden
	}
	from, to, err := s.checkWindow(from, to)
	if err != nil {
		return nil, err
	}
	entries, err := s.store.EntriesInWindow(ctx, []string{p.UserID}, []string{p.UserID}, from, to)
	if err != nil {
		return nil, err
	}
	return views(entries, from, to), nil
}

// EntryFilter selects the entries of other Users in the viewer's scope.
type EntryFilter struct {
	UserID, TeamID string
	From, To       time.Time
}

// canSeeDetail reports whether the viewer may see the detail of another User's entry: the owner allowed it, or
// the viewer entered it.
func canSeeDetail(e Entry, viewer string) bool {
	return e.Visibility == VisibilityDetail || (e.CreatedBy != nil && *e.CreatedBy == viewer)
}

// Entries lists the detail of other Users' entries within the viewer's scope (presence.view_entries). Entries
// whose owner kept them to availability are not returned. Every read is audited: viewer, scope, subject count and
// window length, never entry content.
func (s *Service) Entries(ctx context.Context, c Caller, p Principal, f EntryFilter) ([]EntryView, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	if !p.ViewEntries || p.UserID == "" {
		return nil, ErrForbidden
	}
	from, to, err := s.checkWindow(f.From, f.To)
	if err != nil {
		return nil, err
	}
	scope, err := s.scope(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	subjects, scopeKind := scope, "own_teams"
	switch {
	case f.UserID != "":
		id, err := checkID(f.UserID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(scope, id) {
			return nil, ErrNotFound
		}
		subjects, scopeKind = []string{id}, "user"
	case f.TeamID != "":
		id, err := checkID(f.TeamID)
		if err != nil {
			return nil, err
		}
		teams, err := s.dir.CurrentTeamIDs(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(teams, id) {
			return nil, ErrNotFound
		}
		if subjects, err = s.dir.CurrentMemberIDs(ctx, id); err != nil {
			return nil, err
		}
		scopeKind = "team"
	}
	subjects = slices.DeleteFunc(slices.Clone(subjects), func(u string) bool { return u == p.UserID })
	entries, err := s.store.EntriesInWindow(ctx, scope, subjects, from, to)
	if err != nil {
		return nil, err
	}
	entries = slices.DeleteFunc(entries, func(e Entry) bool { return !canSeeDetail(e, p.UserID) })
	out := views(entries, from, to)
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		return recordAudit(ctx, tx, c, "entries.detail_viewed", "presence_scope", p.UserID, nil, nil, map[string]any{
			"scope": scopeKind, "subjectCount": len(subjects), "windowDays": int(to.Sub(from).Hours() / 24)})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Availability ----------------------------------------------------------------------------------------

// AvailabilityResult is the Operational Availability of one User at an instant.
type AvailabilityResult struct {
	UserID      string
	Value       string
	Explanation string
	// LimitedBy is remote, travelling or other_location when Value is limited.
	LimitedBy string
	// Until is when the value is expected to change, nil when it holds for at least the next 31 days.
	Until   *time.Time
	Sources []SourceInfo
	// MayBeUnavailable is set for a User outside the viewer's scope who is unavailable now: the only fact
	// that leaves the scope (the Ticket assignment warning); it is not part of the HTTP API.
	MayBeUnavailable bool
}

func (s *Service) userSegments(ctx context.Context, scope []string, userIDs []string, from, to time.Time, need Need) (map[string][]Segment, error) {
	entries, err := s.store.EntriesInWindow(ctx, scope, userIDs, from, to)
	if err != nil {
		return nil, err
	}
	by := map[string][]Entry{}
	for _, e := range entries {
		by[e.UserID] = append(by[e.UserID], e)
	}
	now := s.now()
	out := make(map[string][]Segment, len(userIDs))
	for _, id := range userIDs {
		out[id] = derive(by[id], from, to, need, now, s.staleAfter)
	}
	return out, nil
}

// Availability derives the Operational Availability of the Users at the instant. Users outside the viewer's
// scope are unknown (out_of_scope) apart from the MayBeUnavailable hint. ErrDisabled when the module is off.
func (s *Service) Availability(ctx context.Context, p Principal, userIDs []string, at time.Time, need Need) ([]AvailabilityResult, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, ErrForbidden
	}
	if len(userIDs) == 0 || len(userIDs) > MaxAvailabilityIDs {
		return nil, invalid("give 1 to %d user ids", MaxAvailabilityIDs)
	}
	at = at.UTC()
	if _, _, err := s.checkWindow(at, at.Add(time.Minute)); err != nil {
		return nil, err
	}
	var ids []string
	for _, raw := range userIDs {
		id, err := checkID(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	scope, err := s.scope(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	var inside, outside []string
	for _, id := range ids {
		if slices.Contains(scope, id) && (id == p.UserID || p.ViewAvailability) {
			inside = append(inside, id)
		} else {
			outside = append(outside, id)
		}
	}
	to := at.Add(MaxWindow)
	segs, err := s.userSegments(ctx, inside, inside, at, to, need)
	if err != nil {
		return nil, err
	}
	hint, err := s.userSegments(ctx, outside, outside, at, at.Add(time.Minute), Need{})
	if err != nil {
		return nil, err
	}
	out := make([]AvailabilityResult, 0, len(ids))
	for _, id := range ids {
		if list, ok := segs[id]; ok && len(list) > 0 {
			first := list[0]
			r := AvailabilityResult{UserID: id, Value: first.Value, Explanation: first.Explanation, LimitedBy: first.LimitedBy, Sources: first.Sources}
			if first.To.Before(to) {
				t := first.To
				r.Until = &t
			}
			out = append(out, r)
			continue
		}
		r := AvailabilityResult{UserID: id, Value: Unknown, Explanation: ExplainOutOfScope}
		if list := hint[id]; len(list) > 0 && list[0].Value == Unavailable {
			r.MayBeUnavailable = true
		}
		out = append(out, r)
	}
	return out, nil
}

// AvailabilityWindow derives the segments of one User over the window. A User outside the viewer's scope is one
// unknown segment.
func (s *Service) AvailabilityWindow(ctx context.Context, p Principal, userID string, from, to time.Time, need Need) ([]Segment, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, ErrForbidden
	}
	id, err := checkID(userID)
	if err != nil {
		return nil, err
	}
	from, to, err = s.checkWindow(from, to)
	if err != nil {
		return nil, err
	}
	scope, err := s.scope(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(scope, id) || (id != p.UserID && !p.ViewAvailability) {
		return []Segment{{From: from, To: to, Value: Unknown, Explanation: ExplainOutOfScope}}, nil
	}
	segs, err := s.userSegments(ctx, scope, []string{id}, from, to, need)
	if err != nil {
		return nil, err
	}
	return segs[id], nil
}

// ---- Team Coverage ---------------------------------------------------------------------------------------

// Coverage states.
const (
	CoverageOK      = "ok"
	CoverageBelow   = "below"
	CoverageUnknown = "unknown"
)

// CoverageDay counts the Team's members per value at local midday of the day. Nobody is named here.
type CoverageDay struct {
	Date        string
	Available   int
	Limited     int
	Unavailable int
	Unknown     int
	// OnsiteAvailable counts members available on site; set only when the Team has an on-site minimum.
	OnsiteAvailable *int
	State           string
	// UnavailableUserIDs names the unavailable members; only for presence.view_entries holders and only for
	// entries their owners made visible in detail (or the viewer entered).
	UnavailableUserIDs []string
}

// CoverageResult is the Team Coverage of a window.
type CoverageResult struct {
	TeamID  string
	Minimum *Minimum
	// Members is the number of current Team members; nil (and Days empty) for a Team below MinCoverageMembers,
	// whose counts would reveal one person.
	Members *int
	State   string
	Days    []CoverageDay
}

// TeamCoverage counts the Team's availability per day. The viewer must hold presence.view_availability and be a
// current member of the Team. Counts are a snapshot at 12:00 local time in the time zone (UTC when empty).
func (s *Service) TeamCoverage(ctx context.Context, p Principal, teamID string, from, to time.Time, timezone string) (CoverageResult, error) {
	if _, err := s.active(ctx); err != nil {
		return CoverageResult{}, err
	}
	if !p.ViewAvailability || p.UserID == "" {
		return CoverageResult{}, ErrForbidden
	}
	id, err := checkID(teamID)
	if err != nil {
		return CoverageResult{}, ErrNotFound
	}
	from, to, err = s.checkWindow(from, to)
	if err != nil {
		return CoverageResult{}, err
	}
	loc := time.UTC
	if timezone != "" {
		if loc, err = loadZone(timezone); err != nil {
			return CoverageResult{}, err
		}
	}
	teams, err := s.dir.CurrentTeamIDs(ctx, p.UserID)
	if err != nil {
		return CoverageResult{}, err
	}
	if !slices.Contains(teams, id) {
		return CoverageResult{}, ErrNotFound
	}
	members, err := s.dir.CurrentMemberIDs(ctx, id)
	if err != nil {
		return CoverageResult{}, err
	}
	min, hasMin, err := s.store.GetMinimum(ctx, id)
	if err != nil {
		return CoverageResult{}, err
	}
	res := CoverageResult{TeamID: id, State: CoverageUnknown}
	if hasMin {
		res.Minimum = &min
	}
	if len(members) < MinCoverageMembers {
		return res, nil
	}
	n := len(members)
	res.Members = &n

	// Local midday of every date touched by the window.
	var noons []time.Time
	first := from.In(loc)
	for d := time.Date(first.Year(), first.Month(), first.Day(), 12, 0, 0, 0, loc); d.Before(to); d = time.Date(d.Year(), d.Month(), d.Day()+1, 12, 0, 0, 0, loc) {
		noons = append(noons, d)
	}
	if len(noons) == 0 {
		return res, nil
	}
	wFrom, wTo := noons[0].UTC().Add(-time.Minute), noons[len(noons)-1].UTC().Add(time.Minute)
	entries, err := s.store.EntriesInWindow(ctx, members, members, wFrom, wTo)
	if err != nil {
		return CoverageResult{}, err
	}
	byUser := map[string][]Entry{}
	visible := map[string][]Entry{}
	for _, e := range entries {
		byUser[e.UserID] = append(byUser[e.UserID], e)
		if canSeeDetail(e, p.UserID) {
			visible[e.UserID] = append(visible[e.UserID], e)
		}
	}
	needOnsite := Need{}
	onsite := hasMin && min.OnsiteMinimum != nil
	if onsite {
		needOnsite = Need{OnSite: true}
		if min.LocationID != nil {
			needOnsite.LocationID = *min.LocationID
		}
	}
	now := s.now()
	anyBelow, anyOK := false, false
	for _, noon := range noons {
		day := CoverageDay{Date: noon.Format(time.DateOnly), State: CoverageUnknown}
		onsiteCount := 0
		for _, m := range members {
			value := valueAt(byUser[m], noon, Need{}, now, s.staleAfter)
			switch value {
			case Available:
				day.Available++
			case Limited:
				day.Limited++
			case Unavailable:
				day.Unavailable++
				if p.ViewEntries && valueAt(visible[m], noon, Need{}, now, s.staleAfter) == Unavailable {
					day.UnavailableUserIDs = append(day.UnavailableUserIDs, m)
				}
			default:
				day.Unknown++
			}
			if onsite && valueAt(byUser[m], noon, needOnsite, now, s.staleAfter) == Available {
				onsiteCount++
			}
		}
		if onsite {
			c := onsiteCount
			day.OnsiteAvailable = &c
		}
		if hasMin && day.Unknown < len(members) {
			day.State = CoverageOK
			if day.Available < min.Minimum || (onsite && onsiteCount < *min.OnsiteMinimum) {
				day.State = CoverageBelow
			}
		}
		anyBelow = anyBelow || day.State == CoverageBelow
		anyOK = anyOK || day.State == CoverageOK
		slices.Sort(day.UnavailableUserIDs)
		res.Days = append(res.Days, day)
	}
	switch {
	case anyBelow:
		res.State = CoverageBelow
	case anyOK:
		res.State = CoverageOK
	}
	return res, nil
}

// valueAt is the derived value of the entries at one instant.
func valueAt(entries []Entry, at time.Time, need Need, now time.Time, stale time.Duration) string {
	segs := derive(entries, at, at.Add(time.Minute), need, now, stale)
	if len(segs) == 0 {
		return Unknown
	}
	return segs[0].Value
}
