package authentication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Throttle limits failed logins per identifier and per client address with
// counters in platform.auth_throttle. Every counter update is a single atomic
// upsert, so concurrent failures are never lost. Keys never contain the
// identifier itself, only its SHA-256.
type Throttle struct {
	pool *pgxpool.Pool
	cfg  ThrottleConfig
	now  func() time.Time

	lastPrune atomic.Int64 // unix nanoseconds
}

// ThrottleConfig holds the limits; zero values select the documented defaults
// (5 per identifier, 30 per client, 15 minute window, 15 minute lock).
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
)

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

// IdentifierKey is the throttle key of a login identifier: "id:" plus the
// SHA-256 hex digest of its lower-cased form.
func IdentifierKey(identifier string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(identifier)))
	return "id:" + hex.EncodeToString(sum[:])
}

// ClientKey is the throttle key of a client address.
func ClientKey(ip string) string { return "ip:" + ip }

func (t *Throttle) clock() time.Time { return t.now().UTC().Truncate(time.Microsecond) }

// Check reports whether any of keys is currently locked and, if so, how long
// until the last lock ends. It must run before any directory or hash work.
func (t *Throttle) Check(ctx context.Context, keys ...string) (time.Duration, bool, error) {
	now := t.clock()
	var until *time.Time
	err := t.pool.QueryRow(ctx,
		`SELECT max(locked_until) FROM platform.auth_throttle WHERE key = ANY($1) AND locked_until > $2`,
		keys, now).Scan(&until)
	if err != nil {
		return 0, false, fmt.Errorf("check login throttle: %w", err)
	}
	if until == nil {
		return 0, false, nil
	}
	return until.Sub(now), true, nil
}

// A counter restarts when its lock has expired or, without a lock, its
// window ended. In the upsert, t.* are the stored values.
const throttleFresh = `((t.locked_until IS NOT NULL AND t.locked_until <= $2) OR (t.locked_until IS NULL AND t.window_started_at + $4::bigint * interval '1 microsecond' <= $2))`

const throttleLockAt = `$2::timestamptz + $5::bigint * interval '1 microsecond'`

const recordFailureSQL = `
INSERT INTO platform.auth_throttle AS t (key, failures, window_started_at, locked_until, updated_at)
VALUES ($1, 1, $2, CASE WHEN $3::int <= 1 THEN ` + throttleLockAt + ` END, $2)
ON CONFLICT (key) DO UPDATE SET
    failures = CASE WHEN ` + throttleFresh + ` THEN 1 ELSE t.failures + 1 END,
    window_started_at = CASE WHEN ` + throttleFresh + ` THEN $2 ELSE t.window_started_at END,
    locked_until = CASE
        WHEN ` + throttleFresh + ` THEN CASE WHEN $3::int <= 1 THEN ` + throttleLockAt + ` END
        WHEN t.locked_until > $2 THEN t.locked_until
        WHEN t.failures + 1 >= $3::int THEN ` + throttleLockAt + `
    END,
    updated_at = $2`

// RecordFailure counts one failed attempt for key and locks the key when
// limit failures were reached within the window. Concurrent calls are
// serialized by the row lock of the upsert. A currently locked key keeps its
// lock (attempts that raced past Check never extend it).
func (t *Throttle) RecordFailure(ctx context.Context, key string, limit int) error {
	now := t.clock()
	if _, err := t.pool.Exec(ctx, recordFailureSQL, key, now, limit, t.cfg.Window.Microseconds(), t.cfg.Lockout.Microseconds()); err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	t.maybePrune(ctx, now)
	return nil
}

// RecordFailures counts a failed attempt against the identifier and the client.
func (t *Throttle) RecordFailures(ctx context.Context, identifierKey, clientKey string) error {
	if err := t.RecordFailure(ctx, identifierKey, t.cfg.IdentifierLimit); err != nil {
		return err
	}
	return t.RecordFailure(ctx, clientKey, t.cfg.ClientLimit)
}

// Clear removes the counter of key (after a successful login).
func (t *Throttle) Clear(ctx context.Context, key string) error {
	if _, err := t.pool.Exec(ctx, `DELETE FROM platform.auth_throttle WHERE key = $1`, key); err != nil {
		return fmt.Errorf("clear login throttle: %w", err)
	}
	return nil
}

// maybePrune deletes long-idle counters at most once per prunePeriod and
// process. Pruning is best effort; failures are ignored.
func (t *Throttle) maybePrune(ctx context.Context, now time.Time) {
	last := t.lastPrune.Load()
	if now.UnixNano()-last < int64(prunePeriod) || !t.lastPrune.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	_, _ = t.pool.Exec(ctx, `DELETE FROM platform.auth_throttle WHERE updated_at < $1`, now.Add(-throttleRetention))
}
