package scheduling

import (
	"errors"
	"testing"
	"time"
)

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func rule(freq string, mutate func(*Rule)) Rule {
	r := Rule{Frequency: freq, Interval: 1, TimeOfDay: "09:00", Timezone: "UTC", StartsOn: "2026-01-01"}
	switch freq {
	case FreqWeekly:
		r.Weekday = 1
	case FreqMonthly:
		r.DayOfMonth = 15
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func nextRun(t *testing.T, r Rule, after time.Time) time.Time {
	t.Helper()
	n, err := r.NextAfter(after)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	return n
}

func TestDailyRule(t *testing.T) {
	r := rule(FreqDaily, nil)
	if got := nextRun(t, r, at(2026, 3, 10, 8, 0)); !got.Equal(at(2026, 3, 10, 9, 0)) {
		t.Errorf("before the time of day: %v", got)
	}
	if got := nextRun(t, r, at(2026, 3, 10, 9, 0)); !got.Equal(at(2026, 3, 11, 9, 0)) {
		t.Errorf("strictly after the instant itself: %v", got)
	}
	if got := nextRun(t, r, at(2026, 12, 31, 23, 0)); !got.Equal(at(2027, 1, 1, 9, 0)) {
		t.Errorf("year end: %v", got)
	}
}

func TestIntervalAnchorsAtStartDate(t *testing.T) {
	r := rule(FreqDaily, func(r *Rule) { r.Interval = 3 }) // Jan 1, 4, 7, ...
	if got := nextRun(t, r, at(2026, 1, 5, 0, 0)); !got.Equal(at(2026, 1, 7, 9, 0)) {
		t.Errorf("every 3 days: %v", got)
	}
	if got := nextRun(t, r, at(2026, 1, 7, 9, 0)); !got.Equal(at(2026, 1, 10, 9, 0)) {
		t.Errorf("every 3 days, next: %v", got)
	}
}

func TestNothingBeforeTheStartDate(t *testing.T) {
	r := rule(FreqDaily, func(r *Rule) { r.StartsOn = "2026-06-10" })
	if got := nextRun(t, r, at(2020, 1, 1, 0, 0)); !got.Equal(at(2026, 6, 10, 9, 0)) {
		t.Errorf("first run = %v, want the start date", got)
	}
	// Starting later in the day than the time of day still runs the next day.
	if got := nextRun(t, r, at(2026, 6, 10, 9, 0)); !got.Equal(at(2026, 6, 11, 9, 0)) {
		t.Errorf("after the first run: %v", got)
	}
	w := rule(FreqWeekly, func(r *Rule) { r.StartsOn = "2026-01-07"; r.Weekday = 1 }) // Wed start, Monday rule
	if got := nextRun(t, w, at(2020, 1, 1, 0, 0)); !got.Equal(at(2026, 1, 12, 9, 0)) {
		t.Errorf("a weekday before the start weekday must not run before the start date: %v", got)
	}
}

func TestWeeklyRule(t *testing.T) {
	r := rule(FreqWeekly, func(r *Rule) { r.Weekday = 5 }) // Fridays; 2026-01-02 is a Friday
	if got := nextRun(t, r, at(2026, 1, 2, 9, 0)); !got.Equal(at(2026, 1, 9, 9, 0)) {
		t.Errorf("weekly: %v", got)
	}
	if got := nextRun(t, r, at(2026, 1, 3, 0, 0)); got.Weekday() != time.Friday {
		t.Errorf("weekday = %v", got.Weekday())
	}
	biweekly := rule(FreqWeekly, func(r *Rule) { r.Weekday = 5; r.Interval = 2 })
	if got := nextRun(t, biweekly, at(2026, 1, 2, 9, 0)); !got.Equal(at(2026, 1, 16, 9, 0)) {
		t.Errorf("biweekly: %v", got)
	}
	sunday := rule(FreqWeekly, func(r *Rule) { r.Weekday = 7 })
	if got := nextRun(t, sunday, at(2026, 1, 5, 0, 0)); !got.Equal(at(2026, 1, 11, 9, 0)) || got.Weekday() != time.Sunday {
		t.Errorf("sunday: %v", got)
	}
}

func TestMonthlyRuleClampsToTheLastDay(t *testing.T) {
	r := rule(FreqMonthly, func(r *Rule) { r.DayOfMonth = 31 })
	want := []time.Time{at(2026, 1, 31, 9, 0), at(2026, 2, 28, 9, 0), at(2026, 3, 31, 9, 0), at(2026, 4, 30, 9, 0)}
	after := at(2026, 1, 1, 0, 0)
	for i, w := range want {
		got := nextRun(t, r, after)
		if !got.Equal(w) {
			t.Fatalf("occurrence %d = %v, want %v", i, got, w)
		}
		after = got
	}
	leap := rule(FreqMonthly, func(r *Rule) { r.DayOfMonth = 30; r.StartsOn = "2028-01-01" })
	if got := nextRun(t, leap, at(2028, 1, 31, 0, 0)); !got.Equal(at(2028, 2, 29, 9, 0)) {
		t.Errorf("leap february: %v", got)
	}
	quarterly := rule(FreqMonthly, func(r *Rule) { r.Interval = 3; r.DayOfMonth = 1 })
	if got := nextRun(t, quarterly, at(2026, 1, 1, 9, 0)); !got.Equal(at(2026, 4, 1, 9, 0)) {
		t.Errorf("quarterly: %v", got)
	}
	if got := nextRun(t, quarterly, at(2026, 10, 1, 9, 0)); !got.Equal(at(2027, 1, 1, 9, 0)) {
		t.Errorf("quarterly across the year end: %v", got)
	}
}

func TestLocalTimeAndDaylightSaving(t *testing.T) {
	berlin := rule(FreqDaily, func(r *Rule) { r.Timezone = "Europe/Berlin"; r.TimeOfDay = "09:00"; r.StartsOn = "2026-03-01" })
	// Winter: UTC+1; the Saturday after the change on 2026-03-29 it is UTC+2.
	if got := nextRun(t, berlin, at(2026, 3, 27, 12, 0)); !got.Equal(at(2026, 3, 28, 8, 0)) {
		t.Errorf("before DST: %v", got)
	}
	if got := nextRun(t, berlin, at(2026, 3, 28, 12, 0)); !got.Equal(at(2026, 3, 29, 7, 0)) {
		t.Errorf("the local time stays 09:00 across the change: %v", got)
	}
	// 02:30 does not exist on 2026-03-29 in Berlin; it runs at the next valid instant that day.
	gap := rule(FreqDaily, func(r *Rule) { r.Timezone = "Europe/Berlin"; r.TimeOfDay = "02:30"; r.StartsOn = "2026-03-01" })
	got := nextRun(t, gap, at(2026, 3, 28, 12, 0))
	if got.Before(at(2026, 3, 29, 0, 30)) || got.After(at(2026, 3, 29, 2, 30)) {
		t.Errorf("gap day = %v, want an instant on 2026-03-29", got)
	}
	if again := nextRun(t, gap, got); !again.After(got) {
		t.Errorf("occurrences must keep increasing: %v then %v", got, again)
	}
	// Auckland is ahead of UTC: 09:00 local on 2026-01-02 is 20:00 UTC on 2026-01-01.
	auckland := rule(FreqDaily, func(r *Rule) { r.Timezone = "Pacific/Auckland" })
	if got := nextRun(t, auckland, at(2025, 12, 1, 0, 0)); !got.Equal(at(2025, 12, 31, 20, 0)) {
		t.Errorf("auckland = %v", got)
	}
}

func TestOccurrencesAreMonotonic(t *testing.T) {
	for _, r := range []Rule{
		rule(FreqDaily, func(r *Rule) { r.Timezone = "Europe/Berlin" }),
		rule(FreqWeekly, func(r *Rule) { r.Timezone = "America/New_York"; r.Weekday = 7; r.TimeOfDay = "02:30" }),
		rule(FreqMonthly, func(r *Rule) { r.Timezone = "Europe/Berlin"; r.DayOfMonth = 31; r.TimeOfDay = "02:30" }),
	} {
		at := at(2026, 1, 1, 0, 0)
		for i := 0; i < 800; i++ {
			n := nextRun(t, r, at)
			if !n.After(at) {
				t.Fatalf("%s: %v is not after %v", r, n, at)
			}
			at = n
		}
	}
}

func TestValidate(t *testing.T) {
	bad := map[string]Rule{
		"unknown frequency":      {Frequency: "hourly", Interval: 1, TimeOfDay: "09:00", Timezone: "UTC", StartsOn: "2026-01-01"},
		"zero interval":          rule(FreqDaily, func(r *Rule) { r.Interval = 0 }),
		"huge interval":          rule(FreqDaily, func(r *Rule) { r.Interval = 366 }),
		"weekly without weekday": rule(FreqWeekly, func(r *Rule) { r.Weekday = 0 }),
		"weekday 8":              rule(FreqWeekly, func(r *Rule) { r.Weekday = 8 }),
		"weekly with day":        rule(FreqWeekly, func(r *Rule) { r.DayOfMonth = 3 }),
		"monthly without day":    rule(FreqMonthly, func(r *Rule) { r.DayOfMonth = 0 }),
		"day 32":                 rule(FreqMonthly, func(r *Rule) { r.DayOfMonth = 32 }),
		"monthly with weekday":   rule(FreqMonthly, func(r *Rule) { r.Weekday = 1 }),
		"daily with weekday":     rule(FreqDaily, func(r *Rule) { r.Weekday = 1 }),
		"time 24:00":             rule(FreqDaily, func(r *Rule) { r.TimeOfDay = "24:00" }),
		"time 9:00":              rule(FreqDaily, func(r *Rule) { r.TimeOfDay = "9:00" }),
		"time with seconds":      rule(FreqDaily, func(r *Rule) { r.TimeOfDay = "09:00:00" }),
		"unknown zone":           rule(FreqDaily, func(r *Rule) { r.Timezone = "Mars/Base" }),
		"empty zone":             rule(FreqDaily, func(r *Rule) { r.Timezone = "" }),
		"Local zone":             rule(FreqDaily, func(r *Rule) { r.Timezone = "Local" }),
		"bad start date":         rule(FreqDaily, func(r *Rule) { r.StartsOn = "01.01.2026" }),
		"impossible start date":  rule(FreqDaily, func(r *Rule) { r.StartsOn = "2026-02-30" }),
		"zone with path tricks":  rule(FreqDaily, func(r *Rule) { r.Timezone = "../etc/passwd" }),
	}
	for name, r := range bad {
		var inv *InvalidError
		if err := r.Validate(); !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want InvalidError", name, err)
		}
		if _, err := r.NextAfter(at(2026, 1, 1, 0, 0)); err == nil {
			t.Errorf("%s: NextAfter must refuse an invalid rule", name)
		}
	}
	for _, r := range []Rule{rule(FreqDaily, nil), rule(FreqWeekly, nil), rule(FreqMonthly, nil)} {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
}

func TestErrInvalidSentinel(t *testing.T) {
	err := Rule{}.Validate()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("errors.Is(%v, ErrInvalid) = false", err)
	}
}

func TestEndsOn(t *testing.T) {
	r := rule(FreqDaily, func(r *Rule) { r.EndsOn = "2026-01-03" })
	if got := nextRun(t, r, at(2026, 1, 2, 10, 0)); !got.Equal(at(2026, 1, 3, 9, 0)) {
		t.Errorf("last occurrence: %v", got)
	}
	if _, err := r.NextAfter(at(2026, 1, 3, 10, 0)); err == nil {
		t.Error("no occurrence after EndsOn")
	}
	if err := rule(FreqDaily, func(r *Rule) { r.EndsOn = "2025-12-31" }).Validate(); err == nil {
		t.Error("EndsOn before StartsOn must be invalid")
	}
	if err := rule(FreqDaily, func(r *Rule) { r.EndsOn = "x" }).Validate(); err == nil {
		t.Error("bad EndsOn must be invalid")
	}
}

func TestBetween(t *testing.T) {
	r := rule(FreqDaily, nil)
	got, err := r.Between(at(2026, 1, 1, 9, 0), at(2026, 1, 4, 9, 0))
	if err != nil || len(got) != 3 || !got[0].Equal(at(2026, 1, 1, 9, 0)) || !got[2].Equal(at(2026, 1, 3, 9, 0)) {
		t.Fatalf("Between = %v, %v (from inclusive, to exclusive)", got, err)
	}
	ended := rule(FreqDaily, func(r *Rule) { r.EndsOn = "2026-01-02" })
	if got, _ := ended.Between(at(2026, 1, 1, 0, 0), at(2026, 2, 1, 0, 0)); len(got) != 2 {
		t.Errorf("EndsOn respected: %v", got)
	}
	if got, _ := r.Between(at(2026, 1, 1, 0, 0), at(2030, 1, 1, 0, 0)); len(got) != MaxBetween {
		t.Errorf("bounded count: %d", len(got))
	}
	// DST: local 09:00 stays across the change.
	b := rule(FreqDaily, func(r *Rule) { r.Timezone = "Europe/Berlin"; r.StartsOn = "2026-03-01" })
	got, _ = b.Between(at(2026, 3, 28, 0, 0), at(2026, 3, 31, 0, 0))
	if len(got) != 3 || !got[1].Equal(at(2026, 3, 29, 7, 0)) {
		t.Errorf("DST: %v", got)
	}
	if got, _ := r.Between(at(2026, 1, 2, 0, 0), at(2026, 1, 1, 0, 0)); len(got) != 0 {
		t.Error("empty window")
	}
}
