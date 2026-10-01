package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

var _ application.LoginAccountStore = (*Repository)(nil)

// FindDirectoryAccounts implements application.LoginAccountStore. The
// identity must be enabled and not deleted, carry a distinguished name and
// belong to an active User. LIMIT 2 is enough to detect ambiguity.
func (r *Repository) FindDirectoryAccounts(ctx context.Context, providerKey, by, value string) ([]application.DirectoryAccount, error) {
	var sql string
	switch by {
	case "username":
		sql = `
			SELECT u.id::text, e.distinguished_name
			FROM organization.external_identities e
			JOIN organization.users u ON u.id = e.user_id
			WHERE e.provider_key = $1 AND lower(e.username) = lower($2)
			  AND e.enabled AND e.deleted_observed_at IS NULL
			  AND e.distinguished_name IS NOT NULL AND e.distinguished_name <> ''
			  AND u.status = 'active'
			LIMIT 2`
	case "email":
		sql = `
			SELECT u.id::text, e.distinguished_name
			FROM organization.users u
			JOIN organization.external_identities e ON e.user_id = u.id
			WHERE e.provider_key = $1 AND lower(u.primary_email) = lower($2)
			  AND e.enabled AND e.deleted_observed_at IS NULL
			  AND e.distinguished_name IS NOT NULL AND e.distinguished_name <> ''
			  AND u.status = 'active'
			LIMIT 2`
	default:
		return nil, fmt.Errorf("find directory accounts: unknown lookup %q", by)
	}
	rows, err := r.pool.Query(ctx, sql, providerKey, value)
	if err != nil {
		return nil, fmt.Errorf("find directory accounts: %w", err)
	}
	defer rows.Close()
	var out []application.DirectoryAccount
	for rows.Next() {
		var a application.DirectoryAccount
		if err := rows.Scan(&a.UserID, &a.DistinguishedName); err != nil {
			return nil, fmt.Errorf("find directory accounts: scan: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find directory accounts: %w", err)
	}
	return out, nil
}

// LockActiveUser implements application.LoginAccountStore. FOR SHARE
// conflicts with the FOR UPDATE of status changes (changeUserStatus), so
// either the login sees the new status or the status change waits for it.
func (r *Repository) LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	if _, ok := parseID(userID); !ok {
		return false, nil
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM organization.users WHERE id = $1 FOR SHARE`, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock user: %w", err)
	}
	return status == statusActive, nil
}

// InsertLocalUser implements application.LoginAccountStore.
func (r *Repository) InsertLocalUser(ctx context.Context, tx pgx.Tx, in application.LocalUserInsert) (string, error) {
	at := in.At.UTC().Truncate(time.Microsecond)
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO organization.users (display_name, status, status_source, created_at, updated_at)
		VALUES ($1, 'active', 'platform', $2, $2) RETURNING id::text`, in.DisplayName, at).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert local user: %w", err)
	}
	after, err := json.Marshal(map[string]string{"displayName": in.DisplayName, "status": statusActive, "statusSource": statusSourcePlatform})
	if err != nil {
		return "", fmt.Errorf("insert local user: marshal audit: %w", err)
	}
	meta := in.Actor
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	var auditID string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&auditID); err != nil {
		return "", fmt.Errorf("insert local user: audit id: %w", err)
	}
	if err := audit.Insert(ctx, tx, audit.Entry{
		ID: auditID, OccurredAt: at, Action: "organization.user.created_local", TargetType: "user", TargetID: id,
		CorrelationID: in.CorrelationID, After: after, Metadata: meta,
	}); err != nil {
		return "", err
	}
	return id, nil
}
