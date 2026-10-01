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

// failures returns the attempts counter of key and whether it is locked.
func failures(t *testing.T, pool *pgxpool.Pool, key string) (n int, locked bool) {
	t.Helper()
	var until *time.Time
	err := pool.QueryRow(context.Background(), `SELECT attempts, locked_until FROM platform.auth_throttle WHERE key = $1`, key).Scan(&n, &until)
	if err != nil {
		return 0, false
	}
	return n, until != nil
}

// shiftBack moves a counter d into the past (the throttle uses database
// time, so tests age rows instead of advancing a clock).
func shiftBack(t *testing.T, pool *pgxpool.Pool, key string, d time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE platform.auth_throttle SET
		  window_started_at = window_started_at - $2::bigint * interval '1 microsecond',
		  locked_until = locked_until - $2::bigint * interval '1 microsecond',
		  updated_at = updated_at - $2::bigint * interval '1 microsecond'
		WHERE key = $1`, key, d.Microseconds())
	if err != nil {
		t.Fatal(err)
	}
}

func reserveN(t *testing.T, th *Throttle, key string, limit, n int) (allowed int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ok, _, err := th.Reserve(context.Background(), key, limit)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			allowed++
		}
	}
	return allowed
}

func TestIdentifierKey(t *testing.T) {
	base := IdentifierKey("alice")
	for _, v := range []string{"Alice", "ALICE", " alice ", "X\\alice", "x\\ alice", `CORP\ Alice `} {
		if IdentifierKey(v) != base {
			t.Fatalf("%q must share the key of alice", v)
		}
	}
	if len(base) != len("id:")+64 || base[:3] != "id:" {
		t.Fatalf("key = %q", base)
	}
	if IdentifierKey("alice") == IdentifierKey("bob") || IdentifierKey("alice@example.test") == base {
		t.Fatal("distinct identifiers collide")
	}
	if NormalizeIdentifier(`X\ Alice `) != "alice" {
		t.Fatal("normalization")
	}
	if AccountKey("0190A000-0000-7000-8000-000000000001") != "acct:0190a000-0000-7000-8000-000000000001" {
		t.Fatal("account key")
	}
}

func TestClientKeyBuckets(t *testing.T) {
	tests := []struct{ ip, want string }{
		{"203.0.113.7", "ip:203.0.113.7"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "ip:2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "ip:2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "ip:2001:db8:1:3::/64"},
		{"::ffff:203.0.113.7", "ip:203.0.113.7"},
		{"unknown", "ip:unknown"},
	}
	for _, tt := range tests {
		if got := ClientKey(tt.ip); got != tt.want {
			t.Errorf("ClientKey(%q) = %q, want %q", tt.ip, got, tt.want)
		}
	}
	if ClientKey("2001:db8:1:2::1") != ClientKey("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Fatal("one /64 must be one bucket")
	}
}

func TestReserveLocksAboveLimit(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	if got := reserveN(t, th, key, 5, 5); got != 5 {
		t.Fatalf("allowed = %d, want 5", got)
	}
	ok, retry, err := th.Reserve(ctx, key, 5)
	if err != nil || ok {
		t.Fatalf("6th attempt: allowed=%v err=%v", ok, err)
	}
	if retry < 14*time.Minute+50*time.Second || retry > 15*time.Minute {
		t.Fatalf("retry = %v, want about 15m", retry)
	}
	if n, locked := failures(t, pool, key); n != 6 || !locked {
		t.Fatalf("attempts=%d locked=%v, want 6 locked", n, locked)
	}
}

func TestReserveLockedKeyIsNotCountedOrExtended(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	reserveN(t, th, key, 5, 6)
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT locked_until FROM platform.auth_throttle WHERE key = $1`, key).Scan(&before); err != nil {
		t.Fatal(err)
	}
	shiftBack(t, pool, key, 10*time.Minute)
	if err := pool.QueryRow(ctx, `SELECT locked_until FROM platform.auth_throttle WHERE key = $1`, key).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ok, retry, _ := th.Reserve(ctx, key, 5)
	if ok || retry > 5*time.Minute || retry < 4*time.Minute+50*time.Second {
		t.Fatalf("allowed=%v retry=%v, want the original lock to end in about 5m", ok, retry)
	}
	var after time.Time
	var n int
	_ = pool.QueryRow(ctx, `SELECT locked_until, attempts FROM platform.auth_throttle WHERE key = $1`, key).Scan(&after, &n)
	if !after.Equal(before) || n != 6 {
		t.Fatalf("locked_until moved from %v to %v, attempts=%d", before, after, n)
	}
}

func TestReserveWindowExpiryRestartsCount(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	key := throttleKey(t, pool, "id:")
	reserveN(t, th, key, 5, 4)
	shiftBack(t, pool, key, 15*time.Minute+time.Second)
	if ok, _, _ := th.Reserve(context.Background(), key, 5); !ok {
		t.Fatal("refused in a new window")
	}
	if n, locked := failures(t, pool, key); n != 1 || locked {
		t.Fatalf("attempts=%d locked=%v, want 1 unlocked (old window expired)", n, locked)
	}
	if got := reserveN(t, th, key, 5, 4); got != 4 {
		t.Fatalf("allowed = %d, want 4 more in the new window", got)
	}
}

func TestReserveAttemptsWithinWindowAccumulate(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	key := throttleKey(t, pool, "id:")
	for i := 0; i < 5; i++ {
		reserveN(t, th, key, 5, 1)
		shiftBack(t, pool, key, 2*time.Minute) // 8 minutes in total, inside the window
	}
	if ok, _, _ := th.Reserve(context.Background(), key, 5); ok {
		t.Fatal("the 6th attempt within 15 minutes must be refused")
	}
}

func TestReserveLockExpiryRestarts(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	reserveN(t, th, key, 5, 6)
	shiftBack(t, pool, key, 15*time.Minute-time.Second)
	if ok, _, _ := th.Reserve(ctx, key, 5); ok {
		t.Fatal("still locked just before expiry")
	}
	shiftBack(t, pool, key, 2*time.Second)
	if ok, _, _ := th.Reserve(ctx, key, 5); !ok {
		t.Fatal("lock must expire")
	}
	if n, locked := failures(t, pool, key); n != 1 || locked {
		t.Fatalf("attempts=%d locked=%v, want a fresh count after the lock expired", n, locked)
	}
}

func TestReserveLimitOne(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	key := throttleKey(t, pool, "id:")
	if got := reserveN(t, th, key, 1, 3); got != 1 {
		t.Fatalf("allowed = %d, want 1", got)
	}
	// Limit 0 refuses even the first attempt.
	zero := throttleKey(t, pool, "id:")
	if got := reserveN(t, th, zero, 0, 1); got != 0 {
		t.Fatal("limit 0 must refuse")
	}
}

func TestReserveClientAndIdentifierLimits(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	id, ip := throttleKey(t, pool, "id:"), throttleKey(t, pool, "ip:")
	ctx := context.Background()
	allowedID, allowedIP := 0, 0
	for i := 0; i < 40; i++ {
		if ok, _, _ := th.ReserveIdentifier(ctx, id); ok {
			allowedID++
		}
		if ok, _, _ := th.ReserveClient(ctx, ip); ok {
			allowedIP++
		}
	}
	if allowedID != 5 || allowedIP != 30 {
		t.Fatalf("allowed identifier=%d client=%d, want 5 and 30", allowedID, allowedIP)
	}
}

// Concurrent reservations are all counted and exactly limit are allowed: the
// upsert is atomic (no check-then-act gap).
func TestReserveConcurrentIsExact(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	run := func(key string, limit, n int) (allowed int) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, _, err := th.Reserve(ctx, key, limit)
				if err != nil {
					t.Error(err)
				}
				if ok {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		return allowed
	}
	open := throttleKey(t, pool, "id:")
	if got := run(open, 1000, 40); got != 40 {
		t.Fatalf("allowed = %d, want 40", got)
	}
	if n, locked := failures(t, pool, open); n != 40 || locked {
		t.Fatalf("attempts=%d locked=%v, want 40 unlocked", n, locked)
	}
	tight := throttleKey(t, pool, "id:")
	if got := run(tight, 5, 60); got != 5 {
		t.Fatalf("allowed = %d, want exactly 5 of 60", got)
	}
}

func TestClearAndRefund(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	key := throttleKey(t, pool, "id:")
	reserveN(t, th, key, 5, 3)
	if err := th.Refund(ctx, key); err != nil {
		t.Fatal(err)
	}
	if n, _ := failures(t, pool, key); n != 2 {
		t.Fatalf("attempts after refund = %d", n)
	}
	for i := 0; i < 5; i++ {
		_ = th.Refund(ctx, key)
	}
	if n, _ := failures(t, pool, key); n != 0 {
		t.Fatalf("refund went below zero: %d", n)
	}
	if err := th.Refund(ctx, key+"-absent"); err != nil {
		t.Fatalf("refunding an absent key: %v", err)
	}
	// A locked key is not unlocked by a refund.
	reserveN(t, th, key, 5, 6)
	_ = th.Refund(ctx, key)
	if _, locked := failures(t, pool, key); !locked {
		t.Fatal("refund must not touch a locked key")
	}
	if err := th.Clear(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := th.Reserve(ctx, key, 5); !ok {
		t.Fatal("cleared key still locked")
	}
	if err := th.Clear(ctx, key+"-absent"); err != nil {
		t.Fatalf("clearing an absent key: %v", err)
	}
}

func TestThrottlePrunesOldRowsInBoundedBatches(t *testing.T) {
	th, _, pool := newTestThrottle(t)
	ctx := context.Background()
	prefix := "id:zt-prune-" + randHex(6) + "-"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.auth_throttle WHERE key LIKE $1`, prefix+"%")
	})
	// A row older than the retention (by database time) and fresh ones.
	_, err := pool.Exec(ctx, `
		INSERT INTO platform.auth_throttle (key, attempts, window_started_at, updated_at)
		SELECT $1 || g, 1, now() - interval '26 hours', now() - interval '25 hours' FROM generate_series(1, $2) g`,
		prefix, pruneBatch+20)
	if err != nil {
		t.Fatal(err)
	}
	other := throttleKey(t, pool, "id:")
	reserveN(t, th, other, 5, 1) // triggers the first prune
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.auth_throttle WHERE key LIKE $1`, prefix+"%").Scan(&left)
	if left != 20 {
		t.Fatalf("%d old rows left, want 20 (one batch of %d pruned)", left, pruneBatch)
	}
	if n, _ := failures(t, pool, other); n != 1 {
		t.Fatal("fresh row must remain")
	}
}
