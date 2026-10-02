package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPollInterval    = 2 * time.Second
	defaultMaxAttempts     = 10
	defaultConsumerTimeout = 30 * time.Second

	backoffBase = 30 * time.Second
	backoffCap  = 30 * time.Minute

	maxLastErrorBytes = 1000
	finalizeTimeout   = 10 * time.Second
)

// Consumer reacts to one outbox event inside the dispatcher's claim
// transaction (ADR-0024). It must touch only its own tables through tx, stay
// short, never call external systems (enqueue a job instead) and be
// idempotent. Returning an error rolls back everything the consumer wrote and
// schedules a retry of the event.
type Consumer func(ctx context.Context, tx pgx.Tx, event OutboxEvent) error

type namedConsumer struct {
	name string
	fn   Consumer
}

// DispatcherOptions configures a Dispatcher.
type DispatcherOptions struct {
	// PollInterval is the wait between polls when no event is due and the
	// back-off after infrastructure errors. Default 2s.
	PollInterval time.Duration
	// MaxAttempts is the number of failed attempts after which an event
	// becomes terminally failed. Default 10.
	MaxAttempts int
	// EventTypes restricts claiming to these event types; empty means all.
	EventTypes []string
	// ConsumerTimeout bounds the consumers of one event, which hold the claim
	// transaction and a connection. A timeout is a failed attempt. Default 30s.
	ConsumerTimeout time.Duration
}

// Dispatcher delivers pending outbox events to registered consumers.
//
// Lifecycle of platform.outbox_events rows:
//
//	pending -> processed  (all consumers succeeded, or none is registered)
//	        -> pending    (a consumer failed; available_at moves forward)
//	        -> failed     (attempts exhausted; terminal, logged at error level)
type Dispatcher struct {
	pool   *pgxpool.Pool
	opts   DispatcherOptions
	logger *slog.Logger

	mu        sync.RWMutex
	consumers map[string][]namedConsumer
}

// NewDispatcher creates a Dispatcher. Zero option values get their defaults.
func NewDispatcher(pool *pgxpool.Pool, opts DispatcherOptions, logger *slog.Logger) *Dispatcher {
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.ConsumerTimeout <= 0 {
		opts.ConsumerTimeout = defaultConsumerTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{pool: pool, opts: opts, logger: logger, consumers: map[string][]namedConsumer{}}
}

// Register adds a consumer for an event type. The event type must be in the
// event registry and the name (used in logs) must be non-empty.
func (d *Dispatcher) Register(eventType, name string, fn Consumer) error {
	if name == "" || fn == nil {
		return errors.New("register outbox consumer: name and function are required")
	}
	if !registered(eventType) {
		return fmt.Errorf("register outbox consumer %q: unregistered event type %q", name, eventType)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.consumers[eventType] = append(d.consumers[eventType], namedConsumer{name: name, fn: fn})
	return nil
}

func registered(eventType string) bool {
	for _, def := range Registry {
		if def.Name == eventType {
			return true
		}
	}
	return false
}

// Run dispatches events until ctx is cancelled, which is not an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		dispatched, err := d.DispatchOne(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			d.logger.ErrorContext(ctx, "outbox dispatch failed", "error", err)
			sleep(ctx, d.opts.PollInterval)
		case err == nil && !dispatched:
			sleep(ctx, d.opts.PollInterval)
		}
	}
	return nil
}

func sleep(ctx context.Context, wait time.Duration) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// DispatchOne claims and delivers one due event. It reports whether an event
// was claimed; an event whose consumers fail still counts as dispatched
// (its failure is recorded, not returned).
func (d *Dispatcher) DispatchOne(ctx context.Context) (bool, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("dispatch outbox: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	event, attempts, err := d.claim(ctx, tx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dispatch outbox: claim: %w", err)
	}

	deliverCtx, cancelDeliver := context.WithTimeout(ctx, d.opts.ConsumerTimeout)
	err = d.deliver(deliverCtx, tx, event)
	cancelDeliver()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err() // shutdown: do not count the attempt
		}
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return true, d.recordFailure(ctx, event, attempts, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE platform.outbox_events
		SET status = 'processed', processed_at = now(), attempts = attempts + 1, last_error = NULL
		WHERE id = $1`, event.ID); err != nil {
		return false, fmt.Errorf("dispatch outbox: mark processed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("dispatch outbox: commit: %w", err)
	}
	return true, nil
}

func (d *Dispatcher) claim(ctx context.Context, tx pgx.Tx) (OutboxEvent, int, error) {
	var (
		e        OutboxEvent
		attempts int
	)
	err := tx.QueryRow(ctx, `
		SELECT id::text, event_type, event_version, occurred_at, actor_id::text,
		       correlation_id, payload, attempts
		FROM platform.outbox_events
		WHERE status = 'pending' AND available_at <= now()
		  AND (cardinality($1::text[]) = 0 OR event_type = ANY($1::text[]))
		ORDER BY available_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, d.opts.EventTypes).Scan(
		&e.ID, &e.EventType, &e.EventVersion, &e.OccurredAt, &e.ActorID,
		&e.CorrelationID, &e.Payload, &attempts)
	return e, attempts, err
}

func (d *Dispatcher) deliver(ctx context.Context, tx pgx.Tx, event OutboxEvent) (err error) {
	d.mu.RLock()
	consumers := d.consumers[event.EventType]
	d.mu.RUnlock()
	for _, c := range consumers {
		if err := runConsumer(ctx, tx, c, event); err != nil {
			return fmt.Errorf("consumer %s: %w", c.name, err)
		}
	}
	return nil
}

func runConsumer(ctx context.Context, tx pgx.Tx, c namedConsumer, event OutboxEvent) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return c.fn(ctx, tx, event)
}

// recordFailure stores the failed attempt in a fresh transaction. The guard
// on status and attempts keeps a concurrent success from being overwritten.
func (d *Dispatcher) recordFailure(ctx context.Context, event OutboxEvent, attempts int, cause error) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()
	next := attempts + 1
	terminal := next >= d.opts.MaxAttempts
	status := "pending"
	if terminal {
		status = "failed"
	}
	tag, err := d.pool.Exec(fctx, `
		UPDATE platform.outbox_events
		SET status = $2, attempts = $3, last_error = $4,
		    available_at = clock_timestamp() + make_interval(secs => $5)
		WHERE id = $1 AND status = 'pending' AND attempts = $6`,
		event.ID, status, next, truncate(cause.Error()), backoff(next).Seconds(), attempts)
	if err != nil {
		return fmt.Errorf("dispatch outbox: record failure: %w", err)
	}
	if tag.RowsAffected() == 1 {
		level := slog.LevelWarn
		if terminal {
			level = slog.LevelError
		}
		d.logger.Log(ctx, level, "outbox event consumer failed",
			"event_id", event.ID, "event_type", event.EventType, "correlation_id", event.CorrelationID,
			"attempt", next, "terminal", terminal, "error", cause)
	}
	return nil
}

func truncate(s string) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxLastErrorBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxLastErrorBytes], "")
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 12 { // backoffBase * 2^11 already exceeds the cap
		return backoffCap
	}
	if d := backoffBase << (attempt - 1); d < backoffCap {
		return d
	}
	return backoffCap
}
