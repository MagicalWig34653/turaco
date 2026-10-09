package wiring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	servicedesktransport "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

type moduleOff struct{ off string }

func (g moduleOff) Enabled(_ context.Context, key string) (bool, error) { return key != g.off, nil }

type queueFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	desk   *servicedeskapp.Service
	views  *views.Service
	admin  servicedeskapp.Principal
	global servicedeskapp.Principal
	corr   string
	queues []string
	// Users: qa views Q1 through a grant, emp is an employee, agent holds tickets.view and manage.
	qa, emp, agent string
}

func newQueueFixture(t *testing.T, gate views.ModuleGate, engine *query.Engine) *queueFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	f := &queueFixture{t: t, pool: pool, corr: "wq-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&f.qa, &f.emp, &f.agent} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	if engine == nil {
		engine = QueryEngine(pool)
	}
	f.desk = servicedeskapp.NewService(servicedeskrepository.New(pool), openDirectory{}, noDevice{}).WithQueryEngine(engine)
	servicedesktransport.Register(mux, f.desk, cookieAuth{}, logger)
	resources, routes := ViewResources()
	svc, err := views.NewService(pool, openDirectory{}, views.NewHTTPRunner(mux, routes), gate, resources)
	if err != nil {
		t.Fatal(err)
	}
	f.views = svc.WithSystemProviders(servicedeskpublic.NewSystemViews(f.desk))
	f.admin = servicedeskapp.Principal{UserID: f.agent, View: true, Manage: true, QueuesManage: true}
	f.global = servicedeskapp.Principal{UserID: f.agent, View: true, Manage: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[]) OR queue_id = ANY($2::uuid[])`, []string{f.qa, f.emp, f.agent}, f.queues)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.queues WHERE id = ANY($1::uuid[])`, f.queues)
	})
	return f
}

func (f *queueFixture) c(user string) servicedeskapp.Caller {
	return servicedeskapp.Caller{Actor: audit.UserActor(user), CorrelationID: f.corr}
}

func (f *queueFixture) queue(visibility string) servicedeskapp.Queue {
	f.t.Helper()
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	pfx := "W" + strings.ToUpper(hex.EncodeToString(b))[:5]
	q, err := f.desk.CreateQueue(context.Background(), f.c(f.agent), f.admin, servicedeskapp.QueueInput{Key: strings.ToLower(pfx), Prefix: pfx, Name: "Desk " + pfx, Visibility: visibility})
	if err != nil {
		f.t.Fatal(err)
	}
	f.queues = append(f.queues, q.ID)
	return q
}

func (f *queueFixture) grant(q servicedeskapp.Queue, user, level string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO servicedesk.queue_grants (queue_id, subject_type, subject_id, level) VALUES ($1::uuid, 'user', $2::uuid, $3)
		ON CONFLICT DO NOTHING`, q.ID, user, level); err != nil {
		f.t.Fatal(err)
	}
}

func (f *queueFixture) revoke(q servicedeskapp.Queue, user string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM servicedesk.queue_grants WHERE queue_id = $1::uuid AND subject_id = $2::uuid`, q.ID, user); err != nil {
		f.t.Fatal(err)
	}
}

func (f *queueFixture) raise(q servicedeskapp.Queue, title string) servicedeskapp.Ticket {
	f.t.Helper()
	tk, err := f.desk.Create(context.Background(), f.c(f.agent), f.global, servicedeskapp.CreateInput{Title: title, QueueID: &q.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	return tk
}

func (f *queueFixture) caller(user string, perms ...string) views.Caller {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return views.Caller{UserID: user, Permissions: m, CorrelationID: f.corr, Header: cookieFor(user, perms...)}
}

func titlesOf(t *testing.T, out views.ResultsOutput) map[string]bool {
	t.Helper()
	var items []struct{ Title string }
	if err := json.Unmarshal(out.Items, &items); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.Title] = true
	}
	return got
}

func TestSystemViewsRunAsTheViewerAndCountThroughTheSameScope(t *testing.T) {
	ctx := context.Background()
	f := newQueueFixture(t, allOn{}, nil)
	q1, q2 := f.queue("internal"), f.queue("internal")
	f.grant(q1, f.qa, "view")
	f.grant(q2, f.qa, "create")
	f.raise(q1, "sv-unassigned-q1")
	t2 := f.raise(q1, "sv-mine-q1")
	f.raise(q2, "sv-unassigned-q2")
	if _, err := f.desk.Assign(ctx, f.c(f.agent), f.global, t2.ID, nil, &f.qa, nil); err != nil {
		t.Fatal(err)
	}
	qa := f.caller(f.qa)

	list, err := f.views.SystemViews(ctx, qa)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, sv := range list {
		keys[sv.Key] = true
	}
	for _, want := range []string{"system:tickets:my-open", "system:tickets:unassigned", "system:tickets:queue:" + q1.ID} {
		if !keys[want] {
			t.Errorf("system view %s missing from %v", want, keys)
		}
	}
	if keys["system:tickets:queue:"+q2.ID] {
		t.Error("a queue the caller cannot view got a sidebar entry")
	}
	results := func(id string) map[string]bool {
		out, err := f.views.Results(ctx, qa, id, views.ResultsInput{Count: true})
		if err != nil {
			t.Fatalf("results %s: %v", id, err)
		}
		return titlesOf(t, out)
	}
	if got := results("system:tickets:queue:" + q1.ID); len(got) != 2 || !got["sv-unassigned-q1"] || !got["sv-mine-q1"] {
		t.Errorf("queue view = %v", got)
	}
	// "Unassigned" is scoped to the queues the caller views: the unassigned ticket of q2 stays out.
	if got := results("system:tickets:unassigned"); len(got) != 1 || !got["sv-unassigned-q1"] {
		t.Errorf("unassigned = %v", got)
	}
	if got := results("system:tickets:my-open"); len(got) != 1 || !got["sv-mine-q1"] {
		t.Errorf("my open = %v", got)
	}
	// Counts use the same compiler and scope, accept the queue:<id> handle and report ok.
	counts, err := f.views.Counts(ctx, qa, []string{"queue:" + q1.ID, "system:tickets:unassigned", "system:tickets:my-open"})
	if err != nil || len(counts) != 3 {
		t.Fatalf("counts = %+v %v", counts, err)
	}
	for i, want := range []int{2, 1, 1} {
		if counts[i].Status != views.CountOK || counts[i].Count != want || counts[i].Capped {
			t.Errorf("count %d = %+v, want %d", i, counts[i], want)
		}
	}

	// No oracle: an employee has no system views; a key or handle of someone else's scope is simply absent.
	emp := f.caller(f.emp)
	if list, _ := f.views.SystemViews(ctx, emp); len(list) != 0 {
		t.Errorf("employee system views = %v", list)
	}
	if _, err := f.views.Results(ctx, emp, "system:tickets:unassigned", views.ResultsInput{}); !errors.Is(err, views.ErrNotFound) {
		t.Errorf("employee runs a system view: %v", err)
	}
	if c, err := f.views.Counts(ctx, qa, []string{"queue:" + q2.ID, "system:tickets:nope"}); err != nil || len(c) != 0 {
		t.Errorf("counts of foreign handles = %+v %v", c, err)
	}
	if _, err := f.views.Counts(ctx, qa, []string{"drop table"}); err == nil {
		t.Error("garbage id accepted")
	}
	if _, err := f.views.Counts(ctx, qa, make([]string, views.MaxCountIDs+1)); err == nil {
		t.Error("too many ids accepted")
	}

	// The sidebar carries the system entries first, with counts.
	sb, err := f.views.Sidebar(ctx, qa)
	if err != nil {
		t.Fatal(err)
	}
	var tickets *views.SidebarGroup
	for i := range sb.Groups {
		if sb.Groups[i].Key == "tickets" {
			tickets = &sb.Groups[i]
		}
	}
	if tickets == nil || len(tickets.Items) < 3 || tickets.Items[0].Source != "system" || tickets.Items[0].NameKey != "views.system.my_open_tickets" {
		t.Fatalf("sidebar = %+v", sb)
	}
	for _, it := range tickets.Items {
		if it.Source == "system" && (it.Count == nil || it.CountStatus != views.CountOK) {
			t.Errorf("sidebar entry %s has no count: %+v", it.ViewID, it)
		}
	}

	// Cache: the scope key is part of the cache key. A new data row is not seen inside the TTL ...
	f.raise(q1, "sv-late")
	again, _ := f.views.Counts(ctx, qa, []string{"queue:" + q1.ID})
	if len(again) != 1 || again[0].Count != 2 {
		t.Errorf("cached count = %+v, want 2 within the TTL", again)
	}
	// ... but a changed grant (another viewable queue) changes the key and is visible at once.
	f.grant(q2, f.qa, "view")
	fresh, _ := f.views.Counts(ctx, qa, []string{"system:tickets:unassigned"})
	if len(fresh) != 1 || fresh[0].Count != 3 {
		t.Errorf("count after a grant change = %+v, want 3 (q1: two unassigned, q2: one)", fresh)
	}
	f.revoke(q2, f.qa)
	revoked, _ := f.views.Counts(ctx, qa, []string{"system:tickets:unassigned"})
	// Back to the first scope key: the entry of the first request (1) is answered, stale by the TTL only.
	if len(revoked) != 1 || revoked[0].Count != 1 {
		t.Errorf("count after revoking = %+v, want the cached 1", revoked)
	}
	// Another user never gets a cached count of qa: the principal is in the key.
	other, _ := f.views.Counts(ctx, f.caller(f.agent, "tickets.view", "tickets.manage"), []string{"system:tickets:unassigned"})
	if len(other) != 1 || other[0].Status != views.CountOK || other[0].Count < 3 {
		t.Errorf("global caller count = %+v", other)
	}
}

func TestSystemViewsFollowTheModuleSwitchAndNeverReportZeroForFailures(t *testing.T) {
	ctx := context.Background()
	// A module that is off offers no entries and runs none.
	off := newQueueFixture(t, moduleOff{off: "servicedesk"}, nil)
	q := off.queue("internal")
	off.grant(q, off.qa, "view")
	if list, err := off.views.SystemViews(ctx, off.caller(off.qa)); err != nil || len(list) != 0 {
		t.Errorf("system views of a disabled module = %v %v", list, err)
	}
	if _, err := off.views.Results(ctx, off.caller(off.qa), "system:tickets:unassigned", views.ResultsInput{}); !errors.Is(err, views.ErrNotFound) {
		t.Errorf("system view of a disabled module ran: %v", err)
	}

	// A count that cannot be computed is "unavailable", never zero: the engine's rate limit answers 429.
	limited := newQueueFixture(t, allOn{}, query.NewEphemeralEngine().WithLimiter(query.NewLimiter(0.0001, 1)))
	lq := limited.queue("internal")
	limited.grant(lq, limited.qa, "view")
	limited.raise(lq, "limited")
	counts, err := limited.views.Counts(ctx, limited.caller(limited.qa), []string{"system:tickets:my-open", "system:tickets:unassigned", "queue:" + lq.ID})
	if err != nil || len(counts) != 3 {
		t.Fatalf("counts = %+v %v", counts, err)
	}
	ok, unavailable := 0, 0
	for _, c := range counts {
		switch c.Status {
		case views.CountOK:
			ok++
		case views.CountUnavailable:
			unavailable++
			if c.Count != 0 {
				t.Errorf("unavailable count carries a number: %+v", c)
			}
		}
	}
	if ok != 1 || unavailable != 2 {
		t.Errorf("ok=%d unavailable=%d (%+v)", ok, unavailable, counts)
	}
	// The sidebar reports the same: an entry without a count, with status unavailable.
	sb, err := limited.views.Sidebar(ctx, limited.caller(limited.qa))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range sb.Groups {
		for _, it := range g.Items {
			if it.CountStatus == views.CountUnavailable && it.Count != nil {
				t.Errorf("unavailable sidebar entry has a count: %+v", it)
			}
		}
	}
}
