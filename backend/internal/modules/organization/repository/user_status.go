package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

// User status changes.
//
// changeUserStatus is the only function in this module that writes
// organization.users.status. Invariants every status change keeps (see
// docs/domain/state-machines.md, "User status"):
//
//   - status_source records who set the status. A platform-side status change
//     (a future operation of this module, not directory sync) MUST pass
//     ToSource "platform" so directory sync does not undo it.
//   - Leaving "active" revokes all of the user's sessions in the same
//     transaction; use revokeSessionsOfLeftActive for the returned flips.
//     Sync defers the revocation to the end of its transaction to keep row
//     locks on platform.sessions short.
//   - Every transition is audited as organization.user.status_changed with
//     before/after {status, statusSource}.

const (
	statusActive   = "active"
	statusInactive = "inactive"

	statusSourcePlatform  = "platform"
	statusSourceDirectory = "directory"
)

type statusState struct {
	Status       string `json:"status"`
	StatusSource string `json:"statusSource"`
}

// statusTransition selects the users a change applies to and the new state.
// Only users currently in From (and, when FromSource is set, with that
// source) and whose directory enablement equals DirectoryEnabled are changed.
// DirectoryEnabled is true for users with at least one enabled, non-deleted
// external identity.
type statusTransition struct {
	From             string
	FromSource       string // empty: any source
	To               string
	ToSource         string
	DirectoryEnabled bool
}

// statusChangeContext describes who is changing the status, for the audit event.
type statusChangeContext struct {
	Metadata      map[string]any
	CorrelationID string
	At            time.Time
}

// statusFlip is one applied status change. Audit has no ID yet; the caller
// inserts it with the other audit entries of its transaction.
type statusFlip struct {
	UserID     string
	Before     statusState
	After      statusState
	LeftActive bool
	Audit      audit.Entry
}

func changeUserStatus(ctx context.Context, tx pgx.Tx, ids []string, t statusTransition, c statusChangeContext) ([]statusFlip, error) {
	if t.ToSource != statusSourcePlatform && t.ToSource != statusSourceDirectory {
		return nil, fmt.Errorf("change user status: invalid status source %q", t.ToSource)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH c AS (
			SELECT u.id, u.status_source AS prev_source FROM organization.users u
			WHERE u.id = ANY($1::uuid[]) AND u.status = $2 AND ($3 = '' OR u.status_source = $3)
			  AND EXISTS (SELECT 1 FROM organization.external_identities e
			              WHERE e.user_id = u.id AND e.enabled AND e.deleted_observed_at IS NULL) = $4
			FOR UPDATE OF u)
		UPDATE organization.users u SET status = $5, status_source = $6, updated_at = $7
		FROM c WHERE u.id = c.id RETURNING u.id::text, c.prev_source`,
		ids, t.From, t.FromSource, t.DirectoryEnabled, t.To, t.ToSource, c.At)
	if err != nil {
		return nil, fmt.Errorf("change user status: %w", err)
	}
	defer rows.Close()
	var flips []statusFlip
	for rows.Next() {
		f := statusFlip{Before: statusState{t.From, ""}, After: statusState{t.To, t.ToSource}, LeftActive: t.From == statusActive && t.To != statusActive}
		if err := rows.Scan(&f.UserID, &f.Before.StatusSource); err != nil {
			return nil, fmt.Errorf("change user status: scan: %w", err)
		}
		flips = append(flips, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change user status: %w", err)
	}
	sort.Slice(flips, func(i, j int) bool { return flips[i].UserID < flips[j].UserID })
	meta, err := json.Marshal(c.Metadata)
	if err != nil {
		return nil, fmt.Errorf("change user status: marshal audit metadata: %w", err)
	}
	for i := range flips {
		f := &flips[i]
		e := audit.Entry{
			OccurredAt: c.At, Action: "organization.user.status_changed", TargetType: "user", TargetID: f.UserID,
			CorrelationID: c.CorrelationID, Metadata: meta,
		}
		if e.Before, err = json.Marshal(f.Before); err != nil {
			return nil, fmt.Errorf("change user status: marshal audit before: %w", err)
		}
		if e.After, err = json.Marshal(f.After); err != nil {
			return nil, fmt.Errorf("change user status: marshal audit after: %w", err)
		}
		f.Audit = e
	}
	return flips, nil
}

// revokeSessionsOfLeftActive revokes every session of the users that left
// "active" and returns the number of sessions revoked.
// system names the non-human actor ("directory-sync").
func revokeSessionsOfLeftActive(ctx context.Context, tx pgx.Tx, userIDs []string, system, correlationID string, at time.Time) (int, error) {
	actor := audit.SystemActor(system)
	sorted := append([]string(nil), userIDs...)
	sort.Strings(sorted)
	total := 0
	for _, id := range sorted {
		n, err := authentication.RevokeUserSessions(ctx, tx, id, "user_deactivated", actor, correlationID, at)
		if err != nil {
			return 0, fmt.Errorf("revoke sessions: %w", err)
		}
		total += n
	}
	return total, nil
}
