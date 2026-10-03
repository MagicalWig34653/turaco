package events_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Tests share one database with other packages, so they use an event type no
// production code emits yet and restrict the dispatcher to it.
const testEventType = "GoodsReceived"

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := dbtest.Pool(t)
	clean := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.outbox_events WHERE event_type = $1`, testEventType)
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.jobs WHERE job_type = 'test.outbox'`)
	}
	clean()
	t.Cleanup(clean)
	return pool
}

func newDispatcher(pool *pgxpool.Pool, maxAttempts int) *events.Dispatcher {
	return events.NewDispatcher(pool, events.DispatcherOptions{
		PollInterval: 10 * time.Millisecond, MaxAttempts: maxAttempts, EventTypes: []string{testEventType},
	}, quietLogger())
}

func insertEvent(t *testing.T, pool *pgxpool.Pool, correlation string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = events.InsertOutbox(ctx, tx, events.OutboxEvent{
		ID: id, EventType: testEventType, EventVersion: 1, OccurredAt: time.Now(),
		CorrelationID: correlation, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

type eventRow struct {
	Status    string
	Attempts  int
	LastError *string
	Future    float64 // seconds until available_at
}

func readEvent(t *testing.T, pool *pgxpool.Pool, id string) eventRow {
	t.Helper()
	var r eventRow
	err := pool.QueryRow(context.Background(), `
		SELECT status, attempts, last_error, extract(epoch FROM available_at - clock_timestamp())
		FROM platform.outbox_events WHERE id = $1`, id).Scan(&r.Status, &r.Attempts, &r.LastError, &r.Future)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func jobCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type = 'test.outbox'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// enqueueJob is a consumer effect that lives in the claim transaction.
func enqueueJob(ctx context.Context, tx pgx.Tx, e events.OutboxEvent) error {
	_, err := tx.Exec(ctx, `INSERT INTO platform.jobs (job_type, payload) VALUES ('test.outbox', jsonb_build_object('event', $1::text))`, e.ID)
	return err
}

func TestRegisterValidates(t *testing.T) {
	d := events.NewDispatcher(nil, events.DispatcherOptions{}, quietLogger())
	if err := d.Register("NoSuchEvent", "x", enqueueJob); err == nil {
		t.Error("unregistered event type must be rejected")
	}
	if err := d.Register(testEventType, "", enqueueJob); err == nil {
		t.Error("empty consumer name must be rejected")
	}
	if err := d.Register(testEventType, "x", nil); err == nil {
		t.Error("nil consumer must be rejected")
	}
}

func TestSuccessCommitsConsumerEffectAndMarksProcessed(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 3)
	if err := d.Register(testEventType, "enqueue", enqueueJob); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-ok")

	dispatched, err := d.DispatchOne(context.Background())
	if err != nil || !dispatched {
		t.Fatalf("dispatched=%v err=%v", dispatched, err)
	}
	if r := readEvent(t, pool, id); r.Status != "processed" || r.Attempts != 1 || r.LastError != nil {
		t.Errorf("event = %+v", r)
	}
	if n := jobCount(t, pool); n != 1 {
		t.Errorf("consumer effects = %d, want 1", n)
	}
	if dispatched, err := d.DispatchOne(context.Background()); err != nil || dispatched {
		t.Errorf("second dispatch: dispatched=%v err=%v, want nothing due", dispatched, err)
	}
}

func TestEventWithoutConsumerIsProcessed(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 3)
	id := insertEvent(t, pool, "corr-none")
	if dispatched, err := d.DispatchOne(context.Background()); err != nil || !dispatched {
		t.Fatalf("dispatched=%v err=%v", dispatched, err)
	}
	if r := readEvent(t, pool, id); r.Status != "processed" {
		t.Errorf("status = %q, want processed", r.Status)
	}
}

func TestFailureRollsBackEffectsAndRetriesWithBackoff(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 3)
	failing := func(ctx context.Context, tx pgx.Tx, e events.OutboxEvent) error {
		if err := enqueueJob(ctx, tx, e); err != nil {
			return err
		}
		return errors.New("boom " + strings.Repeat("x", 3000))
	}
	if err := d.Register(testEventType, "failing", failing); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-fail")

	if dispatched, err := d.DispatchOne(context.Background()); err != nil || !dispatched {
		t.Fatalf("dispatched=%v err=%v", dispatched, err)
	}
	r := readEvent(t, pool, id)
	if r.Status != "pending" || r.Attempts != 1 || r.LastError == nil || len(*r.LastError) > 1000 {
		t.Errorf("event = %+v", r)
	}
	if r.Future < 20 || r.Future > 40 {
		t.Errorf("retry in %.0fs, want about 30s", r.Future)
	}
	if n := jobCount(t, pool); n != 0 {
		t.Errorf("failed consumer left %d effects behind, want 0", n)
	}
	if dispatched, err := d.DispatchOne(context.Background()); err != nil || dispatched {
		t.Errorf("event must not be due before back-off: dispatched=%v err=%v", dispatched, err)
	}
}

func TestAttemptsExhaustedBecomesTerminallyFailed(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 2)
	if err := d.Register(testEventType, "failing", func(context.Context, pgx.Tx, events.OutboxEvent) error {
		return errors.New("always")
	}); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-exhaust")

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := d.DispatchOne(context.Background()); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 { // make the retry due without waiting for the back-off
			if _, err := pool.Exec(context.Background(), `UPDATE platform.outbox_events SET available_at = now() - interval '1 second' WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if r := readEvent(t, pool, id); r.Status != "failed" || r.Attempts != 2 {
		t.Errorf("event = %+v, want failed after 2 attempts", r)
	}
	if dispatched, _ := d.DispatchOne(context.Background()); dispatched {
		t.Error("terminally failed event must not be claimed again")
	}
}

func TestPanickingConsumerIsContained(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 3)
	if err := d.Register(testEventType, "panics", func(context.Context, pgx.Tx, events.OutboxEvent) error {
		panic("kaboom")
	}); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-panic")
	if _, err := d.DispatchOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := readEvent(t, pool, id); r.Status != "pending" || r.Attempts != 1 || r.LastError == nil || !strings.Contains(*r.LastError, "kaboom") {
		t.Errorf("event = %+v", r)
	}
}

func TestConcurrentDispatchersDeliverEachEventOnce(t *testing.T) {
	pool := testPool(t)
	const total = 30
	var (
		mu    sync.Mutex
		count = map[string]int{}
	)
	consumer := func(_ context.Context, _ pgx.Tx, e events.OutboxEvent) error {
		mu.Lock()
		defer mu.Unlock()
		count[e.ID]++
		return nil
	}
	ids := make([]string, total)
	for i := range ids {
		ids[i] = insertEvent(t, pool, fmt.Sprintf("corr-%d", i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		d := newDispatcher(pool, 3)
		if err := d.Register(testEventType, "count", consumer); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				dispatched, err := d.DispatchOne(ctx)
				if err != nil {
					t.Errorf("dispatch: %v", err)
					return
				}
				if !dispatched {
					return
				}
			}
		}()
	}
	wg.Wait()

	for _, id := range ids {
		if count[id] != 1 {
			t.Errorf("event %s delivered %d times, want 1", id, count[id])
		}
		if r := readEvent(t, pool, id); r.Status != "processed" {
			t.Errorf("event %s status %q", id, r.Status)
		}
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestConsumerTimeoutIsARecordedFailure(t *testing.T) {
	pool := testPool(t)
	d := events.NewDispatcher(pool, events.DispatcherOptions{
		PollInterval: 10 * time.Millisecond, MaxAttempts: 3, EventTypes: []string{testEventType},
		ConsumerTimeout: 50 * time.Millisecond,
	}, quietLogger())
	if err := d.Register(testEventType, "slow", func(ctx context.Context, _ pgx.Tx, _ events.OutboxEvent) error {
		<-ctx.Done() // a hung consumer only ends through its deadline
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-timeout")
	dispatched, err := d.DispatchOne(context.Background())
	if err != nil || !dispatched {
		t.Fatalf("a consumer timeout must be recorded, not returned: dispatched=%v err=%v", dispatched, err)
	}
	if r := readEvent(t, pool, id); r.Status != "pending" || r.Attempts != 1 || r.LastError == nil || !strings.Contains(*r.LastError, "deadline") {
		t.Errorf("event = %+v", r)
	}
}

func TestPermanentConsumerErrorFailsTheEventImmediately(t *testing.T) {
	pool := testPool(t)
	d := newDispatcher(pool, 10)
	if err := d.Register(testEventType, "bad payload", func(context.Context, pgx.Tx, events.OutboxEvent) error {
		return events.Permanent(errors.New("cannot decode payload"))
	}); err != nil {
		t.Fatal(err)
	}
	id := insertEvent(t, pool, "corr-permanent")
	if _, err := d.DispatchOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := readEvent(t, pool, id); r.Status != "failed" || r.Attempts != 1 {
		t.Errorf("event = %+v, want failed after one attempt", r)
	}
	if !events.IsPermanent(events.Permanent(errors.New("x"))) || events.IsPermanent(errors.New("x")) || events.Permanent(nil) != nil {
		t.Error("Permanent/IsPermanent contract")
	}
}
