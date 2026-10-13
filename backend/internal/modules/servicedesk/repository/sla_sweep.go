package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.SLASweepStore = (*Repository)(nil)
var _ application.SLANotifyStore = (*Repository)(nil)

// SLASweepCandidates lists running clocks whose warning or deadline instant has passed and that still lack the
// recorded fact. It reads through the partial sweep indexes and does not lock; SweepSLAClockTx re-checks under lock.
func (r *Repository) SLASweepCandidates(ctx context.Context, now time.Time, limit int) ([]application.SLAClockRef, error) {
	rows, err := r.pool.Query(ctx, `SELECT c.ticket_id::text, c.kind FROM servicedesk.ticket_sla_clocks c
		WHERE c.status='running' AND ((c.warned_at IS NULL AND c.warn_at <= $1) OR (c.breached_at IS NULL AND c.due_at <= $1))
		AND EXISTS (SELECT 1 FROM servicedesk.tickets t WHERE t.id=c.ticket_id AND t.status IN ('new','open','in_progress','waiting'))
		ORDER BY c.due_at, c.ticket_id, c.kind LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("sla sweep candidates: %w", err)
	}
	defer rows.Close()
	out := []application.SLAClockRef{}
	for rows.Next() {
		var ref application.SLAClockRef
		if err := rows.Scan(&ref.TicketID, &ref.Kind); err != nil {
			return nil, fmt.Errorf("sla sweep candidates: scan: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// SweepSLAClockTx records the warning and/or the breach of one running clock as clock facts. Lock order is the
// Ticket row, then the clock row, both SKIP LOCKED: a Ticket being worked on is left for the next pass. The facts
// are idempotent (warned_at/breached_at IS NULL) and are written with the unversioned Ticket projection; the Ticket
// version is not touched. breached_at is the factual deadline, not the sweep time.
func (r *Repository) SweepSLAClockTx(ctx context.Context, tx pgx.Tx, ref application.SLAClockRef, now time.Time) (application.SLASweepResult, error) {
	now = now.UTC()
	res := application.SLASweepResult{TicketID: ref.TicketID, Kind: ref.Kind}
	err := tx.QueryRow(ctx, `SELECT queue_id::text, reference, assignee_user_id::text FROM servicedesk.tickets
		WHERE id=$1::uuid AND status IN ('new','open','in_progress','waiting') FOR UPDATE SKIP LOCKED`, ref.TicketID).Scan(&res.QueueID, &res.Reference, &res.AssigneeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("lock ticket for sla sweep: %w", err)
	}
	c, err := scanClock(tx.QueryRow(ctx, `SELECT `+clockColumns+` FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1::uuid AND kind=$2 AND status='running' FOR UPDATE SKIP LOCKED`, ref.TicketID, ref.Kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("lock sla clock for sweep: %w", err)
	}
	warn := c.WarnedAt == nil && c.WarnAt != nil && !c.WarnAt.After(now)
	breach := c.BreachedAt == nil && c.DueAt != nil && !c.DueAt.After(now)
	if !warn && !breach {
		return res, nil
	}
	// A warning that is overtaken by a breach (late sweep, recompute, retarget) is recorded but not announced.
	res.Warned, res.Breached = warn, breach
	res.AnnounceWarning = warn && !breach && c.BreachedAt == nil
	res.DueAt = *c.DueAt
	if warn {
		c.WarnedAt = c.WarnAt
	}
	if breach {
		c.BreachedAt = c.DueAt
	}
	if _, err := tx.Exec(ctx, `UPDATE servicedesk.ticket_sla_clocks SET warned_at=$3, breached_at=$4, version=version+1 WHERE ticket_id=$1::uuid AND kind=$2`,
		ref.TicketID, ref.Kind, c.WarnedAt, c.BreachedAt); err != nil {
		return res, fmt.Errorf("record sla clock facts: %w", err)
	}
	targetSeconds := int64(c.TargetMinutes) * 60
	if warn {
		remaining := targetSeconds - int64(c.TargetMinutes*c.WarningPercent)*60/100
		if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.ticket_sla_clock_transitions(ticket_id,kind,operation,from_status,to_status,cause,actor_id,occurred_at,remaining_seconds) VALUES($1::uuid,$2,'mark_warned','running','running','sweep',NULL,$3,$4)`,
			ref.TicketID, ref.Kind, *c.WarnedAt, remaining); err != nil {
			return res, fmt.Errorf("record sla warning transition: %w", err)
		}
	}
	if breach {
		if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.ticket_sla_clock_transitions(ticket_id,kind,operation,from_status,to_status,cause,actor_id,occurred_at,remaining_seconds) VALUES($1::uuid,$2,'mark_breached','running','running','sweep',NULL,$3,0)`,
			ref.TicketID, ref.Kind, *c.BreachedAt); err != nil {
			return res, fmt.Errorf("record sla breach transition: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT p.escalation_task, q.default_team_id::text, (SELECT affected_location_id::text FROM servicedesk.tickets WHERE id=$3::uuid)
			FROM servicedesk.sla_policies p, servicedesk.queues q WHERE p.id=$1::uuid AND q.id=$2::uuid`,
			c.PolicyID, res.QueueID, ref.TicketID).Scan(&res.EscalationTask, &res.DefaultTeamID, &res.LocationID); err != nil {
			return res, fmt.Errorf("load escalation setting: %w", err)
		}
	}
	if err := r.projectSLATx(ctx, tx, ref.TicketID); err != nil {
		return res, fmt.Errorf("project sla facts: %w", err)
	}
	return res, nil
}

// SLANotifyFacts implements application.SLANotifyStore: the clock state and the Queue's default Team, read in the
// consumer's transaction. ok is false when the clock does not exist.
func (r *Repository) SLANotifyFacts(ctx context.Context, tx pgx.Tx, ticketID, kind string) (application.SLANotifyFacts, bool, error) {
	var f application.SLANotifyFacts
	err := tx.QueryRow(ctx, `SELECT c.status, t.queue_id::text, q.default_team_id::text FROM servicedesk.ticket_sla_clocks c
		JOIN servicedesk.tickets t ON t.id=c.ticket_id JOIN servicedesk.queues q ON q.id=t.queue_id
		WHERE c.ticket_id=$1::uuid AND c.kind=$2`, ticketID, kind).Scan(&f.ClockStatus, &f.QueueID, &f.DefaultTeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, false, nil
	}
	return f, err == nil, err
}
