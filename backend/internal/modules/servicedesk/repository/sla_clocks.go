package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
	"github.com/jackc/pgx/v5"
)

type clockRow struct {
	TicketID, Kind, Status, PolicyID, TimeBasis                              string
	PolicyVersion, TargetMinutes, WarningPercent, Version                    int
	CalendarID                                                               *string
	StartedAt                                                                time.Time
	ElapsedSeconds                                                           int64
	RunningSince, DueAt, WarnAt, PausedAt, CompletedAt, WarnedAt, BreachedAt *time.Time
	PauseReason, EndReason                                                   *string
}

const clockColumns = `ticket_id::text,kind,status,policy_id::text,policy_version,target_minutes,time_basis,calendar_id::text,warning_percent,started_at,elapsed_seconds,running_since,due_at,warn_at,paused_at,pause_reason,completed_at,end_reason,warned_at,breached_at,version`
const clockReadColumns = `c.ticket_id::text,c.kind,c.status,c.policy_id::text,c.policy_version,c.target_minutes,c.time_basis,c.calendar_id::text,c.warning_percent,c.started_at,c.elapsed_seconds,c.running_since,c.due_at,c.warn_at,c.paused_at,c.pause_reason,c.completed_at,c.end_reason,c.warned_at,c.breached_at,c.version`

func scanClock(row pgx.Row) (clockRow, error) {
	var c clockRow
	err := row.Scan(&c.TicketID, &c.Kind, &c.Status, &c.PolicyID, &c.PolicyVersion, &c.TargetMinutes, &c.TimeBasis, &c.CalendarID, &c.WarningPercent, &c.StartedAt, &c.ElapsedSeconds, &c.RunningSince, &c.DueAt, &c.WarnAt, &c.PausedAt, &c.PauseReason, &c.CompletedAt, &c.EndReason, &c.WarnedAt, &c.BreachedAt, &c.Version)
	return c, err
}

func loadCalendar(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, id string) (scheduling.BusinessCalendar, error) {
	var zone string
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT timezone,weekly_hours FROM platform.business_calendars WHERE id=$1`, id).Scan(&zone, &raw); err != nil {
		return scheduling.BusinessCalendar{}, err
	}
	cal := scheduling.BusinessCalendar{Timezone: zone, Closures: map[string]struct{}{}}
	if err := json.Unmarshal(raw, &cal.WeeklyHours); err != nil {
		return cal, err
	}
	rows, err := q.Query(ctx, `SELECT closed_on::text FROM platform.business_calendar_closures WHERE calendar_id=$1`, id)
	if err != nil {
		return cal, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return cal, err
		}
		cal.Closures[day] = struct{}{}
	}
	return cal, rows.Err()
}

func measured(ctx context.Context, q pgx.Tx, c clockRow, from, to time.Time) (int64, error) {
	if !to.After(from) {
		return 0, nil
	}
	if c.TimeBasis == "continuous" {
		return int64(to.Sub(from).Seconds()), nil
	}
	cal, err := loadCalendar(ctx, q, *c.CalendarID)
	if err != nil {
		return 0, err
	}
	d, err := businessDuration(cal, from, to)
	return int64(d.Seconds()), err
}

func businessDuration(cal scheduling.BusinessCalendar, from, to time.Time) (time.Duration, error) {
	var total time.Duration
	for from.Before(to) {
		end := from.Add(300 * 24 * time.Hour)
		if end.After(to) {
			end = to
		}
		part, err := scheduling.BusinessDurationBetween(cal, from, end)
		if err != nil {
			return 0, err
		}
		total += part
		from = end
	}
	return total, nil
}

func deadline(ctx context.Context, q pgx.Tx, c clockRow, from time.Time, seconds int64) (time.Time, error) {
	if seconds < 0 {
		seconds = 0
	}
	if c.TimeBasis == "continuous" {
		return from.Add(time.Duration(seconds) * time.Second), nil
	}
	cal, err := loadCalendar(ctx, q, *c.CalendarID)
	if err != nil {
		return time.Time{}, err
	}
	return scheduling.AddBusinessDuration(cal, from, time.Duration(seconds)*time.Second)
}

func setDue(ctx context.Context, tx pgx.Tx, c *clockRow, at time.Time) error {
	remaining := int64(c.TargetMinutes)*60 - c.ElapsedSeconds
	due, err := deadline(ctx, tx, *c, at, remaining)
	if err != nil {
		return err
	}
	warnRemaining := int64(c.TargetMinutes*c.WarningPercent)*60/100 - c.ElapsedSeconds
	warn, err := deadline(ctx, tx, *c, at, warnRemaining)
	if err != nil {
		return err
	}
	c.RunningSince = &at
	c.DueAt = &due
	c.WarnAt = &warn
	return nil
}

func (r *Repository) policyTarget(ctx context.Context, tx pgx.Tx, queueID, priority, kind string) (clockRow, []string, bool, error) {
	var c clockRow
	var reasons []string
	err := tx.QueryRow(ctx, `SELECT p.id::text,p.version,t.target_minutes,t.time_basis,p.calendar_id::text,p.warning_percent,p.pause_waiting_reasons
		FROM servicedesk.queues q JOIN servicedesk.sla_policies p ON p.id=q.sla_policy_id AND p.status='active'
		JOIN servicedesk.sla_targets t ON t.policy_id=p.id AND t.priority=$2 AND t.kind=$3 WHERE q.id=$1`, queueID, priority, kind).
		Scan(&c.PolicyID, &c.PolicyVersion, &c.TargetMinutes, &c.TimeBasis, &c.CalendarID, &c.WarningPercent, &reasons)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, false, nil
	}
	return c, reasons, err == nil, err
}

func (r *Repository) persistClock(ctx context.Context, tx pgx.Tx, c clockRow, insert bool) error {
	if insert {
		_, err := tx.Exec(ctx, `INSERT INTO servicedesk.ticket_sla_clocks(ticket_id,kind,status,policy_id,policy_version,target_minutes,time_basis,calendar_id,warning_percent,started_at,elapsed_seconds,running_since,due_at,warn_at,paused_at,pause_reason,completed_at,end_reason,warned_at,breached_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, c.TicketID, c.Kind, c.Status, c.PolicyID, c.PolicyVersion, c.TargetMinutes, c.TimeBasis, c.CalendarID, c.WarningPercent, c.StartedAt, c.ElapsedSeconds, c.RunningSince, c.DueAt, c.WarnAt, c.PausedAt, c.PauseReason, c.CompletedAt, c.EndReason, c.WarnedAt, c.BreachedAt)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE servicedesk.ticket_sla_clocks SET status=$3,policy_id=$4,policy_version=$5,target_minutes=$6,time_basis=$7,calendar_id=$8,warning_percent=$9,elapsed_seconds=$10,running_since=$11,due_at=$12,warn_at=$13,paused_at=$14,pause_reason=$15,completed_at=$16,end_reason=$17,warned_at=$18,breached_at=$19,version=version+1 WHERE ticket_id=$1 AND kind=$2`,
		c.TicketID, c.Kind, c.Status, c.PolicyID, c.PolicyVersion, c.TargetMinutes, c.TimeBasis, c.CalendarID, c.WarningPercent, c.ElapsedSeconds, c.RunningSince, c.DueAt, c.WarnAt, c.PausedAt, c.PauseReason, c.CompletedAt, c.EndReason, c.WarnedAt, c.BreachedAt)
	return err
}

// clockOutcomeSQL is the outcome of a finished clock c: met or breached when it completed, not_measured when it was
// cancelled (including exempted), NULL while it is still measuring. The migration 000100 backfill uses the same rule.
const clockOutcomeSQL = `CASE WHEN c.status='completed' AND c.breached_at IS NOT NULL THEN 'breached' WHEN c.status='completed' THEN 'met' WHEN c.status='cancelled' THEN 'not_measured' END`

func (r *Repository) projectSLATx(ctx context.Context, tx pgx.Tx, ticketID string) error {
	// The projection alone owns these columns. Ticket version and updated_at deliberately stay unchanged.
	_, err := tx.Exec(ctx, `UPDATE servicedesk.tickets t SET
		sla_response_due_at=(SELECT due_at FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id AND kind='first_response' AND status='running'),
		sla_resolution_due_at=(SELECT due_at FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id AND kind='resolution' AND status='running'),
		sla_warn_at=(SELECT min(warn_at) FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id AND status='running'),
		sla_breached_at=(SELECT min(breached_at) FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id),
		sla_tracked=EXISTS(SELECT 1 FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id),
		sla_paused=EXISTS(SELECT 1 FROM servicedesk.ticket_sla_clocks WHERE ticket_id=t.id AND status='paused'),
		sla_response_outcome=(SELECT `+clockOutcomeSQL+` FROM servicedesk.ticket_sla_clocks c WHERE c.ticket_id=t.id AND c.kind='first_response'),
		sla_resolution_outcome=(SELECT `+clockOutcomeSQL+` FROM servicedesk.ticket_sla_clocks c WHERE c.ticket_id=t.id AND c.kind='resolution')
		WHERE t.id=$1`, ticketID)
	return err
}

// ApplySLATx is called with the Ticket row locked by the owning operation. It never creates clocks except on create.
func (r *Repository) ApplySLATx(ctx context.Context, tx pgx.Tx, t application.Ticket, operation, reason string, actor application.Caller, at time.Time) ([]map[string]any, error) {
	at = at.UTC()
	rows, err := tx.Query(ctx, `SELECT `+clockColumns+` FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 ORDER BY kind FOR UPDATE`, t.ID)
	if err != nil {
		return nil, err
	}
	clocks := []clockRow{}
	for rows.Next() {
		c, e := scanClock(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		clocks = append(clocks, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if operation == "create" && len(clocks) == 0 {
		for _, kind := range []string{"first_response", "resolution"} {
			c, _, ok, e := r.policyTarget(ctx, tx, t.QueueID, t.Priority, kind)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			c.TicketID = t.ID
			c.Kind = kind
			c.Status = "running"
			c.StartedAt = t.CreatedAt
			if err := setDue(ctx, tx, &c, t.CreatedAt); err != nil {
				return nil, err
			}
			clocks = append(clocks, c)
		}
	}
	changes := []map[string]any{}
	for _, original := range clocks {
		c := original
		insert := operation == "create"
		from := c.Status
		op := ""
		// Preserve a factual breach before a pause, cancellation or retarget removes the old deadline.
		if !insert && c.Status == "running" && c.BreachedAt == nil && c.DueAt != nil && at.After(*c.DueAt) {
			c.BreachedAt = c.DueAt
		}
		switch operation {
		case "create":
			op = "start"
			from = ""
		case "wait":
			if c.Status != "running" {
				continue
			}
			var reasons []string
			if e := tx.QueryRow(ctx, `SELECT pause_waiting_reasons FROM servicedesk.sla_policies WHERE id=$1`, c.PolicyID).Scan(&reasons); e != nil {
				return nil, e
			}
			pause := false
			for _, v := range reasons {
				if v == reason {
					pause = true
					break
				}
			}
			if !pause {
				continue
			}
			n, e := measured(ctx, tx, c, *c.RunningSince, at)
			if e != nil {
				return nil, e
			}
			c.ElapsedSeconds += n
			c.Status = "paused"
			c.RunningSince = nil
			c.DueAt = nil
			c.WarnAt = nil
			c.PausedAt = &at
			c.PauseReason = &reason
			op = "pause"
		case "resume":
			if c.Status != "paused" {
				continue
			}
			c.Status = "running"
			c.PausedAt = nil
			c.PauseReason = nil
			if err := setDue(ctx, tx, &c, at); err != nil {
				return nil, err
			}
			op = "resume"
		case "first_response":
			if c.Kind != "first_response" || (c.Status != "running" && c.Status != "paused") {
				continue
			}
			op = "complete"
		case "resolve":
			if c.Status != "running" && c.Status != "paused" {
				continue
			}
			op = "complete"
		case "reopen":
			if c.Kind != "resolution" || c.Status != "completed" {
				continue
			}
			c.Status = "running"
			c.CompletedAt = nil
			if err := setDue(ctx, tx, &c, at); err != nil {
				return nil, err
			}
			op = "reopen_resume"
		case "cancel", "mark_duplicate":
			if c.Status != "running" && c.Status != "paused" {
				continue
			}
			c.Status = "cancelled"
			v := "ticket_cancelled"
			c.EndReason = &v
			op = "cancel"
		case "set_priority", "move_queue":
			if c.Status != "running" && c.Status != "paused" {
				continue
			}
			target, _, ok, e := r.policyTarget(ctx, tx, t.QueueID, t.Priority, c.Kind)
			if e != nil {
				return nil, e
			}
			if !ok {
				c.Status = "cancelled"
				v := "no_target"
				c.EndReason = &v
				op = "retarget"
				break
			}
			if c.Status == "running" {
				n, e := measured(ctx, tx, c, *c.RunningSince, at)
				if e != nil {
					return nil, e
				}
				c.ElapsedSeconds += n
			}
			c.PolicyID = target.PolicyID
			c.PolicyVersion = target.PolicyVersion
			c.TargetMinutes = target.TargetMinutes
			c.TimeBasis = target.TimeBasis
			c.CalendarID = target.CalendarID
			c.WarningPercent = target.WarningPercent
			if c.Status == "running" {
				if err := setDue(ctx, tx, &c, at); err != nil {
					return nil, err
				}
			}
			if c.BreachedAt == nil && c.ElapsedSeconds >= int64(c.TargetMinutes)*60 {
				c.BreachedAt = &at
			}
			op = "retarget"
		case "exempt":
			if c.Status != "running" && c.Status != "paused" {
				continue
			}
			c.Status = "cancelled"
			v := "exempted"
			c.EndReason = &v
			op = "exempt"
		default:
			continue
		}
		if op == "complete" {
			if c.Status == "running" {
				n, e := measured(ctx, tx, c, *c.RunningSince, at)
				if e != nil {
					return nil, e
				}
				c.ElapsedSeconds += n
				if c.BreachedAt == nil && c.DueAt != nil && at.After(*c.DueAt) {
					c.BreachedAt = c.DueAt
				}
			}
			c.Status = "completed"
			c.CompletedAt = &at
		}
		if c.Status == "cancelled" && from == "running" {
			n, e := measured(ctx, tx, original, *original.RunningSince, at)
			if e != nil {
				return nil, e
			}
			c.ElapsedSeconds += n
		}
		if c.Status == "cancelled" || c.Status == "completed" {
			c.RunningSince = nil
			c.DueAt = nil
			c.WarnAt = nil
			c.PausedAt = nil
			c.PauseReason = nil
		}
		if err := r.persistClock(ctx, tx, c, insert); err != nil {
			return nil, fmt.Errorf("persist SLA clock: %w", err)
		}
		remaining := int64(c.TargetMinutes)*60 - c.ElapsedSeconds
		var actorID *string
		if actor.Actor.UserID != "" {
			actorID = &actor.Actor.UserID
		}
		_, err = tx.Exec(ctx, `INSERT INTO servicedesk.ticket_sla_clock_transitions(ticket_id,kind,operation,from_status,to_status,cause,actor_id,occurred_at,remaining_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, t.ID, c.Kind, op, nilIfEmpty(from), c.Status, operation, actorID, at, remaining)
		if err != nil {
			return nil, err
		}
		changes = append(changes, map[string]any{"kind": c.Kind, "from": from, "to": c.Status, "remainingSeconds": remaining, "operation": op})
	}
	if len(changes) > 0 {
		if err := r.projectSLATx(ctx, tx, t.ID); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

func (r *Repository) SLAClocks(ctx context.Context, ticketID string) ([]application.SLAClock, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+clockReadColumns+`,p.name,coalesce(b.name,'') FROM servicedesk.ticket_sla_clocks c JOIN servicedesk.sla_policies p ON p.id=c.policy_id LEFT JOIN platform.business_calendars b ON b.id=c.calendar_id WHERE c.ticket_id=$1 ORDER BY c.kind`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []application.SLAClock{}
	for rows.Next() {
		var c clockRow
		var policy, calendar string
		err := rows.Scan(&c.TicketID, &c.Kind, &c.Status, &c.PolicyID, &c.PolicyVersion, &c.TargetMinutes, &c.TimeBasis, &c.CalendarID, &c.WarningPercent, &c.StartedAt, &c.ElapsedSeconds, &c.RunningSince, &c.DueAt, &c.WarnAt, &c.PausedAt, &c.PauseReason, &c.CompletedAt, &c.EndReason, &c.WarnedAt, &c.BreachedAt, &c.Version, &policy, &calendar)
		if err != nil {
			return nil, err
		}
		outcome := ""
		switch {
		case c.Status == "cancelled" && c.EndReason != nil && *c.EndReason == "exempted":
			outcome = "exempted"
		case c.Status == "cancelled":
			outcome = "not_measured"
		case c.BreachedAt != nil:
			outcome = "breached"
		case c.Status == "completed":
			outcome = "met"
		}
		remaining := int64(c.TargetMinutes)*60 - c.ElapsedSeconds
		if c.Status == "running" {
			n, e := r.readElapsed(ctx, c, time.Now().UTC())
			if e != nil {
				return nil, e
			}
			remaining -= n
		}
		out = append(out, application.SLAClock{Kind: c.Kind, Status: c.Status, Outcome: outcome, TargetMinutes: c.TargetMinutes, TimeBasis: c.TimeBasis, CalendarName: calendar, PolicyName: policy, DueAt: c.DueAt, RemainingSeconds: remaining, PausedAt: c.PausedAt, PauseReason: c.PauseReason, CompletedAt: c.CompletedAt, WarnedAt: c.WarnedAt, BreachedAt: c.BreachedAt})
	}
	return out, rows.Err()
}

func (r *Repository) readElapsed(ctx context.Context, c clockRow, at time.Time) (int64, error) {
	if c.TimeBasis == "continuous" {
		return int64(at.Sub(*c.RunningSince).Seconds()), nil
	}
	cal, err := loadCalendar(ctx, r.pool, *c.CalendarID)
	if err != nil {
		return 0, err
	}
	d, err := businessDuration(cal, *c.RunningSince, at)
	return int64(d.Seconds()), err
}

// RecomputeCalendarClocksTx is invoked after a calendar's hours or closures change. Tickets are
// locked before their clocks, matching the ordering of every Ticket operation.
func (r *Repository) RecomputeCalendarClocksTx(ctx context.Context, tx pgx.Tx, calendarID string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ticket_id::text FROM servicedesk.ticket_sla_clocks WHERE calendar_id=$1 AND status='running' ORDER BY ticket_id::text`, calendarID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		var locked int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM servicedesk.tickets WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
			return err
		}
		cs, err := tx.Query(ctx, `SELECT `+clockColumns+` FROM servicedesk.ticket_sla_clocks WHERE ticket_id=$1 AND calendar_id=$2 AND status='running' ORDER BY kind FOR UPDATE`, id, calendarID)
		if err != nil {
			return err
		}
		clocks := []clockRow{}
		for cs.Next() {
			c, e := scanClock(cs)
			if e != nil {
				cs.Close()
				return e
			}
			clocks = append(clocks, c)
		}
		err = cs.Err()
		cs.Close()
		if err != nil {
			return err
		}
		for _, c := range clocks {
			if err := setDue(ctx, tx, &c, *c.RunningSince); err != nil {
				return err
			}
			if err := r.persistClock(ctx, tx, c, false); err != nil {
				return err
			}
		}
		if len(clocks) > 0 {
			if err := r.projectSLATx(ctx, tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}
