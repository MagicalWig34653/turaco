package transport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type userAuth struct{ userID string }

func (a userAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: a.userID, Permissions: map[string]struct{}{"organization.directory.sync": {}}}, true, nil
}

// End to end against PostgreSQL: POST enqueues one job in the same transaction
// as the audit event, a second POST is deduplicated.
func TestRequestDirectorySyncEndToEnd(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var suffix, actor string
	if err := pool.QueryRow(ctx, `SELECT substr(md5(random()::text), 1, 10)`).Scan(&suffix); err != nil {
		t.Fatal(err)
	}
	provider := "zt-" + suffix
	if err := pool.QueryRow(ctx, `INSERT INTO organization.users(display_name) VALUES ($1) RETURNING id::text`, "Requester "+suffix).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE dedupe_key = $1`, public.DirectorySyncDedupeKey(provider))
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'directory_provider' AND target_id = $1`, provider)
		_, _ = pool.Exec(ctx, `DELETE FROM organization.users WHERE id = $1`, actor)
	})

	repo := repository.New(pool)
	mux := http.NewServeMux()
	Register(mux, repo, repo, provider, userAuth{userID: actor}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	post := func() (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/directory-sync-runs", nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, first := post()
	if code != 202 || first["created"] != true || first["jobId"] == "" {
		t.Fatalf("first = %d %v", code, first)
	}
	code, second := post()
	if code != 202 || second["created"] != false || second["jobId"] != first["jobId"] {
		t.Fatalf("second = %d %v", code, second)
	}
	var jobs, audits, byActor int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.jobs WHERE dedupe_key = $1 AND status = 'pending'`, public.DirectorySyncDedupeKey(provider)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE actor_id::text = $2) FROM platform.audit_events
		WHERE action = 'organization.directory_sync.requested' AND target_id = $1`, provider, actor).Scan(&audits, &byActor); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || audits != 2 || byActor != 2 {
		t.Errorf("jobs=%d audits=%d byActor=%d", jobs, audits, byActor)
	}
}
