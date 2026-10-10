package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// uniqueType returns a job type no other test uses and removes its rows when
// the test ends; tests share one database with other packages.
func uniqueType(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	jobType := "test." + hex.EncodeToString(b)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.jobs WHERE job_type = $1`, jobType)
	})
	return jobType
}

func newRunner(t *testing.T, pool *pgxpool.Pool, workerID string, lockTimeout time.Duration) *jobs.Runner {
	t.Helper()
	return jobs.NewRunner(pool, jobs.RunnerOptions{WorkerID: workerID, PollInterval: 10 * time.Millisecond, LockTimeout: lockTimeout}, quietLogger())
}

func mustEnqueue(t *testing.T, pool *pgxpool.Pool, req jobs.EnqueueRequest) string {
	t.Helper()
	id, created, err := jobs.Enqueue(context.Background(), pool, req)
	if err != nil || !created {
		t.Fatalf("enqueue: id=%q created=%v err=%v", id, created, err)
	}
	return id
}

type jobRow struct {
	Status     string
	Attempts   int
	LastError  *string
	LockedBy   *string
	LockedAt   *time.Time
	Completed  *time.Time
	FutureSecs float64 // seconds until available_at (negative if past)
}

func fetch(t *testing.T, pool *pgxpool.Pool, id string) jobRow {
	t.Helper()
	var r jobRow
	err := pool.QueryRow(context.Background(), `
		SELECT status, attempts, last_error, locked_by, locked_at, completed_at,
		       extract(epoch FROM available_at - now())::float8
		FROM platform.jobs WHERE id = $1`, id).
		Scan(&r.Status, &r.Attempts, &r.LastError, &r.LockedBy, &r.LockedAt, &r.Completed, &r.FutureSecs)
	if err != nil {
		t.Fatalf("fetch job: %v", err)
	}
	return r
}

func mustRunOnce(t *testing.T, r *jobs.Runner, want bool) {
	t.Helper()
	got, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got != want {
		t.Fatalf("RunOnce processed = %v, want %v", got, want)
	}
}

func TestEnqueueDedupe(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	jobType := uniqueType(t, pool)
	key := jobType + ":k"

	first := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType, DedupeKey: key, Payload: map[string]string{"a": "b"}})
	id, created, err := jobs.Enqueue(ctx, pool, jobs.EnqueueRequest{Type: jobType, DedupeKey: key})
	if err != nil || created || id != first {
		t.Fatalf("duplicate enqueue: id=%q created=%v err=%v, want existing %q", id, created, err, first)
	}

	r := newRunner(t, pool, "w-dedupe", time.Hour)
	var payload string
	if err := r.Register(jobType, time.Minute, func(_ context.Context, j jobs.Job) error {
		payload = string(j.Payload)
		if j.DedupeKey == nil || *j.DedupeKey != key {
			t.Errorf("dedupe key = %v", j.DedupeKey)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, true)
	if payload != `{"a": "b"}` {
		t.Errorf("payload = %s", payload)
	}

	second, created, err := jobs.Enqueue(ctx, pool, jobs.EnqueueRequest{Type: jobType, DedupeKey: key})
	if err != nil || !created || second == first {
		t.Fatalf("enqueue after completion: id=%q created=%v err=%v", second, created, err)
	}
}

func TestRunOnceOutcomes(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)

	tests := []struct {
		name        string
		maxAttempts int
		handler     jobs.Handler
		wantStatus  string
		wantErr     string
		wantRetry   bool
		// wantDelay, when set, is the expected retry delay in seconds (instead of the 30s back-off).
		wantDelay float64
	}{
		{name: "success", handler: func(context.Context, jobs.Job) error { return nil }, wantStatus: "completed"},
		{name: "retryable", handler: func(context.Context, jobs.Job) error { return errors.New("boom") }, wantStatus: "pending", wantErr: "boom", wantRetry: true},
		{name: "permanent", handler: func(context.Context, jobs.Job) error { return jobs.Permanent(errors.New("bad input")) }, wantStatus: "failed", wantErr: "bad input"},
		{name: "attempts exhausted", maxAttempts: 1, handler: func(context.Context, jobs.Job) error { return errors.New("last try") }, wantStatus: "failed", wantErr: "last try"},
		{name: "retry after longer than back-off", handler: func(context.Context, jobs.Job) error {
			return jobs.RetryAfter(errors.New("rate limited"), 5*time.Minute)
		}, wantStatus: "pending", wantErr: "rate limited", wantRetry: true, wantDelay: 300},
		{name: "retry after shorter than back-off", handler: func(context.Context, jobs.Job) error {
			return jobs.RetryAfter(errors.New("rate limited"), time.Second)
		}, wantStatus: "pending", wantErr: "rate limited", wantRetry: true},
		{name: "retry after is capped", handler: func(context.Context, jobs.Job) error {
			return jobs.RetryAfter(errors.New("rate limited"), 48*time.Hour)
		}, wantStatus: "pending", wantErr: "rate limited", wantRetry: true, wantDelay: 3600},
		{name: "panic", handler: func(context.Context, jobs.Job) error { panic("kaboom secret") }, wantStatus: "pending", wantErr: "handler panicked", wantRetry: true},
		{name: "long error is truncated", handler: func(context.Context, jobs.Job) error { return errors.New(strings.Repeat("ä", 2000)) }, wantStatus: "pending", wantRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jobType := uniqueType(t, pool)
			id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType, MaxAttempts: tt.maxAttempts})
			r := newRunner(t, pool, "w-outcome", time.Hour)
			if err := r.Register(jobType, time.Minute, tt.handler); err != nil {
				t.Fatal(err)
			}
			mustRunOnce(t, r, true)

			row := fetch(t, pool, id)
			if row.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", row.Status, tt.wantStatus)
			}
			if row.Attempts != 1 {
				t.Errorf("attempts = %d, want 1", row.Attempts)
			}
			if row.LockedBy != nil || row.LockedAt != nil {
				t.Errorf("lock not released: %v %v", row.LockedBy, row.LockedAt)
			}
			if tt.wantStatus == "completed" {
				if row.Completed == nil || row.LastError != nil {
					t.Errorf("completed_at=%v last_error=%v", row.Completed, row.LastError)
				}
			} else if row.LastError == nil || !strings.Contains(*row.LastError, tt.wantErr) {
				t.Errorf("last_error = %v, want containing %q", row.LastError, tt.wantErr)
			}
			if row.LastError != nil && (len(*row.LastError) > 1000 || !utf8.ValidString(*row.LastError)) {
				t.Errorf("last_error not truncated to 1000 valid bytes: %d", len(*row.LastError))
			}
			if want := tt.wantDelay; want > 0 {
				if row.FutureSecs < want-10 || row.FutureSecs > want+10 {
					t.Errorf("available_at in %.1fs, want about %.0fs", row.FutureSecs, want)
				}
			} else if tt.wantRetry && (row.FutureSecs < 20 || row.FutureSecs > 40) {
				t.Errorf("available_at in %.1fs, want about 30s backoff", row.FutureSecs)
			}
			// A job with a future available_at is not picked up again.
			if tt.wantRetry {
				mustRunOnce(t, r, false)
			}
		})
	}
}

func TestRunOnceNoJobsOrHandlers(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	r := newRunner(t, pool, "w-empty", time.Hour)
	mustRunOnce(t, r, false) // no handlers
	if err := r.Register(uniqueType(t, pool), time.Minute, func(context.Context, jobs.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, false) // nothing due
}

func TestRunOnceIgnoresUnregisteredTypes(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	other := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: other})
	r := newRunner(t, pool, "w-ignore", time.Hour)
	if err := r.Register(uniqueType(t, pool), time.Minute, func(context.Context, jobs.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, false)
	if row := fetch(t, pool, id); row.Status != "pending" || row.Attempts != 0 {
		t.Errorf("unregistered job touched: %+v", row)
	}
}

func TestHandlerTimeout(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	jobType := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType})
	r := newRunner(t, pool, "w-timeout", time.Hour)
	if err := r.Register(jobType, 50*time.Millisecond, func(ctx context.Context, _ jobs.Job) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, true)
	row := fetch(t, pool, id)
	if row.Status != "pending" || row.LastError == nil || !strings.Contains(*row.LastError, "deadline exceeded") {
		t.Errorf("timeout not recorded as retryable error: %+v", row)
	}
}

func TestShutdownReturnsJobWithoutConsumingAttempt(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	jobType := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType, MaxAttempts: 1})
	r := newRunner(t, pool, "w-shutdown", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Register(jobType, time.Minute, func(ctx context.Context, _ jobs.Job) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if processed, err := r.RunOnce(ctx); err != nil || !processed {
		t.Fatalf("RunOnce: %v %v", processed, err)
	}
	row := fetch(t, pool, id)
	if row.Status != "pending" || row.Attempts != 0 || row.FutureSecs > 1 {
		t.Errorf("interrupted job = %+v, want pending, attempts 0, immediately available", row)
	}
}

func TestStaleProcessingJobIsReclaimed(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	jobType := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType})
	r := newRunner(t, pool, "w-new", 30*time.Minute)
	runs := 0
	if err := r.Register(jobType, time.Minute, func(context.Context, jobs.Job) error { runs++; return nil }); err != nil {
		t.Fatal(err)
	}

	// Locked recently by a live worker: not claimable.
	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET status='processing', attempts=1, locked_by='w-dead', locked_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, false)

	// Lock older than the lock timeout: reclaimed.
	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET locked_at=now() - interval '1 hour' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, true)
	row := fetch(t, pool, id)
	if row.Status != "completed" || row.Attempts != 2 || runs != 1 {
		t.Errorf("row=%+v runs=%d, want completed, attempts 2, 1 run", row, runs)
	}
}

func TestStaleJobWithExhaustedAttemptsFails(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	jobType := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType, MaxAttempts: 2})
	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET status='processing', attempts=2, locked_by='w-dead', locked_at=now() - interval '1 hour' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, pool, "w-new", 30*time.Minute)
	if err := r.Register(jobType, time.Minute, func(context.Context, jobs.Job) error {
		t.Error("handler must not run for an abandoned job with exhausted attempts")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, true)
	row := fetch(t, pool, id)
	if row.Status != "failed" || row.LastError == nil || *row.LastError != "attempts exhausted (abandoned)" || row.LockedBy != nil {
		t.Errorf("row = %+v", row)
	}
}

func TestCompletionGuardAfterReclaim(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	jobType := uniqueType(t, pool)
	id := mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType})

	newWorker := newRunner(t, pool, "w-new", 30*time.Minute)
	if err := newWorker.Register(jobType, time.Minute, func(context.Context, jobs.Job) error {
		return jobs.Permanent(errors.New("handled by new worker"))
	}); err != nil {
		t.Fatal(err)
	}

	oldWorker := newRunner(t, pool, "w-old", 30*time.Minute)
	if err := oldWorker.Register(jobType, time.Minute, func(context.Context, jobs.Job) error {
		// The old worker stalls: its lock expires and another worker takes over.
		if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET locked_at = now() - interval '1 hour' WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
		if processed, err := newWorker.RunOnce(ctx); err != nil || !processed {
			t.Errorf("reclaim: %v %v", processed, err)
		}
		return nil // late "success" must be discarded
	}); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, oldWorker, true)

	row := fetch(t, pool, id)
	if row.Status != "failed" || row.Attempts != 2 || row.LastError == nil || *row.LastError != "handled by new worker" {
		t.Errorf("old worker overwrote the reclaimed job: %+v", row)
	}
}

func TestConcurrentRunnersExecuteEachJobOnce(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	const workers, total = 8, 60
	jobType := uniqueType(t, pool)
	for i := 0; i < total; i++ {
		mustEnqueue(t, pool, jobs.EnqueueRequest{Type: jobType})
	}

	var mu sync.Mutex
	executed := map[string]int{}
	handler := func(_ context.Context, j jobs.Job) error {
		mu.Lock()
		executed[j.ID]++
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		r := newRunner(t, pool, "w-conc-"+string(rune('a'+w)), time.Hour)
		if err := r.Register(jobType, time.Minute, handler); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				processed, err := r.RunOnce(context.Background())
				if err != nil {
					t.Errorf("RunOnce: %v", err)
					return
				}
				if !processed {
					return
				}
			}
		}()
	}
	wg.Wait()

	if len(executed) != total {
		t.Fatalf("executed %d distinct jobs, want %d", len(executed), total)
	}
	for id, n := range executed {
		if n != 1 {
			t.Errorf("job %s executed %d times", id, n)
		}
	}
	var completed int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type=$1 AND status='completed'`, jobType).Scan(&completed); err != nil || completed != total {
		t.Errorf("completed = %d (err %v), want %d", completed, err, total)
	}
}

func TestEnsureScheduled(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	jobType := uniqueType(t, pool)
	s := jobs.Schedule{JobType: jobType, DedupeKey: jobType + ":sync", Payload: map[string]string{"trigger": "schedule"}, Interval: time.Hour, MaxAttempts: 3}

	ensure := func(want bool) {
		t.Helper()
		got, err := jobs.EnsureScheduled(ctx, pool, s)
		if err != nil || got != want {
			t.Fatalf("EnsureScheduled = %v, %v; want %v", got, err, want)
		}
	}
	ensure(true)
	ensure(false) // active (pending)

	r := newRunner(t, pool, "w-sched", time.Hour)
	if err := r.Register(jobType, time.Minute, func(context.Context, jobs.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	mustRunOnce(t, r, true)
	ensure(false) // finished, but created within the interval

	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET created_at = now() - interval '2 hours' WHERE job_type=$1`, jobType); err != nil {
		t.Fatal(err)
	}
	ensure(true) // interval elapsed

	var maxAttempts int
	if err := pool.QueryRow(ctx, `SELECT max_attempts FROM platform.jobs WHERE job_type=$1 AND status='pending'`, jobType).Scan(&maxAttempts); err != nil || maxAttempts != 3 {
		t.Errorf("max_attempts = %d, err %v", maxAttempts, err)
	}
}

func TestEnsureScheduledConcurrent(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	jobType := uniqueType(t, pool)
	s := jobs.Schedule{JobType: jobType, DedupeKey: jobType + ":sync", Interval: time.Hour}

	const callers = 12
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		enqueued int
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := jobs.EnsureScheduled(context.Background(), pool, s)
			if err != nil {
				t.Errorf("EnsureScheduled: %v", err)
				return
			}
			if ok {
				mu.Lock()
				enqueued++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if enqueued != 1 {
		t.Errorf("enqueued %d jobs, want 1", enqueued)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type=$1`, jobType).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d, err %v", n, err)
	}
}

func TestEnsureScheduledValidation(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	for _, s := range []jobs.Schedule{
		{DedupeKey: "k", Interval: time.Hour},
		{JobType: "t", Interval: time.Hour},
		{JobType: "t", DedupeKey: "k"},
	} {
		if _, err := jobs.EnsureScheduled(context.Background(), pool, s); err == nil {
			t.Errorf("EnsureScheduled(%+v) succeeded, want error", s)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	ok := func(context.Context, jobs.Job) error { return nil }
	r := jobs.NewRunner(pool, jobs.RunnerOptions{WorkerID: "w", LockTimeout: time.Minute}, quietLogger())

	if err := r.Register("a.b", 30*time.Second, ok); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
	tests := []struct {
		name    string
		jobType string
		timeout time.Duration
		h       jobs.Handler
	}{
		{"empty type", "", time.Second, ok},
		{"duplicate", "a.b", time.Second, ok},
		{"nil handler", "c.d", time.Second, nil},
		{"zero timeout", "c.d", 0, ok},
		{"negative timeout", "c.d", -time.Second, ok},
		{"timeout equals lock timeout", "c.d", time.Minute, ok},
		{"timeout exceeds lock timeout", "c.d", time.Hour, ok},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := r.Register(tt.jobType, tt.timeout, tt.h); err == nil {
				t.Error("want error")
			}
		})
	}
}

func TestAddScheduleValidation(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	r := newRunner(t, pool, "w", time.Hour)
	if err := r.Register("a.b", time.Second, func(context.Context, jobs.Job) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.AddSchedule(jobs.Schedule{JobType: "a.b", DedupeKey: "k", Interval: time.Hour}); err != nil {
		t.Fatalf("valid schedule: %v", err)
	}
	for _, s := range []jobs.Schedule{
		{JobType: "unregistered", DedupeKey: "k", Interval: time.Hour},
		{JobType: "a.b", Interval: time.Hour},
		{JobType: "a.b", DedupeKey: "k"},
	} {
		if err := r.AddSchedule(s); err == nil {
			t.Errorf("AddSchedule(%+v) succeeded, want error", s)
		}
	}
}

func TestRunSchedulesAndExecutesUntilCancelled(t *testing.T) {
	t.Parallel()
	pool := dbtest.Pool(t)
	jobType := uniqueType(t, pool)
	r := newRunner(t, pool, "w-run", time.Hour)
	done := make(chan struct{})
	var once sync.Once
	if err := r.Register(jobType, time.Minute, func(context.Context, jobs.Job) error {
		once.Do(func() { close(done) })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddSchedule(jobs.Schedule{JobType: jobType, DedupeKey: jobType + ":s", Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- r.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scheduled job was not executed")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}

	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type=$1`, jobType).Scan(&n); err != nil || n != 1 {
		t.Errorf("scheduled rows = %d, err %v, want exactly 1 within the interval", n, err)
	}
}
