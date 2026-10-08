package modules_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// setup returns a registry over a clean module_switches table (every module at its default).
func setup(t *testing.T, pre map[string]modules.Precondition) (*modules.Service, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM platform.module_switches`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM platform.module_switches`) })
	return modules.NewService(pool, modules.Options{Preconditions: pre, CacheTTL: time.Millisecond}), pool
}

func admin() modules.Caller {
	return modules.Caller{UserID: newID(), CorrelationID: "req-" + newID(), CanManage: true}
}

func ver(v int) *int { return &v }

func info(t *testing.T, s *modules.Service, key string) modules.Info {
	t.Helper()
	list, err := s.List(context.Background(), admin())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range list {
		if in.Module.Key == key {
			return in
		}
	}
	t.Fatalf("module %s missing", key)
	return modules.Info{}
}

func TestDefaultsAreUpgradeSafe(t *testing.T) {
	s, _ := setup(t, nil)
	for _, m := range modules.Catalog() {
		in := info(t, s, m.Key)
		want := m.Core || m.DefaultEnabled
		if in.Enabled != want || in.Version != 0 {
			t.Errorf("%s: enabled=%v version=%d, want enabled=%v version=0", m.Key, in.Enabled, in.Version, want)
		}
	}
	for _, key := range []string{"presence", "ai"} {
		if info(t, s, key).Enabled {
			t.Errorf("%s must default to off", key)
		}
	}
	for _, key := range []string{"servicedesk", "endpoints", "changes", "remoteaccess", "briefing"} {
		if !info(t, s, key).Enabled {
			t.Errorf("%s must default to on so an upgrade changes nothing", key)
		}
	}
}

func TestMigrationKeepsRunningPresenceAndAI(t *testing.T) {
	s, pool := setup(t, nil)
	ctx := context.Background()
	var presenceOn, aiOn bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM presence.settings`).Scan(&presenceOn); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT enabled FROM ai.settings`).Scan(&aiOn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE presence.settings SET enabled=$1`, presenceOn)
		_, _ = pool.Exec(context.Background(), `UPDATE ai.settings SET enabled=$1`, aiOn)
	})
	if _, err := pool.Exec(ctx, `UPDATE presence.settings SET enabled=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai.settings SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000059_module_switches.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if p := info(t, s, "presence"); !p.SwitchOn || p.ReasonCode != "upgrade_default" || p.Version != 1 {
		t.Errorf("presence was running before the upgrade and must stay on: %+v", p)
	}
	if a := info(t, s, "ai"); a.SwitchOn || a.Version != 1 {
		t.Errorf("ai was off before the upgrade and must stay off: %+v", a)
	}
}

func TestDisableEnableLifecycleAndAudit(t *testing.T) {
	s, pool := setup(t, nil)
	ctx := context.Background()
	c := admin()

	got, err := s.Disable(ctx, c, "briefing", "not_needed", ver(0))
	if err != nil || got.Enabled || got.State != modules.StateDisabled || got.Version != 1 || got.ReasonCode != "not_needed" || got.ChangedBy == nil || *got.ChangedBy != c.UserID {
		t.Fatalf("disable = %+v, %v", got, err)
	}
	if on, err := s.Enabled(ctx, "briefing"); err != nil || on {
		t.Fatalf("Enabled after disable = %v, %v (changes made through the service must be visible at once)", on, err)
	}
	if _, err := s.Disable(ctx, c, "briefing", "not_needed", ver(1)); !errors.Is(err, modules.ErrNoChange) {
		t.Errorf("second disable err = %v, want ErrNoChange", err)
	}
	if _, err := s.Enable(ctx, c, "briefing", "business_need", ver(0)); !errors.Is(err, modules.ErrVersionConflict) {
		t.Errorf("stale version err = %v, want ErrVersionConflict", err)
	}
	got, err = s.Enable(ctx, c, "briefing", "business_need", ver(1))
	if err != nil || !got.Enabled || got.Version != 2 {
		t.Fatalf("enable = %+v, %v", got, err)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM platform.audit_events
		WHERE target_type='module' AND target_id='briefing' AND actor_id=$1::uuid AND correlation_id=$2
		  AND action IN ('platform.module.disabled','platform.module.enabled') AND metadata->>'reasonCode' IS NOT NULL`,
		c.UserID, c.CorrelationID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("audit events = %d, want 2 (disable and enable, each with a reason code)", n)
	}
	var before, after bool
	if err := pool.QueryRow(ctx, `SELECT (before_data->>'enabled')::bool, (after_data->>'enabled')::bool FROM platform.audit_events
		WHERE action='platform.module.disabled' AND target_id='briefing' AND actor_id=$1::uuid`, c.UserID).Scan(&before, &after); err != nil || !before || after {
		t.Errorf("audit before/after = %v/%v, %v", before, after, err)
	}
}

func TestInputAndAuthorization(t *testing.T) {
	s, pool := setup(t, nil)
	ctx := context.Background()
	c := admin()
	var inv *modules.InvalidError
	if _, err := s.Disable(ctx, c, "briefing", "because", ver(0)); !errors.As(err, &inv) {
		t.Errorf("unknown reason code err = %v", err)
	}
	if _, err := s.Disable(ctx, c, "briefing", "upgrade_default", ver(0)); !errors.As(err, &inv) {
		t.Errorf("migration-only reason code must be refused, err = %v", err)
	}
	if _, err := s.Disable(ctx, c, "briefing", "not_needed", nil); !errors.As(err, &inv) {
		t.Errorf("missing expectedVersion err = %v", err)
	}
	if _, err := s.Disable(ctx, c, "no_such_module", "not_needed", ver(0)); !errors.Is(err, modules.ErrNotFound) {
		t.Errorf("unknown module err = %v", err)
	}
	for _, key := range []string{"organization", "access", "audit", "tasks", "notifications", "approvals", "platform"} {
		if _, err := s.Disable(ctx, c, key, "not_needed", ver(0)); !errors.Is(err, modules.ErrNotSwitchable) {
			t.Errorf("core %s err = %v, want ErrNotSwitchable", key, err)
		}
	}
	denied := modules.Caller{UserID: newID(), CorrelationID: "r", CanManage: false}
	if _, err := s.Disable(ctx, denied, "briefing", "not_needed", ver(0)); !errors.Is(err, modules.ErrForbidden) {
		t.Errorf("caller without modules.manage err = %v", err)
	}
	if _, err := s.List(ctx, denied); !errors.Is(err, modules.ErrForbidden) {
		t.Errorf("list without modules.manage err = %v", err)
	}
	if _, err := s.Disable(ctx, modules.Caller{CanManage: true, CorrelationID: "r"}, "briefing", "not_needed", ver(0)); !errors.Is(err, modules.ErrForbidden) {
		t.Errorf("anonymous caller err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.module_switches`).Scan(&n); err != nil || n != 0 {
		t.Errorf("refused calls must change nothing, rows = %d, %v", n, err)
	}
}

func TestDependenciesBlockBothWays(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	c := admin()

	// changes is required by planning, security and endpoints: it cannot go off first.
	var dep *modules.DependencyError
	_, err := s.Disable(ctx, c, "changes", "not_needed", ver(0))
	if !errors.As(err, &dep) || dep.Enabling || strings.Join(dep.Blockers, ",") != "endpoints,planning,security" {
		t.Fatalf("disable changes err = %v (%+v)", err, dep)
	}
	// remoteaccess requires endpoints, so it goes first.
	for _, key := range []string{"planning", "security", "remoteaccess", "endpoints"} {
		if _, err := s.Disable(ctx, c, key, "not_needed", ver(0)); err != nil {
			t.Fatalf("disable %s: %v", key, err)
		}
	}
	if _, err := s.Disable(ctx, c, "changes", "not_needed", ver(0)); err != nil {
		t.Fatalf("disable changes after its dependents: %v", err)
	}
	_, err = s.Enable(ctx, c, "planning", "business_need", ver(1))
	if !errors.As(err, &dep) || !dep.Enabling || strings.Join(dep.Blockers, ",") != "changes" {
		t.Fatalf("enable planning err = %v (%+v)", err, dep)
	}
	_, err = s.Enable(ctx, c, "remoteaccess", "business_need", ver(1))
	if !errors.As(err, &dep) || strings.Join(dep.Blockers, ",") != "endpoints" {
		t.Fatalf("enable remoteaccess err = %v (%+v)", err, dep)
	}
	if got := info(t, s, "planning"); got.Version != 1 || got.SwitchOn {
		t.Errorf("a refused enable must not change the module: %+v", got)
	}
	// Bring the chain back in dependency order.
	if _, err := s.Enable(ctx, c, "changes", "business_need", ver(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enable(ctx, c, "endpoints", "business_need", ver(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enable(ctx, c, "remoteaccess", "business_need", ver(1)); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSwitchesKeepDependenciesConsistent(t *testing.T) {
	for i := 0; i < 5; i++ {
		s, _ := setup(t, nil)
		ctx := context.Background()
		c := admin()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.Disable(ctx, c, "planning", "not_needed", ver(0)) }()
		go func() { defer wg.Done(); _, _ = s.Disable(ctx, c, "procurement", "not_needed", ver(0)) }()
		wg.Wait()
		// planning requires procurement: planning on and procurement off must never be stored.
		if p, pr := info(t, s, "planning"), info(t, s, "procurement"); p.SwitchOn && !pr.SwitchOn {
			t.Fatalf("iteration %d: planning is on while its required procurement is off", i)
		}
	}
}

func TestPreconditionsAreNeverBypassed(t *testing.T) {
	reason := "dpia_not_recorded"
	pre := map[string]modules.Precondition{"presence": func(context.Context) (string, error) { return reason, nil }}
	s, pool := setup(t, pre)
	ctx := context.Background()
	c := admin()

	p := info(t, s, "presence")
	if p.Enabled || p.State != modules.StateDisabled || p.BlockedReason != "dpia_not_recorded" {
		t.Fatalf("off module shows why it cannot be enabled: %+v", p)
	}
	var blocked *modules.BlockedError
	if _, err := s.Enable(ctx, c, "presence", "business_need", ver(0)); !errors.As(err, &blocked) || blocked.Reason != "dpia_not_recorded" {
		t.Fatalf("enable with unmet precondition err = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.module_switches WHERE module_key='presence'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("blocked enable must not store anything: %d, %v", n, err)
	}

	reason = ""
	if got, err := s.Enable(ctx, c, "presence", "business_need", ver(0)); err != nil || !got.Enabled {
		t.Fatalf("enable with met precondition = %+v, %v", got, err)
	}
	// The precondition fails later (record removed): the switch stays on but the module is blocked and behaves as off.
	reason = "dpia_not_recorded"
	p = info(t, s, "presence")
	if p.Enabled || p.State != modules.StateBlocked || !p.SwitchOn || p.BlockedReason != "dpia_not_recorded" {
		t.Fatalf("switch on + unmet precondition must be blocked: %+v", p)
	}
	if on, err := s.Enabled(ctx, "presence"); err != nil || on {
		t.Fatalf("blocked module must not be effectively enabled: %v, %v", on, err)
	}
	// Disabling a blocked module is always possible.
	if got, err := s.Disable(ctx, c, "presence", "not_needed", ver(1)); err != nil || got.State != modules.StateDisabled {
		t.Fatalf("disable blocked = %+v, %v", got, err)
	}
}

func TestPreconditionErrorFailsClosed(t *testing.T) {
	boom := errors.New("db down")
	s, _ := setup(t, map[string]modules.Precondition{"ai": func(context.Context) (string, error) { return "", boom }})
	if on, err := s.Enabled(context.Background(), "servicedesk"); err == nil || on {
		t.Errorf("Enabled with failing precondition = %v, %v; the gate must fail closed", on, err)
	}
}

type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	if r.Header.Get("X-User") == "" {
		return authorization.Principal{}, false, nil
	}
	return authorization.Principal{UserID: r.Header.Get("X-User"), Permissions: map[string]struct{}{}}, true, nil
}

func TestGateAnswers404ForDisabledModuleRoutes(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	if _, err := s.Disable(ctx, admin(), "knowledge", "maintenance", ver(0)); err != nil {
		t.Fatal(err)
	}
	reached := ""
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	h := s.Gate(headerAuth{}, quiet(), next)

	do := func(method, path string, signedIn bool) *httptest.ResponseRecorder {
		reached = ""
		req := httptest.NewRequest(method, path, nil)
		if signedIn {
			req.Header.Set("X-User", newID())
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/v1/knowledge-articles", "/api/v1/knowledge-articles/", "/api/v1/knowledge-articles/abc/publish", "/api/v1/runbooks/x", "/api/v1/runbook-executions"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := do(method, path, true)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"platform.module_disabled"`) || reached != "" {
				t.Errorf("%s %s = %d %s (reached %q)", method, path, rec.Code, rec.Body.String(), reached)
			}
		}
	}
	if rec := do(http.MethodGet, "/api/v1/runbooks", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous request to a disabled module = %d, want 401 (switch state is not revealed)", rec.Code)
	}
	for _, path := range []string{"/api/v1/tickets", "/api/v1/tasks", "/api/v1/roles", "/api/v1/modules/status", "/api/v1/admin/modules", "/health/ready", "/api/v1/unknown"} {
		if rec := do(http.MethodGet, path, true); rec.Code != http.StatusNoContent || reached != path {
			t.Errorf("%s = %d (reached %q), want pass-through", path, rec.Code, reached)
		}
	}
	// Status probes of default-off modules stay reachable; their other routes do not.
	if rec := do(http.MethodGet, "/api/v1/ai/status", true); rec.Code != http.StatusNoContent {
		t.Errorf("/ai/status = %d, want pass-through", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/v1/ai/conversations", true); rec.Code != http.StatusNotFound {
		t.Errorf("/ai/conversations = %d, want 404 while ai is off", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/v1/presence/status", true); rec.Code != http.StatusNoContent {
		t.Errorf("/presence/status = %d, want pass-through", rec.Code)
	}
	// Re-enabling brings the routes back without a restart.
	if _, err := s.Enable(ctx, admin(), "knowledge", "business_need", ver(1)); err != nil {
		t.Fatal(err)
	}
	if rec := do(http.MethodGet, "/api/v1/runbooks", true); rec.Code != http.StatusNoContent {
		t.Errorf("after enable = %d", rec.Code)
	}
}

func TestJobGateSkipsDisabledModulesButNotRetention(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	if _, err := s.Disable(ctx, admin(), "security", "maintenance", ver(0)); err != nil {
		t.Fatal(err)
	}
	// presence and ai are off by default.
	cases := map[string]bool{
		"security.advisory_sync":      false,
		"security.match":              false,
		"endpoints.deployment_tick":   true,
		"changes.reminders":           true,
		"tasks.recurrence.generate":   true,
		"notifications.email.send":    true,
		"organization.directory_sync": true,
		"presence.purge":              true,
		"ai.sessions.expire":          true,
		"ai.retention.purge":          true,
		"remoteaccess.observe":        true,
		"unknown":                     true,
	}
	for typ, want := range cases {
		if got, err := s.JobGate(ctx, typ); err != nil || got != want {
			t.Errorf("JobGate(%s) = %v, %v; want %v", typ, got, err, want)
		}
	}
}

func TestRunnerSkipsJobsOfDisabledModule(t *testing.T) {
	s, pool := setup(t, nil)
	ctx := context.Background()
	typ := "endpoints.gate_test_" + strings.ReplaceAll(newID(), "-", "")
	runner := jobs.NewRunner(pool, jobs.RunnerOptions{WorkerID: "gate-" + newID(), PollInterval: 10 * time.Millisecond, Gate: s.JobGate}, quiet())
	calls := 0
	if err := runner.Register(typ, time.Minute, func(context.Context, jobs.Job) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	status := func(id string) string {
		var st string
		if err := pool.QueryRow(ctx, `SELECT status FROM platform.jobs WHERE id=$1`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if _, err := s.Disable(ctx, admin(), "remoteaccess", "maintenance", ver(0)); err != nil { // keeps endpoints' dependents consistent
		t.Fatal(err)
	}
	if _, err := s.Disable(ctx, admin(), "security", "maintenance", ver(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Disable(ctx, admin(), "endpoints", "maintenance", ver(0)); err != nil {
		t.Fatal(err)
	}
	id, _, err := jobs.Enqueue(ctx, pool, jobs.EnqueueRequest{Type: typ})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.RunOnce(ctx); err != nil || !processed {
		t.Fatalf("RunOnce = %v, %v", processed, err)
	}
	if calls != 0 || status(id) != "completed" {
		t.Fatalf("job of a disabled module: handler calls=%d status=%s; want skipped and completed", calls, status(id))
	}
	if _, err := s.Enable(ctx, admin(), "endpoints", "business_need", ver(1)); err != nil {
		t.Fatal(err)
	}
	id, _, err = jobs.Enqueue(ctx, pool, jobs.EnqueueRequest{Type: typ})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.RunOnce(ctx); err != nil || !processed {
		t.Fatalf("RunOnce = %v, %v", processed, err)
	}
	if calls != 1 || status(id) != "completed" {
		t.Fatalf("job of an enabled module: handler calls=%d status=%s", calls, status(id))
	}
}
