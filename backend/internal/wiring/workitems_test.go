package wiring

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	workitemstransport "github.com/MagicalWig34653/turaco/backend/internal/platform/workitems/transport"
)

type taskDirectory struct{ openDirectory }

func (taskDirectory) CurrentMemberIDs(context.Context, string) ([]string, error) { return nil, nil }

type feedItem struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Reference string `json:"reference"`
	Href      string `json:"href"`
}

type feed struct {
	Items       []feedItem `json:"items"`
	NextCursor  string     `json:"nextCursor"`
	Unavailable []string   `json:"unavailable"`
}

func TestMyWorkMergesTasksAndTicketsWithPerItemAuthorization(t *testing.T) {
	ctx := context.Background()
	f := newQueueFixture(t, allOn{}, nil)
	tasks := tasksapp.NewService(tasksrepository.New(f.pool), taskDirectory{}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	serve := func(gate moduleOff) http.Handler {
		mux := http.NewServeMux()
		svc, err := WorkItems(tasks, f.desk, gate)
		if err != nil {
			t.Fatal(err)
		}
		workitemstransport.Register(mux, svc, cookieAuth{}, logger)
		return httpx.Middleware(logger, mux)
	}
	get := func(h http.Handler, user, path string, perms ...string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header = cookieFor(user, perms...)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	items := func(h http.Handler, user string, perms ...string) feed {
		code, body := get(h, user, "/api/v1/my-work/items?limit=50", perms...)
		if code != http.StatusOK {
			t.Fatalf("items = %d %s", code, body)
		}
		var out feed
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	q := f.queue("internal")
	f.grant(q, f.qa, "work")
	urgent := f.raise(q, "mw-ticket-urgent")
	later := f.raise(q, "mw-ticket-later")
	hiddenQ := f.queue("internal")
	foreign := f.raise(hiddenQ, "mw-ticket-foreign")
	for _, tk := range []servicedeskapp.Ticket{urgent, later, foreign} {
		if _, err := f.desk.Assign(ctx, f.c(f.agent), f.global, tk.ID, nil, &f.qa, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.desk.SetPriority(ctx, f.c(f.agent), f.global, urgent.ID, nil, "urgent"); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Create(ctx, tasksapp.Caller{Actor: audit.UserActor(f.agent), CorrelationID: f.corr}, tasksapp.Principal{UserID: f.agent, Manage: true},
		tasksapp.CreateInput{Title: "mw-task", Priority: "high", AssignedUserID: &f.qa})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.tasks WHERE id = $1::uuid`, task.ID)
	})

	h := serve(moduleOff{})
	out := items(h, f.qa, "tasks.work")
	var titles []string
	for _, it := range out.Items {
		titles = append(titles, it.Title)
	}
	// Shared order: priority first (urgent ticket, high task, normal ticket); the ticket of a queue the user cannot
	// view is not listed although it is assigned to them.
	if strings.Join(titles, ",") != "mw-ticket-urgent,mw-task,mw-ticket-later" {
		t.Fatalf("feed = %v", titles)
	}
	if out.Items[0].Kind != "ticket" || out.Items[0].Source != "tickets" || !strings.HasPrefix(out.Items[0].Reference, q.Prefix+"-") || out.Items[0].Href != "/tickets/"+urgent.ID ||
		out.Items[1].Kind != "task" || out.Items[1].Href != "/tasks/"+task.ID {
		t.Errorf("items = %+v", out.Items)
	}
	// Paging across sources: a page of one continues without repeating or skipping.
	var paged []string
	cursor := ""
	for i := 0; i < 5; i++ {
		_, body := get(h, f.qa, "/api/v1/my-work/items?limit=1&cursor="+cursor, "tasks.work")
		var page feed
		_ = json.Unmarshal([]byte(body), &page)
		for _, it := range page.Items {
			paged = append(paged, it.Title)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(paged, ",") != strings.Join(titles, ",") {
		t.Errorf("paged feed = %v, want %v", paged, titles)
	}
	// Without a tasks permission the tasks source contributes nothing; tickets stay.
	noTasks := items(h, f.qa)
	for _, it := range noTasks.Items {
		if it.Kind == "task" {
			t.Errorf("task listed without a tasks permission: %+v", it)
		}
	}
	if len(noTasks.Items) != 2 {
		t.Errorf("tickets only = %d items", len(noTasks.Items))
	}
	// Counts come from the sources, per source.
	code, body := get(h, f.qa, "/api/v1/my-work/counts", "tasks.work")
	if code != http.StatusOK || !strings.Contains(body, `"source":"tickets","count":2`) || !strings.Contains(body, `"source":"tasks","count":1`) {
		t.Errorf("counts = %d %s", code, body)
	}
	// Losing access removes the tickets of that queue at once, in the feed and in the count.
	f.revoke(q, f.qa)
	if out := items(h, f.qa, "tasks.work"); len(out.Items) != 1 || out.Items[0].Kind != "task" {
		t.Errorf("feed after revoke = %+v", out.Items)
	}
	if _, body := get(h, f.qa, "/api/v1/my-work/counts?sources=tickets"); !strings.Contains(body, `"count":0`) || strings.Contains(body, `"unavailable"`) {
		t.Errorf("count after revoke = %s", body)
	}
	f.grant(q, f.qa, "work")
	// A module that is off is skipped, not shown as empty-with-error.
	off := serve(moduleOff{off: "servicedesk"})
	for _, it := range items(off, f.qa, "tasks.work").Items {
		if it.Kind == "ticket" {
			t.Errorf("ticket listed while the Service Desk is off: %+v", it)
		}
	}
	if _, body := get(off, f.qa, "/api/v1/my-work/counts", "tasks.work"); strings.Contains(body, `"tickets"`) {
		t.Errorf("count of a disabled module: %s", body)
	}
	// Other people never see the items.
	if out := items(h, f.emp, "tasks.work"); len(out.Items) != 0 {
		t.Errorf("foreign feed = %+v", out.Items)
	}
	// Input validation.
	for _, path := range []string{"/api/v1/my-work/items?sources=nope", "/api/v1/my-work/items?cursor=%21%21", "/api/v1/my-work/items?limit=0", "/api/v1/my-work/items?limit=100000"} {
		if code, _ := get(h, f.qa, path, "tasks.work"); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, code)
		}
	}
	if code, _ := get(h, "", "/api/v1/my-work/items"); code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", code)
	}
}
