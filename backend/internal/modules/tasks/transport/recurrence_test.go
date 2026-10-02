package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type stubDefs struct {
	def   application.Definition
	calls int
	last  application.NewDefinition
}

func (s *stubDefs) Insert(_ context.Context, _ application.Caller, n application.NewDefinition) (application.Definition, error) {
	s.calls++
	s.last = n
	d := s.def
	d.Title, d.Rule, d.NextRunAt = n.Title, n.Rule, &n.NextRunAt
	return d, nil
}
func (s *stubDefs) Get(context.Context, string) (application.Definition, error) { return s.def, nil }
func (s *stubDefs) List(context.Context, application.Page) (application.Result[application.Definition], error) {
	return application.Result[application.Definition]{Items: []application.Definition{s.def}}, nil
}
func (s *stubDefs) Change(_ context.Context, _ application.Caller, _ string, decide func(application.Definition) (application.DefinitionChange, error)) (application.Definition, error) {
	s.calls++
	ch, err := decide(s.def)
	if err != nil {
		return application.Definition{}, err
	}
	return ch.Next, nil
}
func (s *stubDefs) Delete(context.Context, application.Caller, string, *int) error {
	s.calls++
	return nil
}
func (s *stubDefs) GenerateDue(context.Context, time.Time, func(application.Definition) (application.Generation, error)) (bool, bool, error) {
	return false, false, nil
}

func newDef() *stubDefs {
	next := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	return &stubDefs{def: application.Definition{
		ID: "00000000-0000-7000-8000-000000000001", Title: "Check backups", Priority: "normal", Active: true, NextRunAt: &next, Version: 1,
		Rule: application.Rule{Frequency: "daily", Interval: 1, TimeOfDay: "09:00", Timezone: "UTC", StartsOn: "2026-01-01"},
	}}
}

func serveRec(t *testing.T, s *stubDefs, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRecurrence(mux, application.NewRecurrenceService(s, stubDir{}, func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

const defPath = "/api/v1/recurring-task-definitions/00000000-0000-7000-8000-000000000001"
const goodDef = `{"title":"Check backups","rule":{"frequency":"daily","interval":1,"timeOfDay":"09:00","timezone":"Europe/Berlin","startsOn":"2026-01-01"}}`

func TestRecurrenceRoutesRequireTheRecurrencePermission(t *testing.T) {
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/recurring-task-definitions", ""},
		{"POST", "/api/v1/recurring-task-definitions", goodDef},
		{"GET", defPath, ""},
		{"PATCH", defPath, `{"expectedVersion":1,"title":"x"}`},
		{"DELETE", defPath, ""},
		{"POST", defPath + "/pause", `{}`},
		{"POST", defPath + "/resume", `{}`},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			store := newDef()
			if rec := serveRec(t, store, authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
				t.Errorf("unauthenticated = %d", rec.Code)
			}
			for _, p := range []string{"tasks.manage", "tasks.view", "tasks.work", "platform.admin"} {
				if rec := serveRec(t, store, with(p), r.method, r.path, r.body); rec.Code != 403 {
					t.Errorf("%s = %d, want 403", p, rec.Code)
				}
			}
			if store.calls != 0 {
				t.Errorf("store called %d times without permission", store.calls)
			}
			if rec := serveRec(t, store, with("tasks.recurrence.manage"), r.method, r.path, r.body); rec.Code >= 400 {
				t.Errorf("with permission = %d: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCreateMapsTheRuleAndSchedulesTheFirstRun(t *testing.T) {
	store := newDef()
	rec := serveRec(t, store, with("tasks.recurrence.manage"), "POST", "/api/v1/recurring-task-definitions", goodDef)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if store.last.Rule.Timezone != "Europe/Berlin" || store.last.Rule.Frequency != "daily" {
		t.Errorf("rule = %+v", store.last.Rule)
	}
	// 2026-10-02 12:00 UTC is 14:00 in Berlin, so the next 09:00 local run is on Oct 3 at 07:00 UTC.
	if want := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC); !store.last.NextRunAt.Equal(want) {
		t.Errorf("first run = %v, want %v", store.last.NextRunAt, want)
	}
	if !strings.Contains(rec.Body.String(), `"weekday":null`) || !strings.Contains(rec.Body.String(), `"dayOfMonth":null`) {
		t.Errorf("unused rule fields must be null: %s", rec.Body)
	}
}

func TestRecurrenceInvalidInput(t *testing.T) {
	cases := map[string]struct {
		method, path, body string
		status             int
	}{
		"not json":         {"POST", "/api/v1/recurring-task-definitions", `x`, 400},
		"unknown field":    {"POST", "/api/v1/recurring-task-definitions", `{"title":"x","admin":1}`, 400},
		"no rule":          {"POST", "/api/v1/recurring-task-definitions", `{"title":"x"}`, 400},
		"bad zone":         {"POST", "/api/v1/recurring-task-definitions", `{"title":"x","rule":{"frequency":"daily","interval":1,"timeOfDay":"09:00","timezone":"Nowhere/City","startsOn":"2026-01-01"}}`, 400},
		"weekly no day":    {"POST", "/api/v1/recurring-task-definitions", `{"title":"x","rule":{"frequency":"weekly","interval":1,"timeOfDay":"09:00","timezone":"UTC","startsOn":"2026-01-01"}}`, 400},
		"empty update":     {"PATCH", defPath, `{"expectedVersion":1}`, 400},
		"missing version":  {"PATCH", defPath, `{"title":"x"}`, 400},
		"bad limit":        {"GET", "/api/v1/recurring-task-definitions?limit=0", ``, 400},
		"bad expected":     {"DELETE", defPath + "?expectedVersion=x", ``, 400},
		"stale version":    {"POST", defPath + "/pause", `{"expectedVersion":9}`, 409},
		"clear and assign": {"PATCH", defPath, `{"expectedVersion":1,"clearAssignment":true,"assignedUserId":"00000000-0000-7000-8000-0000000000b1"}`, 400},
	}
	for name, c := range cases {
		rec := serveRec(t, newDef(), with("tasks.recurrence.manage"), c.method, c.path, c.body)
		if rec.Code != c.status {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, c.status, rec.Body)
		}
	}
}

func TestRecurrenceResponsesAreNotCached(t *testing.T) {
	rec := serveRec(t, newDef(), with("tasks.recurrence.manage"), "GET", "/api/v1/recurring-task-definitions", "")
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
