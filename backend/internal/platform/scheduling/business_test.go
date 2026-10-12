package scheduling

import (
	"errors"
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBusinessTime(t *testing.T) {
	cases := []struct {
		name     string
		cal      BusinessCalendar
		from     string
		duration time.Duration
		want     string
	}{
		{"split shift", BusinessCalendar{"Europe/Berlin", map[int][]Interval{1: {{540, 720}, {780, 1020}}}, nil}, "2026-10-12T10:30:00+02:00", 3 * time.Hour, "2026-10-12T14:30:00+02:00"},
		{"spring gap", BusinessCalendar{"Europe/Berlin", map[int][]Interval{7: {{60, 240}}}, nil}, "2026-03-29T01:30:00+01:00", 90 * time.Minute, "2026-03-29T04:00:00+02:00"},
		{"fall repeat", BusinessCalendar{"Europe/Berlin", map[int][]Interval{7: {{60, 240}}}, nil}, "2026-10-25T01:30:00+02:00", 150 * time.Minute, "2026-10-25T03:00:00+01:00"},
		{"holiday", BusinessCalendar{"Europe/Berlin", map[int][]Interval{1: {{540, 1020}}, 2: {{540, 1020}}}, map[string]struct{}{"2026-10-12": {}}}, "2026-10-12T09:00:00+02:00", time.Hour, "2026-10-13T10:00:00+02:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AddBusinessDuration(tc.cal, instant(tc.from), tc.duration)
			if err != nil || !got.Equal(instant(tc.want)) {
				t.Fatalf("add: %v %v", got, err)
			}
			d, err := BusinessDurationBetween(tc.cal, instant(tc.from), got)
			if err != nil || d != tc.duration {
				t.Fatalf("between: %v %v", d, err)
			}
		})
	}
}

func TestBusinessCalendarValidation(t *testing.T) {
	for _, v := range []Interval{{1320, 60}, {60, 60}, {-1, 20}, {20, 1441}} {
		c := BusinessCalendar{"UTC", map[int][]Interval{1: {v}}, nil}
		if !errors.Is(c.Validate(), ErrBusinessTime) {
			t.Fatalf("accepted %#v", v)
		}
	}
	c := BusinessCalendar{"UTC", map[int][]Interval{1: {{0, 1440}}}, nil}
	if _, err := AddBusinessDuration(c, instant("2026-01-01T00:00:00Z"), -time.Minute); !errors.Is(err, ErrBusinessTime) {
		t.Fatal(err)
	}
}

func TestBusinessTimeBoundaries(t *testing.T) {
	c := BusinessCalendar{Timezone: "UTC", WeeklyHours: map[int][]Interval{1: {{540, 600}, {660, 720}}}, Closures: map[string]struct{}{}}
	from := instant("2026-10-12T09:15:30Z")
	got, err := AddBusinessDuration(c, from, 90*time.Minute)
	if err != nil || !got.Equal(instant("2026-10-12T11:45:30Z")) {
		t.Fatalf("add across break: %v %v", got, err)
	}
	d, err := BusinessDurationBetween(c, got, from)
	if err != nil || d != -90*time.Minute {
		t.Fatalf("reverse: %v %v", d, err)
	}
	c.Closures["2026-10-12"] = struct{}{}
	if _, err := AddBusinessDuration(c, from, 120*time.Hour); !errors.Is(err, ErrBusinessTime) {
		t.Fatalf("bound: %v", err)
	}
}

func TestBusinessTimeRejectsInvalidHours(t *testing.T) {
	cases := []BusinessCalendar{
		{Timezone: "Local", WeeklyHours: map[int][]Interval{1: {{0, 1}}}},
		{Timezone: "UTC", WeeklyHours: map[int][]Interval{8: {{0, 1}}}},
		{Timezone: "UTC", WeeklyHours: map[int][]Interval{1: {{60, 120}, {100, 180}}}},
		{Timezone: "UTC", WeeklyHours: map[int][]Interval{1: {{0, 1}}}, Closures: map[string]struct{}{"2026-02-30": {}}},
	}
	for _, c := range cases {
		if !errors.Is(c.Validate(), ErrBusinessTime) {
			t.Fatalf("accepted %#v", c)
		}
	}
}
