package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

func TestSyncRunReadAuthorization(t *testing.T) {
	for _, route := range []string{"/api/v1/directory-sync-runs", "/api/v1/directory-sync-runs/r1"} {
		t.Run(route, func(t *testing.T) {
			if rec, _ := serve(t, &fakeReader{}, fakeAuth{}, "GET", route); rec.Code != 401 {
				t.Errorf("unauthenticated = %d", rec.Code)
			}
			for _, only := range []string{"organization.view", "organization.directory.view", "organization.directory.sync"} {
				if rec, _ := serve(t, &fakeReader{}, with(only), "GET", route); rec.Code != 403 {
					t.Errorf("%s only = %d", only, rec.Code)
				}
			}
			rec, _ := serve(t, &fakeReader{}, with("organization.view", "organization.directory.view"), "GET", route)
			if rec.Code != 200 {
				t.Fatalf("allowed = %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestListSyncRunsParamsAndDTO(t *testing.T) {
	finished := now.Add(time.Minute)
	reason := "abandoned"
	fr := &fakeReader{nextCursor: "next", runs: []application.DirectorySyncRun{{
		ID: "r1", ProviderKey: "ad", Trigger: "scheduled", StartedAt: now, FinishedAt: &finished, Outcome: "failed",
		Counts: nil, Conflicts: nil, ConflictCount: 3, Error: &reason,
	}}}
	both := with("organization.view", "organization.directory.view")
	rec, _ := serve(t, fr, both, "GET", "/api/v1/directory-sync-runs?providerKey=ad&limit=5&cursor=c")
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if fr.runF.ProviderKey != "ad" || fr.runF.Limit != 5 || fr.runF.Cursor != "c" {
		t.Errorf("filter = %+v", fr.runF)
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.NextCursor != "next" || len(body.Items) != 1 {
		t.Fatalf("body = %s", rec.Body)
	}
	it := body.Items[0]
	if it["conflictCount"] != float64(3) || it["error"] != "abandoned" || it["observedAt"] != nil || it["finishedAt"] != "2026-01-02T03:05:05Z" {
		t.Errorf("item = %v", it)
	}
	if c, ok := it["conflicts"].([]any); !ok || len(c) != 0 {
		t.Errorf("conflicts must be an empty array: %v", it["conflicts"])
	}
	if c, ok := it["counts"].(map[string]any); !ok || len(c) != 0 {
		t.Errorf("counts must be an empty object: %v", it["counts"])
	}
	rec, _ = serve(t, &fakeReader{}, both, "GET", "/api/v1/directory-sync-runs?limit=0")
	assertError(t, rec, 400, "organization.invalid_limit")
	rec, _ = serve(t, &fakeReader{}, both, "GET", "/api/v1/directory-sync-runs?providerKey="+strings.Repeat("a", 101))
	assertError(t, rec, 400, "organization.invalid_provider_key")
	rec, _ = serve(t, &fakeReader{err: application.ErrNotFound}, both, "GET", "/api/v1/directory-sync-runs/x")
	assertError(t, rec, 404, "organization.not_found")
	rec, _ = serve(t, &fakeReader{err: application.ErrInvalidCursor}, both, "GET", "/api/v1/directory-sync-runs?cursor=bad")
	assertError(t, rec, 400, "organization.invalid_cursor")
}

func TestUserDetailIncludesExternalIdentitiesButListDoesNot(t *testing.T) {
	seen := now
	user := "ada"
	fr := &fakeReader{identities: []application.ExternalIdentity{{ProviderKey: "ad", Username: &user, Enabled: true, LastSeenAt: &seen}}}
	rec, _ := serve(t, fr, with("organization.view"), "GET", "/api/v1/users/u1")
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	ids, ok := detail["externalIdentities"].([]any)
	if !ok || len(ids) != 1 {
		t.Fatalf("externalIdentities = %v", detail["externalIdentities"])
	}
	got := ids[0].(map[string]any)
	if got["providerKey"] != "ad" || got["username"] != "ada" || got["enabled"] != true || got["lastSeenAt"] != "2026-01-02T03:04:05Z" || got["deletedObservedAt"] != nil {
		t.Errorf("identity = %v", got)
	}
	for _, forbidden := range []string{"externalSubject", "distinguishedName", "subject"} {
		if _, present := got[forbidden]; present {
			t.Errorf("identity exposes %s", forbidden)
		}
	}
	rec, _ = serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users/u1")
	if !strings.Contains(rec.Body.String(), `"externalIdentities":[]`) {
		t.Errorf("no identities must render []: %s", rec.Body)
	}
	rec, _ = serve(t, &fakeReader{}, with("organization.view"), "GET", "/api/v1/users")
	if strings.Contains(rec.Body.String(), "externalIdentities") {
		t.Errorf("list must not carry externalIdentities: %s", rec.Body)
	}
}

func TestRequestDirectorySync(t *testing.T) {
	const route = "/api/v1/directory-sync-runs"
	t.Run("unauthenticated", func(t *testing.T) {
		s := &fakeSyncer{}
		rec, _ := serveSync(t, &fakeReader{}, s, "ad", fakeAuth{}, "POST", route)
		assertStatus(t, rec, 401)
		if s.calls != 0 {
			t.Error("enqueued without authentication")
		}
	})
	t.Run("forbidden without sync permission", func(t *testing.T) {
		for _, perms := range [][]string{nil, {"organization.view", "organization.directory.view"}} {
			s := &fakeSyncer{}
			rec, _ := serveSync(t, &fakeReader{}, s, "ad", with(perms...), "POST", route)
			assertStatus(t, rec, 403)
			if s.calls != 0 {
				t.Error("enqueued without permission")
			}
		}
	})
	t.Run("not configured", func(t *testing.T) {
		s := &fakeSyncer{}
		rec, _ := serveSync(t, &fakeReader{}, s, "", with("organization.directory.sync"), "POST", route)
		assertError(t, rec, 409, "organization.directory_sync_not_configured")
		if s.calls != 0 {
			t.Error("enqueued although not configured")
		}
	})
	t.Run("accepted", func(t *testing.T) {
		s := &fakeSyncer{jobID: "job-1", created: true}
		rec, _ := serveSync(t, &fakeReader{}, s, "ad", with("organization.directory.sync"), "POST", route)
		assertStatus(t, rec, http.StatusAccepted)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["jobId"] != "job-1" || body["created"] != true {
			t.Errorf("body = %s (%v)", rec.Body, err)
		}
		if s.actor != "u" || s.key != "ad" || s.calls != 1 {
			t.Errorf("syncer = %+v", s)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
		}
	})
	t.Run("deduplicated", func(t *testing.T) {
		s := &fakeSyncer{jobID: "job-1", created: false}
		rec, _ := serveSync(t, &fakeReader{}, s, "ad", with("organization.directory.sync"), "POST", route)
		assertStatus(t, rec, http.StatusAccepted)
		if !strings.Contains(rec.Body.String(), `"created":false`) {
			t.Errorf("body = %s", rec.Body)
		}
	})
	t.Run("internal error does not leak", func(t *testing.T) {
		s := &fakeSyncer{err: errors.New("pq: secret detail")}
		rec, _ := serveSync(t, &fakeReader{}, s, "ad", with("organization.directory.sync"), "POST", route)
		assertError(t, rec, 500, "platform.internal_error")
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("leaked: %s", rec.Body)
		}
	})
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status = %d, want %d (%s)", rec.Code, want, rec.Body)
	}
}
