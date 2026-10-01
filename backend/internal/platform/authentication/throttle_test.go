package authentication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type syncClock struct {
	mu sync.Mutex
	t  time.Time
}

func newSyncClock() *syncClock { return &syncClock{t: time.Now().UTC().Truncate(time.Microsecond)} }
func (c *syncClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *syncClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newTestThrottle(t *testing.T) (*Throttle, *syncClock, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.Pool(t)
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('platform.auth_throttle') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		dbtest.Unavailable(t, "platform.auth_throttle missing; run make migrate")
	}
	clk := newSyncClock()
	return NewThrottle(pool, ThrottleConfig{}, clk.now), clk, pool
}

// throttleKey returns a unique key and removes it after the test.
func throttleKey(t *testing.T, pool *pgxpool.Pool, prefix string) string {
	t.Helper()
	key := prefix + "zt-" + randHex(8)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.auth_throttle WHERE key = $1`, key)
	})
	return key
}

func failures(t *testing.T, pool *pgxpool.Pool, key string) (n int, locked bool) {
	t.Helper()
	var until *time.Time
	err := pool.QueryRow(context.Background(), `SELECT failures, locked_until FROM platform.auth_throttle WHERE key = $1`, key).Scan(&n, &until)
	if err != nil {
		return 0, false
	}
	return n, until != nil
}

func TestIdentifierKey(t *testing.T) {
	a, b := IdentifierKey("Alice@Example.Test"), IdentifierKey("alice@example.test")
	if a != b {
		t.Fatal("key must be case-insensitive")
	}
	if len(a) != len("id:")+64 || a[:3] != "id:" {
		t.Fatalf("key = %q", a)
	}
	if IdentifierKey("alice") == IdentifierKey("bob") {
		t.Fatal("collision")
	}
	if ClientKey("203.0.113.7") != "ip:203.0.113.7" {
		t.Fatal("client key")
	}
}

func TestThrottleLocksAtLimit(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 1; i <= 4; i++ {
		if err := th.RecordFailure(ctx, key, 5); err != nil {
			t.Fatal(err)
		}
		if _, locked, err := th.Check(ctx, key); err != nil || locked {
			t.Fatalf("after %d failures: locked=%v err=%v", i, locked, err)
		}
	}
	if err := th.RecordFailure(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	retry, locked, err := th.Check(ctx, key)
	if err != nil || !locked {
		t.Fatalf("locked=%v err=%v", locked, err)
	}
	if retry != 15*time.Minute {
		t.Fatalf("retry = %v, want 15m", retry)
	}
}

func TestThrottleCheckReturnsLongestLock(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	k1, k2, k3 := throttleKey(t, pool, "id:"), throttleKey(t, pool, "ip:"), throttleKey(t, pool, "id:")
	_ = th.RecordFailure(ctx, k1, 1)
	clk.advance(5 * time.Minute)
	_ = th.RecordFailure(ctx, k2, 1)
	retry, locked, err := th.Check(ctx, k1, k2, k3)
	if err != nil || !locked || retry != 15*time.Minute {
		t.Fatalf("retry=%v locked=%v err=%v", retry, locked, err)
	}
	if _, locked, _ := th.Check(ctx, k3); locked {
		t.Fatal("unrelated key locked")
	}
}

func TestThrottleWindowExpiryRestartsCount(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 4; i++ {
		_ = th.RecordFailure(ctx, key, 5)
	}
	clk.advance(15*time.Minute + time.Second)
	_ = th.RecordFailure(ctx, key, 5)
	if n, locked := failures(t, pool, key); n != 1 || locked {
		t.Fatalf("failures=%d locked=%v, want 1 unlocked (old window expired)", n, locked)
	}
	for i := 0; i < 3; i++ {
		_ = th.RecordFailure(ctx, key, 5)
	}
	if _, locked, _ := th.Check(ctx, key); locked {
		t.Fatal("4 failures in the new window must not lock")
	}
}

func TestThrottleFailuresWithinWindowAccumulate(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 4; i++ {
		_ = th.RecordFailure(ctx, key, 5)
		clk.advance(3 * time.Minute) // 12 minutes in total, inside the window
	}
	_ = th.RecordFailure(ctx, key, 5) // 5th failure at +12m
	if _, locked, _ := th.Check(ctx, key); !locked {
		t.Fatal("5 failures within 15 minutes must lock")
	}
}

func TestThrottleLockExpiryAndRestart(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 5; i++ {
		_ = th.RecordFailure(ctx, key, 5)
	}
	clk.advance(15*time.Minute - time.Second)
	if _, locked, _ := th.Check(ctx, key); !locked {
		t.Fatal("still locked just before expiry")
	}
	clk.advance(2 * time.Second)
	if _, locked, _ := th.Check(ctx, key); locked {
		t.Fatal("lock must expire")
	}
	_ = th.RecordFailure(ctx, key, 5)
	if n, locked := failures(t, pool, key); n != 1 || locked {
		t.Fatalf("failures=%d locked=%v, want a fresh count after the lock expired", n, locked)
	}
}

func TestThrottleLockedKeyIsNotExtended(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 5; i++ {
		_ = th.RecordFailure(ctx, key, 5)
	}
	clk.advance(10 * time.Minute)
	_ = th.RecordFailure(ctx, key, 5) // raced past Check
	retry, locked, _ := th.Check(ctx, key)
	if !locked || retry != 5*time.Minute {
		t.Fatalf("retry=%v locked=%v, want the original lock to end in 5m", retry, locked)
	}
}

func TestThrottleClear(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 5; i++ {
		_ = th.RecordFailure(ctx, key, 5)
	}
	if err := th.Clear(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, locked, _ := th.Check(ctx, key); locked {
		t.Fatal("cleared key still locked")
	}
	if err := th.Clear(ctx, key); err != nil {
		t.Fatalf("clearing an absent key: %v", err)
	}
}

func TestThrottleLimitOne(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	_ = th.RecordFailure(ctx, key, 1)
	if _, locked, _ := th.Check(ctx, key); !locked {
		t.Fatal("limit 1 locks on the first failure")
	}
}

func TestThrottleRecordFailuresUsesBothLimits(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	id, ip := throttleKey(t, pool, "id:"), throttleKey(t, pool, "ip:")
	for i := 0; i < 5; i++ {
		_ = th.RecordFailures(ctx, id, ip)
	}
	if _, locked := failures(t, pool, id); !locked {
		t.Fatal("identifier must lock at 5")
	}
	if n, locked := failures(t, pool, ip); n != 5 || locked {
		t.Fatalf("client failures=%d locked=%v, want 5 unlocked (limit 30)", n, locked)
	}
	for i := 0; i < 25; i++ {
		_ = th.RecordFailures(ctx, id, ip)
	}
	if _, locked := failures(t, pool, ip); !locked {
		t.Fatal("client must lock at 30")
	}
}

// Concurrent failures must all be counted: the upsert is atomic.
func TestThrottleConcurrentFailuresAreAllCounted(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- th.RecordFailure(ctx, key, 1000)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, locked := failures(t, pool, key); got != n || locked {
		t.Fatalf("failures=%d locked=%v, want %d unlocked", got, locked, n)
	}
}

func TestThrottleConcurrentFailuresLockExactlyOnce(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = th.RecordFailure(ctx, key, 5)
		}()
	}
	wg.Wait()
	retry, locked, err := th.Check(ctx, key)
	if err != nil || !locked || retry != 15*time.Minute {
		t.Fatalf("retry=%v locked=%v err=%v", retry, locked, err)
	}
	if got, _ := failures(t, pool, key); got != 20 {
		t.Fatalf("failures = %d, want 20", got)
	}
}

func TestThrottlePrunesOldRows(t *testing.T) {
	th, clk, pool := newTestThrottle(t)
	ctx := context.Background()
	old := throttleKey(t, pool, "id:")
	_ = th.RecordFailure(ctx, old, 5)
	clk.advance(25 * time.Hour)
	other := throttleKey(t, pool, "id:")
	_ = th.RecordFailure(ctx, other, 5) // triggers the (first) prune
	if n, _ := failures(t, pool, old); n != 0 {
		t.Fatal("row older than 24h must be pruned")
	}
	if n, _ := failures(t, pool, other); n != 1 {
		t.Fatal("fresh row must remain")
	}
}
