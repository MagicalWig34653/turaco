// Package repository implements the Presence store on PostgreSQL.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
)

// Repository stores settings, entries and Team minimums.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F'):
			return false
		}
	}
	return true
}

func validIDs(in []string) []string {
	out := []string{}
	for _, s := range in {
		if validUUID(s) {
			out = append(out, s)
		}
	}
	return out
}

// ---- Settings --------------------------------------------------------------------------------------------

const settingsCols = `enabled, dpia_recorded_on::text, council_confirmed_on::text, retention_days, external_sources_enabled,
	disabled_at, updated_by::text, updated_at, version`

func scanSettings(row pgx.Row) (application.Settings, error) {
	var s application.Settings
	var days int16
	err := row.Scan(&s.Enabled, &s.DPIARecordedOn, &s.CouncilConfirmedOn, &days, &s.ExternalSourcesEnabled, &s.DisabledAt, &s.UpdatedBy,
		&s.UpdatedAt, &s.Version)
	s.RetentionDays = int(days)
	return s, err
}

func (r *Repository) GetSettings(ctx context.Context) (application.Settings, error) {
	s, err := scanSettings(r.pool.QueryRow(ctx, `SELECT `+settingsCols+` FROM presence.settings WHERE singleton`))
	if err != nil {
		return application.Settings{}, fmt.Errorf("get presence settings: %w", err)
	}
	return s, nil
}

func (r *Repository) LockSettingsTx(ctx context.Context, tx pgx.Tx) (application.Settings, error) {
	s, err := scanSettings(tx.QueryRow(ctx, `SELECT `+settingsCols+` FROM presence.settings WHERE singleton FOR UPDATE`))
	if err != nil {
		return application.Settings{}, fmt.Errorf("lock presence settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettingsTx(ctx context.Context, tx pgx.Tx, s application.Settings) (application.Settings, error) {
	out, err := scanSettings(tx.QueryRow(ctx, `
		UPDATE presence.settings SET enabled = $1, dpia_recorded_on = $2::date, council_confirmed_on = $3::date, retention_days = $4,
			external_sources_enabled = $5, disabled_at = $6, updated_by = $7::uuid, updated_at = now(), version = version + 1
		WHERE singleton RETURNING `+settingsCols,
		s.Enabled, s.DPIARecordedOn, s.CouncilConfirmedOn, s.RetentionDays, s.ExternalSourcesEnabled, s.DisabledAt, s.UpdatedBy))
	if err != nil {
		return application.Settings{}, fmt.Errorf("update presence settings: %w", err)
	}
	return out, nil
}

// ---- Entries ---------------------------------------------------------------------------------------------

const entryCols = `id::text, user_id::text, kind, location_type, location_id::text, starts_at, ends_at, all_day, recurrence,
	source, source_ref, status, observed_from, observed_to, observed_at, visibility, ended_at, created_by::text, created_at,
	updated_at, cancelled_at, version`

func scanEntry(row pgx.Row) (application.Entry, error) {
	var e application.Entry
	var rec []byte
	err := row.Scan(&e.ID, &e.UserID, &e.Kind, &e.LocationType, &e.LocationID, &e.StartsAt, &e.EndsAt, &e.AllDay, &rec,
		&e.Source, &e.SourceRef, &e.Status, &e.ObservedFrom, &e.ObservedTo, &e.ObservedAt, &e.Visibility, &e.EndedAt, &e.CreatedBy,
		&e.CreatedAt, &e.UpdatedAt, &e.CancelledAt, &e.Version)
	if err != nil {
		return e, err
	}
	if rec != nil {
		e.Recurrence = &application.Recurrence{}
		if err := json.Unmarshal(rec, e.Recurrence); err != nil {
			return e, fmt.Errorf("decode recurrence: %w", err)
		}
	}
	return e, nil
}

func recurrenceJSON(e application.Entry) ([]byte, error) {
	if e.Recurrence == nil {
		return nil, nil
	}
	return json.Marshal(e.Recurrence)
}

func (r *Repository) InsertEntryTx(ctx context.Context, tx pgx.Tx, e application.Entry) (application.Entry, error) {
	rec, err := recurrenceJSON(e)
	if err != nil {
		return application.Entry{}, err
	}
	out, err := scanEntry(tx.QueryRow(ctx, `
		INSERT INTO presence.entries(user_id, kind, location_type, location_id, starts_at, ends_at, all_day, recurrence, source,
			status, visibility, ended_at, created_by)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13::uuid) RETURNING `+entryCols,
		e.UserID, e.Kind, e.LocationType, e.LocationID, e.StartsAt, e.EndsAt, e.AllDay, rec, e.Source, e.Status, e.Visibility, e.EndedAt, e.CreatedBy))
	if err != nil {
		return application.Entry{}, fmt.Errorf("insert presence entry: %w", err)
	}
	return out, nil
}

func (r *Repository) LockEntryTx(ctx context.Context, tx pgx.Tx, id string) (application.Entry, error) {
	if !validUUID(id) {
		return application.Entry{}, application.ErrNotFound
	}
	e, err := scanEntry(tx.QueryRow(ctx, `SELECT `+entryCols+` FROM presence.entries WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Entry{}, application.ErrNotFound
	}
	if err != nil {
		return application.Entry{}, fmt.Errorf("lock presence entry: %w", err)
	}
	return e, nil
}

func (r *Repository) UpdateEntryTx(ctx context.Context, tx pgx.Tx, e application.Entry) (application.Entry, error) {
	rec, err := recurrenceJSON(e)
	if err != nil {
		return application.Entry{}, err
	}
	out, err := scanEntry(tx.QueryRow(ctx, `
		UPDATE presence.entries SET location_type = $2, location_id = $3::uuid, starts_at = $4, ends_at = $5, all_day = $6,
			recurrence = $7::jsonb, status = $8, ended_at = $9, cancelled_at = $10, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+entryCols,
		e.ID, e.LocationType, e.LocationID, e.StartsAt, e.EndsAt, e.AllDay, rec, e.Status, e.EndedAt, e.CancelledAt))
	if err != nil {
		return application.Entry{}, fmt.Errorf("update presence entry: %w", err)
	}
	return out, nil
}

func (r *Repository) CountActiveEntriesTx(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	var n int
	// The subject's entries are serialized so concurrent creates cannot pass the cap together.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('presence.entries:' || $1, 0))`, userID); err != nil {
		return 0, fmt.Errorf("lock presence entries: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM presence.entries WHERE user_id = $1::uuid AND status = 'active' AND observed_to IS NULL
		AND source = 'manual' AND ended_at > now()`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count presence entries: %w", err)
	}
	return n, nil
}

// EntriesInWindow applies the viewer-scope filter in SQL: a row is returned only when its User is in both
// userIDs and scope, so a missing check in the application cannot leak rows.
func (r *Repository) EntriesInWindow(ctx context.Context, scope, userIDs []string, from, to time.Time) ([]application.Entry, error) {
	scope, userIDs = validIDs(scope), validIDs(userIDs)
	if len(scope) == 0 || len(userIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+entryCols+` FROM presence.entries
		WHERE user_id = ANY($1::text[]::uuid[]) AND user_id = ANY($2::text[]::uuid[])
		  AND status = 'active' AND observed_to IS NULL AND starts_at < $4 AND ended_at > $3
		ORDER BY user_id, starts_at LIMIT 5000`, userIDs, scope, from, to)
	if err != nil {
		return nil, fmt.Errorf("list presence entries: %w", err)
	}
	defer rows.Close()
	var out []application.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("list presence entries: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Minimums --------------------------------------------------------------------------------------------

const minCols = `team_id::text, minimum, onsite_minimum, location_id::text, updated_by::text, updated_at, version`

func scanMinimum(row pgx.Row) (application.Minimum, error) {
	var m application.Minimum
	var min int16
	var on *int16
	err := row.Scan(&m.TeamID, &min, &on, &m.LocationID, &m.UpdatedBy, &m.UpdatedAt, &m.Version)
	m.Minimum = int(min)
	if on != nil {
		v := int(*on)
		m.OnsiteMinimum = &v
	}
	return m, err
}

func (r *Repository) GetMinimum(ctx context.Context, teamID string) (application.Minimum, bool, error) {
	if !validUUID(teamID) {
		return application.Minimum{}, false, nil
	}
	m, err := scanMinimum(r.pool.QueryRow(ctx, `SELECT `+minCols+` FROM presence.team_coverage_minimums WHERE team_id = $1::uuid`, teamID))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Minimum{}, false, nil
	}
	if err != nil {
		return application.Minimum{}, false, fmt.Errorf("get presence minimum: %w", err)
	}
	return m, true, nil
}

func (r *Repository) UpsertMinimumTx(ctx context.Context, tx pgx.Tx, m application.Minimum, expectedVersion *int) (application.Minimum, error) {
	var cur int
	err := tx.QueryRow(ctx, `SELECT version FROM presence.team_coverage_minimums WHERE team_id = $1::uuid FOR UPDATE`, m.TeamID).Scan(&cur)
	exists := true
	if errors.Is(err, pgx.ErrNoRows) {
		exists = false
	} else if err != nil {
		return application.Minimum{}, fmt.Errorf("lock presence minimum: %w", err)
	}
	if exists {
		if expectedVersion == nil || *expectedVersion != cur {
			return application.Minimum{}, application.ErrVersionConflict
		}
		out, err := scanMinimum(tx.QueryRow(ctx, `UPDATE presence.team_coverage_minimums SET minimum = $2, onsite_minimum = $3,
			location_id = $4::uuid, updated_by = $5::uuid, updated_at = now(), version = version + 1 WHERE team_id = $1::uuid RETURNING `+minCols,
			m.TeamID, m.Minimum, m.OnsiteMinimum, m.LocationID, m.UpdatedBy))
		if err != nil {
			return application.Minimum{}, fmt.Errorf("update presence minimum: %w", err)
		}
		return out, nil
	}
	if expectedVersion != nil && *expectedVersion != 0 {
		return application.Minimum{}, application.ErrVersionConflict
	}
	out, err := scanMinimum(tx.QueryRow(ctx, `INSERT INTO presence.team_coverage_minimums(team_id, minimum, onsite_minimum, location_id, updated_by)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid) ON CONFLICT (team_id) DO NOTHING RETURNING `+minCols,
		m.TeamID, m.Minimum, m.OnsiteMinimum, m.LocationID, m.UpdatedBy))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Minimum{}, application.ErrVersionConflict
	}
	if err != nil {
		return application.Minimum{}, fmt.Errorf("insert presence minimum: %w", err)
	}
	return out, nil
}

// ---- Retention -------------------------------------------------------------------------------------------

func (r *Repository) PurgeTx(ctx context.Context, tx pgx.Tx, cutoff time.Time, everything bool) (application.PurgeCounts, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT presence.purge_entries($1, $2)`, cutoff, everything).Scan(&n); err != nil {
		return application.PurgeCounts{}, fmt.Errorf("purge presence entries: %w", err)
	}
	return application.PurgeCounts{Entries: n}, nil
}

func (r *Repository) NewID(ctx context.Context, tx pgx.Tx) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", fmt.Errorf("new id: %w", err)
	}
	return id, nil
}
