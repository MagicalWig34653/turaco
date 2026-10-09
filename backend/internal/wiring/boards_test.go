package wiring

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	taskstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// boardDirectory is the Organization stand-in of the board tests: every user and team is active, and teams maps a
// user to the Teams the user belongs to.
type boardDirectory struct{ teams map[string][]string }

func (boardDirectory) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (boardDirectory) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (boardDirectory) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "User " + id[len(id)-4:]
	}
	return out, nil
}
func (boardDirectory) TeamNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Team " + id[len(id)-4:]
	}
	return out, nil
}
func (d boardDirectory) CurrentTeamIDs(_ context.Context, userID string) ([]string, error) {
	return d.teams[userID], nil
}
func (boardDirectory) CurrentMemberIDs(context.Context, string) ([]string, error) { return nil, nil }
func (boardDirectory) GroupIDsOfUser(context.Context, string) ([]string, error)   { return nil, nil }

type boardFixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	mux   *http.ServeMux
	views *views.Service
	token string
	users []string
	tasks []string
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = (b[6]&0x0f)|0x40, (b[8]&0x3f)|0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (f *boardFixture) user() string {
	id := newUUID(f.t)
	f.users = append(f.users, id)
	return id
}

func newBoardFixture(t *testing.T, teams map[string][]string) *boardFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	f := &boardFixture{t: t, pool: pool, mux: http.NewServeMux(), token: "bt" + newUUID(t)[:8]}
	dir := boardDirectory{teams: teams}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := query.NewEphemeralEngine().WithLimiter(query.NewLimiter(100000, 100000))
	tasksSvc := tasksapp.NewService(tasksrepository.New(pool), dir, nil).WithQueryEngine(engine)
	taskstransport.Register(f.mux, tasksSvc, cookieAuth{}, logger)
	resources, routes := ViewResources()
	vs, err := views.NewService(pool, dir, views.NewHTTPRunner(f.mux, routes), allOn{}, resources)
	if err != nil {
		t.Fatal(err)
	}
	f.views = vs
	taskstransport.RegisterBoards(f.mux, TaskBoards(tasksSvc, pool, vs), tasksSvc, cookieAuth{}, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		// Deleting the Views cascades to Boards, columns and ranks.
		_, _ = pool.Exec(ctx, `DELETE FROM views.saved_views WHERE owner_user_id = ANY($1::uuid[])`, f.users)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.tasks WHERE id = ANY($1::uuid[])`, f.tasks)
	})
	return f
}

type resp struct {
	code int
	body []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

func (r resp) errCode() string {
	var e struct {
		Error struct{ Code string } `json:"error"`
		Code  string                `json:"code"`
	}
	_ = json.Unmarshal(r.body, &e)
	if e.Error.Code != "" {
		return e.Error.Code
	}
	return e.Code
}

func (f *boardFixture) do(user string, perms []string, method, path string, body any) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header = cookieFor(user, perms...)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	httpx.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), f.mux).ServeHTTP(rec, req)
	return resp{code: rec.Code, body: rec.Body.Bytes()}
}

type taskJSON struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Status  string  `json:"status"`
	Version int     `json:"version"`
	Reason  *string `json:"statusReason"`
}

func (f *boardFixture) task(owner string, title string, assignee string) taskJSON {
	f.t.Helper()
	body := map[string]any{"title": f.token + " " + title}
	if assignee != "" {
		body["assignedUserId"] = assignee
	}
	r := f.do(owner, []string{"tasks.manage"}, "POST", "/api/v1/tasks", body)
	if r.code != http.StatusCreated {
		f.t.Fatalf("create task: %d %s", r.code, r.body)
	}
	var tk taskJSON
	r.json(f.t, &tk)
	f.tasks = append(f.tasks, tk.ID)
	return tk
}

type columnJSON struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Title    string `json:"title"`
	MapsTo   string `json:"mapsTo"`
	WIPLimit *int   `json:"wipLimit"`
}

type boardJSON struct {
	ID          string       `json:"id"`
	ViewID      string       `json:"viewId"`
	ViewVersion int          `json:"viewVersion"`
	Version     int          `json:"version"`
	Access      string       `json:"access"`
	CanEdit     bool         `json:"canEdit"`
	Columns     []columnJSON `json:"columns"`
}

func (b boardJSON) col(t *testing.T, title string) columnJSON {
	t.Helper()
	for _, c := range b.Columns {
		if c.Title == title {
			return c
		}
	}
	t.Fatalf("no column %q in %+v", title, b.Columns)
	return columnJSON{}
}

func (f *boardFixture) board(user string, perms []string, id string) boardJSON {
	f.t.Helper()
	r := f.do(user, perms, "GET", "/api/v1/tasks/boards/"+id, nil)
	if r.code != http.StatusOK {
		f.t.Fatalf("get board: %d %s", r.code, r.body)
	}
	var b boardJSON
	r.json(f.t, &b)
	return b
}

// create makes a Board over the fixture's tasks (title contains the fixture token).
func (f *boardFixture) create(owner string, extra map[string]any) boardJSON {
	f.t.Helper()
	body := map[string]any{
		"name":   "Board " + f.token,
		"filter": map[string]any{"v": 1, "root": map[string]any{"type": "condition", "field": "title", "op": "contains", "value": f.token}},
	}
	for k, v := range extra {
		body[k] = v
	}
	r := f.do(owner, []string{"tasks.manage"}, "POST", "/api/v1/tasks/boards", body)
	if r.code != http.StatusCreated {
		f.t.Fatalf("create board: %d %s", r.code, r.body)
	}
	var b boardJSON
	r.json(f.t, &b)
	return b
}

type cardJSON struct {
	taskJSON
	Rank *string `json:"rank"`
}

type colCardsJSON struct {
	ColumnID    string     `json:"columnId"`
	Items       []cardJSON `json:"items"`
	NextCursor  string     `json:"nextCursor"`
	Count       int        `json:"count"`
	CountCapped bool       `json:"countCapped"`
	OverWIP     bool       `json:"overWipLimit"`
}

func (f *boardFixture) cards(user string, perms []string, boardID string) map[string]colCardsJSON {
	f.t.Helper()
	r := f.do(user, perms, "GET", "/api/v1/tasks/boards/"+boardID+"/cards", nil)
	if r.code != http.StatusOK {
		f.t.Fatalf("cards: %d %s", r.code, r.body)
	}
	var out struct{ Columns []colCardsJSON }
	r.json(f.t, &out)
	m := map[string]colCardsJSON{}
	for _, c := range out.Columns {
		m[c.ColumnID] = c
	}
	return m
}

func titles(c colCardsJSON, token string) []string {
	var out []string
	for _, it := range c.Items {
		out = append(out, strings.TrimPrefix(it.Title, token+" "))
	}
	return out
}

func eq(a []string, b ...string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

var (
	manage = []string{"tasks.manage"}
	viewer = []string{"tasks.view"}
	worker = []string{"tasks.work"}
)

func TestBoardCardsMovesAndLifecycleOperations(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	if len(b.Columns) != 4 || b.Version != 1 || b.Access != "owner" || !b.CanEdit {
		t.Fatalf("new board: %+v", b)
	}
	open, prog, blocked, done := b.col(t, "Open"), b.col(t, "In progress"), b.col(t, "Blocked"), b.col(t, "Done")
	a, c2, c3 := f.task(owner, "a", ""), f.task(owner, "b", ""), f.task(owner, "c", "")
	// An unrelated task does not match the Board's filter.
	if r := f.do(owner, manage, "POST", "/api/v1/tasks", map[string]any{"title": "unrelated"}); r.code != http.StatusCreated {
		t.Fatal(r.body)
	} else {
		var u taskJSON
		r.json(t, &u)
		f.tasks = append(f.tasks, u.ID)
	}
	got := f.cards(owner, manage, b.ID)
	if got[open.ID].Count != 3 || len(got[open.ID].Items) != 3 || got[prog.ID].Count != 0 {
		t.Fatalf("open column: %+v", got[open.ID])
	}
	move := func(user string, perms []string, body map[string]any) resp {
		return f.do(user, perms, "POST", "/api/v1/tasks/boards/"+b.ID+"/moves", body)
	}

	// A move without expectedVersion is refused and changes nothing.
	if r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": prog.ID}); r.code != http.StatusBadRequest {
		t.Fatalf("move without version: %d %s", r.code, r.body)
	}
	// A stale version is a conflict and changes nothing.
	if r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": prog.ID, "expectedVersion": a.Version + 5}); r.code != http.StatusConflict || r.errCode() != "tasks.version_conflict" {
		t.Fatalf("stale move: %d %s", r.code, r.body)
	}
	// open -> in_progress runs "start".
	r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": prog.ID, "expectedVersion": a.Version})
	var mv struct {
		Task      taskJSON `json:"task"`
		Operation string   `json:"operation"`
	}
	r.json(t, &mv)
	if r.code != http.StatusOK || mv.Operation != "start" || mv.Task.Status != "in_progress" || mv.Task.Version != a.Version+1 {
		t.Fatalf("start move: %d %s", r.code, r.body)
	}
	// The same version again is stale now (a replay cannot apply twice).
	if r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": blocked.ID, "expectedVersion": a.Version, "reason": "x"}); r.code != http.StatusConflict {
		t.Fatalf("replayed move: %d %s", r.code, r.body)
	}
	// Block needs a reason code; without one nothing happens.
	if r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": blocked.ID, "expectedVersion": mv.Task.Version}); r.code != http.StatusBadRequest {
		t.Fatalf("block without reason: %d %s", r.code, r.body)
	}
	r = move(owner, manage, map[string]any{"taskId": a.ID, "columnId": blocked.ID, "expectedVersion": mv.Task.Version, "reason": "waiting for parts"})
	r.json(t, &mv)
	if r.code != http.StatusOK || mv.Operation != "block" || mv.Task.Status != "blocked" || mv.Task.Reason == nil || *mv.Task.Reason != "waiting for parts" {
		t.Fatalf("block move: %d %s", r.code, r.body)
	}
	// blocked -> completed is not a Task transition: refused by the state machine.
	if r := move(owner, manage, map[string]any{"taskId": a.ID, "columnId": done.ID, "expectedVersion": mv.Task.Version}); r.code != http.StatusConflict || r.errCode() != "tasks.invalid_transition" {
		t.Fatalf("forbidden transition: %d %s", r.code, r.body)
	}
	// blocked -> open runs unblock.
	r = move(owner, manage, map[string]any{"taskId": a.ID, "columnId": open.ID, "expectedVersion": mv.Task.Version})
	r.json(t, &mv)
	if mv.Operation != "unblock" || mv.Task.Status != "open" {
		t.Fatalf("unblock move: %d %s", r.code, r.body)
	}
	// open -> completed runs complete; completed -> in_progress is forbidden; completed -> open needs a reason (reopen).
	r = move(owner, manage, map[string]any{"taskId": c2.ID, "columnId": done.ID, "expectedVersion": c2.Version})
	var done2 struct {
		Task      taskJSON `json:"task"`
		Operation string   `json:"operation"`
	}
	r.json(t, &done2)
	if r.code != http.StatusOK || done2.Operation != "complete" || done2.Task.Status != "completed" {
		t.Fatalf("complete move: %d %s", r.code, r.body)
	}
	if r := move(owner, manage, map[string]any{"taskId": c2.ID, "columnId": prog.ID, "expectedVersion": done2.Task.Version}); r.code != http.StatusConflict || r.errCode() != "tasks.invalid_transition" {
		t.Fatalf("completed -> in progress: %d %s", r.code, r.body)
	}
	if r := move(owner, manage, map[string]any{"taskId": c2.ID, "columnId": open.ID, "expectedVersion": done2.Task.Version}); r.code != http.StatusBadRequest {
		t.Fatalf("reopen without reason: %d %s", r.code, r.body)
	}
	r = move(owner, manage, map[string]any{"taskId": c2.ID, "columnId": open.ID, "expectedVersion": done2.Task.Version, "reason": "was not finished"})
	r.json(t, &done2)
	if done2.Operation != "reopen" || done2.Task.Status != "open" {
		t.Fatalf("reopen move: %d %s", r.code, r.body)
	}
	// The cards follow the status; counts are per column.
	got = f.cards(owner, manage, b.ID)
	if got[open.ID].Count != 3 || got[done.ID].Count != 0 || got[prog.ID].Count != 0 {
		t.Fatalf("counts after moves: open=%d prog=%d done=%d", got[open.ID].Count, got[prog.ID].Count, got[done.ID].Count)
	}
	_ = c3
	// A move writes exactly the audit event of the Task operation (no generic status audit).
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.audit_events WHERE target_id = $1::text AND action = 'tasks.task.started'`, a.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("start audit rows = %d (%v)", n, err)
	}
	// Unknown task ids, unknown columns and malformed ids fail without effect.
	for _, body := range []map[string]any{
		{"taskId": newUUID(t), "columnId": open.ID, "expectedVersion": 1},
		{"taskId": a.ID, "columnId": newUUID(t), "expectedVersion": 1},
		{"taskId": "not-a-uuid", "columnId": open.ID, "expectedVersion": 1},
	} {
		if r := move(owner, manage, body); r.code < 400 || r.code == 500 {
			t.Errorf("bad move %v: %d %s", body, r.code, r.body)
		}
	}
}

func TestBoardRanksOrderCardsAndRejectInvalidAnchors(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	ta, tb, tc, td := f.task(owner, "a", ""), f.task(owner, "b", ""), f.task(owner, "c", ""), f.task(owner, "d", "")
	place := func(body map[string]any) resp {
		body["columnId"] = open.ID
		return f.do(owner, manage, "PUT", "/api/v1/tasks/boards/"+b.ID+"/ranks", body)
	}
	order := func() []string { return titles(f.cards(owner, manage, b.ID)[open.ID], f.token) }
	// Unranked cards come in the task order; ranking one after another ranks the cards above it first.
	if r := place(map[string]any{"taskId": ta.ID, "afterTaskId": tc.ID}); r.code != http.StatusOK {
		t.Fatalf("place a after c: %d %s", r.code, r.body)
	}
	got := order()
	posA, posC := indexOf(got, "a"), indexOf(got, "c")
	if posA != posC+1 {
		t.Fatalf("a must follow c: %v", got)
	}
	if r := place(map[string]any{"taskId": td.ID, "top": true}); r.code != http.StatusOK {
		t.Fatalf("place d on top: %d %s", r.code, r.body)
	}
	if got = order(); got[0] != "d" {
		t.Fatalf("d must be first: %v", got)
	}
	if r := place(map[string]any{"taskId": tb.ID, "afterTaskId": td.ID}); r.code != http.StatusOK {
		t.Fatalf("place b after d: %d %s", r.code, r.body)
	}
	if got = order(); got[0] != "d" || got[1] != "b" {
		t.Fatalf("b must follow d: %v", got)
	}
	// Ranks are unique and strictly ordered.
	cs := f.cards(owner, manage, b.ID)[open.ID]
	seen := map[string]bool{}
	prev := ""
	for _, it := range cs.Items {
		if it.Rank == nil || seen[*it.Rank] || *it.Rank <= prev {
			t.Fatalf("ranks not unique and ascending: %+v", cs.Items)
		}
		seen[*it.Rank], prev = true, *it.Rank
	}
	// Invalid placements: both anchors, none, itself, unknown, a card of another column.
	for name, body := range map[string]map[string]any{
		"both":    {"taskId": ta.ID, "afterTaskId": tb.ID, "top": true},
		"neither": {"taskId": ta.ID},
		"self":    {"taskId": ta.ID, "afterTaskId": ta.ID},
		"unknown": {"taskId": ta.ID, "afterTaskId": newUUID(t)},
		"badid":   {"taskId": "x", "top": true},
	} {
		if r := place(body); r.code < 400 || r.code == 500 {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	// A card whose status does not match the column cannot be ranked there (it must be moved).
	prog := b.col(t, "In progress")
	if r := f.do(owner, manage, "PUT", "/api/v1/tasks/boards/"+b.ID+"/ranks", map[string]any{"taskId": ta.ID, "columnId": prog.ID, "top": true}); r.code != http.StatusBadRequest {
		t.Fatalf("rank into a column of another status: %d %s", r.code, r.body)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// Concurrent placements serialize on the Board's rank lock: every one succeeds, ranks stay unique, the order is total.
func TestBoardRankWritesAreSerialized(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	const n = 16
	ids := make([]string, n)
	for i := range ids {
		ids[i] = f.task(owner, fmt.Sprintf("t%02d", i), "").ID
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := map[string]any{"taskId": ids[i], "columnId": open.ID}
			if i%2 == 0 {
				body["top"] = true
			} else {
				body["afterTaskId"] = ids[(i+n-1)%n]
			}
			codes[i] = f.do(owner, manage, "PUT", "/api/v1/tasks/boards/"+b.ID+"/ranks", body).code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		// The anchor may not be ranked yet, but it is always a card of the column, so every request succeeds.
		if c != http.StatusOK {
			t.Errorf("placement %d: %d", i, c)
		}
	}
	cs := f.cards(owner, manage, b.ID)[open.ID]
	if len(cs.Items) != n {
		t.Fatalf("cards = %d, want %d", len(cs.Items), n)
	}
	seen := map[string]bool{}
	prev := ""
	for _, it := range cs.Items {
		if it.Rank == nil || seen[*it.Rank] || *it.Rank <= prev {
			t.Fatalf("ranks not unique and ascending: %+v", cs.Items)
		}
		seen[*it.Rank], prev = true, *it.Rank
	}
	var dup int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) - count(DISTINCT rank) FROM tasks.board_card_ranks WHERE board_id = $1::uuid`, b.ID).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("duplicate ranks: %d %v", dup, err)
	}
}

// Rank writes re-check the Board edit access under the lock: a viewer, a revoked editor and a stranger are refused.
func TestBoardAccessIDORAndEditLevels(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner, bob, mallory := f.user(), f.user(), f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	ta, tb := f.task(owner, "a", ""), f.task(owner, "b", "")
	base := "/api/v1/tasks/boards/" + b.ID

	// A stranger with every tasks permission still gets a uniform 404 on every route: no oracle for board ids.
	all := []string{"tasks.manage", "tasks.view", "tasks.work", "tasks.boards.manage_team"}
	probes := []struct {
		method, path string
		body         any
	}{
		{"GET", base, nil},
		{"GET", base + "/cards", nil},
		{"PATCH", base, map[string]any{"expectedVersion": 1, "name": "x"}},
		{"POST", base + "/columns", map[string]any{"expectedVersion": 1, "operation": "rename", "columnId": open.ID, "title": "x"}},
		{"POST", base + "/moves", map[string]any{"taskId": ta.ID, "columnId": open.ID, "expectedVersion": 1}},
		{"PUT", base + "/ranks", map[string]any{"taskId": ta.ID, "columnId": open.ID, "top": true}},
		{"POST", base + "/archive", map[string]any{"expectedVersion": 1}},
	}
	for _, p := range probes {
		if r := f.do(mallory, all, p.method, p.path, p.body); r.code != http.StatusNotFound || r.errCode() != "tasks.board_not_found" {
			t.Errorf("stranger %s %s: %d %s", p.method, p.path, r.code, r.body)
		}
	}
	// The same answer for a board id that does not exist at all.
	if r := f.do(mallory, all, "GET", "/api/v1/tasks/boards/"+newUUID(t), nil); r.code != http.StatusNotFound || r.errCode() != "tasks.board_not_found" {
		t.Errorf("unknown board: %d %s", r.code, r.body)
	}
	// Boards a user cannot use are absent from the list.
	var list struct{ Items []boardJSON }
	f.do(mallory, all, "GET", "/api/v1/tasks/boards", nil).json(t, &list)
	for _, it := range list.Items {
		if it.ID == b.ID {
			t.Fatal("unshared board listed for a stranger")
		}
	}

	// Share with bob at the use level through the Views platform (the one sharing mechanism).
	vc := views.Caller{UserID: owner, Permissions: map[string]struct{}{"tasks.manage": {}, views.PermShare: {}}, CorrelationID: "board-share", Header: cookieFor(owner, "tasks.manage")}
	info, err := f.views.Get(context.Background(), vc, b.ViewID)
	if err != nil {
		t.Fatal(err)
	}
	info, err = f.views.SetShares(context.Background(), vc, b.ViewID, info.Version, []views.ShareInput{{SubjectType: "user", SubjectID: bob, Level: "use"}})
	if err != nil {
		t.Fatal(err)
	}
	bb := f.board(bob, viewer, b.ID)
	if bb.Access != "use" || bb.CanEdit {
		t.Fatalf("bob's access: %+v", bb)
	}
	// A user-level viewer cannot rank, change columns, update, archive or place a card in a non-first column.
	for _, p := range []struct {
		method, path string
		body         any
	}{
		{"PUT", base + "/ranks", map[string]any{"taskId": ta.ID, "columnId": open.ID, "top": true}},
		{"POST", base + "/columns", map[string]any{"expectedVersion": 1, "operation": "rename", "columnId": open.ID, "title": "x"}},
		{"PATCH", base, map[string]any{"expectedVersion": 1, "swimlane": "assignee"}},
		{"POST", base + "/archive", map[string]any{"expectedVersion": 1}},
		{"POST", base + "/moves", map[string]any{"taskId": ta.ID, "columnId": open.ID, "expectedVersion": ta.Version, "top": true}},
	} {
		if r := f.do(bob, append(viewer, "tasks.manage"), p.method, p.path, p.body); r.code != http.StatusForbidden {
			t.Errorf("use-level %s %s: %d %s", p.method, p.path, r.code, r.body)
		}
	}
	// Moving a card needs the Task permission, not the Board level: bob with tasks.view only is refused and the
	// task is untouched.
	prog := b.col(t, "In progress")
	if r := f.do(bob, viewer, "POST", base+"/moves", map[string]any{"taskId": ta.ID, "columnId": prog.ID, "expectedVersion": ta.Version}); r.code != http.StatusForbidden {
		t.Fatalf("view-only move: %d %s", r.code, r.body)
	}
	var cur taskJSON
	f.do(owner, manage, "GET", "/api/v1/tasks/"+ta.ID, nil).json(t, &cur)
	if cur.Status != "open" || cur.Version != ta.Version {
		t.Fatalf("task changed by a refused move: %+v", cur)
	}
	// With tasks.manage bob may move the card (still a use-level Board member) but not place it explicitly.
	if r := f.do(bob, manage, "POST", base+"/moves", map[string]any{"taskId": ta.ID, "columnId": prog.ID, "expectedVersion": ta.Version}); r.code != http.StatusOK {
		t.Fatalf("manager move on a shared board: %d %s", r.code, r.body)
	}

	// Edit level: bob may now rank and change columns.
	info, _ = f.views.Get(context.Background(), vc, b.ViewID)
	if _, err = f.views.SetShares(context.Background(), vc, b.ViewID, info.Version, []views.ShareInput{{SubjectType: "user", SubjectID: bob, Level: "edit"}}); err != nil {
		t.Fatal(err)
	}
	if r := f.do(bob, viewer, "PUT", base+"/ranks", map[string]any{"taskId": tb.ID, "columnId": open.ID, "top": true}); r.code != http.StatusOK {
		t.Fatalf("editor rank: %d %s", r.code, r.body)
	}
	cur2 := f.board(bob, viewer, b.ID)
	if r := f.do(bob, viewer, "POST", base+"/columns", map[string]any{"expectedVersion": cur2.Version, "operation": "rename", "columnId": open.ID, "title": "Backlog"}); r.code != http.StatusOK {
		t.Fatalf("editor columns: %d %s", r.code, r.body)
	}
	// An editor may not archive the board or change shares (owner only).
	if r := f.do(bob, viewer, "POST", base+"/archive", map[string]any{"expectedVersion": 2}); r.code != http.StatusForbidden {
		t.Fatalf("editor archive: %d %s", r.code, r.body)
	}

	// Revoking the share ends all access at once, including rank writes.
	info, _ = f.views.Get(context.Background(), vc, b.ViewID)
	if _, err = f.views.SetShares(context.Background(), vc, b.ViewID, info.Version, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range probes[:3] {
		if r := f.do(bob, all, p.method, p.path, p.body); r.code != http.StatusNotFound {
			t.Errorf("revoked %s %s: %d %s", p.method, p.path, r.code, r.body)
		}
	}
	if r := f.do(bob, all, "PUT", base+"/ranks", map[string]any{"taskId": tb.ID, "columnId": open.ID, "top": true}); r.code != http.StatusNotFound {
		t.Errorf("revoked rank write: %d %s", r.code, r.body)
	}
}

// A viewer who cannot see a task sees neither the card nor the count, cannot rank it and cannot use it as an anchor.
func TestBoardTaskVisibilityHidesCardsCountsAndRankTargets(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner, worker1 := f.user(), f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	mine, other, other2 := f.task(owner, "mine", worker1), f.task(owner, "other", ""), f.task(owner, "other2", "")
	vc := views.Caller{UserID: owner, Permissions: map[string]struct{}{"tasks.manage": {}, views.PermShare: {}}, Header: cookieFor(owner, "tasks.manage")}
	info, _ := f.views.Get(context.Background(), vc, b.ViewID)
	if _, err := f.views.SetShares(context.Background(), vc, b.ViewID, info.Version, []views.ShareInput{{SubjectType: "user", SubjectID: worker1, Level: "edit"}}); err != nil {
		t.Fatal(err)
	}
	cs := f.cards(worker1, worker, b.ID)[open.ID]
	if cs.Count != 1 || len(cs.Items) != 1 || cs.Items[0].ID != mine.ID {
		t.Fatalf("worker must see only the assigned task: %+v", cs)
	}
	base := "/api/v1/tasks/boards/" + b.ID
	// Ranking or moving an invisible task is a plain not-found, and an invisible anchor is rejected.
	if r := f.do(worker1, worker, "PUT", base+"/ranks", map[string]any{"taskId": other.ID, "columnId": open.ID, "top": true}); r.code != http.StatusNotFound {
		t.Errorf("rank invisible task: %d %s", r.code, r.body)
	}
	if r := f.do(worker1, worker, "PUT", base+"/ranks", map[string]any{"taskId": mine.ID, "columnId": open.ID, "afterTaskId": other2.ID}); r.code != http.StatusConflict || r.errCode() != "tasks.board_anchor_invalid" {
		t.Errorf("invisible anchor: %d %s", r.code, r.body)
	}
	prog := b.col(t, "In progress")
	if r := f.do(worker1, worker, "POST", base+"/moves", map[string]any{"taskId": other.ID, "columnId": prog.ID, "expectedVersion": other.Version}); r.code != http.StatusNotFound {
		t.Errorf("move invisible task: %d %s", r.code, r.body)
	}
	var cur taskJSON
	f.do(owner, manage, "GET", "/api/v1/tasks/"+other.ID, nil).json(t, &cur)
	if cur.Status != "open" {
		t.Fatalf("invisible task changed: %+v", cur)
	}
	// A worker may start the assigned task from the board (tasks.work on an own task).
	if r := f.do(worker1, worker, "POST", base+"/moves", map[string]any{"taskId": mine.ID, "columnId": prog.ID, "expectedVersion": mine.Version}); r.code != http.StatusOK {
		t.Fatalf("worker start: %d %s", r.code, r.body)
	}
	// A worker may not cancel (needs tasks.manage): add a cancelled column through the editor first.
	bd := f.board(worker1, worker, b.ID)
	if r := f.do(worker1, worker, "POST", base+"/columns", map[string]any{"expectedVersion": bd.Version, "operation": "add", "title": "Dropped", "mapsTo": "cancelled"}); r.code != http.StatusOK {
		t.Fatalf("add cancelled column: %d %s", r.code, r.body)
	}
	bd = f.board(worker1, worker, b.ID)
	if r := f.do(worker1, worker, "POST", base+"/moves", map[string]any{"taskId": mine.ID, "columnId": bd.col(t, "Dropped").ID, "expectedVersion": mine.Version + 1, "reason": "no"}); r.code != http.StatusForbidden {
		t.Fatalf("worker cancel: %d %s", r.code, r.body)
	}
}

func TestBoardColumnsWIPAndSamestatusColumns(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	base := "/api/v1/tasks/boards/" + b.ID
	open := b.col(t, "Open")
	colOp := func(version int, body map[string]any) (resp, boardJSON) {
		body["expectedVersion"] = version
		r := f.do(owner, manage, "POST", base+"/columns", body)
		var nb boardJSON
		if r.code == http.StatusOK {
			r.json(t, &nb)
		}
		return r, nb
	}
	// Stale version, unknown column, bad operation, bad mapping.
	if r, _ := colOp(99, map[string]any{"operation": "rename", "columnId": open.ID, "title": "x"}); r.code != http.StatusConflict || r.errCode() != "tasks.board_conflict" {
		t.Errorf("stale: %d %s", r.code, r.body)
	}
	for name, body := range map[string]map[string]any{
		"unknown column": {"operation": "rename", "columnId": newUUID(t), "title": "x"},
		"bad op":         {"operation": "explode", "columnId": open.ID},
		"bad mapping":    {"operation": "remap", "columnId": open.ID, "mapsTo": "archived"},
		"empty title":    {"operation": "rename", "columnId": open.ID, "title": "  "},
		"long title":     {"operation": "rename", "columnId": open.ID, "title": strings.Repeat("x", 41)},
		"bad wip":        {"operation": "set_wip", "columnId": open.ID, "wipLimit": 0},
	} {
		if r, _ := colOp(1, body); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	// Add a second column mapped to open, then rename, WIP limit, reorder and remap.
	r, nb := colOp(1, map[string]any{"operation": "add", "title": "Backlog", "mapsTo": "open", "position": 0})
	if r.code != http.StatusOK || len(nb.Columns) != 5 || nb.Columns[0].Title != "Backlog" || nb.Version != 2 {
		t.Fatalf("add: %d %s", r.code, r.body)
	}
	backlog := nb.col(t, "Backlog")
	if r, nb = colOp(2, map[string]any{"operation": "set_wip", "columnId": open.ID, "wipLimit": 2}); r.code != http.StatusOK || nb.Version != 3 {
		t.Fatalf("wip: %d %s", r.code, r.body)
	}
	ta, tb, tc := f.task(owner, "a", ""), f.task(owner, "b", ""), f.task(owner, "c", "")
	_ = tb
	_ = tc
	// Backlog is the first column of status open: all three open cards are listed there, none in the later Open column.
	got := f.cards(owner, manage, b.ID)
	if got[backlog.ID].Count != 3 || got[open.ID].Count != 0 || got[backlog.ID].OverWIP {
		t.Fatalf("first column of the status holds the cards: backlog=%d open=%d", got[backlog.ID].Count, got[open.ID].Count)
	}
	// Placing a card into the second open column is a presentation change: the task keeps status and version.
	r = f.do(owner, manage, "POST", base+"/moves", map[string]any{"taskId": ta.ID, "columnId": open.ID, "expectedVersion": ta.Version, "top": true})
	var mv struct {
		Task      taskJSON `json:"task"`
		Operation string   `json:"operation"`
		Placed    bool     `json:"placed"`
	}
	r.json(t, &mv)
	if r.code != http.StatusOK || mv.Operation != "" || !mv.Placed || mv.Task.Version != ta.Version || mv.Task.Status != "open" {
		t.Fatalf("placement move: %d %s", r.code, r.body)
	}
	got = f.cards(owner, manage, b.ID)
	if got[backlog.ID].Count != 2 || got[open.ID].Count != 1 || got[open.ID].Items[0].ID != ta.ID {
		t.Fatalf("after placement: backlog=%d open=%d", got[backlog.ID].Count, got[open.ID].Count)
	}
	// Dropping it back on the first column of the status ends the placement.
	r = f.do(owner, manage, "POST", base+"/moves", map[string]any{"taskId": ta.ID, "columnId": backlog.ID, "expectedVersion": ta.Version})
	if r.code != http.StatusOK {
		t.Fatalf("drop back: %d %s", r.code, r.body)
	}
	if got = f.cards(owner, manage, b.ID); got[backlog.ID].Count != 3 || got[open.ID].Count != 0 {
		t.Fatalf("after drop back: backlog=%d open=%d", got[backlog.ID].Count, got[open.ID].Count)
	}
	// WIP is soft: three cards in a column limited to 2 are reported, never refused.
	if r, nb = colOp(3, map[string]any{"operation": "set_wip", "columnId": backlog.ID, "wipLimit": 2}); r.code != http.StatusOK {
		t.Fatalf("wip backlog: %d %s", r.code, r.body)
	}
	if got = f.cards(owner, manage, b.ID); !got[backlog.ID].OverWIP || got[backlog.ID].Count != 3 {
		t.Fatalf("over wip not reported: %+v", got[backlog.ID])
	}
	// Reorder, collapse, remap, remove; the minimum of two columns holds.
	if r, nb = colOp(nb.Version, map[string]any{"operation": "reorder", "columnId": backlog.ID, "position": 4}); r.code != http.StatusOK || nb.Columns[4].ID != backlog.ID {
		t.Fatalf("reorder: %d %s", r.code, r.body)
	}
	if r, nb = colOp(nb.Version, map[string]any{"operation": "collapse", "columnId": open.ID, "collapsed": true}); r.code != http.StatusOK {
		t.Fatalf("collapse: %d %s", r.code, r.body)
	}
	if r, nb = colOp(nb.Version, map[string]any{"operation": "remap", "columnId": open.ID, "mapsTo": "blocked"}); r.code != http.StatusOK || nb.col(t, "Open").MapsTo != "blocked" {
		t.Fatalf("remap: %d %s", r.code, r.body)
	}
	for len(nb.Columns) > 2 {
		if r, nb = colOp(nb.Version, map[string]any{"operation": "remove", "columnId": nb.Columns[0].ID}); r.code != http.StatusOK {
			t.Fatalf("remove: %d %s", r.code, r.body)
		}
	}
	if r, _ = colOp(nb.Version, map[string]any{"operation": "remove", "columnId": nb.Columns[0].ID}); r.code != http.StatusBadRequest {
		t.Fatalf("remove below the minimum: %d %s", r.code, r.body)
	}
	// Cards were never touched by column changes.
	var cur taskJSON
	f.do(owner, manage, "GET", "/api/v1/tasks/"+ta.ID, nil).json(t, &cur)
	if cur.Status != "open" || cur.Version != ta.Version {
		t.Fatalf("column changes touched a task: %+v", cur)
	}
}

func TestBoardLimitsArchiveAndPins(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner, bob := f.user(), f.user()
	// Column count limits on create.
	cols := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"title": fmt.Sprintf("c%d", i), "mapsTo": "open"}
		}
		return out
	}
	for _, n := range []int{1, 9} {
		if r := f.do(owner, manage, "POST", "/api/v1/tasks/boards", map[string]any{"name": fmt.Sprintf("bad%d", n), "columns": cols(n)}); r.code != http.StatusBadRequest {
			t.Errorf("%d columns: %d %s", n, r.code, r.body)
		}
	}
	// No task permission: no boards at all.
	if r := f.do(bob, nil, "GET", "/api/v1/tasks/boards", nil); r.code != http.StatusForbidden {
		t.Errorf("no permission: %d", r.code)
	}
	// Per-owner limit of 20 boards.
	var first boardJSON
	for i := 0; i < 20; i++ {
		r := f.do(owner, manage, "POST", "/api/v1/tasks/boards", map[string]any{"name": fmt.Sprintf("Limit %02d", i)})
		if r.code != http.StatusCreated {
			t.Fatalf("board %d: %d %s", i, r.code, r.body)
		}
		if i == 0 {
			r.json(t, &first)
		}
	}
	if r := f.do(owner, manage, "POST", "/api/v1/tasks/boards", map[string]any{"name": "Limit 21"}); r.code != http.StatusConflict {
		t.Errorf("21st board: %d %s", r.code, r.body)
	}
	// Archive: stale version, then success; archived boards vanish for non-owners and refuse changes.
	base := "/api/v1/tasks/boards/" + first.ID
	if r := f.do(owner, manage, "POST", base+"/archive", map[string]any{"expectedVersion": 9}); r.code != http.StatusConflict {
		t.Errorf("stale archive: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "POST", base+"/archive", map[string]any{}); r.code != http.StatusBadRequest {
		t.Errorf("archive without version: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "POST", base+"/archive", map[string]any{"expectedVersion": 1}); r.code != http.StatusOK {
		t.Fatalf("archive: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "PATCH", base, map[string]any{"expectedVersion": 2, "name": "x"}); r.code != http.StatusConflict || r.errCode() != "tasks.board_archived" {
		t.Errorf("update archived: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "POST", base+"/restore", map[string]any{"expectedVersion": 2}); r.code != http.StatusOK {
		t.Fatalf("restore: %d %s", r.code, r.body)
	}
	// Update with a stale board version, and with a filter the editor cannot run.
	if r := f.do(owner, manage, "PATCH", base, map[string]any{"expectedVersion": 1, "name": "x"}); r.code != http.StatusConflict {
		t.Errorf("stale update: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "PATCH", base, map[string]any{"expectedVersion": 3, "filter": map[string]any{"v": 1, "root": map[string]any{"type": "condition", "field": "nope", "op": "equals", "value": "x"}}}); r.code != http.StatusBadRequest {
		t.Errorf("invalid filter: %d %s", r.code, r.body)
	}

	// Pins reuse the existing mechanism: a pinned board View is reported as a board with its id.
	vc := views.Caller{UserID: owner, Permissions: map[string]struct{}{"tasks.manage": {}}, Header: cookieFor(owner, "tasks.manage")}
	if err := f.views.ReplacePins(context.Background(), vc, []views.PinInput{{ViewID: first.ViewID, GroupKey: "tasks", Position: 0}}); err != nil {
		t.Fatalf("pin: %v", err)
	}
	pins, err := f.views.Pins(context.Background(), vc)
	if err != nil || len(pins) != 1 || pins[0].Kind != "board" || pins[0].Ref != first.ID {
		t.Fatalf("pins = %+v %v", pins, err)
	}
}

func TestTeamBoardsNeedTheTeamPermissionAndReachMembers(t *testing.T) {
	team := newUUID(t)
	f := newBoardFixture(t, nil)
	owner, member, outsider := f.user(), f.user(), f.user()
	f2 := newBoardFixture(t, map[string][]string{member: {team}, owner: {team}})
	// Reuse the second fixture (it knows the team membership); track users for cleanup there.
	f2.users = append(f2.users, owner, member, outsider)
	_ = f
	// tasks.work alone may not create a Team board.
	if r := f2.do(member, worker, "POST", "/api/v1/tasks/boards", map[string]any{"name": "Team " + f2.token, "teamId": team}); r.code != http.StatusForbidden {
		t.Fatalf("team board without permission: %d %s", r.code, r.body)
	}
	r := f2.do(owner, []string{"tasks.work", "tasks.boards.manage_team"}, "POST", "/api/v1/tasks/boards", map[string]any{"name": "Team " + f2.token, "teamId": team})
	if r.code != http.StatusCreated {
		t.Fatalf("create team board: %d %s", r.code, r.body)
	}
	var b boardJSON
	r.json(t, &b)
	// Members see it through the implicit Team share; outsiders do not.
	if got := f2.board(member, worker, b.ID); got.Access != "use" || got.CanEdit {
		t.Fatalf("member access: %+v", got)
	}
	if r := f2.do(outsider, worker, "GET", "/api/v1/tasks/boards/"+b.ID, nil); r.code != http.StatusNotFound {
		t.Fatalf("outsider: %d %s", r.code, r.body)
	}
	// A member with the team permission may change columns and ranks; without it the board is read-only.
	if got := f2.board(member, []string{"tasks.work", "tasks.boards.manage_team"}, b.ID); !got.CanEdit {
		t.Fatalf("member with manage_team cannot edit: %+v", got)
	}
	if r := f2.do(member, worker, "POST", "/api/v1/tasks/boards/"+b.ID+"/columns", map[string]any{"expectedVersion": 1, "operation": "rename", "columnId": b.Columns[0].ID, "title": "x"}); r.code != http.StatusForbidden {
		t.Fatalf("member without manage_team edits columns: %d %s", r.code, r.body)
	}
	if r := f2.do(member, []string{"tasks.work", "tasks.boards.manage_team"}, "POST", "/api/v1/tasks/boards/"+b.ID+"/columns", map[string]any{"expectedVersion": 1, "operation": "rename", "columnId": b.Columns[0].ID, "title": "Todo"}); r.code != http.StatusOK {
		t.Fatalf("member with manage_team edits columns: %d %s", r.code, r.body)
	}
}

// Board audit entries carry ids, versions and counts only: never the name or the filter values.
func TestBoardAuditCarriesNoNamesOrFilterValues(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, map[string]any{"name": "Secret payroll board " + f.token})
	f.do(owner, manage, "POST", "/api/v1/tasks/boards/"+b.ID+"/columns", map[string]any{"expectedVersion": 1, "operation": "rename", "columnId": b.Columns[0].ID, "title": "Salaries"})
	rows, err := f.pool.Query(context.Background(), `SELECT action, coalesce(metadata::text, '') || coalesce(after_data::text, '') || coalesce(before_data::text, '') FROM platform.audit_events WHERE target_id = $1::text`, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actions := map[string]bool{}
	for rows.Next() {
		var action, payload string
		if err := rows.Scan(&action, &payload); err != nil {
			t.Fatal(err)
		}
		actions[action] = true
		if strings.Contains(payload, "payroll") || strings.Contains(payload, "Salaries") || strings.Contains(payload, f.token) {
			t.Errorf("audit %s leaks content: %s", action, payload)
		}
	}
	if !actions["tasks.board.created"] || !actions["tasks.board.column_changed"] {
		t.Errorf("audit actions = %v", actions)
	}
}

// A column with more cards than one page pages by cursor: ranked cards first in rank order, then the rest, with no
// duplicate or missing card, and the count is the visible total.
func TestBoardColumnPagination(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	const n = 130
	ids := make([]string, n)
	for i := range ids {
		ids[i] = f.task(owner, fmt.Sprintf("p%03d", i), "").ID
	}
	base := "/api/v1/tasks/boards/" + b.ID
	// Rank two cards to the top, in a known order.
	for _, body := range []map[string]any{{"taskId": ids[100], "top": true}, {"taskId": ids[50], "top": true}} {
		body["columnId"] = open.ID
		if r := f.do(owner, manage, "PUT", base+"/ranks", body); r.code != http.StatusOK {
			t.Fatalf("rank: %d %s", r.code, r.body)
		}
	}
	seen := map[string]bool{}
	var order []string
	cursor := ""
	pages := 0
	for {
		path := base + "/cards?column=" + open.ID + "&limit=40"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r := f.do(owner, manage, "GET", path, nil)
		if r.code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, r.code, r.body)
		}
		var out struct{ Columns []colCardsJSON }
		r.json(t, &out)
		c := out.Columns[0]
		if c.Count != n || c.CountCapped {
			t.Fatalf("count = %d capped=%v", c.Count, c.CountCapped)
		}
		for _, it := range c.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate card %s", it.ID)
			}
			seen[it.ID] = true
			order = append(order, it.ID)
		}
		pages++
		if c.NextCursor == "" {
			break
		}
		cursor = c.NextCursor
	}
	if len(seen) != n || pages != 4 {
		t.Fatalf("cards = %d pages = %d", len(seen), pages)
	}
	if order[0] != ids[50] || order[1] != ids[100] {
		t.Fatalf("ranked cards must come first in rank order: %v", order[:3])
	}
	// A cursor of another column or a tampered cursor is refused.
	if r := f.do(owner, manage, "GET", base+"/cards?column="+b.col(t, "Blocked").ID+"&cursor="+cursor, nil); r.code != http.StatusBadRequest {
		t.Errorf("foreign cursor: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "GET", base+"/cards?cursor=abc", nil); r.code != http.StatusBadRequest {
		t.Errorf("cursor without column: %d %s", r.code, r.body)
	}
	if r := f.do(owner, manage, "GET", base+"/cards?limit=0", nil); r.code != http.StatusBadRequest {
		t.Errorf("limit 0: %d %s", r.code, r.body)
	}
}

// Repeated placement at the same spot lengthens ranks; the board rebalances before the length limit, keeps the
// order and never fails.
func TestBoardRanksRebalanceKeepsTheOrder(t *testing.T) {
	f := newBoardFixture(t, nil)
	owner := f.user()
	b := f.create(owner, nil)
	open := b.col(t, "Open")
	ids := []string{f.task(owner, "a", "").ID, f.task(owner, "b", "").ID, f.task(owner, "c", "").ID}
	base := "/api/v1/tasks/boards/" + b.ID
	for i := 0; i < 330; i++ {
		id := ids[i%3]
		if r := f.do(owner, manage, "PUT", base+"/ranks", map[string]any{"taskId": id, "columnId": open.ID, "top": true}); r.code != http.StatusOK {
			t.Fatalf("step %d: %d %s", i, r.code, r.body)
		}
	}
	// 330 = 3 * 110: the last placed is ids[2], before it ids[1], then ids[0].
	cs := f.cards(owner, manage, b.ID)[open.ID]
	if len(cs.Items) != 3 || cs.Items[0].ID != ids[2] || cs.Items[1].ID != ids[1] || cs.Items[2].ID != ids[0] {
		t.Fatalf("order after many top placements: %+v", cs.Items)
	}
	var longest int
	if err := f.pool.QueryRow(context.Background(), `SELECT max(length(rank)) FROM tasks.board_card_ranks WHERE board_id = $1::uuid`, b.ID).Scan(&longest); err != nil || longest > 48 {
		t.Fatalf("longest rank %d %v", longest, err)
	}
}
