package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Timer limits (design section 7): a timer older than StaleTimerNeedsDuration needs an explicit duration to stop.
const StaleTimerNeedsDuration = 12 * time.Hour

// StartTimer starts the caller's personal timer on a Ticket they may work. At most one timer runs per person;
// a second start is refused (the UI offers stop-and-start). Timers are personal state and are not audited.
func (s *Service) StartTimer(ctx context.Context, p Principal, subjectType, subjectID, note string) (Timer, error) {
	if p.UserID == "" || !p.mayLog() {
		return Timer{}, ErrForbidden
	}
	if subjectType != SubjectTicket {
		return Timer{}, invalid("subjectType must be ticket")
	}
	ticketID, err := checkID(subjectID)
	if err != nil {
		return Timer{}, err
	}
	n, err := cleanNote(note)
	if err != nil {
		return Timer{}, err
	}
	if _, err := s.authorizeTicket(ctx, p, p.UserID, ticketID); err != nil {
		return Timer{}, err
	}
	return s.store.StartTimer(ctx, Timer{UserID: p.UserID, SubjectType: SubjectTicket, SubjectID: ticketID, Note: n})
}

// CurrentTimer returns the caller's running timer, ErrNotFound when none runs.
func (s *Service) CurrentTimer(ctx context.Context, p Principal) (Timer, error) {
	if p.UserID == "" || !p.mayLog() {
		return Timer{}, ErrForbidden
	}
	return s.store.GetTimer(ctx, p.UserID)
}

// DiscardTimer removes the caller's timer without logging anything.
func (s *Service) DiscardTimer(ctx context.Context, p Principal) error {
	if p.UserID == "" || !p.mayLog() {
		return ErrForbidden
	}
	return s.store.DeleteTimer(ctx, p.UserID)
}

// StopTimer turns the caller's timer into a timer entry. The duration is the elapsed time rounded up to the minute
// (at least one minute); a timer older than 12 hours needs an explicit duration (1 minute to 24 hours). The work
// date is the date of the stop (UTC). The entry follows every rule of a manual entry (access, 24-hour limit, audit),
// and the timer is removed in the same transaction, so a retry cannot log the same time twice.
func (s *Service) StopTimer(ctx context.Context, c Caller, p Principal, durationMinutes int) (Entry, error) {
	if p.UserID == "" || !p.mayLog() {
		return Entry{}, ErrForbidden
	}
	t, err := s.store.GetTimer(ctx, p.UserID)
	if err != nil {
		return Entry{}, err
	}
	now := s.now().UTC()
	elapsed := now.Sub(t.StartedAt)
	minutes := int((elapsed + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if elapsed > StaleTimerNeedsDuration {
		if durationMinutes <= 0 {
			return Entry{}, invalid("the timer is older than 12 hours: give the duration to log")
		}
		minutes = durationMinutes
	} else if durationMinutes > 0 {
		minutes = durationMinutes
	}
	note := ""
	if t.Note != nil {
		note = *t.Note
	}
	started := t.StartedAt
	return s.Create(ctx, c, p, NewEntry{
		SubjectType: SubjectTicket, SubjectID: t.SubjectID, WorkDate: now.Format("2006-01-02"), DurationMinutes: minutes, Note: note,
		source: SourceTimer, startedAt: &started,
		inTx: func(ctx context.Context, tx pgx.Tx) error {
			locked, err := s.store.LockTimerTx(ctx, tx, p.UserID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return ErrNotFound
				}
				return err
			}
			if !locked.StartedAt.Equal(t.StartedAt) || locked.SubjectID != t.SubjectID {
				return ErrVersionConflict // restarted meanwhile
			}
			return s.store.DeleteTimerTx(ctx, tx, p.UserID)
		},
	})
}
