package health

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

var ErrProbeUnsupported = errors.New("health: probe unsupported")
var ErrProbeRateLimited = errors.New("health: probe rate limited")

// ProbeResult is a stored observation from an explicit read-only connection test.
type ProbeResult struct {
	Status      Status    `json:"status"`
	AttemptedAt time.Time `json:"attemptedAt"`
	ErrorCode   string    `json:"errorCode,omitempty"`
}

// Prober admits one attempt per check per minute across API processes, then records a sanitized result and audit event.
type Prober struct {
	pool   *pgxpool.Pool
	checks map[string]func(context.Context) error
}

func NewProber(pool *pgxpool.Pool, checks map[string]func(context.Context) error) *Prober {
	copy := make(map[string]func(context.Context) error, len(checks))
	for key, fn := range checks {
		if fn != nil {
			copy[key] = fn
		}
	}
	return &Prober{pool: pool, checks: copy}
}

func (p *Prober) Supports(key string) bool { _, ok := p.checks[key]; return ok }

func (p *Prober) Run(ctx context.Context, key, userID, correlationID string) (ProbeResult, error) {
	fn, ok := p.checks[key]
	if !ok {
		return ProbeResult{}, ErrProbeUnsupported
	}
	var at time.Time
	err := p.pool.QueryRow(ctx, `INSERT INTO platform.health_probes (check_key, last_attempt_at, status)
		VALUES ($1, now(), 'unknown') ON CONFLICT (check_key) DO UPDATE
		SET last_attempt_at = now(), status = 'unknown', error_code = ''
		WHERE platform.health_probes.last_attempt_at <= now() - interval '1 minute'
		RETURNING last_attempt_at`, key).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProbeResult{}, ErrProbeRateLimited
	}
	if err != nil {
		return ProbeResult{}, fmt.Errorf("claim health probe: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	probeErr := fn(probeCtx)
	cancel()
	result := ProbeResult{Status: StatusOK, AttemptedAt: at}
	if probeErr != nil {
		result.Status, result.ErrorCode = StatusFailing, "connection_failed"
	}
	// Finish the observation even if the client disconnected during the bounded probe.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer finishCancel()
	err = pgx.BeginFunc(finishCtx, p.pool, func(tx pgx.Tx) error {
		if _, e := tx.Exec(finishCtx, `UPDATE platform.health_probes SET status = $2, error_code = $3 WHERE check_key = $1`, key, result.Status, result.ErrorCode); e != nil {
			return e
		}
		return audit.Record(finishCtx, tx, audit.Change{Action: "platform.health.probe_run", TargetType: "health_check", TargetID: key, Actor: audit.UserActor(userID), CorrelationID: correlationID, Metadata: map[string]any{"status": result.Status, "errorCode": result.ErrorCode, "attemptedAt": at}})
	})
	if err != nil {
		return ProbeResult{}, fmt.Errorf("record health probe: %w", err)
	}
	return result, nil
}

func (p *Prober) Snapshot(ctx context.Context) (map[string]ProbeResult, error) {
	out := map[string]ProbeResult{}
	rows, err := p.pool.Query(ctx, `SELECT check_key, last_attempt_at, status, error_code FROM platform.health_probes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var result ProbeResult
		if err := rows.Scan(&key, &result.AttemptedAt, &result.Status, &result.ErrorCode); err != nil {
			return nil, err
		}
		out[key] = result
	}
	return out, rows.Err()
}
