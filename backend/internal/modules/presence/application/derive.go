package application

import (
	"slices"
	"time"
)

// Freshness of a contributing source.
const (
	FreshnessManual = "manual"
	FreshnessFresh  = "fresh"
	FreshnessStale  = "stale"
)

// Additional explanation code for a work location that is not the one asked for.
const ExplainOtherLocation = "other_location"

// Need is what an availability question is about: with OnSite the User must be at a Location (optionally the
// given one); without, any work location counts (remote included).
type Need struct {
	OnSite     bool
	LocationID string
}

// SourceInfo is a contributing source and how fresh its signal is. Manual entries are always current.
type SourceInfo struct {
	Source     string
	Freshness  string
	ObservedAt *time.Time
}

// Segment is a stretch of time with one derived value. Segments of a window are contiguous and ascending.
type Segment struct {
	From, To    time.Time
	Value       string
	Explanation string
	// LimitedBy is "remote", "travelling" or "other_location" when Value is limited.
	LimitedBy string
	Sources   []SourceInfo
}

type occurrence struct {
	Interval
	e     Entry
	stale bool
}

func (o occurrence) contains(a, b time.Time) bool { return !o.From.After(a) && !o.To.Before(b) }

// derive computes the Operational Availability of one User over [from, to) from that User's entries. The
// precedence is fixed: any applicable unavailable entry wins; otherwise a work location that satisfies the
// need is available; any other work location is limited; no applicable entry (or only a stale external
// signal) is unknown, never available. External and manual entries are not merged silently: every segment
// lists its contributing sources with freshness.
func derive(entries []Entry, from, to time.Time, need Need, now time.Time, staleAfter time.Duration) []Segment {
	if !from.Before(to) {
		return nil
	}
	var occs []occurrence
	cuts := []time.Time{from, to}
	for _, e := range entries {
		if e.Status != StatusActive {
			continue
		}
		stale := false
		if e.Source != SourceManual {
			if e.ObservedTo != nil {
				continue
			}
			stale = e.ObservedAt == nil || now.Sub(*e.ObservedAt) > staleAfter
		}
		for _, iv := range e.Occurrences(from, to) {
			occs = append(occs, occurrence{Interval: iv, e: e, stale: stale})
			for _, t := range []time.Time{iv.From, iv.To} {
				if t.After(from) && t.Before(to) {
					cuts = append(cuts, t)
				}
			}
		}
	}
	slices.SortFunc(cuts, func(a, b time.Time) int { return a.Compare(b) })
	cuts = slices.CompactFunc(cuts, func(a, b time.Time) bool { return a.Equal(b) })

	var out []Segment
	for i := 0; i+1 < len(cuts); i++ {
		seg := segmentAt(occs, cuts[i], cuts[i+1], need)
		if n := len(out); n > 0 && sameSegment(out[n-1], seg) {
			out[n-1].To = seg.To
			continue
		}
		out = append(out, seg)
	}
	return out
}

func segmentAt(occs []occurrence, a, b time.Time, need Need) Segment {
	seg := Segment{From: a, To: b, Value: Unknown, Explanation: ExplainNoEntry}
	var unavailable, satisfied, limited []occurrence
	var staleSources []occurrence
	for _, o := range occs {
		if !o.contains(a, b) {
			continue
		}
		if o.stale {
			staleSources = append(staleSources, o)
			continue
		}
		switch {
		case o.e.Kind == KindUnavailable:
			unavailable = append(unavailable, o)
		case satisfies(o.e, need):
			satisfied = append(satisfied, o)
		default:
			limited = append(limited, o)
		}
	}
	var used []occurrence
	switch {
	case len(unavailable) > 0:
		seg.Value, seg.Explanation, used = Unavailable, ExplainUnavailable, unavailable
	case len(satisfied) > 0:
		seg.Value, seg.Explanation, used = Available, ExplainLocation, satisfied
	case len(limited) > 0:
		seg.Value, used = Limited, limited
		seg.LimitedBy, seg.Explanation = limitedBy(limited[0].e, need)
	case len(staleSources) > 0:
		seg.Explanation = ExplainSourceStale
	}
	seg.Sources = sourcesOf(used, staleSources)
	return seg
}

// satisfies reports whether the work location entry meets the need.
func satisfies(e Entry, need Need) bool {
	if e.LocationType != nil && *e.LocationType == LocationTravelling {
		return false
	}
	if !need.OnSite {
		return true
	}
	if e.LocationType == nil || *e.LocationType != LocationLocation {
		return false
	}
	return need.LocationID == "" || (e.LocationID != nil && *e.LocationID == need.LocationID)
}

func limitedBy(e Entry, need Need) (by, code string) {
	switch {
	case e.LocationType != nil && *e.LocationType == LocationTravelling:
		return LocationTravelling, ExplainTravelling
	case e.LocationType != nil && *e.LocationType == LocationRemote:
		return LocationRemote, ExplainRemoteNotOnsit
	}
	return "other_location", ExplainOtherLocation
}

func sourcesOf(used, stale []occurrence) []SourceInfo {
	seen := map[string]int{}
	var out []SourceInfo
	add := func(o occurrence) {
		fresh := FreshnessFresh
		switch {
		case o.e.Source == SourceManual:
			fresh = FreshnessManual
		case o.stale:
			fresh = FreshnessStale
		}
		if i, ok := seen[o.e.Source+"/"+fresh]; ok {
			_ = i
			return
		}
		seen[o.e.Source+"/"+fresh] = len(out)
		out = append(out, SourceInfo{Source: o.e.Source, Freshness: fresh, ObservedAt: o.e.ObservedAt})
	}
	for _, o := range used {
		add(o)
	}
	for _, o := range stale {
		add(o)
	}
	slices.SortFunc(out, func(a, b SourceInfo) int {
		if a.Source != b.Source {
			if a.Source < b.Source {
				return -1
			}
			return 1
		}
		if a.Freshness < b.Freshness {
			return -1
		} else if a.Freshness > b.Freshness {
			return 1
		}
		return 0
	})
	return out
}

func sameSegment(a, b Segment) bool {
	return a.Value == b.Value && a.Explanation == b.Explanation && a.LimitedBy == b.LimitedBy && slices.EqualFunc(a.Sources, b.Sources,
		func(x, y SourceInfo) bool { return x.Source == y.Source && x.Freshness == y.Freshness })
}
