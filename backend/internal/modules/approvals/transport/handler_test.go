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

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type auth struct {
	user string
	ok   bool
}

func (a auth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: a.user, Permissions: map[string]struct{}{}}, a.ok, nil
}

type store struct {
	application.Store
	approval application.Approval
	err      error
	query    application.InboxQuery
}

func (s *store) Get(context.Context, string) (application.Approval, error) { return s.approval, s.err }
func (s *store) Decide(_ context.Context, _ application.Caller, _ string, decide func(application.Approval) (application.Approval, []application.Event, error)) (application.Approval, error) {
	next, _, err := decide(s.approval)
	return next, err
}
func (s *store) Inbox(_ context.Context, q application.InboxQuery) (application.Result, error) {
	s.query = q
	return application.Result{Items: []application.Approval{s.approval}}, nil
}
func (s *store) InsertTx(context.Context, pgx.Tx, application.Caller, application.NewApproval) (application.Approval, error) {
	return application.Approval{}, nil
}

type dir struct{}

func (dir) ActiveUsers(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (dir) ActiveTeams(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (dir) CurrentTeamIDs(context.Context, string) ([]string, error)       { return nil, nil }
func (dir) CurrentMemberIDs(context.Context, string) ([]string, error)     { return nil, nil }

const me = "00000000-0000-7000-8000-0000000000a1"

func serve(t *testing.T, s *store, a authorization.Authenticator, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, application.NewService(s, dir{}, nil), a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", "req-1")
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func mine() *store {
	u := me
	return &store{approval: application.Approval{ID: "a1", SubjectType: "service_request", SubjectLabel: "REQ-1", Status: "pending", ApproverUserID: &u, Version: 1}}
}

func TestEveryRouteNeedsASignedInUserButNoPermission(t *testing.T) {
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/approvals", ""},
		{"GET", "/api/v1/approvals/a1", ""},
		{"POST", "/api/v1/approvals/a1/approve", `{}`},
		{"POST", "/api/v1/approvals/a1/reject", `{"comment":"no"}`},
	} {
		if rec := serve(t, mine(), authorization.DenyAll{}, r.method, r.path, r.body); rec.Code != 401 {
			t.Errorf("%s %s unauthenticated = %d", r.method, r.path, rec.Code)
		}
		if rec := serve(t, mine(), auth{user: me, ok: true}, r.method, r.path, r.body); rec.Code != 200 {
			t.Errorf("%s %s as the approver = %d: %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

func TestStrangersSee404AndExcludedUsersGet403(t *testing.T) {
	s := mine()
	stranger := auth{user: "00000000-0000-7000-8000-0000000000b9", ok: true}
	for _, path := range []string{"/api/v1/approvals/a1"} {
		if rec := serve(t, s, stranger, "GET", path, ""); rec.Code != 404 {
			t.Errorf("stranger GET = %d", rec.Code)
		}
	}
	if rec := serve(t, s, stranger, "POST", "/api/v1/approvals/a1/approve", `{}`); rec.Code != 404 {
		t.Errorf("stranger approve = %d, want 404", rec.Code)
	}
	s.approval.ExcludedUserIDs = []string{me}
	if rec := serve(t, s, auth{user: me, ok: true}, "POST", "/api/v1/approvals/a1/approve", `{}`); rec.Code != 403 || !strings.Contains(rec.Body.String(), "approvals.not_approver") {
		t.Errorf("excluded approver = %d %s", rec.Code, rec.Body)
	}
}

func TestErrorsAndBodies(t *testing.T) {
	a := auth{user: me, ok: true}
	s := mine()
	s.approval.Status = "approved"
	if rec := serve(t, s, a, "POST", "/api/v1/approvals/a1/approve", `{}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "approvals.not_pending") {
		t.Errorf("decided = %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{"not json": `x`, "unknown field": `{"status":"approved"}`, "long comment": `{"comment":"` + strings.Repeat("a", 1001) + `"}`} {
		if rec := serve(t, mine(), a, "POST", "/api/v1/approvals/a1/approve", body); rec.Code != 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	if rec := serve(t, mine(), a, "POST", "/api/v1/approvals/a1/approve", `{"expectedVersion":9}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), "version_conflict") {
		t.Errorf("stale = %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, mine(), a, "GET", "/api/v1/approvals?status=archived", ""); rec.Code != 400 {
		t.Errorf("bad status = %d", rec.Code)
	}
	if rec := serve(t, mine(), a, "GET", "/api/v1/approvals?limit=0", ""); rec.Code != 400 {
		t.Errorf("bad limit = %d", rec.Code)
	}
	rec := serve(t, mine(), a, "GET", "/api/v1/approvals", "")
	if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "excluded") {
		t.Errorf("headers/body: %q %s", rec.Header().Get("Cache-Control"), rec.Body)
	}
}
