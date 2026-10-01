package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// ListUserExternalIdentities returns the directory accounts of a user. An
// unknown user yields ErrNotFound.
func (r *Repository) ListUserExternalIdentities(ctx context.Context, userID string) ([]application.ExternalIdentity, error) {
	if err := r.exists(ctx, `SELECT 1 FROM organization.users WHERE id = $1`, userID); err != nil {
		return nil, err
	}
	u, _ := parseID(userID)
	rows, err := r.pool.Query(ctx, `
		SELECT provider_key, username, enabled, last_seen_at, deleted_observed_at
		FROM organization.external_identities WHERE user_id = $1
		ORDER BY provider_key, created_at, id`, u)
	if err != nil {
		return nil, fmt.Errorf("list external identities: %w", err)
	}
	defer rows.Close()
	out := []application.ExternalIdentity{}
	for rows.Next() {
		var e application.ExternalIdentity
		if err := rows.Scan(&e.ProviderKey, &e.Username, &e.Enabled, &e.LastSeenAt, &e.DeletedObservedAt); err != nil {
			return nil, fmt.Errorf("scan external identity: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate external identities: %w", err)
	}
	return out, nil
}

const runColumns = `id::text, provider_key, job_id::text, trigger, started_at, observed_at, finished_at, outcome,
	counts, conflicts, conflict_count, error`

func scanRun(row pgx.Row) (application.DirectorySyncRun, error) {
	var run application.DirectorySyncRun
	var counts, conflicts []byte
	if err := row.Scan(&run.ID, &run.ProviderKey, &run.JobID, &run.Trigger, &run.StartedAt, &run.ObservedAt, &run.FinishedAt,
		&run.Outcome, &counts, &conflicts, &run.ConflictCount, &run.Error); err != nil {
		return run, err
	}
	run.Counts = map[string]int{}
	if err := json.Unmarshal(counts, &run.Counts); err != nil {
		return run, fmt.Errorf("decode run counts: %w", err)
	}
	var stored []syncConflict
	if err := json.Unmarshal(conflicts, &stored); err != nil {
		return run, fmt.Errorf("decode run conflicts: %w", err)
	}
	run.Conflicts = make([]application.SyncConflict, 0, len(stored))
	for _, c := range stored {
		run.Conflicts = append(run.Conflicts, application.SyncConflict{Kind: c.Kind, ExternalID: c.ExternalID, Username: c.Username})
	}
	return run, nil
}

// ListDirectorySyncRuns pages runs newest first (descending UUIDv7 id).
func (r *Repository) ListDirectorySyncRuns(ctx context.Context, f application.RunFilter) (application.Result[application.DirectorySyncRun], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.DirectorySyncRun]{}, err
	}
	var q listQuery
	if f.ProviderKey != "" {
		q.add(`provider_key = ?`, f.ProviderKey)
	}
	if cur.Valid {
		q.add(`id < ?`, cur)
	}
	q.args = append(q.args, p.Limit+1)
	sql := `SELECT ` + runColumns + ` FROM organization.directory_sync_runs` + q.where() +
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(q.args))
	rows, err := r.pool.Query(ctx, sql, q.args...)
	if err != nil {
		return application.Result[application.DirectorySyncRun]{}, fmt.Errorf("list directory sync runs: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.DirectorySyncRun, string, error) {
		run, err := scanRun(rows)
		return run, run.ID, err
	})
}

func (r *Repository) GetDirectorySyncRun(ctx context.Context, id string) (application.DirectorySyncRun, error) {
	return getOne(ctx, r, "directory sync run", id, `SELECT `+runColumns+` FROM organization.directory_sync_runs WHERE id = $1`, scanRun)
}
