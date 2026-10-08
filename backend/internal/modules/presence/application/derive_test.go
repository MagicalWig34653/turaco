package application

import (
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func sp(s string) *string { return &s }

func manual(kind string, locType *string, from, to time.Time) Entry {
	return Entry{Kind: kind, LocationType: locType, StartsAt: from, EndsAt: to, Source: SourceManual, Status: StatusActive, EndedAt: to}
}

var now = utc(2026, 10, 7, 8)

func TestUnknownIsNeverAvailable(t *testing.T) {
	segs := derive(nil, utc(2026, 10, 7, 8), utc(2026, 10, 7, 18), Need{}, now, DefaultStale)
	if len(segs) != 1 || segs[0].Value != Unknown || segs[0].Explanation != ExplainNoEntry {
		t.Fatalf("no entry must be unknown: %+v", segs)
	}
}

func TestPrecedenceUnavailableWins(t *testing.T) {
	office := manual(KindWorkLocation, sp(LocationLocation), utc(2026, 10, 7, 0), utc(2026, 10, 8, 0))
	away := manual(KindUnavailable, nil, utc(2026, 10, 7, 12), utc(2026, 10, 7, 14))
	segs := derive([]Entry{office, away}, utc(2026, 10, 7, 8), utc(2026, 10, 7, 18), Need{OnSite: true}, now, DefaultStale)
	want := []string{Available, Unavailable, Available}
	if len(segs) != 3 {
		t.Fatalf("segments: %+v", segs)
	}
	for i, w := range want {
		if segs[i].Value != w {
			t.Errorf("segment %d = %s, want %s", i, segs[i].Value, w)
		}
	}
}

func TestRemoteIsLimitedWhenOnSiteNeeded(t *testing.T) {
	remote := manual(KindWorkLocation, sp(LocationRemote), utc(2026, 10, 7, 0), utc(2026, 10, 8, 0))
	if s := derive([]Entry{remote}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{OnSite: true}, now, DefaultStale)[0]; s.Value != Limited || s.LimitedBy != LocationRemote {
		t.Errorf("onsite: %+v", s)
	}
	if s := derive([]Entry{remote}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{}, now, DefaultStale)[0]; s.Value != Available {
		t.Errorf("any: %+v", s)
	}
	trav := manual(KindWorkLocation, sp(LocationTravelling), utc(2026, 10, 7, 0), utc(2026, 10, 8, 0))
	if s := derive([]Entry{trav}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{}, now, DefaultStale)[0]; s.Value != Limited || s.Explanation != ExplainTravelling {
		t.Errorf("travelling: %+v", s)
	}
}

func TestStaleExternalSignalIsUnknown(t *testing.T) {
	obs := now.Add(-48 * time.Hour)
	ext := Entry{Kind: KindUnavailable, StartsAt: utc(2026, 10, 7, 0), EndsAt: utc(2026, 10, 8, 0), Source: "m365", Status: StatusActive, ObservedAt: &obs}
	s := derive([]Entry{ext}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{}, now, DefaultStale)[0]
	if s.Value != Unknown || s.Explanation != ExplainSourceStale || s.Sources[0].Freshness != FreshnessStale {
		t.Errorf("stale: %+v", s)
	}
	fresh := now.Add(-time.Hour)
	ext.ObservedAt = &fresh
	s = derive([]Entry{ext}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{}, now, DefaultStale)[0]
	if s.Value != Unavailable || s.Sources[0].Source != "m365" || s.Sources[0].Freshness != FreshnessFresh {
		t.Errorf("fresh: %+v", s)
	}
}

func TestCancelledAndClosedEntriesDoNotCount(t *testing.T) {
	e := manual(KindUnavailable, nil, utc(2026, 10, 7, 0), utc(2026, 10, 8, 0))
	e.Status = StatusCancelled
	if s := derive([]Entry{e}, utc(2026, 10, 7, 9), utc(2026, 10, 7, 10), Need{}, now, DefaultStale)[0]; s.Value != Unknown {
		t.Errorf("cancelled: %+v", s)
	}
}

func TestRecurrenceExpansionAcrossDST(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	// Daily 09:00-17:00 Berlin from Fri 2026-03-27 to Tue 2026-03-31; the clocks change on Sun 03-29.
	starts := time.Date(2026, 3, 27, 9, 0, 0, 0, berlin).UTC()
	ends := time.Date(2026, 3, 27, 17, 0, 0, 0, berlin).UTC()
	rec, endedAt, err := buildRecurrence(&RecurrenceInput{Frequency: "daily", EndsOn: "2026-03-31"}, "Europe/Berlin", starts, ends, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 3, 31, 17, 0, 0, 0, berlin).UTC(); !endedAt.Equal(want) {
		t.Errorf("endedAt = %v, want %v", endedAt, want)
	}
	e := Entry{Kind: KindUnavailable, StartsAt: starts, EndsAt: ends, Recurrence: rec, Source: SourceManual, Status: StatusActive, EndedAt: endedAt}
	occ := e.Occurrences(utc(2026, 3, 26, 0), utc(2026, 4, 2, 0))
	if len(occ) != 5 {
		t.Fatalf("occurrences = %v", occ)
	}
	if !occ[1].From.Equal(time.Date(2026, 3, 28, 9, 0, 0, 0, berlin)) || !occ[2].From.Equal(time.Date(2026, 3, 29, 9, 0, 0, 0, berlin)) {
		t.Errorf("local 09:00 must hold across the change: %v", occ)
	}
	if occ[2].From.Hour() != 7 || occ[1].From.Hour() != 8 {
		t.Errorf("UTC hours must shift by one: %v", occ)
	}
	if got := e.Occurrences(utc(2026, 4, 1, 0), utc(2026, 4, 5, 0)); len(got) != 0 {
		t.Errorf("nothing after EndsOn: %v", got)
	}
}

func TestRecurrenceValidation(t *testing.T) {
	s, e := utc(2026, 10, 7, 9), utc(2026, 10, 7, 17)
	cases := map[string]*RecurrenceInput{
		"no end":         {Frequency: "daily"},
		"too long":       {Frequency: "daily", EndsOn: "2028-01-01"},
		"bad frequency":  {Frequency: "hourly", EndsOn: "2026-11-01"},
		"end before":     {Frequency: "daily", EndsOn: "2026-10-01"},
		"weekday & freq": {Frequency: "daily", Weekday: 2, EndsOn: "2026-11-01"},
	}
	for name, in := range cases {
		_, _, err := buildRecurrence(in, "UTC", s, e, false)
		if _, ok := err.(*InvalidRecurrenceError); !ok {
			t.Errorf("%s: err = %v, want InvalidRecurrenceError", name, err)
		}
	}
	if _, _, err := buildRecurrence(&RecurrenceInput{Frequency: "weekly", EndsOn: "2026-12-01"}, "UTC", s, e, false); err != nil {
		t.Errorf("weekly defaults to the first occurrence's weekday: %v", err)
	}
}

func TestAllDayAcrossTimezone(t *testing.T) {
	s, e, allDay, err := resolveTimes(TimeInput{StartDate: "2026-10-12", EndDate: "2026-10-13", Timezone: "Europe/Berlin"})
	if err != nil || !allDay {
		t.Fatal(err)
	}
	if !s.Equal(utc(2026, 10, 11, 22)) || !e.Equal(utc(2026, 10, 13, 22)) {
		t.Errorf("all-day span = %v..%v", s, e)
	}
	if _, _, _, err := resolveTimes(TimeInput{StartDate: "2026-10-12", EndDate: "2026-12-30", Timezone: "UTC"}); err == nil {
		t.Error("more than 31 days must be refused")
	}
}
