package authentication

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const sessionColumns = `id::text, user_id::text, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at`

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.UserID, &s.AuthMethod, &s.CreatedAt, &s.LastSeenAt, &s.IdleExpiresAt, &s.AbsoluteExpiresAt)
	return s, err
}

func insertSession(ctx context.Context, q querier, tokenHash []byte, userID, authMethod string, now, idleExp, absExp time.Time) (Session, error) {
	s, err := scanSession(q.QueryRow(ctx, `
		INSERT INTO platform.sessions
		(token_hash, user_id, auth_method, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $4, $5, $6)
		RETURNING `+sessionColumns,
		tokenHash, userID, authMethod, now, idleExp, absExp))
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return s, nil
}

// sessionRow carries revocation state alongside the session for validation.
type sessionRow struct {
	Session
	Revoked bool
}

func findByHash(ctx context.Context, q querier, tokenHash []byte) (sessionRow, error) {
	var r sessionRow
	err := q.QueryRow(ctx, `
		SELECT `+sessionColumns+`, revoked_at IS NOT NULL
		FROM platform.sessions WHERE token_hash = $1`, tokenHash).
		Scan(&r.ID, &r.UserID, &r.AuthMethod, &r.CreatedAt, &r.LastSeenAt, &r.IdleExpiresAt, &r.AbsoluteExpiresAt, &r.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionRow{}, ErrInvalidSession
	}
	if err != nil {
		return sessionRow{}, fmt.Errorf("find session: %w", err)
	}
	return r, nil
}

// touchSession slides the idle expiry. The predicate re-checks revocation and
// expiry, so a session revoked before the touch is never returned as valid.
// Requests inside the touch interval skip this call and rely on the earlier
// read, so a revoke racing with such a request may be seen once.
func touchSession(ctx context.Context, q querier, id string, now time.Time, idle time.Duration) (Session, error) {
	s, err := scanSession(q.QueryRow(ctx, `
		UPDATE platform.sessions
		SET last_seen_at = $2,
		    idle_expires_at = least($2::timestamptz + $3 * interval '1 microsecond', absolute_expires_at)
		WHERE id = $1 AND revoked_at IS NULL
		  AND idle_expires_at > $2 AND absolute_expires_at > $2
		RETURNING `+sessionColumns,
		id, now, idle.Microseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrInvalidSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("touch session: %w", err)
	}
	return s, nil
}

// revokeSession revokes an active session and returns its owner and method for
// the audit record; changed is false for unknown or already revoked sessions.
func revokeSession(ctx context.Context, tx pgx.Tx, id string, now time.Time) (userID, authMethod string, changed bool, err error) {
	err = tx.QueryRow(ctx, `UPDATE platform.sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL RETURNING user_id::text, auth_method`, id, now).Scan(&userID, &authMethod)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("revoke session: %w", err)
	}
	return userID, authMethod, true, nil
}
