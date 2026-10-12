package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
)

func (e *env) setTimerStart(p application.Principal, d time.Duration) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE timetracking.timers SET started_at = $2 WHERE user_id = $1::uuid`, p.UserID, e.now.Add(-d)); err != nil {
		e.t.Fatal(err)
	}
}

func TestTimerStartStopCreatesTimerEntryAndRemovesTimer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", e.ticket, "printer"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", e.ticket, ""); !errors.Is(err, application.ErrTimerRunning) {
		t.Fatalf("second start: %v", err)
	}
	e.setTimerStart(e.alice, 61*time.Minute+10*time.Second)
	entry, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 0)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Source != application.SourceTimer || entry.DurationMinutes != 62 || entry.StartedAt == nil || entry.UserID != e.alice.UserID {
		t.Fatalf("entry = %+v", entry)
	}
	if _, err := e.svc.CurrentTimer(ctx, e.alice); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("timer still present: %v", err)
	}
	if _, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 0); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stop without timer: %v", err)
	}
}

func TestTimerStaleNeedsExplicitDurationAndRespectsLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", e.ticket, ""); err != nil {
		t.Fatal(err)
	}
	e.setTimerStart(e.alice, 13*time.Hour)
	var inv *application.InvalidInputError
	if _, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 0); !errors.As(err, &inv) {
		t.Fatalf("stale stop without duration: %v", err)
	}
	if _, err := e.svc.CurrentTimer(ctx, e.alice); err != nil {
		t.Fatalf("a refused stop must keep the timer: %v", err)
	}
	if _, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 25*60); !errors.As(err, &inv) {
		t.Fatalf("duration above 24h: %v", err)
	}
	entry, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 90)
	if err != nil || entry.DurationMinutes != 90 {
		t.Fatalf("explicit duration: %+v %v", entry, err)
	}
}

func TestTimerAccessAndPrivacy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// View-only access cannot start; a person without log/manage cannot start; unknown ticket is not found.
	if _, err := e.svc.StartTimer(ctx, e.viewer, "ticket", e.ticket, ""); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := e.svc.StartTimer(ctx, application.Principal{UserID: newID()}, "ticket", e.ticket, ""); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", newID(), ""); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown ticket: %v", err)
	}
	// Timers are personal: bob sees and stops only his own.
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", e.ticket, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CurrentTimer(ctx, e.bob); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("bob sees alice's timer: %v", err)
	}
	if err := e.svc.DiscardTimer(ctx, e.bob); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CurrentTimer(ctx, e.alice); err != nil {
		t.Fatalf("alice's timer was removed by bob: %v", err)
	}
	// Losing work access before stop refuses the stop and keeps the timer.
	e.tickets.grant(e.alice.UserID, e.ticket, application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true})
	e.setTimerStart(e.alice, 5*time.Minute)
	if _, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 0); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("stop after losing access: %v", err)
	}
	if _, err := e.svc.CurrentTimer(ctx, e.alice); err != nil {
		t.Fatalf("timer lost: %v", err)
	}
	if err := e.svc.DiscardTimer(ctx, e.alice); err != nil {
		t.Fatal(err)
	}
}

func TestTimerStopIsAtomicUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.StartTimer(ctx, e.alice, "ticket", e.ticket, ""); err != nil {
		t.Fatal(err)
	}
	e.setTimerStart(e.alice, 10*time.Minute)
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() {
			_, err := e.svc.StopTimer(ctx, e.caller(e.alice), e.alice, 0)
			results <- err
		}()
	}
	ok := 0
	for i := 0; i < 6; i++ {
		if err := <-results; err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("successful stops = %d, want 1", ok)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM timetracking.time_entries WHERE user_id=$1::uuid AND source='timer'`, e.alice.UserID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("timer entries = %d, %v", n, err)
	}
}
