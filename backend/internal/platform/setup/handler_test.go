package setup

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fakeGranter struct {
	mu     sync.Mutex
	exists bool
	err    error
	calls  atomic.Int32
	last   Grant
	pool   *pgxpool.Pool
}

func (g *fakeGranter) AdministratorExists(context.Context) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.exists, nil
}

func (g *fakeGranter) Grant(ctx context.Context, in Grant) (Result, error) {
	g.calls.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exists {
		return Result{}, ErrAdministratorExists
	}
	if g.err != nil {
		return Result{}, g.err
	}
	// Mirrors wiring.FirstAdministrator: the token is consumed under the lock before the change.
	if in.TokenHash != nil {
		tag, err := g.pool.Exec(ctx, `UPDATE platform.setup_tokens SET used_at = now()
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`, in.TokenHash)
		if err != nil {
			return Result{}, err
		}
		if tag.RowsAffected() != 1 {
			return Result{}, ErrTokenInvalid
		}
	}
	g.last = in
	g.exists = true
	return Result{UserID: "u", AssignmentID: "a"}, nil
}

type countingLimiter struct {
	mu    sync.Mutex
	count map[string]int
}

func (l *countingLimiter) Reserve(_ context.Context, key string, limit int) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count[key]++
	return l.count[key] <= limit, time.Minute, nil
}

func newHandler(t *testing.T, g *fakeGranter) (*Handler, *Store, http.Handler) {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	// Open tokens of other tests would make the page "available"; start clean.
	if _, err := pool.Exec(ctx, `UPDATE platform.setup_tokens SET used_at = now() WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	st := NewStore(pool)
	g.pool = pool
	h := &Handler{Store: st, Granter: g, Limiter: &countingLimiter{count: map[string]int{}}, AllowedTenants: []string{"22222222-2222-2222-2222-222222222222"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	h.Register(mux)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE platform.setup_tokens SET used_at = now() WHERE used_at IS NULL`)
	})
	return h, st, mux
}

func post(mux http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/first-administrator", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.test")
	r.Host = "example.test"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func issue(t *testing.T, st *Store, ttl time.Duration) string {
	t.Helper()
	tok, _, err := st.Issue(context.Background(), audit.SystemActor("setup-test"), "corr", ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestFirstAdministratorWithToken(t *testing.T) {
	g := &fakeGranter{}
	_, st, mux := newHandler(t, g)
	tok := issue(t, st, time.Minute)

	// Wrong token, no token: uniform closed answer, no grant.
	for _, body := range []string{`{"token":"WRONG","user":"alice"}`, `{"user":"alice"}`} {
		w := post(mux, body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), codeClosed) {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if g.calls.Load() != 0 {
		t.Fatal("grant called without a valid token")
	}
	if w := post(mux, `{"token":"`+tok+`","user":"alice","tenant":"x","object":"y"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad guid: %d", w.Code)
	}
	const guid = "11111111-1111-1111-1111-111111111111"
	if w := post(mux, `{"token":"`+tok+`","user":"alice","tenant":"`+guid+`","object":"`+guid+`"}`); w.Code != http.StatusUnprocessableEntity || g.calls.Load() != 0 {
		t.Fatalf("tenant outside the allow-list: %d, calls %d", w.Code, g.calls.Load())
	}
	if w := post(mux, `{"token":"`+tok+`","user":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", w.Code, w.Body.String())
	}
	if g.last.Actor.System != "setup-wizard" || g.last.Via != "web" || g.last.Force {
		t.Fatalf("grant = %+v", g.last)
	}
	// The token is single use and the page is closed now.
	g.exists = false
	if w := post(mux, `{"token":"`+tok+`","user":"bob"}`); w.Code != http.StatusForbidden {
		t.Fatalf("reuse: %d", w.Code)
	}
}

func TestTokenSurvivesRefusedUserAndIsConsumedOnGrant(t *testing.T) {
	g := &fakeGranter{}
	_, st, mux := newHandler(t, g)
	tok := issue(t, st, time.Minute)
	if w := post(mux, `{"token":"`+tok+`","user":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("grant: %d", w.Code)
	}
	if ok, err := st.Valid(context.Background(), tok); err != nil || ok {
		t.Fatalf("token still valid after the grant: %v, %v", ok, err)
	}
}

func TestSetupClosedWhenAdministratorExists(t *testing.T) {
	g := &fakeGranter{exists: true}
	_, st, mux := newHandler(t, g)
	tok := issue(t, st, time.Minute)
	if w := post(mux, `{"token":"`+tok+`","user":"alice"}`); w.Code != http.StatusForbidden || g.calls.Load() != 0 {
		t.Fatalf("%d, calls %d", w.Code, g.calls.Load())
	}
	r := httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if !strings.Contains(r.Body.String(), `"available":false`) {
		t.Fatalf("status = %s", r.Body.String())
	}
}

func TestExpiredAndReplacedToken(t *testing.T) {
	g := &fakeGranter{}
	_, st, mux := newHandler(t, g)
	pool := dbtest.Pool(t)
	old := issue(t, st, time.Minute)
	fresh := issue(t, st, time.Minute) // issuing revokes earlier open tokens
	if w := post(mux, `{"token":"`+old+`","user":"alice"}`); w.Code != http.StatusForbidden {
		t.Fatalf("replaced token: %d", w.Code)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE platform.setup_tokens SET expires_at = now() - interval '1 second', created_at = now() - interval '1 hour' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if w := post(mux, `{"token":"`+fresh+`","user":"alice"}`); w.Code != http.StatusForbidden || g.calls.Load() != 0 {
		t.Fatalf("expired: %d", w.Code)
	}
}

func TestUserErrorsKeepTokenUsable(t *testing.T) {
	g := &fakeGranter{err: ErrUserNotFound}
	_, st, mux := newHandler(t, g)
	tok := issue(t, st, time.Minute)
	if w := post(mux, `{"token":"`+tok+`","user":"nobody"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("not found: %d", w.Code)
	}
	g.mu.Lock()
	g.err = ErrLocalAccount
	g.mu.Unlock()
	if w := post(mux, `{"token":"`+tok+`","user":"local"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("local: %d", w.Code)
	}
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	if w := post(mux, `{"token":"`+tok+`","user":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("retry: %d", w.Code)
	}
}

func TestConcurrentAttemptsGrantOnce(t *testing.T) {
	g := &fakeGranter{}
	_, st, mux := newHandler(t, g)
	tok := issue(t, st, time.Minute)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if post(mux, `{"token":"`+tok+`","user":"alice"}`).Code == http.StatusOK {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("successful grants = %d, want 1", ok.Load())
	}
}

func TestRateLimit(t *testing.T) {
	g := &fakeGranter{}
	_, _, mux := newHandler(t, g)
	var limited bool
	for i := 0; i < clientAttemptLimit+2; i++ {
		if post(mux, `{"token":"x","user":"a"}`).Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("no 429 after the client limit")
	}
}

func TestIssueRejectsLongTTL(t *testing.T) {
	st := NewStore(dbtest.Pool(t))
	if _, _, err := st.Issue(context.Background(), audit.SystemActor("setup-test"), "c", 48*time.Hour); err == nil {
		t.Fatal("ttl above the maximum accepted")
	}
}
