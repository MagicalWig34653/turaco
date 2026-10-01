package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Schedule describes work that must be enqueued at most once per Interval.
type Schedule struct {
	JobType     string
	DedupeKey   string
	Payload     any
	Interval    time.Duration
	MaxAttempts int // 0 means the table default
}

func (s Schedule) validate() error {
	switch {
	case s.JobType == "":
		return errors.New("job type is required")
	case s.DedupeKey == "":
		return errors.New("dedupe key is required")
	case s.Interval <= 0:
		return errors.New("interval must be positive")
	case s.MaxAttempts < 0:
		return errors.New("max attempts must not be negative")
	}
	return nil
}

// EnsureScheduled enqueues the scheduled job unless a pending/processing job
// with the same dedupe key exists or the most recent job with that key was
// created less than Interval ago. The decision uses the database clock and is
// serialized per dedupe key with a transaction-scoped advisory lock, so
// concurrent callers (several processes) enqueue at most one job.
func EnsureScheduled(ctx context.Context, pool *pgxpool.Pool, s Schedule) (enqueued bool, err error) {
	if err := s.validate(); err != nil {
		return false, fmt.Errorf("ensure scheduled job: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("ensure scheduled job: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('platform.jobs:' || $1::text, 0))`, s.DedupeKey); err != nil {
		return false, fmt.Errorf("ensure scheduled job: lock: %w", err)
	}
	var due bool
	err = tx.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM platform.jobs
			WHERE dedupe_key = $1
			  AND (status IN ('pending', 'processing')
			       OR created_at > clock_timestamp() - make_interval(secs => $2)))`,
		s.DedupeKey, s.Interval.Seconds()).Scan(&due)
	if err != nil {
		return false, fmt.Errorf("ensure scheduled job: check: %w", err)
	}
	if !due {
		return false, nil
	}
	_, created, err := Enqueue(ctx, tx, EnqueueRequest{
		Type: s.JobType, Payload: s.Payload, DedupeKey: s.DedupeKey, MaxAttempts: s.MaxAttempts,
	})
	if err != nil {
		return false, fmt.Errorf("ensure scheduled job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("ensure scheduled job: commit: %w", err)
	}
	return created, nil
}
