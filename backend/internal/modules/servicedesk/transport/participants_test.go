package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const vendor = "00000000-0000-7000-8000-0000000000d4"

type vendorDirectory struct{}

func (vendorDirectory) ActiveExternals(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = id == vendor
	}
	return out, nil
}

func (vendorDirectory) SearchExternals(context.Context, string, int) ([]application.ExternalAccount, error) {
	return []application.ExternalAccount{{UserID: vendor, DisplayName: "Vera Vendor", PartyName: "Acme"}}, nil
}

type externalAuth struct{ fakeAuth }

func (e externalAuth) Authenticate(r *http.Request) (authorization.Principal, bool, error) {
	p, ok, err := e.fakeAuth.Authenticate(r)
	p.External = true
	return p, ok, err
}

func serveShared(t *testing.T, a authorization.Authenticator) http.Handler {
	t.Helper()
	pool := dbtest.Pool(t)
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := application.NewService(repository.New(pool), dir{}, noDevice{}).WithExternalAccounts(repository.New(pool), vendorDirectory{})
	transport.Register(mux, svc, a, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{alice, bob, agent})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE actor_id = ANY($1::uuid[])`, []string{alice, bob, agent, vendor})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE actor_id = ANY($1::uuid[])`, []string{alice, bob, agent, vendor})
	})
	return httpx.Middleware(logger, mux)
}

func TestTicketParticipantsOverHTTP(t *testing.T) {
	employee := serveShared(t, as(alice))
	rec := do(employee, "POST", "/api/v1/tickets", `{"title":"Switch down","description":"Floor 2"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var tk struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	share := `{"userId":"` + vendor + `","level":"comment"}`

	if rec := do(employee, "POST", "/api/v1/tickets/"+tk.ID+"/participants", share); rec.Code != http.StatusForbidden {
		t.Errorf("requester shares = %d %s", rec.Code, rec.Body)
	}
	if rec := do(serveShared(t, fakeAuth{}), "POST", "/api/v1/tickets/"+tk.ID+"/participants", share); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous shares = %d", rec.Code)
	}
	staff := serveShared(t, as(agent, "tickets.manage", "tickets.view"))
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/participants", `{"userId":"`+alice+`","level":"read"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tickets.participant_invalid") {
		t.Errorf("share with an employee = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/participants", `{"userId":"`+vendor+`","level":"root"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown level = %d", rec.Code)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/participants", `{"userId":"`+vendor+`","level":"read","expiresAt":"tomorrow"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad expiry = %d", rec.Code)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/participants", share); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"level":"comment"`) {
		t.Fatalf("share = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+tk.ID+"/comments", `{"body":"internal: vendor contract expires","internal":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("internal note = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "GET", "/api/v1/tickets/"+tk.ID+"/participants", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), vendor) {
		t.Errorf("list participants = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "GET", "/api/v1/tickets/"+tk.ID+"/participant-candidates?q=vera", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Acme") {
		t.Errorf("candidates = %d %s", rec.Code, rec.Body)
	}

	// The external account: sees the Ticket without internal notes, comments publicly, reaches nothing else.
	ext := serveShared(t, externalAuth{as(vendor, "tasks.work")})
	rec = do(ext, "GET", "/api/v1/tickets/"+tk.ID, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "vendor contract") || strings.Contains(rec.Body.String(), `"queueId"`) {
		t.Errorf("participant read = %d %s", rec.Code, rec.Body)
	}
	if rec := do(ext, "POST", "/api/v1/tickets/"+tk.ID+"/comments", `{"body":"switch replaced"}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"internal":false`) {
		t.Errorf("participant comment = %d %s", rec.Code, rec.Body)
	}
	if rec := do(ext, "POST", "/api/v1/tickets/"+tk.ID+"/comments", `{"body":"note","internal":true}`); rec.Code != http.StatusForbidden {
		t.Errorf("participant internal note = %d", rec.Code)
	}
	for _, p := range []struct{ method, path, body string }{
		{"GET", "/api/v1/tickets/" + tk.ID + "/participants", ""},
		{"POST", "/api/v1/tickets/" + tk.ID + "/participants", share},
		{"GET", "/api/v1/tickets/" + tk.ID + "/participant-candidates?q=vera", ""},
		{"DELETE", "/api/v1/tickets/" + tk.ID + "/participants/" + vendor, ""},
		{"POST", "/api/v1/tickets", `{"title":"mine"}`},
	} {
		if rec := do(ext, p.method, p.path, p.body); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Errorf("external %s %s = %d, want 403 or 404", p.method, p.path, rec.Code)
		}
	}

	// Removing: staff only, once.
	if rec := do(employee, "DELETE", "/api/v1/tickets/"+tk.ID+"/participants/"+vendor, ""); rec.Code != http.StatusForbidden {
		t.Errorf("requester removes = %d", rec.Code)
	}
	if rec := do(staff, "DELETE", "/api/v1/tickets/"+tk.ID+"/participants/"+vendor, ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove = %d %s", rec.Code, rec.Body)
	}
	if rec := do(staff, "DELETE", "/api/v1/tickets/"+tk.ID+"/participants/"+vendor, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove twice = %d", rec.Code)
	}
	if rec := do(ext, "GET", "/api/v1/tickets/"+tk.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("participant read after removal = %d", rec.Code)
	}
}
