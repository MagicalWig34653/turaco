// Package setup is the first-administrator web path of a fresh installation. The operator proves control of the
// installation with a single-use setup token issued by "turaco-admin admin setup-token"; the page then performs the
// same domain operation as "turaco-admin admin create-first" (docs/product/f14-administration-design.md, "First
// administrator wizard"). Every call re-checks on the server that no administrator exists.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Errors of the first-administrator operation. The CLI and the web page map them to their own wording.
var (
	ErrAdministratorExists  = errors.New("setup: a platform administrator already exists")
	ErrUserNotFound         = errors.New("setup: user not found or ambiguous")
	ErrLocalAccount         = errors.New("setup: local accounts cannot be platform administrators")
	ErrAlreadyAdministrator = errors.New("setup: user is already a platform administrator")
	// ErrEntraLink wraps a refusal to link the given Entra identity (unknown user, identity taken, ...).
	ErrEntraLink = errors.New("setup: entra identity cannot be linked")
	// ErrTokenInvalid covers an unknown, used or expired token and a closed setup alike; callers must not
	// distinguish them.
	ErrTokenInvalid = errors.New("setup: token invalid")
)

const (
	DefaultTokenTTL = 30 * time.Minute
	MaxTokenTTL     = 24 * time.Hour
	tokenBytes      = 32
)

// Grant is one first-administrator request.
type Grant struct {
	User          string
	Tenant        string // Entra tenant GUID, with Object
	Object        string // Entra object GUID
	Force         bool   // CLI only
	Actor         audit.Actor
	CorrelationID string
	Via           string // "cli" or "web", recorded in the audit event
}

// Result names what was granted.
type Result struct {
	UserID       string
	AssignmentID string
}

// Granter is the first-administrator domain operation (implemented in the composition root over the
// Organization and role services).
type Granter interface {
	Grant(ctx context.Context, g Grant) (Result, error)
	AdministratorExists(ctx context.Context) (bool, error)
}

// Store keeps the setup tokens.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(token))))
	return sum[:]
}

// Issue creates a token valid for ttl (default 30 minutes, at most 24 hours), revokes every earlier open token
// and audits the issue. The plaintext is returned once and never stored.
func (s *Store) Issue(ctx context.Context, actor audit.Actor, correlationID string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if ttl > MaxTokenTTL {
		return "", time.Time{}, fmt.Errorf("setup: ttl above %s", MaxTokenTTL)
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("setup: random: %w", err)
	}
	token = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.setup_tokens SET used_at = now() WHERE used_at IS NULL`); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.setup_tokens (token_hash, expires_at)
			VALUES ($1, now() + make_interval(secs => $2)) RETURNING id::text, expires_at`, hashToken(token), ttl.Seconds()).Scan(&id, &expiresAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "setup.token_issued", TargetType: "setup_token", TargetID: id, Actor: actor, CorrelationID: correlationID,
			Metadata: map[string]any{"expiresAt": expiresAt.UTC().Format(time.RFC3339)},
		})
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("setup: issue token: %w", err)
	}
	return token, expiresAt, nil
}

// OpenTokenExists reports whether an unused, unexpired token exists.
func (s *Store) OpenTokenExists(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.setup_tokens WHERE used_at IS NULL AND expires_at > now())`).Scan(&ok)
	return ok, err
}

// Valid reports whether token is an unused, unexpired token.
func (s *Store) Valid(ctx context.Context, token string) (bool, error) {
	if token == "" || len(token) > 128 {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.setup_tokens
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now())`, hashToken(token)).Scan(&ok)
	return ok, err
}

// Consume marks token used; it reports false when it was not open (a concurrent use won).
func (s *Store) Consume(ctx context.Context, token string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE platform.setup_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, hashToken(token))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
