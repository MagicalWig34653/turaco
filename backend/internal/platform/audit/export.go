package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// ExportMaxRows is the largest export; more matching events are refused with the matching count.
	ExportMaxRows = 10000
	// ExportMaxRange is the longest time range of one export.
	ExportMaxRange = 92 * 24 * time.Hour
	// ExportsPerHour is the per-user export limit.
	ExportsPerHour = 5
	// ActionExported is the audit action written before an export streams.
	ActionExported = "platform.audit.exported"
	// ActionPurged is the audit action of the retention job.
	ActionPurged = "platform.audit.purged"
	// MinRetentionDays is the smallest configurable retention; the database function enforces it again.
	MinRetentionDays = 365
)

// Each calls fn for every event matching f, newest first, in keyset pages of 500, and stops at max rows or the first
// error of fn. It returns the number of events delivered.
func (r *Reader) Each(ctx context.Context, f Filter, max int, fn func(Event) error) (int, error) {
	n, cursor := 0, ""
	for n < max {
		page := 500
		if max-n < page {
			page = max - n
		}
		res, err := r.List(ctx, f, Page{Limit: page, Cursor: cursor})
		if err != nil {
			return n, err
		}
		for _, e := range res.Items {
			if err := fn(e); err != nil {
				return n, err
			}
			n++
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return n, nil
}

// RecentExports counts the exports the user started in the last hour (audit events, so the limit holds across
// instances and restarts).
func (r *Reader) RecentExports(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events
		WHERE action = $1 AND actor_id = $2::uuid AND occurred_at > now() - interval '1 hour'`, ActionExported, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count audit exports: %w", err)
	}
	return n, nil
}

// RecordExport writes the audit event of an export before it streams. The filter is stored as a hash and the range;
// no filter text is copied.
func (r *Reader) RecordExport(ctx context.Context, userID, correlationID string, f Filter, rows int, details bool) error {
	if correlationID == "" {
		correlationID = "audit-export-" + userID
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return Record(ctx, tx, Change{
			Action: ActionExported, TargetType: "audit_export", TargetID: userID, Actor: UserActor(userID), CorrelationID: correlationID,
			After: map[string]any{"filterHash": f.Hash(), "from": rangeText(f.From), "to": rangeText(f.To), "rows": rows, "includeDetails": details},
		})
	})
}

func rangeText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Hash returns a stable hash of the filter, for audit records that must identify a filter without storing it.
func (f Filter) Hash() string {
	from, to := rangeText(f.From), rangeText(f.To)
	h := sha256.Sum256([]byte(strings.Join([]string{f.TargetType, f.TargetID, f.Action, f.ActionPrefix, f.ActorID,
		f.CorrelationID, f.Via, f.ActorKind, f.SystemActor, from, to}, "\x1f")))
	return hex.EncodeToString(h[:8])
}

// PurgeResult is one batch of the retention purge.
type PurgeResult struct {
	Deleted         int64
	EffectiveCutoff time.Time
	IDHash          string
}

// PurgeBefore deletes one batch of events older than cutoff through platform.purge_audit_before, which clamps the
// cutoff to 365 days itself, and records platform.audit.purged for a non-empty batch. Callers loop until Deleted is 0.
func (r *Reader) PurgeBefore(ctx context.Context, cutoff time.Time, batch int, correlationID string) (PurgeResult, error) {
	var res PurgeResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT deleted, effective_cutoff, id_hash FROM platform.purge_audit_before($1, $2)`, cutoff.UTC(), batch).
			Scan(&res.Deleted, &res.EffectiveCutoff, &res.IDHash); err != nil {
			return fmt.Errorf("purge audit events: %w", err)
		}
		if res.Deleted == 0 {
			return nil
		}
		return Record(ctx, tx, Change{
			Action: ActionPurged, TargetType: "audit_retention", TargetID: "audit", Actor: SystemActor("audit-retention"), CorrelationID: correlationID,
			After: map[string]any{"cutoff": res.EffectiveCutoff.UTC().Format(time.RFC3339), "deleted": res.Deleted, "idHash": res.IDHash},
		})
	})
	return res, err
}
