// Package repository implements the Time Tracking store on PostgreSQL with explicit SQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
)

// Repository stores settings and time entries.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

// ---- Settings --------------------------------------------------------------------------------------------

const settingsCols = `codetermination_basis, codetermination_recorded_on::text, dpia_recorded_on::text, edit_window_days,
	retention_months, show_entry_authors, min_report_group_size, version, updated_by::text, updated_at`

func scanSettings(row pgx.Row) (application.Settings, error) {
	var s application.Settings
	var edit, ret, group int16
	err := row.Scan(&s.CodeterminationBasis, &s.CodeterminationRecordedOn, &s.DPIARecordedOn, &edit, &ret, &s.ShowEntryAuthors, &group,
		&s.Version, &s.UpdatedBy, &s.UpdatedAt)
	s.EditWindowDays, s.RetentionMonths, s.MinReportGroupSize = int(edit), int(ret), int(group)
	return s, err
}

func (r *Repository) GetSettings(ctx context.Context) (application.Settings, error) {
	s, err := scanSettings(r.pool.QueryRow(ctx, `SELECT `+settingsCols+` FROM timetracking.settings WHERE singleton`))
	if err != nil {
		return application.Settings{}, fmt.Errorf("get timetracking settings: %w", err)
	}
	return s, nil
}

func (r *Repository) LockSettingsTx(ctx context.Context, tx pgx.Tx) (application.Settings, error) {
	s, err := scanSettings(tx.QueryRow(ctx, `SELECT `+settingsCols+` FROM timetracking.settings WHERE singleton FOR UPDATE`))
	if err != nil {
		return application.Settings{}, fmt.Errorf("lock timetracking settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettingsTx(ctx context.Context, tx pgx.Tx, s application.Settings) (application.Settings, error) {
	out, err := scanSettings(tx.QueryRow(ctx, `
		UPDATE timetracking.settings SET codetermination_basis = $1, codetermination_recorded_on = $2::date, dpia_recorded_on = $3::date,
			edit_window_days = $4, retention_months = $5, show_entry_authors = $6, min_report_group_size = $7,
			updated_by = $8::uuid, updated_at = now(), version = version + 1
		WHERE singleton RETURNING `+settingsCols,
		s.CodeterminationBasis, s.CodeterminationRecordedOn, s.DPIARecordedOn, s.EditWindowDays, s.RetentionMonths, s.ShowEntryAuthors,
		s.MinReportGroupSize, s.UpdatedBy))
	if err != nil {
		return application.Settings{}, fmt.Errorf("update timetracking settings: %w", err)
	}
	return out, nil
}

// ---- Entries ---------------------------------------------------------------------------------------------

const entryCols = `id::text, subject_type, subject_id::text, queue_id::text, user_id::text, work_date::text, duration_minutes,
	started_at, source, note, created_by::text, created_at, updated_at, version`

func scanEntry(row pgx.Row) (application.Entry, error) {
	var e application.Entry
	err := row.Scan(&e.ID, &e.SubjectType, &e.SubjectID, &e.QueueID, &e.UserID, &e.WorkDate, &e.DurationMinutes, &e.StartedAt, &e.Source,
		&e.Note, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.Version)
	return e, err
}

func (r *Repository) LockPersonDateTx(ctx context.Context, tx pgx.Tx, userID, workDate string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('timetracking.day:' || $1::text || ':' || $2::text, 0))`, userID, workDate); err != nil {
		return fmt.Errorf("lock person and date: %w", err)
	}
	return nil
}

func (r *Repository) DayMinutesTx(ctx context.Context, tx pgx.Tx, userID, workDate, excludeID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(duration_minutes), 0)::int FROM timetracking.time_entries
		WHERE user_id = $1::uuid AND work_date = $2::date AND deleted_at IS NULL AND ($3::text = '' OR id::text <> $3::text)`,
		userID, workDate, excludeID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sum day minutes: %w", err)
	}
	return n, nil
}

func (r *Repository) InsertEntryTx(ctx context.Context, tx pgx.Tx, e application.Entry) (application.Entry, error) {
	out, err := scanEntry(tx.QueryRow(ctx, `
		INSERT INTO timetracking.time_entries (subject_type, subject_id, queue_id, user_id, work_date, duration_minutes, started_at, source, note, created_by)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::date, $6, $7, $8, $9, $10::uuid)
		RETURNING `+entryCols,
		e.SubjectType, e.SubjectID, e.QueueID, e.UserID, e.WorkDate, e.DurationMinutes, e.StartedAt, e.Source, e.Note, e.CreatedBy))
	if err != nil {
		return application.Entry{}, fmt.Errorf("insert time entry: %w", err)
	}
	return out, nil
}

func (r *Repository) LockEntryTx(ctx context.Context, tx pgx.Tx, id string) (application.Entry, error) {
	e, err := scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM timetracking.time_entries WHERE id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Entry{}, application.ErrNotFound
	}
	if err != nil {
		return application.Entry{}, fmt.Errorf("lock time entry: %w", err)
	}
	return e, nil
}

func (r *Repository) UpdateEntryTx(ctx context.Context, tx pgx.Tx, e application.Entry) (application.Entry, error) {
	out, err := scanEntry(tx.QueryRow(ctx, `
		UPDATE timetracking.time_entries SET subject_id = $2::uuid, queue_id = $3::uuid, work_date = $4::date, duration_minutes = $5,
			note = $6, updated_at = now(), version = version + 1
		WHERE id = $1::uuid AND deleted_at IS NULL RETURNING `+entryCols,
		e.ID, e.SubjectID, e.QueueID, e.WorkDate, e.DurationMinutes, e.Note))
	if err != nil {
		return application.Entry{}, fmt.Errorf("update time entry: %w", err)
	}
	return out, nil
}

func (r *Repository) SoftDeleteTx(ctx context.Context, tx pgx.Tx, id, deletedBy, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE timetracking.time_entries SET deleted_at = now(), deleted_by = $2::uuid, delete_reason = $3, updated_at = now(), version = version + 1
		WHERE id = $1::uuid AND deleted_at IS NULL`, id, deletedBy, reason)
	if err != nil {
		return fmt.Errorf("delete time entry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrNotFound
	}
	return nil
}

func (r *Repository) SubjectTotal(ctx context.Context, subjectType, subjectID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(duration_minutes), 0)::int FROM timetracking.time_entries
		WHERE subject_type = $1 AND subject_id = $2::uuid AND deleted_at IS NULL`, subjectType, subjectID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sum subject minutes: %w", err)
	}
	return n, nil
}

func (r *Repository) list(ctx context.Context, query string, args ...any) ([]application.Entry, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list time entries: %w", err)
	}
	defer rows.Close()
	out := []application.Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan time entry: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListBySubject returns the non-deleted entries of one subject, newest work date first.
func (r *Repository) ListBySubject(ctx context.Context, subjectType, subjectID string) ([]application.Entry, error) {
	return r.list(ctx, `SELECT `+entryCols+` FROM timetracking.time_entries
		WHERE subject_type = $1 AND subject_id = $2::uuid AND deleted_at IS NULL
		ORDER BY work_date DESC, created_at DESC, id DESC LIMIT 500`, subjectType, subjectID)
}

// ListMine returns one person's non-deleted entries between two dates (inclusive), newest first.
func (r *Repository) ListMine(ctx context.Context, userID, from, to string, limit int) ([]application.Entry, error) {
	return r.list(ctx, `SELECT `+entryCols+` FROM timetracking.time_entries
		WHERE user_id = $1::uuid AND work_date BETWEEN $2::date AND $3::date AND deleted_at IS NULL
		ORDER BY work_date DESC, created_at DESC, id DESC LIMIT $4`, userID, from, to, limit)
}

func (r *Repository) PurgeTx(ctx context.Context, tx pgx.Tx, workDateBefore string, deletedBefore, timersBefore time.Time) (application.PurgeCounts, error) {
	var c application.PurgeCounts
	tag, err := tx.Exec(ctx, `DELETE FROM timetracking.time_entries WHERE work_date < $1::date`, workDateBefore)
	if err != nil {
		return c, fmt.Errorf("purge time entries: %w", err)
	}
	c.Entries = int(tag.RowsAffected())
	if tag, err = tx.Exec(ctx, `DELETE FROM timetracking.time_entries WHERE deleted_at IS NOT NULL AND deleted_at < $1`, deletedBefore); err != nil {
		return c, fmt.Errorf("purge deleted time entries: %w", err)
	}
	c.Deleted = int(tag.RowsAffected())
	if tag, err = tx.Exec(ctx, `DELETE FROM timetracking.timers WHERE started_at < $1`, timersBefore); err != nil {
		return c, fmt.Errorf("purge timers: %w", err)
	}
	c.Timers = int(tag.RowsAffected())
	return c, nil
}

func (r *Repository) NewID(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", fmt.Errorf("new id: %w", err)
	}
	return id, nil
}

// ---- Timers ----------------------------------------------------------------------------------------------

func (r *Repository) StartTimer(ctx context.Context, t application.Timer) (application.Timer, error) {
	var out application.Timer
	err := r.pool.QueryRow(ctx, `
		INSERT INTO timetracking.timers (user_id, subject_type, subject_id, note) VALUES ($1::uuid, $2, $3::uuid, $4)
		ON CONFLICT (user_id) DO NOTHING
		RETURNING user_id::text, subject_type, subject_id::text, started_at, note`,
		t.UserID, t.SubjectType, t.SubjectID, t.Note).Scan(&out.UserID, &out.SubjectType, &out.SubjectID, &out.StartedAt, &out.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Timer{}, application.ErrTimerRunning
	}
	if err != nil {
		return application.Timer{}, fmt.Errorf("start timer: %w", err)
	}
	return out, nil
}

const timerCols = `user_id::text, subject_type, subject_id::text, started_at, note`

func (r *Repository) GetTimer(ctx context.Context, userID string) (application.Timer, error) {
	var t application.Timer
	err := r.pool.QueryRow(ctx, `SELECT `+timerCols+` FROM timetracking.timers WHERE user_id = $1::uuid`, userID).
		Scan(&t.UserID, &t.SubjectType, &t.SubjectID, &t.StartedAt, &t.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Timer{}, application.ErrNotFound
	}
	if err != nil {
		return application.Timer{}, fmt.Errorf("get timer: %w", err)
	}
	return t, nil
}

func (r *Repository) LockTimerTx(ctx context.Context, tx pgx.Tx, userID string) (application.Timer, error) {
	var t application.Timer
	err := tx.QueryRow(ctx, `SELECT `+timerCols+` FROM timetracking.timers WHERE user_id = $1::uuid FOR UPDATE`, userID).
		Scan(&t.UserID, &t.SubjectType, &t.SubjectID, &t.StartedAt, &t.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Timer{}, application.ErrNotFound
	}
	if err != nil {
		return application.Timer{}, fmt.Errorf("lock timer: %w", err)
	}
	return t, nil
}

func (r *Repository) DeleteTimerTx(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM timetracking.timers WHERE user_id = $1::uuid`, userID); err != nil {
		return fmt.Errorf("delete timer: %w", err)
	}
	return nil
}

func (r *Repository) DeleteTimer(ctx context.Context, userID string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM timetracking.timers WHERE user_id = $1::uuid`, userID); err != nil {
		return fmt.Errorf("delete timer: %w", err)
	}
	return nil
}

// ---- Report ----------------------------------------------------------------------------------------------

func (r *Repository) Summary(ctx context.Context, from, to, groupBy string, queueIDs []string) ([]application.SummaryRow, error) {
	var key string
	switch groupBy {
	case application.GroupQueue:
		key = "queue_id::text"
	case application.GroupMonth:
		key = "to_char(work_date, 'YYYY-MM')"
	case application.GroupTicket:
		key = "subject_id::text"
	default:
		return nil, fmt.Errorf("summary: unsupported grouping %q", groupBy)
	}
	args := []any{from, to}
	scope := ""
	if queueIDs != nil {
		args = append(args, queueIDs)
		scope = " AND queue_id = ANY($3::uuid[])"
	}
	rows, err := r.pool.Query(ctx, `SELECT `+key+` AS k, sum(duration_minutes)::int, count(*)::int, count(DISTINCT user_id)::int
		FROM timetracking.time_entries
		WHERE deleted_at IS NULL AND work_date BETWEEN $1::date AND $2::date`+scope+`
		GROUP BY k ORDER BY sum(duration_minutes) DESC, k LIMIT 1000`, args...)
	if err != nil {
		return nil, fmt.Errorf("time summary: %w", err)
	}
	defer rows.Close()
	var out []application.SummaryRow
	for rows.Next() {
		var row application.SummaryRow
		if err := rows.Scan(&row.Key, &row.Minutes, &row.Entries, &row.Contributors); err != nil {
			return nil, fmt.Errorf("scan time summary: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
