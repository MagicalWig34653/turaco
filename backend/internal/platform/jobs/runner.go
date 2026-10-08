package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultLockTimeout  = 30 * time.Minute

	backoffBase = 30 * time.Second
	backoffCap  = 30 * time.Minute

	maxLastErrorBytes = 1000
	finalizeTimeout   = 10 * time.Second
)

// RunnerOptions configures a Runner.
type RunnerOptions struct {
	// WorkerID identifies this runner in platform.jobs.locked_by. It must be
	// unique per running Runner. When empty, NewRunner uses "<hostname>-<pid>".
	WorkerID string
	// PollInterval is the wait between polls when no job is available and the
	// back-off after infrastructure errors. Default 2s.
	PollInterval time.Duration
	// LockTimeout is how long a processing job stays locked before it is
	// considered abandoned and may be claimed again. Every handler timeout must
	// be shorter. Default 30m.
	LockTimeout time.Duration
	// Gate, when set, is asked after a job was claimed whether it may run (module switches, ADR-0032). GateDrop
	// completes a disposable job without running it; GateDefer keeps a durable job pending (attempt refunded) and
	// retries it after DeferDelay; an error returns the job for a delayed retry.
	Gate func(ctx context.Context, jobType string) (GateDecision, error)
	// DeferDelay is the wait before a deferred job is offered again. Default 1m.
	DeferDelay time.Duration
}

// GateDecision is the answer of RunnerOptions.Gate.
type GateDecision int

// Gate decisions.
const (
	GateRun GateDecision = iota
	GateDrop
	GateDefer
)

type registration struct {
	timeout time.Duration
	handler Handler
}

// Runner claims and executes jobs from platform.jobs. It runs one job at a
// time; run several Runners (with distinct WorkerIDs) for more concurrency.
type Runner struct {
	pool   *pgxpool.Pool
	opts   RunnerOptions
	logger *slog.Logger

	mu        sync.RWMutex
	handlers  map[string]registration
	schedules []Schedule
}

// NewRunner creates a Runner. Zero option values get their defaults.
func NewRunner(pool *pgxpool.Pool, opts RunnerOptions, logger *slog.Logger) *Runner {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = defaultLockTimeout
	}
	if opts.WorkerID == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		opts.WorkerID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		pool:     pool,
		opts:     opts,
		logger:   logger.With("component", "jobs.runner", "worker_id", opts.WorkerID),
		handlers: map[string]registration{},
	}
}

// Register adds the handler for a job type. timeout bounds one handler
// execution and must be positive and shorter than the lock timeout, so a job
// cannot outlive its lock.
func (r *Runner) Register(jobType string, timeout time.Duration, h Handler) error {
	switch {
	case jobType == "":
		return errors.New("register job handler: job type is required")
	case h == nil:
		return fmt.Errorf("register job handler %q: handler is nil", jobType)
	case timeout <= 0:
		return fmt.Errorf("register job handler %q: timeout must be positive", jobType)
	case timeout >= r.opts.LockTimeout:
		return fmt.Errorf("register job handler %q: timeout %s must be shorter than lock timeout %s", jobType, timeout, r.opts.LockTimeout)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.handlers[jobType]; ok {
		return fmt.Errorf("register job handler %q: already registered", jobType)
	}
	r.handlers[jobType] = registration{timeout: timeout, handler: h}
	return nil
}

// RegisteredJobs returns the timeout of every registered job type. It lets a startup test assert the
// registrations of the real worker.
func (r *Runner) RegisteredJobs() map[string]time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]time.Duration, len(r.handlers))
	for t, reg := range r.handlers {
		out[t] = reg.timeout
	}
	return out
}

// AddSchedule registers a recurring enqueue evaluated on every Run iteration.
// The job type must already be registered.
func (r *Runner) AddSchedule(s Schedule) error {
	if err := s.validate(); err != nil {
		return fmt.Errorf("add job schedule: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.handlers[s.JobType]; !ok {
		return fmt.Errorf("add job schedule: job type %q is not registered", s.JobType)
	}
	r.schedules = append(r.schedules, s)
	return nil
}

// Run evaluates schedules and executes jobs until ctx is cancelled, then
// returns nil. While jobs are available it does not wait; otherwise it waits
// PollInterval. Database errors are logged and retried after PollInterval.
func (r *Runner) Run(ctx context.Context) error {
	r.logger.Info("job runner started")
	defer r.logger.Info("job runner stopped")
	for ctx.Err() == nil {
		r.evaluateSchedules(ctx)
		busy := true
		for busy && ctx.Err() == nil {
			processed, err := r.RunOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					r.logger.Error("job runner iteration failed", "error", err)
				}
				busy = false
			} else {
				busy = processed
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(r.opts.PollInterval):
		}
	}
	return nil
}

func (r *Runner) evaluateSchedules(ctx context.Context) {
	r.mu.RLock()
	schedules := append([]Schedule(nil), r.schedules...)
	r.mu.RUnlock()
	for _, s := range schedules {
		if ctx.Err() != nil {
			return
		}
		enqueued, err := EnsureScheduled(ctx, r.pool, s)
		if err != nil {
			if ctx.Err() == nil {
				r.logger.Error("ensure scheduled job failed", "job_type", s.JobType, "dedupe_key", s.DedupeKey, "error", err)
			}
			continue
		}
		if enqueued {
			r.logger.Info("scheduled job enqueued", "job_type", s.JobType, "dedupe_key", s.DedupeKey)
		}
	}
}

// RunOnce claims at most one due job of a registered type and executes it. It
// reports whether a job was claimed. An error means claiming failed; handler
// failures are recorded on the job and are not returned.
func (r *Runner) RunOnce(ctx context.Context) (processed bool, err error) {
	r.mu.RLock()
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	r.mu.RUnlock()
	if len(types) == 0 {
		return false, nil
	}
	sort.Strings(types)

	job, err := r.claim(ctx, types)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	log := r.logger.With("job_id", job.ID, "job_type", job.Type, "attempt", job.Attempts)
	if job.Attempts > job.MaxAttempts {
		// A stale lock was reclaimed after the last attempt already ran.
		log.Warn("job abandoned with exhausted attempts")
		r.finish(ctx, log, job, outcomeFailed, "attempts exhausted (abandoned)", 0)
		return true, nil
	}

	if r.opts.Gate != nil {
		decision, gerr := r.opts.Gate(ctx, job.Type)
		switch {
		case gerr != nil && ctx.Err() != nil:
			r.finish(ctx, log, job, outcomeInterrupted, "interrupted by worker shutdown: "+gerr.Error(), 0)
			return true, nil
		case gerr != nil:
			delay := backoff(job.Attempts)
			log.Warn("job gate failed, will retry", "error", gerr, "retry_in", delay)
			r.finish(ctx, log, job, outcomeRetry, "job gate: "+gerr.Error(), delay)
			return true, nil
		case decision == GateDrop:
			log.Info("disposable job dropped: its module is switched off")
			r.finish(ctx, log, job, outcomeCompleted, "", 0)
			return true, nil
		case decision == GateDefer:
			delay := r.opts.DeferDelay
			if delay <= 0 {
				delay = time.Minute
			}
			log.Info("durable job deferred: its module is switched off", "retry_in", delay)
			r.finish(ctx, log, job, outcomeDeferred, "deferred: module is switched off", delay)
			return true, nil
		}
	}

	r.mu.RLock()
	reg := r.handlers[job.Type]
	r.mu.RUnlock()

	herr := r.execute(ctx, log, reg, *job)
	switch {
	case herr == nil:
		r.finish(ctx, log, job, outcomeCompleted, "", 0)
	case IsPermanent(herr):
		log.Warn("job failed permanently", "error", herr)
		r.finish(ctx, log, job, outcomeFailed, herr.Error(), 0)
	case ctx.Err() != nil:
		// Worker shutdown interrupted the handler. Return the job for
		// immediate retry and refund the attempt: it was not the job's fault.
		log.Info("job interrupted by shutdown", "error", herr)
		r.finish(ctx, log, job, outcomeInterrupted, "interrupted by worker shutdown: "+herr.Error(), 0)
	case job.Attempts >= job.MaxAttempts:
		log.Warn("job failed, attempts exhausted", "error", herr)
		r.finish(ctx, log, job, outcomeFailed, herr.Error(), 0)
	default:
		delay := backoff(job.Attempts)
		log.Warn("job failed, will retry", "error", herr, "retry_in", delay)
		r.finish(ctx, log, job, outcomeRetry, herr.Error(), delay)
	}
	return true, nil
}

const claimSQL = `
UPDATE platform.jobs
SET status = 'processing', locked_at = now(), locked_by = $1,
    attempts = attempts + 1, updated_at = now()
WHERE id = (
    SELECT id FROM platform.jobs
    WHERE job_type = ANY($2)
      AND ((status = 'pending' AND available_at <= now())
        OR (status = 'processing' AND locked_at < now() - make_interval(secs => $3)))
    ORDER BY available_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1)
RETURNING id::text, job_type, payload, dedupe_key, attempts, max_attempts, created_at`

func (r *Runner) claim(ctx context.Context, types []string) (*Job, error) {
	var j Job
	err := r.pool.QueryRow(ctx, claimSQL, r.opts.WorkerID, types, r.opts.LockTimeout.Seconds()).
		Scan(&j.ID, &j.Type, &j.Payload, &j.DedupeKey, &j.Attempts, &j.MaxAttempts, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return &j, nil
}

// execute runs the handler under its timeout and converts a panic into a
// retryable error. The stack is logged, not stored.
func (r *Runner) execute(ctx context.Context, log *slog.Logger, reg registration, job Job) (err error) {
	hctx, cancel := context.WithTimeout(ctx, reg.timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			log.Error("job handler panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = errors.New("handler panicked")
		}
	}()
	return reg.handler(hctx, job)
}

type outcome int

const (
	outcomeCompleted outcome = iota
	outcomeFailed
	outcomeRetry
	outcomeInterrupted
	outcomeDeferred
)

// finish records an outcome. Every update is guarded by the lock owner so a
// slow worker cannot overwrite a job that was reclaimed after its lock expired.
// It uses a context detached from ctx so shutdown does not lose the outcome.
func (r *Runner) finish(ctx context.Context, log *slog.Logger, job *Job, o outcome, lastError string, delay time.Duration) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	lastError = truncateError(lastError)
	var (
		sql  string
		args = []any{job.ID, r.opts.WorkerID}
	)
	switch o {
	case outcomeCompleted:
		sql = `UPDATE platform.jobs SET status = 'completed', completed_at = now(), locked_at = NULL, locked_by = NULL, last_error = NULL, updated_at = now()
			WHERE id = $1 AND status = 'processing' AND locked_by = $2`
	case outcomeFailed:
		sql = `UPDATE platform.jobs SET status = 'failed', locked_at = NULL, locked_by = NULL, last_error = $3, updated_at = now()
			WHERE id = $1 AND status = 'processing' AND locked_by = $2`
		args = append(args, lastError)
	case outcomeRetry:
		sql = `UPDATE platform.jobs SET status = 'pending', available_at = now() + make_interval(secs => $4), locked_at = NULL, locked_by = NULL, last_error = $3, updated_at = now()
			WHERE id = $1 AND status = 'processing' AND locked_by = $2`
		args = append(args, lastError, delay.Seconds())
	case outcomeDeferred:
		sql = `UPDATE platform.jobs SET status = 'pending', available_at = now() + make_interval(secs => $4), attempts = GREATEST(attempts - 1, 0), locked_at = NULL, locked_by = NULL, last_error = $3, updated_at = now()
			WHERE id = $1 AND status = 'processing' AND locked_by = $2`
		args = append(args, lastError, delay.Seconds())
	case outcomeInterrupted:
		sql = `UPDATE platform.jobs SET status = 'pending', available_at = now(), attempts = GREATEST(attempts - 1, 0), locked_at = NULL, locked_by = NULL, last_error = $3, updated_at = now()
			WHERE id = $1 AND status = 'processing' AND locked_by = $2`
		args = append(args, lastError)
	}
	tag, err := r.pool.Exec(fctx, sql, args...)
	if err != nil {
		// The lock will expire and the job will be reclaimed.
		log.Error("record job outcome failed", "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		log.Warn("job lock lost, outcome discarded")
	}
}

// backoff returns the retry delay after the given (1-based) attempt:
// 30s * 2^(attempt-1), capped at 30m.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 12 { // 30s * 2^11 already exceeds the cap
		return backoffCap
	}
	d := backoffBase << (attempt - 1)
	if d > backoffCap {
		return backoffCap
	}
	return d
}

// truncateError limits s to 1000 bytes of valid UTF-8.
func truncateError(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxLastErrorBytes {
		return s
	}
	cut := maxLastErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
