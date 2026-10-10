// Package authentication owns server-side session lifecycle. Session tokens
// are random bearer secrets; only their SHA-256 hash is persisted.
package authentication

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// ErrTemporarilyUnavailable means session creation timed out waiting for a
// row lock (for example a user status change in progress); nothing was
// written and the login may be retried.
var ErrTemporarilyUnavailable = errors.New("authentication: temporarily unavailable")

// ErrInvalidSession means the token is malformed, unknown, revoked or expired.
var ErrInvalidSession = errors.New("authentication: invalid session")

const defaultTouchInterval = time.Minute

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Session struct {
	ID                string
	UserID            string
	AuthMethod        string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

type Config struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// AbsoluteTimeoutOverride, when set, is asked for every new session; (d, true) with d > 0 replaces
	// AbsoluteTimeout (the administration setting auth.session_absolute_timeout). The environment value stays the
	// fallback when it reports false.
	AbsoluteTimeoutOverride func(ctx context.Context) (time.Duration, bool)
	// TouchInterval throttles last-seen writes; defaults to one minute.
	TouchInterval time.Duration
}

type Service struct {
	pool *pgxpool.Pool
	cfg  Config
	now  func() time.Time
}

func NewService(pool *pgxpool.Pool, cfg Config, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if cfg.TouchInterval == 0 {
		cfg.TouchInterval = defaultTouchInterval
	}
	return &Service{pool: pool, cfg: cfg, now: now}
}

// clock returns the current time truncated to PostgreSQL's microsecond precision.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// Create issues a new session with a fresh token and audits it atomically.
// The raw token is returned only here and is never persisted or logged.
func (s *Service) Create(ctx context.Context, userID, authMethod, correlationID string) (string, Session, error) {
	return s.create(ctx, LoginSession{UserID: userID, AuthMethod: authMethod, CorrelationID: correlationID})
}

// LoginSession describes a session created by a login.
type LoginSession struct {
	UserID        string
	AuthMethod    string
	CorrelationID string
	// MaxLifetime caps the absolute lifetime below the configured one when
	// positive (the emergency account uses one hour).
	MaxLifetime time.Duration
	// Locker, when set, locks the user row and requires the user to be
	// active inside the creating transaction; otherwise CreateLogin returns
	// ErrUserInactive and nothing is written.
	Locker UserLocker
	// ReplaceSessionID, when set, is revoked in the same transaction
	// (session fixation defence: a login never keeps the previous session).
	ReplaceSessionID string
	// AfterCreate runs in the same transaction after the session exists; an
	// error rolls everything back.
	AfterCreate func(ctx context.Context, tx pgx.Tx, s Session) error
}

// CreateLogin creates the session of a successful login in one transaction:
// lock and check the user, revoke the replaced session, insert the session and
// audit it (auth.session.created).
func (s *Service) CreateLogin(ctx context.Context, p LoginSession) (string, Session, error) {
	return s.create(ctx, p)
}

func (s *Service) create(ctx context.Context, p LoginSession) (string, Session, error) {
	userID, authMethod := p.UserID, p.AuthMethod
	if !uuidPattern.MatchString(userID) {
		return "", Session{}, errors.New("create session: user id must be a UUID")
	}
	if authMethod == "" {
		return "", Session{}, errors.New("create session: auth method is required")
	}
	if p.ReplaceSessionID != "" && !uuidPattern.MatchString(p.ReplaceSessionID) {
		return "", Session{}, errors.New("create session: replaced session id must be a UUID")
	}
	token, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	now := s.clock()
	lifetime := s.cfg.AbsoluteTimeout
	if s.cfg.AbsoluteTimeoutOverride != nil {
		if d, ok := s.cfg.AbsoluteTimeoutOverride(ctx); ok && d > 0 {
			lifetime = d
		}
	}
	if p.MaxLifetime > 0 && p.MaxLifetime < lifetime {
		lifetime = p.MaxLifetime
	}
	absExp := now.Add(lifetime)
	idleExp := now.Add(s.cfg.IdleTimeout)
	if idleExp.After(absExp) {
		idleExp = absExp
	}

	var sess Session
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Waiting for the user row (a status change in progress) must not pin
		// a connection and a request indefinitely.
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
			return fmt.Errorf("set lock timeout: %w", err)
		}
		if p.Locker != nil {
			active, err := p.Locker.LockActiveUser(ctx, tx, userID)
			if err != nil {
				return fmt.Errorf("lock user: %w", err)
			}
			if !active {
				return ErrUserInactive
			}
		}
		if p.ReplaceSessionID != "" {
			owner, method, changed, err := revokeSession(ctx, tx, p.ReplaceSessionID, now)
			if err != nil {
				return err
			}
			if changed {
				if err := recordSessionAudit(ctx, tx, "auth.session.revoked", p.ReplaceSessionID, audit.SystemActor("login"), p.CorrelationID, now,
					map[string]any{"userId": owner, "authMethod": method, "reason": "replaced_by_login", "replacedByUserId": userID}); err != nil {
					return err
				}
			}
		}
		var err error
		sess, err = insertSession(ctx, tx, HashToken(token), userID, authMethod, now, idleExp, absExp)
		if err != nil {
			return err
		}
		if err := recordSessionAudit(ctx, tx, "auth.session.created", sess.ID, audit.UserActor(userID), p.CorrelationID, now,
			map[string]any{"authMethod": authMethod}); err != nil {
			return err
		}
		if p.AfterCreate != nil {
			return p.AfterCreate(ctx, tx, sess)
		}
		return nil
	})
	if errors.Is(err, ErrUserInactive) {
		return "", Session{}, err
	}
	if isLockTimeout(err) {
		return "", Session{}, ErrTemporarilyUnavailable
	}
	if err != nil {
		return "", Session{}, fmt.Errorf("create session: %w", err)
	}
	return token, sess, nil
}

// Authenticate resolves a raw token to a valid session, sliding the idle
// expiry at most once per TouchInterval and never beyond the absolute expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if !validTokenFormat(token) {
		return Session{}, ErrInvalidSession
	}
	now := s.clock()
	row, err := findByHash(ctx, s.pool, HashToken(token))
	if err != nil {
		return Session{}, err
	}
	if row.Revoked || !now.Before(row.IdleExpiresAt) || !now.Before(row.AbsoluteExpiresAt) {
		return Session{}, ErrInvalidSession
	}
	if now.Sub(row.LastSeenAt) < s.cfg.TouchInterval {
		return row.Session, nil
	}
	return touchSession(ctx, s.pool, row.ID, now, s.cfg.IdleTimeout)
}

// Revoke ends a session. It is idempotent: unknown or already revoked
// sessions return nil and produce no audit event.
func (s *Service) Revoke(ctx context.Context, sessionID, actorID, correlationID string) error {
	if !uuidPattern.MatchString(sessionID) {
		return errors.New("revoke session: session id must be a UUID")
	}
	actor := audit.SystemActor("system")
	if actorID != "" {
		if !uuidPattern.MatchString(actorID) {
			return errors.New("revoke session: actor id must be a UUID")
		}
		actor = audit.UserActor(actorID)
	}
	now := s.clock()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		owner, method, changed, err := revokeSession(ctx, tx, sessionID, now)
		if err != nil || !changed {
			return err
		}
		return recordSessionAudit(ctx, tx, "auth.session.revoked", sessionID, actor, correlationID, now,
			map[string]any{"userId": owner, "authMethod": method})
	})
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func recordSessionAudit(ctx context.Context, tx pgx.Tx, action, sessionID string, actor audit.Actor, correlationID string, at time.Time, metadata map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "session", TargetID: sessionID, Actor: actor,
		CorrelationID: correlationID, Metadata: metadata, OccurredAt: at,
	})
}

// isLockTimeout reports a PostgreSQL lock_not_available error (SQLSTATE 55P03).
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// RevokeUserSessions revokes every unrevoked session of userID inside tx and
// audits each revocation with reason (for example "user_deactivated") and the
// given actor (for example audit.SystemActor("directory-sync")). It lets an
// operation that ends a user's `active` status (such as directory sync) or
// replaces their credential revoke sessions atomically with that change, so a
// later reactivation does not revive old sessions. It returns the number
// revoked.
func RevokeUserSessions(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, now time.Time) (int, error) {
	if !uuidPattern.MatchString(userID) {
		return 0, errors.New("revoke user sessions: user id must be a UUID")
	}
	if reason == "" {
		return 0, errors.New("revoke user sessions: reason is required")
	}
	if err := actor.Validate(); err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	now = now.UTC().Truncate(time.Microsecond)
	rows, err := tx.Query(ctx, `
		UPDATE platform.sessions SET revoked_at = $2
		WHERE user_id = $1 AND revoked_at IS NULL
		RETURNING id::text, auth_method`, userID, now)
	if err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	type revoked struct{ id, method string }
	var sessions []revoked
	for rows.Next() {
		var r revoked
		if err := rows.Scan(&r.id, &r.method); err != nil {
			rows.Close()
			return 0, fmt.Errorf("revoke user sessions: scan: %w", err)
		}
		sessions = append(sessions, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	for _, r := range sessions {
		if err := recordSessionAudit(ctx, tx, "auth.session.revoked", r.id, actor, correlationID, now,
			map[string]any{"userId": userID, "authMethod": r.method, "reason": reason}); err != nil {
			return 0, err
		}
	}
	return len(sessions), nil
}
