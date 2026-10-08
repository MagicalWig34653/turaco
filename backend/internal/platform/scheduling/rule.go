// Package scheduling holds the platform recurrence rule shared by Tasks
// (Recurring Task Definitions) and Presence (recurring entries). It is pure:
// no database, no clock, no module imports.
package scheduling

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

// ErrInvalid matches every validation error returned by this package.
var ErrInvalid = errors.New("scheduling: invalid rule")

// InvalidError carries a user-safe validation message.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "scheduling: invalid rule: " + e.Message }

// Is makes errors.Is(err, ErrInvalid) true for every InvalidError.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(msg string) error { return &InvalidError{Message: msg} }

// Rule says when a recurring schedule runs. Occurrences are anchored
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
	// EndsOn optionally ends the schedule: no occurrence on a later local
	// date (YYYY-MM-DD). Empty means open ended.
	EndsOn string
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
	start, err := time.Parse(time.DateOnly, r.StartsOn)
	if err != nil {
		return invalid("start date must be a date in the form YYYY-MM-DD")
	}
	if r.EndsOn != "" {
		end, err := time.Parse(time.DateOnly, r.EndsOn)
		if err != nil {
			return invalid("end date must be a date in the form YYYY-MM-DD")
		}
		if end.Before(start) {
			return invalid("end date must not be before the start date")
		}
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
		return time.Time{}, errors.New("scheduling: no next occurrence within the supported range")
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
	next := r.occurrence(lo, loc, start, hour, minute)
	if r.pastEnd(next, loc) {
		return time.Time{}, errors.New("scheduling: no next occurrence within the supported range")
	}
	return next.UTC(), nil
}

// pastEnd reports whether the local date of occ is after EndsOn.
func (r Rule) pastEnd(occ time.Time, loc *time.Location) bool {
	if r.EndsOn == "" {
		return false
	}
	end, err := time.Parse(time.DateOnly, r.EndsOn)
	if err != nil {
		return true
	}
	y, m, d := occ.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).After(end)
}

// MaxBetween bounds the number of occurrences Between returns.
const MaxBetween = 1000

// Between returns the occurrences t with from <= t < to in UTC, ascending, at
// most MaxBetween of them. It honours StartsOn and EndsOn. The window may be
// any size; the count bound keeps the cost fixed.
func (r Rule) Between(from, to time.Time) ([]time.Time, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var out []time.Time
	if !from.Before(to) {
		return out, nil
	}
	// NextAfter is strictly after its argument; step back one nanosecond so
	// an occurrence exactly at from is included.
	cur := from.Add(-time.Nanosecond)
	for len(out) < MaxBetween {
		n, err := r.NextAfter(cur)
		if err != nil || !n.Before(to) {
			break
		}
		out = append(out, n)
		cur = n
	}
	return out, nil
}

// String describes the rule for logs and audit metadata (no user text).
func (r Rule) String() string {
	return fmt.Sprintf("%s/%s@%s %s from %s", r.Frequency, strconv.Itoa(r.Interval), r.TimeOfDay, r.Timezone, r.StartsOn)
}
