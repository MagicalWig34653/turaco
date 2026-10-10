package transport

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type nameRes struct {
	types []string
	names map[string]string
}

func (n nameRes) Types() []string { return n.types }
func (n nameRes) Names(context.Context, []string) (map[string]string, error) {
	return n.names, nil
}

func uuid(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func principal(userID string, perms ...string) authorization.Authenticator {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fixedAuth{p: authorization.Principal{UserID: userID, Permissions: m}, ok: true}
}

func exportMux(t *testing.T, pool *pgxpool.Pool, auth authorization.Authenticator, opts ...Option) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, audit.NewReader(pool), auth, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	return mux
}

func seedEvents(t *testing.T, pool *pgxpool.Pool, pfx string, actor *string, targetID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	meta := `{}`
	if actor == nil {
		meta = `{"actor":"cli","osUser":"someone"}`
	}
	if _, err := pool.Exec(ctx, `INSERT INTO platform.audit_events (id, occurred_at, actor_id, action, target_type, target_id, correlation_id, metadata, before_data)
		VALUES (uuidv7(), $1, $2::uuid, $3, 'team', $4, $5, $6::jsonb, '{"secret":"x"}'::jsonb)`, at, actor, pfx+".changed", targetID, pfx+"-c", meta); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%' OR action LIKE $1 || '%' OR target_id LIKE $1 || '%'`, pfx)
	})
}

func rangeQuery(pfx string) string {
	from := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	to := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return "actionPrefix=" + pfx + "&from=" + from + "&to=" + to
}

func TestExportNeedsExportPermissionAndRange(t *testing.T) {
	pool := dbtest.Pool(t)
	user := uuid(t, pool)
	pfx := "zx" + user[len(user)-6:]
	viewOnly := exportMux(t, pool, principal(user, "platform.audit.view"))
	if rec := get(viewOnly, "/api/v1/audit-events/export.csv?"+rangeQuery(pfx)); rec.Code != 403 {
		t.Fatalf("view only: %d", rec.Code)
	}
	exp := exportMux(t, pool, principal(user, "platform.audit.view", "platform.audit.export"))
	if rec := get(exp, "/api/v1/audit-events/export.csv?actionPrefix="+pfx); rec.Code != 400 || !strings.Contains(rec.Body.String(), "audit.range_required") {
		t.Fatalf("no range: %d %s", rec.Code, rec.Body.String())
	}
	long := "from=" + time.Now().Add(-100*24*time.Hour).UTC().Format(time.RFC3339) + "&to=" + time.Now().UTC().Format(time.RFC3339)
	if rec := get(exp, "/api/v1/audit-events/export.csv?"+long); rec.Code != 400 || !strings.Contains(rec.Body.String(), "audit.range_too_long") {
		t.Fatalf("long range: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(exp, "/api/v1/audit-events/export.csv?module=Bad%20Mod&"+rangeQuery(pfx)); rec.Code != 400 {
		t.Fatalf("bad module: %d", rec.Code)
	}
}

func TestExportCSVContentNamesAndSafety(t *testing.T) {
	pool := dbtest.Pool(t)
	user := uuid(t, pool)
	pfx := "zc" + user[len(user)-6:]
	actor := uuid(t, pool)
	seedEvents(t, pool, pfx, &actor, "=HYPERLINK(\"http://evil\")", time.Now().Add(-time.Hour))
	seedEvents(t, pool, pfx, nil, "t2", time.Now().Add(-2*time.Hour))
	res := audit.NewResolvers(
		nameRes{types: []string{"user"}, names: map[string]string{actor: "=Anna Admin"}},
		nameRes{types: []string{"team"}, names: map[string]string{"t2": "Service Desk"}},
	)
	mux := exportMux(t, pool, principal(user, "platform.audit.view", "platform.audit.export"), WithResolvers(res))
	rec := get(mux, "/api/v1/audit-events/export.csv?"+rangeQuery(pfx))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("status %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\ufeff") {
		t.Fatal("missing BOM")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v\n%s", err, body)
	}
	if len(rows) != 3 || strings.Join(rows[0], ",") != "time,actor,actor_id,action,target_type,target,target_id,correlation_id,via" {
		t.Fatalf("rows: %v", rows)
	}
	for _, r := range rows[1:] {
		for _, c := range r {
			if strings.HasPrefix(strings.TrimLeft(c, " "), "=") {
				t.Errorf("formula cell not neutralized: %q", c)
			}
		}
	}
	joined := fmt.Sprint(rows)
	if !strings.Contains(joined, "'=Anna Admin") || !strings.Contains(joined, "system:cli") || !strings.Contains(joined, "Service Desk") {
		t.Errorf("names missing: %v", rows)
	}
	if strings.Contains(joined, "secret") || strings.Contains(joined, "someone") {
		t.Errorf("details leaked without includeDetails: %v", rows)
	}
	// The export itself is audited.
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.audit_events WHERE action = 'platform.audit.exported' AND actor_id = $1::uuid`, user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("export audit events = %d (%v)", n, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid`, user)
	})
	// includeDetails adds the three detail columns.
	rec = get(mux, "/api/v1/audit-events/export.csv?includeDetails=true&"+rangeQuery(pfx))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "before,after,metadata") || !strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("details: %d %s", rec.Code, rec.Body.String())
	}
}

func TestExportRateLimitAndSizeLimit(t *testing.T) {
	pool := dbtest.Pool(t)
	user := uuid(t, pool)
	pfx := "zr" + user[len(user)-6:]
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid OR action LIKE $2 || '%'`, user, pfx)
	})
	mux := exportMux(t, pool, principal(user, "platform.audit.view", "platform.audit.export"))

	// Too large: more than 10000 matching events answers 413 and writes no export event.
	if _, err := pool.Exec(ctx, `INSERT INTO platform.audit_events (id, occurred_at, action, target_type, target_id, correlation_id, metadata)
		SELECT uuidv7(), now() - (g || ' seconds')::interval, $1 || '.bulk', 'team', 't' || g, $1 || '-c', '{}'::jsonb FROM generate_series(1, 10001) g`, pfx); err != nil {
		t.Fatal(err)
	}
	if rec := get(mux, "/api/v1/audit-events/export.csv?"+rangeQuery(pfx)); rec.Code != 413 || !strings.Contains(rec.Body.String(), "audit.export_too_large") {
		t.Fatalf("too large: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'platform.audit.exported' AND actor_id = $1::uuid`, user).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused export must not be recorded as an export: %d", n)
	}

	// Rate limit: five exports in the last hour, the sixth is refused.
	small := "zs" + user[len(user)-6:]
	seedEvents(t, pool, small, nil, "t1", time.Now().Add(-time.Hour))
	for i := 0; i < audit.ExportsPerHour; i++ {
		if rec := get(mux, "/api/v1/audit-events/export.csv?"+rangeQuery(small)); rec.Code != 200 {
			t.Fatalf("export %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := get(mux, "/api/v1/audit-events/export.csv?"+rangeQuery(small))
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "audit.export_rate_limited") {
		t.Fatalf("sixth export: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListResolvedNamesAndFilters(t *testing.T) {
	pool := dbtest.Pool(t)
	user := uuid(t, pool)
	pfx := "zl" + user[len(user)-6:]
	actor := uuid(t, pool)
	seedEvents(t, pool, pfx, &actor, "t1", time.Now().Add(-time.Hour))
	seedEvents(t, pool, pfx, nil, "t-gone", time.Now().Add(-2*time.Hour))
	res := audit.NewResolvers(
		nameRes{types: []string{"user"}, names: map[string]string{actor: "Anna Admin"}},
		nameRes{types: []string{"team"}, names: map[string]string{"t1": "Service Desk"}},
	)
	mux := exportMux(t, pool, principal(user, "platform.audit.view"), WithResolvers(res), WithRetentionDays(730))
	rec := get(mux, "/api/v1/audit-events?actionPrefix="+pfx)
	body := rec.Body.String()
	for _, want := range []string{`"text":"Anna Admin"`, `"text":"Service Desk"`, `"gone":true`, `"systemActor":"cli"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	win := "&from=" + time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339) + "&to=" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if rec := get(mux, "/api/v1/audit-events?actionPrefix="+pfx+"&systemActor=cli"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "audit.range_required") {
		t.Errorf("systemActor without a range: %d %s", rec.Code, rec.Body.String())
	}
	for q, wantCount := range map[string]int{"&actorKind=user": 1, "&actorKind=system": 1, "&systemActor=cli" + win: 1, "&systemActor=nobody" + win: 0} {
		rec := get(mux, "/api/v1/audit-events?actionPrefix="+pfx+q)
		if got := strings.Count(rec.Body.String(), `"correlationId"`); rec.Code != 200 || got != wantCount {
			t.Errorf("%s: code %d count %d want %d (%s)", q, rec.Code, got, wantCount, rec.Body.String())
		}
	}
	if rec := get(mux, "/api/v1/audit-events?actorKind=robot"); rec.Code != 400 {
		t.Errorf("bad actorKind: %d", rec.Code)
	}
	if rec := get(mux, "/api/v1/audit-events?via=other"); rec.Code != 400 {
		t.Errorf("bad via: %d", rec.Code)
	}
	if rec := get(mux, "/api/v1/audit-events?module="+pfx); rec.Code != 200 {
		t.Errorf("module filter: %d", rec.Code)
	}
	rec = get(mux, "/api/v1/audit-events/retention")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"retentionDays":730`) || !strings.Contains(rec.Body.String(), `"policy":"purge"`) || !strings.Contains(rec.Body.String(), "oldestEventAt") {
		t.Errorf("retention: %d %s", rec.Code, rec.Body.String())
	}
	// Viewers without audit.view get nothing, including the retention route.
	noView := exportMux(t, pool, principal(user, "platform.roles.view"))
	for _, p := range []string{"/api/v1/audit-events/retention", "/api/v1/audit-events/export.csv"} {
		rec := httptest.NewRecorder()
		noView.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 403 {
			t.Errorf("%s without audit.view: %d", p, rec.Code)
		}
	}
}

// Parallel requests must not all pass the per-hour check: the limit is enforced under a per-user lock.
func TestExportRateLimitHoldsUnderParallelRequests(t *testing.T) {
	pool := dbtest.Pool(t)
	user := uuid(t, pool)
	pfx := "zp" + user[len(user)-6:]
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = $1::uuid OR action LIKE $2 || '%'`, user, pfx)
	})
	seedEvents(t, pool, pfx, nil, "t1", time.Now().Add(-time.Hour))
	mux := exportMux(t, pool, principal(user, "platform.audit.view", "platform.audit.export"))
	const parallel = 12
	codes := make(chan int, parallel)
	for i := 0; i < parallel; i++ {
		go func() { codes <- get(mux, "/api/v1/audit-events/export.csv?"+rangeQuery(pfx)).Code }()
	}
	ok, limited := 0, 0
	for i := 0; i < parallel; i++ {
		switch c := <-codes; c {
		case 200:
			ok++
		case 429:
			limited++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != audit.ExportsPerHour || limited != parallel-audit.ExportsPerHour {
		t.Fatalf("ok=%d limited=%d, want %d and %d", ok, limited, audit.ExportsPerHour, parallel-audit.ExportsPerHour)
	}
}
