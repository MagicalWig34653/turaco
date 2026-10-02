package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Definitions stores Recurring Task Definitions and generates their tasks.
type Definitions struct{ pool *pgxpool.Pool }

var _ application.DefinitionStore = (*Definitions)(nil)

// NewDefinitions creates the definition store.
func NewDefinitions(pool *pgxpool.Pool) *Definitions { return &Definitions{pool: pool} }

const definitionTarget = "recurring_task_definition"

const definitionColumns = `id::text, title, description, priority, assigned_user_id::text, assigned_team_id::text,
	due_after_hours, frequency, interval_count, coalesce(weekday, 0), coalesce(day_of_month, 0), time_of_day, timezone,
	to_char(starts_on, 'YYYY-MM-DD'), active, next_run_at, last_generated_at, created_by_user_id::text,
	version, created_at, updated_at`

func scanDefinition(row pgx.Row) (application.Definition, error) {
	var d application.Definition
	err := row.Scan(&d.ID, &d.Title, &d.Description, &d.Priority, &d.AssignedUserID, &d.AssignedTeamID,
		&d.DueAfterHours, &d.Rule.Frequency, &d.Rule.Interval, &d.Rule.Weekday, &d.Rule.DayOfMonth, &d.Rule.TimeOfDay,
		&d.Rule.Timezone, &d.Rule.StartsOn, &d.Active, &d.NextRunAt, &d.LastGeneratedAt, &d.CreatedByUserID,
		&d.Version, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// definitionAuditState never contains the title or description.
func definitionAuditState(d application.Definition) map[string]any {
	return map[string]any{
		"active": d.Active, "priority": d.Priority, "assignedUserId": d.AssignedUserID, "assignedTeamId": d.AssignedTeamID,
		"dueAfterHours": d.DueAfterHours, "rule": d.Rule, "nextRunAt": d.NextRunAt, "version": d.Version,
	}
}

func nullInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// Insert creates an active definition with its first run scheduled.
func (r *Definitions) Insert(ctx context.Context, c application.Caller, n application.NewDefinition) (application.Definition, error) {
	var out application.Definition
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDefinition(tx.QueryRow(ctx, `
			INSERT INTO platform.recurring_task_definitions(
				title, description, priority, assigned_user_id, assigned_team_id, due_after_hours,
				frequency, interval_count, weekday, day_of_month, time_of_day, timezone, starts_on,
				next_run_at, created_by_user_id)
			VALUES ($1, $2, $3, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13::date, $14, $15::uuid)
			RETURNING `+definitionColumns,
			n.Title, n.Description, n.Priority, n.AssignedUserID, n.AssignedTeamID, n.DueAfterHours,
			n.Rule.Frequency, n.Rule.Interval, nullInt(n.Rule.Weekday), nullInt(n.Rule.DayOfMonth), n.Rule.TimeOfDay,
			n.Rule.Timezone, n.Rule.StartsOn, n.NextRunAt, n.CreatedBy))
		if err != nil {
			return fmt.Errorf("insert definition: %w", err)
		}
		return recordDefinition(ctx, tx, c, "tasks.recurrence.created", out.ID, nil, definitionAuditState(out), nil)
	})
	return out, err
}

func recordDefinition(ctx context.Context, tx pgx.Tx, c application.Caller, action, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: definitionTarget, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func (r *Definitions) Get(ctx context.Context, id string) (application.Definition, error) {
	if !validUUID(id) {
		return application.Definition{}, application.ErrNotFound
	}
	d, err := scanDefinition(r.pool.QueryRow(ctx, `SELECT `+definitionColumns+` FROM platform.recurring_task_definitions WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Definition{}, application.ErrNotFound
	}
	if err != nil {
		return application.Definition{}, fmt.Errorf("get definition: %w", err)
	}
	return d, nil
}

func (r *Definitions) List(ctx context.Context, p application.Page) (application.Result[application.Definition], error) {
	p = p.Normalize()
	args := []any{p.Limit + 1}
	cursor := ""
	if p.Cursor != "" {
		if !validUUID(p.Cursor) {
			return application.Result[application.Definition]{}, application.ErrInvalidCursor
		}
		args = append(args, p.Cursor)
		cursor = " WHERE id > $2::uuid"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+definitionColumns+` FROM platform.recurring_task_definitions`+cursor+` ORDER BY id LIMIT $1`, args...)
	if err != nil {
		return application.Result[application.Definition]{}, fmt.Errorf("list definitions: %w", err)
	}
	defer rows.Close()
	items := make([]application.Definition, 0, p.Limit+1)
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return application.Result[application.Definition]{}, fmt.Errorf("list definitions: scan: %w", err)
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Definition]{}, fmt.Errorf("list definitions: %w", err)
	}
	res := application.Result[application.Definition]{Items: items}
	if len(items) > p.Limit {
		res.Items = items[:p.Limit]
		res.NextCursor = res.Items[p.Limit-1].ID
	}
	return res, nil
}

func (r *Definitions) Change(ctx context.Context, c application.Caller, id string, decide func(application.Definition) (application.DefinitionChange, error)) (application.Definition, error) {
	if !validUUID(id) {
		return application.Definition{}, application.ErrNotFound
	}
	var out application.Definition
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scanDefinition(tx.QueryRow(ctx, `SELECT `+definitionColumns+` FROM platform.recurring_task_definitions WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock definition: %w", err)
		}
		ch, err := decide(cur)
		if err != nil {
			return err
		}
		if ch.NoChange {
			out = cur
			return nil
		}
		n := ch.Next
		out, err = scanDefinition(tx.QueryRow(ctx, `
			UPDATE platform.recurring_task_definitions SET
				title = $2, description = $3, priority = $4, assigned_user_id = $5::uuid, assigned_team_id = $6::uuid,
				due_after_hours = $7, frequency = $8, interval_count = $9, weekday = $10, day_of_month = $11,
				time_of_day = $12, timezone = $13, starts_on = $14::date, active = $15, next_run_at = $16,
				version = version + 1, updated_at = now()
			WHERE id = $1::uuid
			RETURNING `+definitionColumns,
			id, n.Title, n.Description, n.Priority, n.AssignedUserID, n.AssignedTeamID, n.DueAfterHours,
			n.Rule.Frequency, n.Rule.Interval, nullInt(n.Rule.Weekday), nullInt(n.Rule.DayOfMonth), n.Rule.TimeOfDay,
			n.Rule.Timezone, n.Rule.StartsOn, n.Active, n.NextRunAt))
		if err != nil {
			return fmt.Errorf("update definition: %w", err)
		}
		return recordDefinition(ctx, tx, c, ch.Action, id, definitionAuditState(cur), definitionAuditState(out), ch.Metadata)
	})
	if err != nil {
		return application.Definition{}, err
	}
	return out, nil
}

func (r *Definitions) Delete(ctx context.Context, c application.Caller, id string, expected *int) error {
	if !validUUID(id) {
		return application.ErrNotFound
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scanDefinition(tx.QueryRow(ctx, `SELECT `+definitionColumns+` FROM platform.recurring_task_definitions WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock definition: %w", err)
		}
		if expected != nil && *expected != cur.Version {
			return application.ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.recurring_task_definitions WHERE id = $1::uuid`, id); err != nil {
			return fmt.Errorf("delete definition: %w", err)
		}
		return recordDefinition(ctx, tx, c, "tasks.recurrence.deleted", id, definitionAuditState(cur), nil, nil)
	})
}

func (r *Definitions) GenerateDue(ctx context.Context, now time.Time, plan func(application.Definition) (application.Generation, error)) (found, generated bool, err error) {
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		def, err := scanDefinition(tx.QueryRow(ctx, `
			SELECT `+definitionColumns+` FROM platform.recurring_task_definitions
			WHERE active AND next_run_at <= $1
			ORDER BY next_run_at, id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim due definition: %w", err)
		}
		found = true
		gen, err := plan(def)
		if err != nil {
			return err
		}
		scheduledFor := *def.NextRunAt
		var dueAt *time.Time
		if def.DueAfterHours != nil {
			d := scheduledFor.Add(time.Duration(*def.DueAfterHours) * time.Hour)
			dueAt = &d
		}
		var taskID string
		err = tx.QueryRow(ctx, `
			INSERT INTO platform.tasks(title, description, priority, due_at, assigned_user_id, assigned_team_id,
			                           recurrence_definition_id, scheduled_for)
			VALUES ($1, $2, $3, $4, $5::uuid, $6::uuid, $7::uuid, $8)
			ON CONFLICT (recurrence_definition_id, scheduled_for) WHERE recurrence_definition_id IS NOT NULL DO NOTHING
			RETURNING id::text`,
			def.Title, def.Description, def.Priority, dueAt, gen.AssignedUserID, gen.AssignedTeamID, def.ID, scheduledFor).Scan(&taskID)
		inserted := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert generated task: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE platform.recurring_task_definitions
			SET next_run_at = $2, last_generated_at = $3, version = version + 1, updated_at = now()
			WHERE id = $1::uuid`, def.ID, gen.NextRunAt, now); err != nil {
			return fmt.Errorf("advance definition: %w", err)
		}
		if !inserted {
			return nil // this run was already generated; only the schedule moved on
		}
		generated = true
		c := application.Caller{
			Actor:         audit.SystemActor("recurrence"),
			CorrelationID: fmt.Sprintf("recurrence:%s:%d", def.ID, scheduledFor.Unix()),
		}
		meta := map[string]any{"recurrenceDefinitionId": def.ID, "scheduledFor": scheduledFor.UTC().Format(time.RFC3339)}
		if gen.AssigneeDropped {
			meta["assigneeDropped"] = true
		}
		task := application.Task{
			ID: taskID, Status: application.StatusOpen, Priority: def.Priority, DueAt: dueAt,
			AssignedUserID: gen.AssignedUserID, AssignedTeamID: gen.AssignedTeamID, Version: 1,
		}
		if err := record(ctx, tx, c, "tasks.task.created", taskID, nil, auditState(task), meta); err != nil {
			return err
		}
		if gen.AssignedUserID != nil || gen.AssignedTeamID != nil {
			return publish(ctx, tx, c, application.Event{Type: "TaskAssigned", Payload: map[string]any{
				"taskId": taskID, "assignedUserId": gen.AssignedUserID, "assignedTeamId": gen.AssignedTeamID,
				"previousUserId": nil, "previousTeamId": nil,
			}})
		}
		return nil
	})
	return found, generated, err
}
