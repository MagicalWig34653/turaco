package scheduling

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Interval is a half-open span of local minutes since midnight. Overnight
// spans must be split into intervals on adjacent weekdays.
type Interval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// BusinessCalendar is pure input to working-time calculations. Weekdays use
// ISO numbering; closures are full local dates in YYYY-MM-DD form.
type BusinessCalendar struct {
	Timezone    string              `json:"timezone"`
	WeeklyHours map[int][]Interval  `json:"weeklyHours"`
	Closures    map[string]struct{} `json:"-"`
}

var ErrBusinessTime = errors.New("scheduling: invalid business calendar or duration")

func (c BusinessCalendar) Validate() error {
	if c.Timezone == "" || c.Timezone == "Local" {
		return fmt.Errorf("%w: timezone", ErrBusinessTime)
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("%w: timezone: %v", ErrBusinessTime, err)
	}
	count := 0
	for day, intervals := range c.WeeklyHours {
		if day < 1 || day > 7 {
			return fmt.Errorf("%w: weekday", ErrBusinessTime)
		}
		prev := 0
		for i, v := range intervals {
			if v.Start < 0 || v.End > 1440 || v.Start >= v.End || (i > 0 && v.Start < prev) {
				return fmt.Errorf("%w: weekly interval", ErrBusinessTime)
			}
			prev = v.End
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("%w: no working hours", ErrBusinessTime)
	}
	for date := range c.Closures {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return fmt.Errorf("%w: closure date", ErrBusinessTime)
		}
	}
	return nil
}

func (c BusinessCalendar) working(t time.Time, loc *time.Location) bool {
	local := t.In(loc)
	if _, closed := c.Closures[local.Format(time.DateOnly)]; closed {
		return false
	}
	day := int(local.Weekday())
	if day == 0 {
		day = 7
	}
	minute := local.Hour()*60 + local.Minute()
	intervals := c.WeeklyHours[day]
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].End > minute })
	return i < len(intervals) && intervals[i].Start <= minute
}

func businessStep(t time.Time) time.Time { return t.Truncate(time.Minute).Add(time.Minute) }

// AddBusinessDuration adds real elapsed working time. Iteration over UTC
// minute boundaries counts repeated DST hours and skips nonexistent hours.
// The search is bounded to 400 local days.
func AddBusinessDuration(c BusinessCalendar, from time.Time, d time.Duration) (time.Time, error) {
	if err := c.Validate(); err != nil {
		return time.Time{}, err
	}
	if d < 0 {
		return time.Time{}, ErrBusinessTime
	}
	if d == 0 {
		return from.UTC(), nil
	}
	loc, _ := time.LoadLocation(c.Timezone)
	start := from.In(loc)
	limit := time.Date(start.Year(), start.Month(), start.Day()+400, 0, 0, 0, 0, loc)
	for t := from.UTC(); t.Before(limit); {
		next := businessStep(t)
		if next.After(limit) {
			next = limit
		}
		if c.working(t, loc) {
			span := next.Sub(t)
			if d <= span {
				return t.Add(d).UTC(), nil
			}
			d -= span
		}
		t = next
	}
	return time.Time{}, fmt.Errorf("%w: duration exceeds 400 days", ErrBusinessTime)
}

// BusinessDurationBetween measures real working time in [a,b). A reversed
// range produces a negative duration; ranges longer than 400 days fail.
func BusinessDurationBetween(c BusinessCalendar, a, b time.Time) (time.Duration, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	if b.Before(a) {
		d, err := BusinessDurationBetween(c, b, a)
		return -d, err
	}
	if !a.Before(b) {
		return 0, nil
	}
	loc, _ := time.LoadLocation(c.Timezone)
	local := a.In(loc)
	limit := time.Date(local.Year(), local.Month(), local.Day()+400, 0, 0, 0, 0, loc)
	if b.After(limit) {
		return 0, fmt.Errorf("%w: range exceeds 400 days", ErrBusinessTime)
	}
	var total time.Duration
	for t := a.UTC(); t.Before(b); {
		next := businessStep(t)
		if next.After(b) {
			next = b
		}
		if c.working(t, loc) {
			total += next.Sub(t)
		}
		t = next
	}
	return total, nil
}
