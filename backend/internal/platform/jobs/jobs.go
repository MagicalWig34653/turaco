// Package jobs is the PostgreSQL-backed background job queue (ADR-0006).
//
// Lifecycle of platform.jobs rows:
//
//	pending -> processing -> completed
//	                      -> pending   (retryable error; available_at moves forward)
//	                      -> failed    (permanent error or attempts exhausted; terminal)
//	pending -> cancelled
//
// A processing job whose lock is older than the runner's lock timeout is
// considered abandoned (crashed worker) and may be claimed again. Handlers must
// therefore be idempotent. A job interrupted by worker shutdown returns to
// pending without consuming the attempt.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job is a claimed job handed to a Handler.
type Job struct {
	ID          string
	Type        string
	Payload     json.RawMessage
	DedupeKey   *string
	Attempts    int // including the current attempt
	MaxAttempts int
	CreatedAt   time.Time
}

// Handler executes one job. Returning nil completes it; a Permanent error
// fails it terminally; any other error is retried with backoff until
// MaxAttempts is reached.
type Handler func(ctx context.Context, job Job) error

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as not retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx, so a job can be
// enqueued atomically with the business change that requires it.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EnqueueRequest describes a job to insert.
type EnqueueRequest struct {
	Type    string
	Payload any // marshalled to JSON; nil means {}
	// DedupeKey, when set, allows at most one pending/processing job with the
	// same key. A duplicate request returns the active job instead.
	DedupeKey   string
	MaxAttempts int // 0 means the table default (5)
}

// Enqueue inserts a pending job and returns its ID. When DedupeKey matches an
// active job, no job is inserted, created is false and id is the active job's
// ID (or empty if it finished concurrently).
func Enqueue(ctx context.Context, q Querier, req EnqueueRequest) (id string, created bool, err error) {
	if req.Type == "" {
		return "", false, errors.New("enqueue job: type is required")
	}
	if req.MaxAttempts < 0 {
		return "", false, errors.New("enqueue job: max attempts must not be negative")
	}
	payload := []byte(`{}`)
	if req.Payload != nil {
		if payload, err = json.Marshal(req.Payload); err != nil {
			return "", false, fmt.Errorf("enqueue job: marshal payload: %w", err)
		}
	}
	var dedupe *string
	if req.DedupeKey != "" {
		dedupe = &req.DedupeKey
	}
	var maxAttempts *int
	if req.MaxAttempts > 0 {
		maxAttempts = &req.MaxAttempts
	}
	err = q.QueryRow(ctx, `
		INSERT INTO platform.jobs (job_type, payload, dedupe_key, max_attempts)
		VALUES ($1, $2, $3, COALESCE($4, 5))
		ON CONFLICT (dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'processing')
		DO NOTHING
		RETURNING id::text`,
		req.Type, payload, dedupe, maxAttempts,
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("enqueue job: %w", err)
	}
	err = q.QueryRow(ctx, `
		SELECT id::text FROM platform.jobs
		WHERE dedupe_key = $1 AND status IN ('pending', 'processing')`, req.DedupeKey,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("enqueue job: find active duplicate: %w", err)
	}
	return id, false, nil
}
