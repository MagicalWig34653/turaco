package application

import (
	"errors"
	"fmt"
	"strconv"
	"time"
	// The tz database is embedded so IANA time zones resolve in minimal
	// container images and on every platform.
	_ "time/tzdata"
)

// Recurrence frequencies.
const (
	FreqDaily   = "daily"
	FreqWeekly  = "weekly"
	FreqMonthly = "monthly"
)

// Rule says when a Recurring Task Definition runs. Occurrences are anchored
// at StartsOn: every Interval days, weeks or months, at TimeOfDay in
// Timezone's local time. Weekly rules run on Weekday (1 = Monday … 7 =
// Sunday) of every Interval-th week; monthly rules on DayOfMonth of every
// Interval-th month, clamped to the last day of shorter months. Local times
// that do not exist (daylight saving gap) run at the next valid instant.
type Rule struct {
	Frequency  string
	Interval   int
	Weekday    int // weekly only
	DayOfMonth int // monthly only
	TimeOfDay  string
	Timezone   string
	StartsOn   string // local date, YYYY-MM-DD
}

const maxOccurrenceIndex = 1 << 20

// Validate reports whether the rule is well formed.
func (r Rule) Validate() error {
	switch r.Frequency {
	case FreqDaily:
		if r.Weekday != 0 || r.DayOfMonth != 0 {
			return invalid("a daily rule takes neither a weekday nor a day of the month")
		}
	case FreqWeekly:
		if r.Weekday < 1 || r.Weekday > 7 {
			return invalid("weekday must be 1 (Monday) to 7 (Sunday)")
		}
		if r.DayOfMonth != 0 {
			return invalid("a weekly rule takes no day of the month")
		}
	case FreqMonthly:
		if r.DayOfMonth < 1 || r.DayOfMonth > 31 {
			return invalid("day of the month must be 1 to 31")
		}
		if r.Weekday != 0 {
			return invalid("a monthly rule takes no weekday")
		}
	default:
		return invalid("frequency must be daily, weekly or monthly")
	}
	if r.Interval < 1 || r.Interval > 365 {
		return invalid("interval must be 1 to 365")
	}
	if _, _, err := parseTimeOfDay(r.TimeOfDay); err != nil {
		return err
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil || r.Timezone == "" || r.Timezone == "Local" {
		return invalid("timezone must be an IANA time zone name such as Europe/Berlin")
	}
	if _, err := time.Parse(time.DateOnly, r.StartsOn); err != nil {
		return invalid("start date must be a date in the form YYYY-MM-DD")
	}
	return nil
}

func parseTimeOfDay(s string) (hour, minute int, err error) {
	t, perr := time.Parse("15:04", s)
	if perr != nil || len(s) != 5 {
		return 0, 0, invalid("time of day must be HH:MM")
	}
	return t.Hour(), t.Minute(), nil
}

// occurrence returns the k-th (k >= 0) scheduled instant. It is strictly
// increasing in k.
func (r Rule) occurrence(k int, loc *time.Location, start time.Time, hour, minute int) time.Time {
	y, m, d := start.Date()
	switch r.Frequency {
	case FreqDaily:
		return time.Date(y, m, d+k*r.Interval, hour, minute, 0, 0, loc)
	case FreqWeekly:
		// Monday of the start week, then whole weeks, then the weekday.
		offsetToMonday := (int(start.Weekday()) + 6) % 7
		return time.Date(y, m, d-offsetToMonday+7*k*r.Interval+(r.Weekday-1), hour, minute, 0, 0, loc)
	default: // monthly
		month := int(m) - 1 + k*r.Interval
		first := time.Date(y, time.Month(1+month), 1, 0, 0, 0, 0, loc)
		last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, loc).Day()
		day := r.DayOfMonth
		if day > last {
			day = last
		}
		return time.Date(first.Year(), first.Month(), day, hour, minute, 0, 0, loc)
	}
}

// NextAfter returns the first occurrence strictly after t that is not before
// the start date. The result is in UTC.
func (r Rule) NextAfter(t time.Time) (time.Time, error) {
	if err := r.Validate(); err != nil {
		return time.Time{}, err
	}
	loc, _ := time.LoadLocation(r.Timezone)
	hour, minute, _ := parseTimeOfDay(r.TimeOfDay)
	startDate, _ := time.Parse(time.DateOnly, r.StartsOn)
	start := time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, loc)
	// Occurrences before the start date's first instant are not part of the schedule.
	if floor := start.Add(-time.Nanosecond); t.Before(floor) {
		t = floor
	}
	hi := maxOccurrenceIndex
	if !r.occurrence(hi, loc, start, hour, minute).After(t) {
		return time.Time{}, errors.New("tasks: no next occurrence within the supported range")
	}
	lo := 0
	for lo < hi { // smallest k with occurrence(k) > t
		mid := lo + (hi-lo)/2
		if r.occurrence(mid, loc, start, hour, minute).After(t) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return r.occurrence(lo, loc, start, hour, minute).UTC(), nil
}

// String describes the rule for logs and audit metadata (no user text).
func (r Rule) String() string {
	return fmt.Sprintf("%s/%s@%s %s from %s", r.Frequency, strconv.Itoa(r.Interval), r.TimeOfDay, r.Timezone, r.StartsOn)
}
