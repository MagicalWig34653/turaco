package authentication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Throttle limits login attempts per client, per account and per unresolved
// identifier with counters in platform.auth_throttle. A request first
// reserves an attempt (Reserve) and only then does any expensive or
// information-revealing work, so concurrent requests cannot all pass a
// check-then-act gap: every reservation is one atomic upsert that increments
// the counter, decides the lock and reads the answer in the same statement.
// All times are database times (now()), never the application clock.
// Keys never contain the identifier itself, only its SHA-256.
type Throttle struct {
	pool *pgxpool.Pool
	cfg  ThrottleConfig
	now  func() time.Time

	lastPrune atomic.Int64 // unix nanoseconds, process-local prune schedule
}

// ThrottleConfig holds the limits; zero values select the documented defaults
// (5 per account or identifier, 30 per client, 15 minute window and lock).
type ThrottleConfig struct {
	IdentifierLimit int
	ClientLimit     int
	Window          time.Duration
	Lockout         time.Duration
}

const (
	defaultIdentifierLimit = 5
	defaultClientLimit     = 30
	defaultThrottleWindow  = 15 * time.Minute
	defaultThrottleLockout = 15 * time.Minute
	throttleRetention      = 24 * time.Hour
	prunePeriod            = time.Hour
	// pruneBatch bounds one prune statement.
	pruneBatch = 500
)

// NewThrottle creates a Throttle. now only schedules the process-local
// pruning of old rows (at most once per hour); it never decides a limit.
func NewThrottle(pool *pgxpool.Pool, cfg ThrottleConfig, now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	if cfg.IdentifierLimit <= 0 {
		cfg.IdentifierLimit = defaultIdentifierLimit
	}
	if cfg.ClientLimit <= 0 {
		cfg.ClientLimit = defaultClientLimit
	}
	if cfg.Window <= 0 {
		cfg.Window = defaultThrottleWindow
	}
	if cfg.Lockout <= 0 {
		cfg.Lockout = defaultThrottleLockout
	}
	return &Throttle{pool: pool, cfg: cfg, now: now}
}

// NormalizeIdentifier is the canonical form of a login identifier, identical
// to how account resolution reads it: a "DOMAIN\" prefix is stripped, the rest
// is trimmed and lower-cased. "alice", "X\alice", "x\ alice" and "ALICE" are
// one identifier.
func NormalizeIdentifier(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	if i := strings.IndexByte(identifier, '\\'); i >= 0 {
		identifier = identifier[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(identifier))
}

// IdentifierKey is the throttle key of a login identifier that did not
// resolve to an account: "id:" plus the SHA-256 hex digest of its normalized
// form.
func IdentifierKey(identifier string) string {
	sum := sha256.Sum256([]byte(NormalizeIdentifier(identifier)))
	return "id:" + hex.EncodeToString(sum[:])
}

// AccountKey is the throttle key of a resolved account.
func AccountKey(userID string) string { return "acct:" + strings.ToLower(userID) }

// ClientKey is the throttle key of a client address: the full address for
// IPv4 and the /64 prefix for IPv6 (one subscriber typically owns a whole
// /64, so single addresses would be a free bypass).
func ClientKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "ip:" + ip
	}
	addr = normalizeAddr(addr)
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return "ip:" + p.String()
		}
	}
	return "ip:" + addr.String()
}

// throttleFresh: a counter restarts when its lock has expired or, without a
// lock, its window ended. In the upsert, t.* are the stored values. $3 is the
// window and $4 the lock duration, both in microseconds.
const throttleFresh = `((t.locked_until IS NOT NULL AND t.locked_until <= now()) OR (t.locked_until IS NULL AND t.window_started_at + $3::bigint * interval '1 microsecond' <= now()))`

const throttleLockAt = `now() + $4::bigint * interval '1 microsecond'`

const reserveSQL = `
INSERT INTO platform.auth_throttle AS t (key, attempts, window_started_at, locked_until, updated_at)
VALUES ($1, 1, now(), CASE WHEN 1 > $2::int THEN ` + throttleLockAt + ` END, now())
ON CONFLICT (key) DO UPDATE SET
    attempts = CASE
        WHEN t.locked_until > now() THEN t.attempts
        WHEN ` + throttleFresh + ` THEN 1
        ELSE t.attempts + 1 END,
    window_started_at = CASE
        WHEN t.locked_until > now() THEN t.window_started_at
        WHEN ` + throttleFresh + ` THEN now()
        ELSE t.window_started_at END,
    locked_until = CASE
        WHEN t.locked_until > now() THEN t.locked_until
        WHEN ` + throttleFresh + ` THEN CASE WHEN 1 > $2::int THEN ` + throttleLockAt + ` END
        WHEN t.attempts + 1 > $2::int THEN ` + throttleLockAt + `
    END,
    updated_at = now()
RETURNING coalesce(locked_until > now(), false),
          coalesce((extract(epoch FROM (locked_until - now())) * 1000000)::bigint, 0)`

// Reserve atomically counts one attempt for key and reports whether it may
// proceed. The attempt that makes the count exceed limit within the window
// locks the key for the lockout period and is itself refused, so at most limit
// attempts are ever allowed per window. A locked key is neither counted nor
// extended. retryAfter is the remaining lock when allowed is false.
func (t *Throttle) Reserve(ctx context.Context, key string, limit int) (allowed bool, retryAfter time.Duration, err error) {
	var locked bool
	var retryMicros int64
	err = t.pool.QueryRow(ctx, reserveSQL, key, limit, t.cfg.Window.Microseconds(), t.cfg.Lockout.Microseconds()).Scan(&locked, &retryMicros)
	if err != nil {
		return false, 0, fmt.Errorf("reserve login attempt: %w", err)
	}
	t.maybePrune(ctx)
	if locked {
		return false, time.Duration(retryMicros) * time.Microsecond, nil
	}
	return true, 0, nil
}

// ReserveClient reserves an attempt for a client key (ClientKey).
func (t *Throttle) ReserveClient(ctx context.Context, key string) (bool, time.Duration, error) {
	return t.Reserve(ctx, key, t.cfg.ClientLimit)
}

// ReserveIdentifier reserves an attempt for an account or identifier key
// (AccountKey, IdentifierKey).
func (t *Throttle) ReserveIdentifier(ctx context.Context, key string) (bool, time.Duration, error) {
	return t.Reserve(ctx, key, t.cfg.IdentifierLimit)
}

// Clear removes the counter of key (after a successful login).
func (t *Throttle) Clear(ctx context.Context, key string) error {
	if _, err := t.pool.Exec(ctx, `DELETE FROM platform.auth_throttle WHERE key = $1`, key); err != nil {
		return fmt.Errorf("clear login throttle: %w", err)
	}
	return nil
}

// Refund gives one reserved attempt of an unlocked key back (never below
// zero), so successful logins do not consume the client budget.
func (t *Throttle) Refund(ctx context.Context, key string) error {
	_, err := t.pool.Exec(ctx, `
		UPDATE platform.auth_throttle SET attempts = greatest(attempts - 1, 0), updated_at = now()
		WHERE key = $1 AND (locked_until IS NULL OR locked_until <= now())`, key)
	if err != nil {
		return fmt.Errorf("refund login attempt: %w", err)
	}
	return nil
}

// maybePrune deletes a bounded batch of long-idle counters at most once per
// prunePeriod and process; row age is judged by database time. Pruning is
// best effort; failures are ignored.
func (t *Throttle) maybePrune(ctx context.Context) {
	now := t.now()
	last := t.lastPrune.Load()
	if now.UnixNano()-last < int64(prunePeriod) || !t.lastPrune.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	_, _ = t.pool.Exec(ctx, `
		DELETE FROM platform.auth_throttle WHERE key IN (
			SELECT key FROM platform.auth_throttle
			WHERE updated_at < now() - $1::bigint * interval '1 microsecond'
			ORDER BY updated_at LIMIT $2)`, throttleRetention.Microseconds(), pruneBatch)
}
