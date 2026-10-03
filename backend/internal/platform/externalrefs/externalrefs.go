// Package externalrefs keeps the mapping between internal records and the
// records of external systems (docs/integrations/autotask.md): never opaque ids
// embedded in domain tables, always (system, entity type, internal id,
// external id) with sync state and timestamps. It also remembers the inbound
// events already processed, so replayed webhooks do nothing.
package externalrefs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Sync states.
const (
	StatePending = "pending"
	StateSynced  = "synced"
	StateFailed  = "failed"
)

// Reference is one mapping.
type Reference struct {
	ID                string
	System            string
	EntityType        string
	EntityID          string
	ExternalID        *string
	SyncState         string
	LastError         *string
	LastSyncedAt      *time.Time
	ExternalUpdatedAt *time.Time
	Attempts          int
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNotFound means there is no such mapping.
var ErrNotFound = errors.New("externalrefs: not found")

const cols = `id::text, system, entity_type, entity_id::text, external_id, sync_state, last_error, last_synced_at, external_updated_at, attempts, version, created_at, updated_at`

func scan(row pgx.Row) (Reference, error) {
	var r Reference
	err := row.Scan(&r.ID, &r.System, &r.EntityType, &r.EntityID, &r.ExternalID, &r.SyncState, &r.LastError, &r.LastSyncedAt, &r.ExternalUpdatedAt, &r.Attempts, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// Ensure returns the mapping of a record, creating a pending one when none exists.
func Ensure(ctx context.Context, q Querier, system, entityType, entityID string) (Reference, error) {
	r, err := scan(q.QueryRow(ctx, `
		INSERT INTO platform.external_references(system, entity_type, entity_id) VALUES ($1, $2, $3::uuid)
		ON CONFLICT (system, entity_type, entity_id) DO UPDATE SET system = EXCLUDED.system
		RETURNING `+cols, system, entityType, entityID))
	if err != nil {
		return Reference{}, fmt.Errorf("ensure external reference: %w", err)
	}
	return r, nil
}

// ForEntity returns the mapping of a record.
func ForEntity(ctx context.Context, q Querier, system, entityType, entityID string) (Reference, error) {
	r, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM platform.external_references WHERE system = $1 AND entity_type = $2 AND entity_id = $3::uuid`, system, entityType, entityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Reference{}, ErrNotFound
	}
	if err != nil {
		return Reference{}, fmt.Errorf("external reference by entity: %w", err)
	}
	return r, nil
}

// ByExternalID returns the mapping of an external record.
func ByExternalID(ctx context.Context, q Querier, system, entityType, externalID string) (Reference, error) {
	r, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM platform.external_references WHERE system = $1 AND entity_type = $2 AND external_id = $3`, system, entityType, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Reference{}, ErrNotFound
	}
	if err != nil {
		return Reference{}, fmt.Errorf("external reference by external id: %w", err)
	}
	return r, nil
}

// ErrStale means the mapping changed while a push was running (a newer push is needed).
var ErrStale = errors.New("externalrefs: the reference changed during the push")

// MarkSynced records a successful push of the state seen at expectedVersion; externalID is set
// once and never changed afterwards. When the mapping was marked pending again in the meantime
// (a newer local change) ErrStale is returned and the external id is still recorded.
func MarkSynced(ctx context.Context, q Querier, id, externalID string, expectedVersion int) error {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" || utf8.RuneCountInString(externalID) > 200 {
		return errors.New("externalrefs: the external id must be 1-200 characters")
	}
	var got string
	err := q.QueryRow(ctx, `
		UPDATE platform.external_references
		SET external_id = coalesce(external_id, $2),
		    sync_state = CASE WHEN version = $3 THEN 'synced' ELSE sync_state END,
		    last_error = CASE WHEN version = $3 THEN NULL ELSE last_error END,
		    last_synced_at = now(), attempts = CASE WHEN version = $3 THEN 0 ELSE attempts END,
		    version = CASE WHEN version = $3 THEN version + 1 ELSE version END, updated_at = now()
		WHERE id = $1::uuid RETURNING external_id`, id, externalID, expectedVersion).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mark external reference synced: %w", err)
	}
	if got != externalID {
		return fmt.Errorf("externalrefs: reference %s is already mapped to a different external record", id)
	}
	var current int
	if err := q.QueryRow(ctx, `SELECT version FROM platform.external_references WHERE id = $1::uuid`, id).Scan(&current); err != nil {
		return fmt.Errorf("mark external reference synced: %w", err)
	}
	if current != expectedVersion+1 {
		return ErrStale
	}
	return nil
}

// MarkFailed records a failed push with a short, secret-free reason.
func MarkFailed(ctx context.Context, q Querier, id, reason string) error {
	reason = strings.ToValidUTF8(reason, "")
	if utf8.RuneCountInString(reason) > 500 {
		reason = string([]rune(reason)[:500])
	}
	var got string
	err := q.QueryRow(ctx, `
		UPDATE platform.external_references SET sync_state = 'failed', last_error = $2, attempts = attempts + 1, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING id::text`, id, reason).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mark external reference failed: %w", err)
	}
	return nil
}

// MarkPending asks for another push (after a local change or a manual retry).
func MarkPending(ctx context.Context, q Querier, id string) error {
	var got string
	err := q.QueryRow(ctx, `UPDATE platform.external_references SET sync_state = 'pending', version = version + 1, updated_at = now() WHERE id = $1::uuid RETURNING id::text`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mark external reference pending: %w", err)
	}
	return nil
}

// TouchExternal records when the external system last changed its record.
func TouchExternal(ctx context.Context, q Querier, id string, at time.Time) error {
	var got string
	err := q.QueryRow(ctx, `
		UPDATE platform.external_references SET external_updated_at = greatest(coalesce(external_updated_at, $2), $2), updated_at = now()
		WHERE id = $1::uuid RETURNING id::text`, id, at).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("touch external reference: %w", err)
	}
	return nil
}

// ClaimEvent records an inbound event and reports whether it is new; a replay
// of the same (system, event id) returns false and must change nothing.
func ClaimEvent(ctx context.Context, q Querier, system, eventID string) (bool, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || utf8.RuneCountInString(eventID) > 200 {
		return false, errors.New("externalrefs: the event id must be 1-200 characters")
	}
	var got string
	err := q.QueryRow(ctx, `INSERT INTO platform.external_events(system, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING event_id`, system, eventID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim external event: %w", err)
	}
	return true, nil
}
