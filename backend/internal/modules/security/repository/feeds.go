package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// ClaimFeed takes the lease of one feed source (created on first use) and returns its state with the
// lease token; claimed is false while another run holds an unexpired lease.
func (r *Repository) ClaimFeed(ctx context.Context, source string, lease time.Duration) (application.FeedState, bool, error) {
	var st application.FeedState
	st.Source = source
	var miss []byte
	err := r.pool.QueryRow(ctx, `
		INSERT INTO security.feed_state AS f (source, last_attempt_at, locked_until, lease_owner)
		VALUES ($1, now(), now() + make_interval(secs => $2), gen_random_uuid()::text)
		ON CONFLICT (source) DO UPDATE SET locked_until = now() + make_interval(secs => $2), lease_owner = gen_random_uuid()::text,
			last_attempt_at = now(), updated_at = now()
		WHERE f.locked_until IS NULL OR f.locked_until < now()
		RETURNING coalesce(f.cursor, ''), coalesce(f.etag, ''), f.last_success_at, coalesce(f.last_error, ''), f.lease_owner, f.kev_miss, f.kev_count`,
		source, lease.Seconds()).Scan(&st.Cursor, &st.ETag, &st.LastSuccessAt, &st.LastError, &st.LeaseToken, &miss, &st.KEVCount)
	if err == pgx.ErrNoRows {
		return application.FeedState{}, false, nil
	}
	if err != nil {
		return application.FeedState{}, false, fmt.Errorf("claim feed: %w", err)
	}
	if err := json.Unmarshal(miss, &st.KEVMiss); err != nil {
		st.KEVMiss = nil // unreadable state only costs a repeated fetch
	}
	return st, true, nil
}

// FinishFeed releases the lease and records the outcome of a run. It changes nothing and returns
// application.ErrFeedLeaseLost when the lease token is not the current one (another run took over).
func (r *Repository) FinishFeed(ctx context.Context, source, leaseToken string, f application.FeedFinish) error {
	var miss any
	if f.KEVMiss != nil {
		b, err := json.Marshal(*f.KEVMiss)
		if err != nil {
			return fmt.Errorf("finish feed: %w", err)
		}
		miss = string(b)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE security.feed_state SET cursor = coalesce($3, cursor),
			etag = CASE WHEN $4::text IS NULL THEN etag ELSE nullif($4::text, '') END,
			last_success_at = CASE WHEN $5 THEN now() ELSE last_success_at END,
			last_error = nullif($6::text, ''), kev_miss = coalesce($7::jsonb, kev_miss), kev_count = coalesce($8::int, kev_count),
			locked_until = NULL, lease_owner = NULL, updated_at = now()
		WHERE source = $1 AND lease_owner = $2`, source, leaseToken, f.Cursor, f.ETag, f.Success, f.ErrorCode, miss, f.KEVCount)
	if err != nil {
		return fmt.Errorf("finish feed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return application.ErrFeedLeaseLost
	}
	return nil
}

// FeedStates lists the feed sources that have run.
func (r *Repository) FeedStates(ctx context.Context) ([]application.FeedState, error) {
	rows, err := r.pool.Query(ctx, `SELECT source, last_success_at, last_attempt_at, coalesce(last_error, '')
		FROM security.feed_state ORDER BY source`)
	if err != nil {
		return nil, fmt.Errorf("list feed states: %w", err)
	}
	defer rows.Close()
	var out []application.FeedState
	for rows.Next() {
		var s application.FeedState
		if err := rows.Scan(&s.Source, &s.LastSuccessAt, &s.LastAttemptAt, &s.LastError); err != nil {
			return nil, fmt.Errorf("list feed states: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MissingExternalIDs returns the ids that have no advisory of the source yet (an empty source: of any source).
func (r *Repository) MissingExternalIDs(ctx context.Context, source string, ids []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT i FROM unnest($2::text[]) AS i
		WHERE NOT EXISTS (SELECT 1 FROM security.advisories a WHERE ($1 = '' OR a.source = $1) AND a.external_id = i) ORDER BY i`, source, ids)
	if err != nil {
		return nil, fmt.Errorf("missing external ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("missing external ids: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ApplyKEVTx marks existing advisories with a matching external id as known exploited and returns the ids
// that changed. It never creates advisories and never clears the mark. It does not bump the advisory
// version: the KEV columns are enrichment, not an analyst edit, so analysts' open edits do not conflict.
func (r *Repository) ApplyKEVTx(ctx context.Context, tx pgx.Tx, entries []application.KEVEntry) ([]string, error) {
	ids, added, due := make([]string, len(entries)), make([]string, len(entries)), make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.CVEID
		if e.AddedAt != nil {
			added[i] = e.AddedAt.Format(time.DateOnly)
		}
		if e.DueDate != nil {
			due[i] = e.DueDate.Format(time.DateOnly)
		}
	}
	rows, err := tx.Query(ctx, `
		WITH input AS (SELECT cve, nullif(added, '')::date AS added, nullif(due, '')::date AS due
			FROM unnest($1::text[], $2::text[], $3::text[]) AS t(cve, added, due))
		UPDATE security.advisories a SET known_exploited = true, known_exploited_added_at = i.added, kev_due_date = i.due,
			updated_at = now()
		FROM input i
		WHERE a.external_id = i.cve AND (NOT a.known_exploited OR a.known_exploited_added_at IS DISTINCT FROM i.added OR a.kev_due_date IS DISTINCT FROM i.due)
		RETURNING a.id::text`, ids, added, due)
	if err != nil {
		return nil, fmt.Errorf("apply kev: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("apply kev: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
