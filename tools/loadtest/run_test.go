package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is a minimal in-memory stand-in for the parts of the Turaco API that the load test uses.
type fakeAPI struct {
	mu      sync.Mutex
	tickets map[string]*fakeTicket
	order   []string
	seq     int
	queue   string
	logins  int
	break5x bool // answer GET /knowledge-articles with 500
	// leakyComments lets everybody who can read a ticket comment, ignoring the abilities (a broken server).
	leakyComments bool
}

type fakeTicket struct {
	ID, Ref, Title, Status, Priority, Reporter string
	Version                                    int
	Assignee                                   *string
}

func (f *fakeTicket) json() map[string]any {
	return map[string]any{"id": f.ID, "reference": f.Ref, "title": f.Title, "status": f.Status, "priority": f.Priority,
		"version": f.Version, "assigneeId": f.Assignee, "queueId": "q1", "reporterId": f.Reporter}
}

func newFakeAPI() *fakeAPI { return &fakeAPI{tickets: map[string]*fakeTicket{}, queue: "q1"} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": code}})
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if r.Method != http.MethodGet && path != "/meta" {
		if r.Header.Get("Origin") == "" {
			writeErr(w, http.StatusForbidden, "platform.csrf_rejected")
			return
		}
	}
	switch {
	case path == "/meta":
		writeJSON(w, 200, map[string]any{"name": "fake", "version": "0", "environment": "development"})
		return
	case path == "/auth/emergency-login":
		var in struct{ Login, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Password == "" {
			writeErr(w, 401, "auth.invalid_credentials")
			return
		}
		a.mu.Lock()
		a.logins++
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s-" + in.Login, Path: "/"})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		writeErr(w, 401, "auth.unauthenticated")
		return
	}
	login := strings.TrimPrefix(ck.Value, "s-")
	switch {
	case path == "/auth/session":
		perms := []string{}
		if isStaffLogin(login) {
			perms = append(perms, "tickets.manage")
		}
		writeJSON(w, 200, map[string]any{"userId": "u-" + login, "authMethod": "emergency", "expiresAt": time.Now().Add(time.Hour), "permissions": perms})
	case path == "/service-desk/queues":
		writeJSON(w, 200, map[string]any{"items": []map[string]any{{"id": a.queue, "key": "it", "prefix": "TKT", "status": "active", "canCreate": true, "level": "work"}}})
	case path == "/tickets" && r.Method == http.MethodPost:
		var in struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.mu.Lock()
		a.seq++
		t := &fakeTicket{ID: fmt.Sprintf("id-%d", a.seq), Ref: fmt.Sprintf("TKT-%06d", a.seq), Title: in.Title, Status: "new", Priority: "normal", Version: 1, Reporter: "u-" + login}
		a.tickets[t.ID] = t
		a.order = append(a.order, t.ID)
		j := t.json()
		a.mu.Unlock()
		writeJSON(w, 201, j)
	case path == "/tickets" && r.Method == http.MethodGet, path == "/tickets/query":
		a.mu.Lock()
		items := []map[string]any{}
		for i := len(a.order) - 1; i >= 0 && len(items) < 25; i-- {
			items = append(items, a.tickets[a.order[i]].json())
		}
		n := len(a.order)
		a.mu.Unlock()
		writeJSON(w, 200, map[string]any{"items": items, "count": n, "warnings": []any{}})
	case path == "/tickets/by-reference":
		ref := r.URL.Query().Get("reference")
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, t := range a.tickets {
			if t.Ref == ref {
				writeJSON(w, 200, map[string]any{"ticketId": t.ID, "reference": t.Ref, "alias": false})
				return
			}
		}
		writeErr(w, 404, "tickets.not_found")
	case strings.HasPrefix(path, "/tickets/"):
		a.ticketRoute(w, r, login, strings.Split(strings.TrimPrefix(path, "/tickets/"), "/"))
	case path == "/views/counts":
		a.mu.Lock()
		n := len(a.order)
		a.mu.Unlock()
		items := []map[string]any{}
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			items = append(items, map[string]any{"id": id, "count": n, "status": "ok"})
		}
		writeJSON(w, 200, map[string]any{"items": items})
	case path == "/knowledge-articles" && a.break5x:
		writeErr(w, 500, "platform.internal")
	default:
		writeJSON(w, 200, map[string]any{"items": []any{}})
	}
}

func isStaffLogin(login string) bool {
	switch login {
	case "lena.bauer", "murat.demir", "oliver.stein", "nadine.roth", "jens.albrecht", "henrik.vogel", "sandra.winter", "tobias.kraft",
		"christian.hoffmann", "silke.brandl", "martin.kessler":
		return true
	}
	return false
}

func (a *fakeAPI) ticketRoute(w http.ResponseWriter, r *http.Request, login string, parts []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.tickets[parts[0]]
	if t == nil {
		writeErr(w, 404, "tickets.not_found")
		return
	}
	isReporter := t.Reporter == "u-"+login
	// uwe.pohl works the ticket's Queue through a queue grant; mirja.engel may only read it.
	canComment := isReporter || isStaffLogin(login) || login == "uwe.pohl"
	if !isReporter && !isStaffLogin(login) && login != "uwe.pohl" && login != "mirja.engel" {
		writeErr(w, 404, "tickets.not_found")
		return
	}
	if len(parts) == 1 {
		j := t.json()
		j["abilities"] = map[string]any{"comment": canComment, "internalComment": isStaffLogin(login) || login == "uwe.pohl"}
		writeJSON(w, 200, j)
		return
	}
	if parts[1] == "comments" {
		if !canComment && !a.leakyComments {
			writeErr(w, 403, "platform.forbidden")
			return
		}
		if t.Status == "closed" {
			writeErr(w, 409, "tickets.invalid_state")
			return
		}
		writeJSON(w, 201, map[string]any{"id": "c1", "body": "x", "internal": false})
		return
	}
	if !isStaffLogin(login) {
		writeErr(w, 403, "platform.forbidden")
		return
	}
	var in struct {
		ExpectedVersion int    `json:"expectedVersion"`
		AssigneeID      string `json:"assigneeId"`
		Priority        string `json:"priority"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.ExpectedVersion != t.Version {
		writeErr(w, 409, "tickets.version_conflict")
		return
	}
	switch parts[1] {
	case "assign":
		t.Assignee = &in.AssigneeID
		if t.Status == "new" {
			t.Status = "open"
		}
	case "priority":
		t.Priority = in.Priority
	case "start":
		t.Status = "in_progress"
	case "wait":
		t.Status = "waiting"
	case "resume":
		t.Status = "in_progress"
	case "resolve":
		t.Status = "resolved"
	case "close":
		t.Status = "closed"
	case "reopen":
		t.Status = "open"
	default:
		writeErr(w, 404, "tickets.not_found")
		return
	}
	t.Version++
	writeJSON(w, 200, t.json())
}

func newTestEnv(t *testing.T, api *fakeAPI, n int) (*Env, *Recorder, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	rec := NewRecorder()
	client, err := NewClient(srv.URL, 32, 5*time.Second, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	env := &Env{
		Cfg:      Config{Tag: "testtag", MaxRetries: 2, HotTickets: 5, HotProb: 0.9, MaxTickets: 1000},
		Client:   client,
		Pool:     NewPool(),
		Trk:      NewTracker(),
		Seen:     newIDRing(100),
		Sessions: map[string][]*Session{},
	}
	for _, p := range SelectPersonas(Roster("pw"), nil, n) {
		p.Password = "pw"
		env.Sessions[p.Class] = append(env.Sessions[p.Class], &Session{P: p})
	}
	if err := loginAll(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	return env, rec, srv
}

func TestLoginSendsSameOriginHeadersAndKeepsSession(t *testing.T) {
	api := newFakeAPI()
	env, _, _ := newTestEnv(t, api, 3)
	for _, ss := range env.Sessions {
		for _, s := range ss {
			if s.UserID() != "u-"+s.P.Login {
				t.Errorf("session user id %q", s.UserID())
			}
		}
	}
	if api.logins != 3 {
		t.Errorf("%d logins", api.logins)
	}
}

func TestReloginOn401(t *testing.T) {
	api := newFakeAPI()
	env, rec, _ := newTestEnv(t, api, 1)
	var s *Session
	for _, ss := range env.Sessions {
		s = ss[0]
	}
	s.mu.Lock()
	s.cookie = "" // the server no longer knows the session
	s.loginAt = time.Now().Add(-time.Hour)
	s.mu.Unlock()
	var out map[string]any
	r := env.Client.Call(context.Background(), &JobCtx{Stage: 0}, s, "x.get", http.MethodGet, "/knowledge-articles", nil, &out)
	if !r.OK() {
		t.Fatalf("call after re-login failed: %+v", r)
	}
	_, cells := rec.ByOp()
	if cells["x.get"].Classes[ClassReauth] != 1 || cells["x.get"].Classes[ClassOK] != 1 {
		t.Errorf("expected one reauth and one ok, got %v", cells["x.get"].Classes)
	}
	if _, relogins := env.Client.LoginCounts(); relogins != 1 {
		t.Errorf("relogins %d", relogins)
	}
}

func TestRunEndToEndAgainstFakeAPI(t *testing.T) {
	api := newFakeAPI()
	env, rec, _ := newTestEnv(t, api, 0)
	ctx := context.Background()
	if err := Seed(ctx, env, 30); err != nil {
		t.Fatal(err)
	}
	if env.Pool.Len() != 30 || len(env.Queues) != 1 {
		t.Fatalf("seed: pool %d queues %d", env.Pool.Len(), len(env.Queues))
	}
	plan := NewPlan([]Stage{{Name: "warm", Kind: "hold", Rate: 150, Duration: 2 * time.Second}, {Name: "off", Kind: "hold", Rate: 0, Duration: time.Second}})
	res := Run(ctx, env, rec, RunOptions{Plan: plan, Poisson: true, Seed: 7, MaxInflight: 32, QueueSize: 1000, Drain: 10 * time.Second, Weights: defaultWeights}, nil)
	if res.Offered < 200 || res.Offered > 400 {
		t.Fatalf("offered %d operations, want about 300", res.Offered)
	}
	if res.Dropped != 0 || res.Abandoned != 0 || res.Finished != res.Offered {
		t.Fatalf("dropped %d abandoned %d finished %d offered %d", res.Dropped, res.Abandoned, res.Finished, res.Offered)
	}
	total := rec.Total()
	if total.Classes[ClassServerError] != 0 || total.Classes[ClassNetwork] != 0 || total.Classes[ClassInvalid] != 0 {
		t.Fatalf("unexpected error classes: %v", total.Classes)
	}
	if len(res.Timeline) == 0 {
		t.Error("no timeline")
	}
	if res.Stages[0].Offered != res.Offered || res.Stages[1].Offered != 0 {
		t.Errorf("stage counters %+v", res.Stages)
	}

	inv := CheckInvariants(ctx, InvariantInput{Env: env, Rec: rec})
	for _, r := range inv {
		if r.Status == "fail" {
			t.Errorf("invariant %s failed: %s", r.Name, r.Detail)
		}
	}
	names := map[string]string{}
	for _, r := range inv {
		names[r.Name] = r.Status
	}
	for _, want := range []string{"no_5xx_or_timeouts", "no_lost_updates_observed", "references_resolve_to_their_ticket", "queue_counts_equal_list_counts"} {
		if names[want] != "pass" {
			t.Errorf("invariant %s: %q, want pass (%v)", want, names[want], names)
		}
	}

	rep := BuildReport(ReportInput{Tag: "testtag", BaseURL: "http://fake", ServerEnv: "development", Plan: plan, Rec: rec, Run: res, Env: env, Invariants: inv, SLOP99: time.Second})
	if !rep.Verdict.Pass {
		t.Errorf("verdict failed: %v", rep.Verdict.Failures)
	}
	if rep.Writes.TicketsCreated < 30 || len(rep.Ops) == 0 || len(rep.Stages) != 2 {
		t.Errorf("report: %+v", rep.Writes)
	}
	var sb strings.Builder
	rep.WriteText(&sb)
	if !strings.Contains(sb.String(), "VERDICT  PASS") || !strings.Contains(sb.String(), "tickets.query") {
		t.Errorf("text report incomplete:\n%s", sb.String())
	}
	if err := rep.WriteJSON(&sb); err != nil {
		t.Error(err)
	}
}

func TestServerErrorsFailTheVerdict(t *testing.T) {
	api := newFakeAPI()
	api.break5x = true
	env, rec, _ := newTestEnv(t, api, 0)
	ctx := context.Background()
	_ = Seed(ctx, env, 5)
	plan := NewPlan([]Stage{{Name: "s", Kind: "hold", Rate: 100, Duration: time.Second}})
	res := Run(ctx, env, rec, RunOptions{Plan: plan, Seed: 3, MaxInflight: 8, QueueSize: 500, Drain: 5 * time.Second, Weights: map[string]float64{ClassEmployee: 1}}, nil)
	inv := CheckInvariants(ctx, InvariantInput{Env: env, Rec: rec})
	rep := BuildReport(ReportInput{Plan: plan, Rec: rec, Run: res, Env: env, Invariants: inv, SLOP99: time.Second})
	if rep.Verdict.Pass || rep.Totals.Classes["server_error"] == 0 {
		t.Fatalf("5xx must fail the run: %+v", rep.Verdict)
	}
}

func TestOverloadIsMeasuredNotHidden(t *testing.T) {
	// A slow server and a tiny worker pool: arrivals queue up, latency is measured from the scheduled
	// start, and the surplus is dropped and reported instead of slowing the schedule down.
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch path {
		case "/meta":
			writeJSON(w, 200, map[string]any{"environment": "development"})
		case "/auth/emergency-login":
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			w.WriteHeader(204)
		case "/auth/session":
			writeJSON(w, 200, map[string]any{"userId": "u", "permissions": []string{}})
		default:
			time.Sleep(50 * time.Millisecond)
			writeJSON(w, 200, map[string]any{"items": []any{}})
		}
	})
	srv := httptest.NewServer(slow)
	defer srv.Close()
	rec := NewRecorder()
	client, _ := NewClient(srv.URL, 2, 5*time.Second, rec)
	defer client.Close()
	env := &Env{Cfg: Config{Tag: "x", MaxTickets: 10}, Client: client, Pool: NewPool(), Trk: NewTracker(), Seen: newIDRing(10),
		Sessions: map[string][]*Session{ClassVendor: {{P: Persona{Login: "vendor.mueller", Class: ClassVendor, Password: "pw"}}}}}
	if err := loginAll(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	// 2 workers at 50 ms capacity = 40 ops/s; offer 200/s for 2 s with a queue of 20.
	plan := NewPlan([]Stage{{Name: "over", Kind: "hold", Rate: 200, Duration: 2 * time.Second}})
	res := Run(context.Background(), env, rec, RunOptions{Plan: plan, Seed: 1, MaxInflight: 2, QueueSize: 20, Drain: 20 * time.Second, Weights: map[string]float64{ClassVendor: 1}}, nil)
	if res.Dropped == 0 {
		t.Fatalf("expected dropped arrivals, got none (offered %d)", res.Offered)
	}
	if res.Offered < 300 {
		t.Fatalf("the schedule was slowed down: only %d of ~400 arrivals offered", res.Offered)
	}
	c := rec.ByStage()[0]
	if p99 := c.Response.Quantile(0.99); p99 < 150*time.Millisecond {
		t.Errorf("p99 %v: queue wait is missing from the latency (coordinated omission)", p99)
	}
	if svc := c.Service.Quantile(0.99); svc > 500*time.Millisecond {
		t.Errorf("service p99 %v should stay near the 50 ms server time", svc)
	}
	rep := BuildReport(ReportInput{Plan: plan, Rec: rec, Run: res, Env: env, SLOP99: time.Second})
	if !rep.Stages[0].Saturated || rep.Stages[0].Dropped == 0 {
		t.Errorf("stage not flagged saturated: %+v", rep.Stages[0])
	}
}

// The leak probe follows the abilities of the ticket: a person who works the Queue (uwe.pohl) legitimately comments
// and is not a leak; a person who only reads (mirja.engel) is probed, and a successful write is a violation.
func TestWriteDeniedProbeUsesTicketAbilities(t *testing.T) {
	run := func(t *testing.T, api *fakeAPI, login string) (probes, leaks int64, violations int) {
		t.Helper()
		env, _, _ := newTestEnv(t, api, 0)
		var sess *Session
		for _, p := range Roster("pw") {
			if p.Login == login {
				p.Password = "pw"
				sess = &Session{P: p}
			}
		}
		if err := env.Client.Login(context.Background(), sess, false); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			env.Pool.Add(&Entry{ID: fmt.Sprintf("id-%d", i+1), Ref: fmt.Sprintf("TKT-%06d", i+1), Reporter: "somebody"})
			api.tickets[fmt.Sprintf("id-%d", i+1)] = &fakeTicket{ID: fmt.Sprintf("id-%d", i+1), Ref: fmt.Sprintf("TKT-%06d", i+1), Status: "open", Reporter: "u-somebody", Version: 1}
		}
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < 20; i++ {
			opWriteDenied(context.Background(), env, &JobCtx{Stage: 0}, sess, rng)
		}
		_, counts, _ := env.Trk.Snapshot()
		return env.probes.Load(), env.leaks.Load(), counts["authorization_leak"]
	}
	if p, l, v := run(t, newFakeAPI(), "uwe.pohl"); p != 0 || l != 0 || v != 0 {
		t.Errorf("a queue worker is not probed: probes %d leaks %d violations %d", p, l, v)
	}
	if p, l, v := run(t, newFakeAPI(), "mirja.engel"); p == 0 || l != 0 || v != 0 {
		t.Errorf("a reader is probed and denied: probes %d leaks %d violations %d", p, l, v)
	}
	broken := newFakeAPI()
	broken.leakyComments = true
	if p, l, v := run(t, broken, "mirja.engel"); p == 0 || l == 0 || v == 0 {
		t.Errorf("a successful write by a reader must be a violation: probes %d leaks %d violations %d", p, l, v)
	}
	if probe, internal := probeWrites(nil); !probe || !internal {
		t.Error("without abilities both comment kinds must be denied")
	}
}
