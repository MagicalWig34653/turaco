package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// DeferredEventJobType is the durable job that carries an outbox event to a consumer of a module that was switched
// off when the event was dispatched. Its type starts with the module key, so the job gate keeps it pending until
// the module is enabled again.
func DeferredEventJobType(key string) string { return key + ".deferred_event" }

// DeferredEventJobTimeout bounds one deferred delivery.
const DeferredEventJobTimeout = time.Minute

// ConsumerGate applies the module switches to outbox consumers. A consumer of a disabled module does not run;
// instead the event is handed, in the dispatcher's transaction, to a durable job for that consumer, so the event is
// neither lost nor does it block the other consumers of the same event. After the module is enabled the job
// delivers it exactly like the dispatcher would have.
type ConsumerGate struct {
	svc *Service
	mu  sync.RWMutex
	fns map[string]events.Consumer
}

// ConsumerGate returns the gate for this registry.
func (s *Service) ConsumerGate() *ConsumerGate {
	return &ConsumerGate{svc: s, fns: map[string]events.Consumer{}}
}

type deferredEvent struct {
	Consumer string             `json:"consumer"`
	Event    events.OutboxEvent `json:"event"`
}

// Wrap is events.DispatcherOptions.WrapConsumer.
func (g *ConsumerGate) Wrap(name string, fn events.Consumer) events.Consumer {
	key, owned := g.svc.ix.ForConsumer(name)
	if !owned {
		return fn
	}
	g.mu.Lock()
	g.fns[name] = fn
	g.mu.Unlock()
	return func(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
		on, err := g.svc.Enabled(ctx, key)
		if err != nil {
			return err
		}
		if on {
			return fn(ctx, tx, ev)
		}
		_, _, err = jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{Type: DeferredEventJobType(key),
			Payload: deferredEvent{Consumer: name, Event: ev}, DedupeKey: ev.ID + ":" + name, MaxAttempts: 10})
		if err != nil {
			return fmt.Errorf("defer event %s for %s: %w", ev.ID, name, err)
		}
		return nil
	}
}

// Handler is the job handler of every DeferredEventJobType.
func (g *ConsumerGate) Handler(ctx context.Context, job jobs.Job) error {
	var d deferredEvent
	if err := json.Unmarshal(job.Payload, &d); err != nil {
		return jobs.Permanent(fmt.Errorf("decode deferred event: %w", err))
	}
	g.mu.RLock()
	fn, ok := g.fns[d.Consumer]
	g.mu.RUnlock()
	if !ok {
		return jobs.Permanent(fmt.Errorf("deferred event: unknown consumer %q", d.Consumer))
	}
	return pgx.BeginFunc(ctx, g.svc.pool, func(tx pgx.Tx) error { return fn(ctx, tx, d.Event) })
}

// RegisterJobs registers the deferred-event handler for every optional module.
func (g *ConsumerGate) RegisterJobs(r *jobs.Runner) error {
	for _, m := range g.svc.ix.All() {
		if m.Core {
			continue
		}
		if err := r.Register(DeferredEventJobType(m.Key), DeferredEventJobTimeout, g.Handler); err != nil {
			return err
		}
	}
	return nil
}
