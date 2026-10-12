package transport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type fakeAuth struct {
	user  string
	perms map[string]struct{}
}

func (f fakeAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: f.user, Permissions: f.perms}, f.user != "", nil
}

func as(user string, perms ...string) fakeAuth {
	m := map[string]struct{}{}
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return fakeAuth{user: user, perms: m}
}

// tickets lets every user work the one Ticket.
type tickets struct{ ticket, queue string }

func (t tickets) Access(_ context.Context, _ string, ids []string) (map[string]application.TicketAccess, error) {
	out := map[string]application.TicketAccess{}
	for _, id := range ids {
		if id == t.ticket {
			out[id] = application.TicketAccess{Found: true, Reference: "T-9", QueueID: t.queue, CanView: true, CanWork: true}
		}
	}
	return out, nil
}

func (t tickets) ViewQueues(context.Context, string) (bool, []string, error) {
	return false, []string{t.queue}, nil
}

type dir struct{}

func (dir) ActiveEmployees(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Someone"
	}
	return out, nil
}

func serve(t *testing.T, ticket string, a fakeAuth) *http.ServeMux {
	pool := dbtest.Pool(t)
	svc := application.NewService(repository.New(pool), tickets{ticket: ticket, queue: newID()}, dir{})
	mux := http.NewServeMux()
	transport.Register(mux, svc, authenticator(a), slog.New(slog.DiscardHandler))
	return mux
}

type authenticator fakeAuth

func (a authenticator) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	return fakeAuth(a).Authenticate(r)
}

func do(mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	httpx.Middleware(slog.New(slog.DiscardHandler), mux).ServeHTTP(rec, req)
	return rec
}

func code(rec *httptest.ResponseRecorder) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Error.Code
}

func TestEntryRoutesAndErrorCodes(t *testing.T) {
	user, ticket := newID(), newID()
	mux := serve(t, ticket, as(user, application.PermLog))
	today := time.Now().UTC().Format(time.DateOnly)
	// A date far away from other test runs keeps the 24-hour rule of this person independent.
	entry := map[string]any{"subjectType": "ticket", "subjectId": ticket, "workDate": today, "durationMinutes": 45, "note": "replaced switch"}

	if rec := do(mux, "POST", "/api/v1/time-tracking/entries", map[string]any{"subjectType": "ticket"}); rec.Code != 400 || code(rec) != "timetracking.invalid_request" {
		t.Errorf("invalid: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "POST", "/api/v1/time-tracking/entries", map[string]any{"subjectType": "ticket", "subjectId": newID(), "workDate": today, "durationMinutes": 5}); rec.Code != 404 || code(rec) != "timetracking.not_found" {
		t.Errorf("unknown ticket: %d %s", rec.Code, rec.Body)
	}
	old := map[string]any{"subjectType": "ticket", "subjectId": ticket, "workDate": time.Now().UTC().AddDate(0, 0, -200).Format(time.DateOnly), "durationMinutes": 5}
	if rec := do(mux, "POST", "/api/v1/time-tracking/entries", old); rec.Code != 422 || code(rec) != "timetracking.outside_edit_window" {
		t.Errorf("outside window: %d %s", rec.Code, rec.Body)
	}
	rec := do(mux, "POST", "/api/v1/time-tracking/entries", entry)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID       string `json:"id"`
		Version  int    `json:"version"`
		UserID   string `json:"userId"`
		Own      bool   `json:"own"`
		Editable bool   `json:"editable"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.UserID != user || !created.Own || !created.Editable {
		t.Errorf("created = %+v", created)
	}
	huge := map[string]any{"subjectType": "ticket", "subjectId": ticket, "workDate": today, "durationMinutes": 1440}
	if rec := do(mux, "POST", "/api/v1/time-tracking/entries", huge); rec.Code != 422 || code(rec) != "timetracking.day_limit" {
		t.Errorf("day limit: %d %s", rec.Code, rec.Body)
	}

	panel := do(mux, "GET", "/api/v1/time-tracking/entries?subjectType=ticket&subjectId="+ticket, nil)
	var p struct {
		Items        []struct{ ID string } `json:"items"`
		TotalMinutes int                   `json:"totalMinutes"`
		CanLog       bool                  `json:"canLog"`
	}
	_ = json.Unmarshal(panel.Body.Bytes(), &p)
	if panel.Code != 200 || !p.CanLog || p.TotalMinutes < 45 || len(p.Items) == 0 || panel.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("panel: %d %s", panel.Code, panel.Body)
	}
	if rec := do(mux, "GET", "/api/v1/time-tracking/entries?subjectType=ticket&subjectId="+newID(), nil); rec.Code != 404 {
		t.Errorf("panel of unknown ticket: %d", rec.Code)
	}
	if rec := do(mux, "GET", "/api/v1/time-tracking/entries", nil); rec.Code != 400 {
		t.Errorf("no selector: %d", rec.Code)
	}
	mine := do(mux, "GET", "/api/v1/time-tracking/entries?mine=true&from="+today+"&to="+today, nil)
	if mine.Code != 200 || !strings.Contains(mine.Body.String(), `"reference":"T-9"`) {
		t.Errorf("mine: %d %s", mine.Code, mine.Body)
	}

	upd := map[string]any{"subjectId": ticket, "workDate": today, "durationMinutes": 50, "note": "", "expectedVersion": created.Version}
	if rec := do(mux, "PUT", "/api/v1/time-tracking/entries/"+created.ID, map[string]any{"subjectId": ticket, "workDate": today, "durationMinutes": 50}); rec.Code != 400 {
		t.Errorf("expectedVersion required: %d", rec.Code)
	}
	upd["expectedVersion"] = created.Version + 5
	if rec := do(mux, "PUT", "/api/v1/time-tracking/entries/"+created.ID, upd); rec.Code != 409 || code(rec) != "timetracking.version_conflict" {
		t.Errorf("conflict: %d %s", rec.Code, rec.Body)
	}
	upd["expectedVersion"] = created.Version
	if rec := do(mux, "PUT", "/api/v1/time-tracking/entries/"+created.ID, upd); rec.Code != 200 {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	other := serve(t, ticket, as(newID(), application.PermLog))
	if rec := do(other, "POST", "/api/v1/time-tracking/entries/"+created.ID+"/delete", map[string]any{"deleteReason": "other", "expectedVersion": 2}); rec.Code != 404 {
		t.Errorf("another person's entry: %d %s", rec.Code, rec.Body)
	}
	if rec := do(mux, "POST", "/api/v1/time-tracking/entries/"+created.ID+"/delete", map[string]any{"deleteReason": "nope", "expectedVersion": 2}); rec.Code != 400 {
		t.Errorf("closed reason: %d", rec.Code)
	}
	if rec := do(mux, "POST", "/api/v1/time-tracking/entries/"+created.ID+"/delete", map[string]any{"deleteReason": "wrong_duration", "expectedVersion": 2}); rec.Code != 204 {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
}

func TestPermissionsAndSettingsRoutes(t *testing.T) {
	ticket := newID()
	nobody := serve(t, ticket, as(newID()))
	today := time.Now().UTC().Format(time.DateOnly)
	if rec := do(nobody, "POST", "/api/v1/time-tracking/entries", map[string]any{"subjectType": "ticket", "subjectId": ticket, "workDate": today, "durationMinutes": 5}); rec.Code != 403 || code(rec) != "timetracking.not_permitted" {
		t.Errorf("create without permission: %d %s", rec.Code, rec.Body)
	}
	if rec := do(nobody, "GET", "/api/v1/time-tracking/entries?mine=true&from="+today+"&to="+today, nil); rec.Code != 403 {
		t.Errorf("mine without permission: %d", rec.Code)
	}
	if rec := do(nobody, "GET", "/api/v1/time-tracking/settings", nil); rec.Code != 403 {
		t.Errorf("settings without admin: %d", rec.Code)
	}
	anon := serve(t, ticket, as(""))
	if rec := do(anon, "GET", "/api/v1/time-tracking/settings", nil); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}

	admin := serve(t, ticket, as(newID(), application.PermAdmin))
	pool := dbtest.Pool(t)
	if _, err := pool.Exec(context.Background(), `UPDATE timetracking.settings SET codetermination_basis=NULL, codetermination_recorded_on=NULL, version=1`); err != nil {
		t.Fatal(err)
	}
	rec := do(admin, "GET", "/api/v1/time-tracking/settings", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"codeterminationBasis":null`) {
		t.Fatalf("get settings: %d %s", rec.Code, rec.Body)
	}
	body := map[string]any{"codeterminationBasis": "not_applicable", "codeterminationRecordedOn": today, "editWindowDays": 14, "retentionMonths": 24,
		"showEntryAuthors": true, "minReportGroupSize": 3}
	if rec := do(admin, "PUT", "/api/v1/time-tracking/settings", body); rec.Code != 400 {
		t.Errorf("expectedVersion required: %d", rec.Code)
	}
	body["expectedVersion"] = 1
	if rec := do(admin, "PUT", "/api/v1/time-tracking/settings", body); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"codeterminationBasis":"not_applicable"`) {
		t.Errorf("put settings: %d %s", rec.Code, rec.Body)
	}
	if rec := do(admin, "PUT", "/api/v1/time-tracking/settings", body); rec.Code != 409 {
		t.Errorf("stale settings: %d", rec.Code)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE timetracking.settings SET codetermination_basis=NULL, codetermination_recorded_on=NULL, version=1`); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryRouteNeedsReportPermissionAndRejectsPersonGrouping(t *testing.T) {
	user := newID()
	mux := serve(t, newID(), as(user, "timetracking.log"))
	if rec := do(mux, "GET", "/api/v1/time-tracking/summary?from=2026-01-01&to=2026-01-31&groupBy=queue", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without report permission: %d", rec.Code)
	}
	mux = serve(t, newID(), as(user, "timetracking.report"))
	for _, g := range []string{"user", "assignee", "person", ""} {
		if rec := do(mux, "GET", "/api/v1/time-tracking/summary?from=2026-01-01&to=2026-01-31&groupBy="+g, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("groupBy=%q: %d", g, rec.Code)
		}
	}
	if rec := do(mux, "GET", "/api/v1/time-tracking/summary?from=2024-01-01&to=2026-01-31&groupBy=queue", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("period over 366 days: %d", rec.Code)
	}
	if rec := do(mux, "GET", "/api/v1/time-tracking/summary?from=2026-01-01&to=2026-01-31&groupBy=month", nil); rec.Code != http.StatusOK {
		t.Errorf("valid: %d", rec.Code)
	}
}
