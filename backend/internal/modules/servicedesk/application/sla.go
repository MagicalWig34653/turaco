package application

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// SLAClock is a staff-only view of a Ticket's measured target.
type SLAClock struct {
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	Outcome          string     `json:"outcome"`
	TargetMinutes    int        `json:"targetMinutes"`
	TimeBasis        string     `json:"timeBasis"`
	CalendarName     string     `json:"calendarName"`
	PolicyName       string     `json:"policyName"`
	DueAt            *time.Time `json:"dueAt"`
	RemainingSeconds int64      `json:"remainingSeconds"`
	PausedAt         *time.Time `json:"pausedAt"`
	PauseReason      *string    `json:"pauseReason"`
	CompletedAt      *time.Time `json:"completedAt"`
	WarnedAt         *time.Time `json:"warnedAt"`
	BreachedAt       *time.Time `json:"breachedAt"`
}

var SLAExemptReasons = []string{"misclassified", "duplicate_of_major_incident", "test_ticket", "other"}

// ExemptSLA ends the active measurements without changing the Ticket lifecycle or version.
func (s *Service) ExemptSLA(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.SLAManage || p.External {
		return ErrForbidden
	}
	if expected == nil || *expected < 1 || !slices.Contains(SLAExemptReasons, reason) {
		return invalid("expectedVersion and a valid exemption reason are required")
	}
	on, err := s.slaOn(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrForbidden
	}
	cs, ok := s.store.(SLAStore)
	if !ok {
		return ErrForbidden
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		if !ep.staff() || s.isOwner(cur, p) {
			return ErrForbidden
		}
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		changes, err := cs.ApplySLATx(ctx, tx, cur, "exempt", reason, c, time.Now().UTC())
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return &InvalidTransitionError{Operation: "sla_exempt", From: cur.Status}
		}
		return record(ctx, tx, c, "servicedesk.ticket.sla_exempted", &cur, &cur, map[string]any{"reasonCode": reason, "sla": changes})
	})
}

// SLAStore owns the clock rows and the unversioned Ticket projection. The caller holds the Ticket lock.
type SLAStore interface {
	ApplySLATx(context.Context, pgx.Tx, Ticket, string, string, Caller, time.Time) ([]map[string]any, error)
	SLAClocks(context.Context, string) ([]SLAClock, error)
}

// SLASummaryStore reads the derived SLA summary of one Ticket (the same expression as the sla_state list field).
type SLASummaryStore interface {
	SLASummary(ctx context.Context, ticketID string) (TicketSLA, error)
}

// withSLAVisibility keeps the SLA summary only on rows where the caller works as staff: they view the Ticket's Queue
// and are neither its reporter nor its affected User (employees see goals, not ITIL fields). Everything else is cleared.
func (s *Service) withSLAVisibility(a access, p Principal, on bool, ts ...*Ticket) {
	for _, t := range ts {
		if t != nil && (!on || p.External || !a.canView(t.QueueID) || s.isOwner(*t, p)) {
			t.SLA = nil
		}
	}
}

// WithSLA enables clock integration. enabled is checked on every operation and read.
func (s *Service) WithSLA(enabled func(context.Context) (bool, error)) *Service {
	s.slaEnabled = enabled
	return s
}

// SLAEnabled reports whether the SLA switch is on (public contract for System Views and the AI tools).
func (s *Service) SLAEnabled(ctx context.Context) (bool, error) { return s.slaOn(ctx) }

func (s *Service) slaOn(ctx context.Context) (bool, error) {
	if s.slaEnabled == nil {
		return false, nil
	}
	return s.slaEnabled(ctx)
}

func (s *Service) applySLA(ctx context.Context, tx pgx.Tx, t Ticket, operation, reason string, c Caller, at time.Time, meta map[string]any) error {
	cs, ok := s.store.(SLAStore)
	if !ok {
		return nil
	}
	changes, err := cs.ApplySLATx(ctx, tx, t, operation, reason, c, at)
	if len(changes) > 0 {
		meta["sla"] = changes
	}
	return err
}

// startSLA uses the switch state resolved before opening the Ticket transaction.
func (s *Service) startSLA(ctx context.Context, tx pgx.Tx, t Ticket, c Caller, enabled bool, meta map[string]any) error {
	if !enabled {
		return nil
	}
	return s.applySLA(ctx, tx, t, "create", "", c, t.CreatedAt, meta)
}
