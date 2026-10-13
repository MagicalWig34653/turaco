package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

const (
	// SLASweepJobType is the recurring job that records warnings and breaches of running clocks (F17 S-D).
	SLASweepJobType = "sla.clock_sweep"
	// SLASweepJobTimeout bounds one sweep run; the schedule starts the next one a minute later.
	SLASweepJobTimeout = 50 * time.Second
	// SLASweepInterval is how often the sweep is scheduled.
	SLASweepInterval = time.Minute

	slaSweepBatch   = 500
	slaSweepBatches = 20
	// SLANotifyMaxAge: a warning or breach event older than this is dropped by the notification consumer, so a
	// backlog delivered after the module was switched on again does not flood people with stale alerts.
	SLANotifyMaxAge = time.Hour
	// SLASystemActor is the audit/Task actor name of the sweep.
	SLASystemActor = "sla-sweep"
)

// SLAClockRef identifies one clock.
type SLAClockRef struct{ TicketID, Kind string }

// SLASweepResult is what one clock's sweep recorded.
type SLASweepResult struct {
	TicketID, Kind, QueueID, Reference string
	AssigneeID                         *string
	DueAt                              time.Time
	// Warned and Breached say which facts were newly recorded; AnnounceWarning is false when a breach (or an
	// earlier breach) makes the warning moot.
	Warned, Breached, AnnounceWarning bool
	// EscalationTask and DefaultTeamID are set for a breach only.
	EscalationTask bool
	DefaultTeamID  *string
	// LocationID is the Ticket's affected Location (set with the escalation Task).
	LocationID *string
}

// SLASweepStore is the persistence port of the sweep. SweepSLAClockTx locks Ticket then clock with SKIP LOCKED and
// writes the idempotent facts, the transition row and the unversioned Ticket projection.
type SLASweepStore interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	SLASweepCandidates(ctx context.Context, now time.Time, limit int) ([]SLAClockRef, error)
	SweepSLAClockTx(ctx context.Context, tx pgx.Tx, ref SLAClockRef, now time.Time) (SLASweepResult, error)
}

// EscalationTaskInput describes the optional Task created for a breach.
type EscalationTaskInput struct {
	TicketID       string
	Title          string
	DueAt          time.Time
	AssignedTeamID string
	LocationID     *string
}

// EscalationTasks is the Tasks public contract as the sweep uses it (servicedesk/public adapter).
type EscalationTasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID string, in EscalationTaskInput) (string, error)
}

// SLASweeper records warnings and breaches of running clocks.
type SLASweeper struct {
	store   SLASweepStore
	enabled func(context.Context) (bool, error)
	tasks   EscalationTasks
	logger  *slog.Logger
	now     func() time.Time
}

// NewSLASweeper builds the sweep. enabled reports the sla module switch; while it is off a run does nothing.
func NewSLASweeper(store SLASweepStore, enabled func(context.Context) (bool, error)) *SLASweeper {
	return &SLASweeper{store: store, enabled: enabled, logger: slog.Default(), now: func() time.Time { return time.Now().UTC() }}
}

// WithEscalationTasks enables the optional escalation Task of policies that ask for it.
func (w *SLASweeper) WithEscalationTasks(t EscalationTasks) *SLASweeper {
	w.tasks = t
	return w
}

// WithNow replaces the wall clock (tests).
func (w *SLASweeper) WithNow(now func() time.Time) *SLASweeper {
	w.now = now
	return w
}

// WithLogger sets the logger for per-clock failures.
func (w *SLASweeper) WithLogger(l *slog.Logger) *SLASweeper {
	w.logger = l
	return w
}

// SLASweepSummary counts what a run recorded.
type SLASweepSummary struct{ Warned, Breached, Failed int }

// HandleSweep is the job handler of sla.clock_sweep.
func (w *SLASweeper) HandleSweep(ctx context.Context, job jobs.Job) error {
	_, err := w.Sweep(ctx, "job:"+job.ID)
	return err
}

// Sweep records every due warning and breach. Each clock is handled in its own transaction (so a Ticket being
// worked on or a failing clock never blocks the others) together with its event and optional escalation Task.
// Re-running, or running in two workers, records every fact exactly once. While the sla switch is off nothing is done
// and existing clocks stay as they are.
func (w *SLASweeper) Sweep(ctx context.Context, correlationID string) (SLASweepSummary, error) {
	var sum SLASweepSummary
	on := false
	if w.enabled != nil {
		var err error
		if on, err = w.enabled(ctx); err != nil {
			return sum, fmt.Errorf("check sla switch: %w", err)
		}
	}
	if !on {
		return sum, nil
	}
	now := w.now()
	skip := map[SLAClockRef]bool{}
	for batch := 0; batch < slaSweepBatches && ctx.Err() == nil; batch++ {
		refs, err := w.store.SLASweepCandidates(ctx, now, slaSweepBatch+len(skip))
		if err != nil {
			return sum, err
		}
		progressed := false
		fresh := 0
		for _, ref := range refs {
			if skip[ref] {
				continue
			}
			fresh++
			skip[ref] = true // each clock is tried once per run; a clock busy right now waits for the next minute
			warned, breached, err := w.sweepClock(ctx, ref, now, correlationID)
			if err != nil {
				sum.Failed++
				w.logger.Error("sla clock sweep failed", "ticket_id", ref.TicketID, "kind", ref.Kind, "error", err)
				continue
			}
			if warned || breached {
				progressed = true
			}
			if warned {
				sum.Warned++
			}
			if breached {
				sum.Breached++
			}
		}
		if fresh < slaSweepBatch || !progressed {
			break
		}
	}
	return sum, ctx.Err()
}

func (w *SLASweeper) sweepClock(ctx context.Context, ref SLAClockRef, now time.Time, correlationID string) (warned, breached bool, err error) {
	err = w.store.InTx(ctx, func(tx pgx.Tx) error {
		res, err := w.store.SweepSLAClockTx(ctx, tx, ref, now)
		if err != nil {
			return err
		}
		warned, breached = res.Warned, res.Breached
		payload := map[string]any{"ticketId": res.TicketID, "queueId": res.QueueID, "clockKind": res.Kind, "dueAt": res.DueAt, "assigneeId": res.AssigneeID}
		switch {
		case res.Breached:
			if err := events.Publish(ctx, tx, events.Publication{Type: "TicketSLABreached", CorrelationID: correlationID, Payload: payload}); err != nil {
				return err
			}
			return w.escalate(ctx, tx, res, now, correlationID)
		case res.AnnounceWarning:
			return events.Publish(ctx, tx, events.Publication{Type: "TicketSLAWarningReached", CorrelationID: correlationID, Payload: payload})
		}
		return nil
	})
	if err != nil {
		return false, false, err
	}
	return warned, breached, nil
}

// escalate creates the optional Task of a breach in the sweep's transaction. The breach fact is recorded once, so
// the Task exists once per Ticket and clock kind. A Queue without an active default Team gets no Task.
func (w *SLASweeper) escalate(ctx context.Context, tx pgx.Tx, res SLASweepResult, now time.Time, correlationID string) error {
	if !res.EscalationTask || w.tasks == nil || res.DefaultTeamID == nil {
		return nil
	}
	_, err := w.tasks.CreateInTx(ctx, tx, audit.SystemActor(SLASystemActor), correlationID, EscalationTaskInput{
		TicketID: res.TicketID, Title: "SLA breached: " + res.Reference, DueAt: now, AssignedTeamID: *res.DefaultTeamID, LocationID: res.LocationID,
	})
	if errors.Is(err, ErrAssigneeInvalid) {
		return nil // the default Team is inactive: the notification still reaches the assignee or nobody
	}
	return err
}
