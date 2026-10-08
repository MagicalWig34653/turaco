package application

import (
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
)

// TimeInput says when an entry applies. A timed entry has StartsAt and EndsAt; an all-day entry has StartDate and
// EndDate (inclusive local dates) and the Timezone they are local to.
type TimeInput struct {
	StartsAt  *time.Time
	EndsAt    *time.Time
	StartDate string
	EndDate   string
	Timezone  string
}

// RecurrenceInput is a requested recurrence. The occurrence time of day and the anchor date come from the
// entry's first occurrence in the time zone; Weekday and DayOfMonth default to those of the first occurrence.
type RecurrenceInput struct {
	Frequency  string
	Interval   int
	Weekday    int
	DayOfMonth int
	EndsOn     string
}

func loadZone(name string) (*time.Location, error) {
	loc, err := time.LoadLocation(name)
	if err != nil || name == "" || name == "Local" {
		return nil, invalid("timezone must be an IANA time zone name such as Europe/Berlin")
	}
	return loc, nil
}

// resolveTimes validates the time input and returns the first occurrence's instants.
func resolveTimes(in TimeInput) (starts, ends time.Time, allDay bool, err error) {
	if in.StartDate != "" || in.EndDate != "" {
		loc, err := loadZone(in.Timezone)
		if err != nil {
			return time.Time{}, time.Time{}, false, err
		}
		if in.StartsAt != nil || in.EndsAt != nil {
			return time.Time{}, time.Time{}, false, invalid("give either startsAt and endsAt or startDate and endDate")
		}
		sd, e1 := time.Parse(time.DateOnly, in.StartDate)
		ed, e2 := time.Parse(time.DateOnly, in.EndDate)
		if e1 != nil || e2 != nil {
			return time.Time{}, time.Time{}, false, invalid("startDate and endDate must be dates in the form YYYY-MM-DD")
		}
		if ed.Before(sd) {
			return time.Time{}, time.Time{}, false, invalid("endDate must not be before startDate")
		}
		starts = time.Date(sd.Year(), sd.Month(), sd.Day(), 0, 0, 0, 0, loc).UTC()
		ends = time.Date(ed.Year(), ed.Month(), ed.Day()+1, 0, 0, 0, 0, loc).UTC()
		allDay = true
	} else {
		if in.StartsAt == nil || in.EndsAt == nil {
			return time.Time{}, time.Time{}, false, invalid("startsAt and endsAt (or startDate and endDate) are required")
		}
		starts, ends = in.StartsAt.UTC().Truncate(time.Minute), in.EndsAt.UTC().Truncate(time.Minute)
	}
	if !ends.After(starts) {
		return time.Time{}, time.Time{}, false, invalid("the end must be after the start")
	}
	if ends.Sub(starts) > MaxSpan {
		return time.Time{}, time.Time{}, false, invalid("an entry spans at most 31 days")
	}
	return starts, ends, allDay, nil
}

// buildRecurrence turns the request into the stored recurrence (nil for none) and the instant the entry ends.
func buildRecurrence(in *RecurrenceInput, timezone string, starts, ends time.Time, allDay bool) (*Recurrence, time.Time, error) {
	if in == nil {
		return nil, ends, nil
	}
	loc, err := loadZone(timezone)
	if err != nil {
		return nil, time.Time{}, &InvalidRecurrenceError{Message: "timezone must be an IANA time zone name such as Europe/Berlin"}
	}
	local := starts.In(loc)
	r := Recurrence{Frequency: in.Frequency, Interval: in.Interval, Weekday: in.Weekday, DayOfMonth: in.DayOfMonth,
		TimeOfDay: local.Format("15:04"), Timezone: timezone, StartsOn: local.Format(time.DateOnly), EndsOn: in.EndsOn}
	if allDay {
		r.TimeOfDay = "00:00"
	}
	if r.Interval == 0 {
		r.Interval = 1
	}
	switch r.Frequency {
	case scheduling.FreqWeekly:
		if r.Weekday == 0 {
			r.Weekday = (int(local.Weekday())+6)%7 + 1
		}
	case scheduling.FreqMonthly:
		if r.DayOfMonth == 0 {
			r.DayOfMonth = local.Day()
		}
	}
	if r.EndsOn == "" {
		return nil, time.Time{}, &InvalidRecurrenceError{Message: "a recurring entry needs an end date"}
	}
	rule := r.Rule()
	if err := rule.Validate(); err != nil {
		var inv *scheduling.InvalidError
		if errors.As(err, &inv) {
			return nil, time.Time{}, &InvalidRecurrenceError{Message: inv.Message}
		}
		return nil, time.Time{}, err
	}
	start, _ := time.Parse(time.DateOnly, r.StartsOn)
	end, _ := time.Parse(time.DateOnly, r.EndsOn)
	if end.Sub(start) > MaxRecurrenceDays*24*time.Hour {
		return nil, time.Time{}, &InvalidRecurrenceError{Message: "a recurrence lasts at most 366 days"}
	}
	tmp := Entry{StartsAt: starts, EndsAt: ends, AllDay: allDay, Recurrence: &r}
	horizon := time.Date(end.Year(), end.Month(), end.Day()+1, 0, 0, 0, 0, loc).UTC().Add(MaxSpan)
	occ := tmp.Occurrences(starts, horizon)
	if len(occ) == 0 {
		return nil, time.Time{}, &InvalidRecurrenceError{Message: "the recurrence has no occurrence before its end date"}
	}
	endedAt := occ[len(occ)-1].To
	return &r, endedAt, nil
}

// Occurrences returns the occurrences of the entry that overlap [from, to), ascending.
func (e Entry) Occurrences(from, to time.Time) []Interval {
	if e.Recurrence == nil {
		if e.StartsAt.Before(to) && e.EndsAt.After(from) {
			return []Interval{{From: e.StartsAt, To: e.EndsAt}}
		}
		return nil
	}
	rule := e.Recurrence.Rule()
	loc, err := time.LoadLocation(rule.Timezone)
	if err != nil {
		return nil
	}
	dur := e.EndsAt.Sub(e.StartsAt)
	spanDays := 0
	if e.AllDay {
		s, en := e.StartsAt.In(loc), e.EndsAt.In(loc)
		spanDays = int(time.Date(en.Year(), en.Month(), en.Day(), 0, 0, 0, 0, time.UTC).Sub(
			time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	}
	starts, err := rule.Between(from.Add(-MaxSpan-24*time.Hour), to)
	if err != nil {
		return nil
	}
	var out []Interval
	for _, s := range starts {
		end := s.Add(dur)
		if e.AllDay {
			l := s.In(loc)
			end = time.Date(l.Year(), l.Month(), l.Day()+spanDays, 0, 0, 0, 0, loc).UTC()
		}
		if s.Before(to) && end.After(from) {
			out = append(out, Interval{From: s, To: end})
		}
	}
	return out
}
