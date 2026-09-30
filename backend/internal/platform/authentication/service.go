// Package authentication owns server-side session lifecycle. Session tokens
// are random bearer secrets; only their SHA-256 hash is persisted.
package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

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
	if !uuidPattern.MatchString(userID) {
		return "", Session{}, errors.New("create session: user id must be a UUID")
	}
	if authMethod == "" {
		return "", Session{}, errors.New("create session: auth method is required")
	}
	token, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	now := s.clock()
	absExp := now.Add(s.cfg.AbsoluteTimeout)
	idleExp := now.Add(s.cfg.IdleTimeout)
	if idleExp.After(absExp) {
		idleExp = absExp
	}
	metadata, err := json.Marshal(map[string]string{"authMethod": authMethod})
	if err != nil {
		return "", Session{}, fmt.Errorf("marshal audit metadata: %w", err)
	}

	var sess Session
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		sess, err = insertSession(ctx, tx, HashToken(token), userID, authMethod, now, idleExp, absExp)
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, "auth.session.created", sess.ID, &userID, correlationID, metadata, now)
	})
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
	var actor *string
	if actorID != "" {
		if !uuidPattern.MatchString(actorID) {
			return errors.New("revoke session: actor id must be a UUID")
		}
		actor = &actorID
	}
	now := s.clock()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		owner, method, changed, err := revokeSession(ctx, tx, sessionID, now)
		if err != nil || !changed {
			return err
		}
		meta, err := json.Marshal(map[string]string{"userId": owner, "authMethod": method})
		if err != nil {
			return fmt.Errorf("marshal audit metadata: %w", err)
		}
		return s.audit(ctx, tx, "auth.session.revoked", sessionID, actor, correlationID, meta, now)
	})
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, action, sessionID string, actor *string, correlationID string, metadata json.RawMessage, now time.Time) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	return audit.Insert(ctx, tx, audit.Entry{
		ID: id, OccurredAt: now, ActorID: actor, Action: action,
		TargetType: "session", TargetID: sessionID, CorrelationID: correlationID, Metadata: metadata,
	})
}
