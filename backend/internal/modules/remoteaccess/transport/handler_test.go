package transport_test

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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
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

type devices struct {
	m map[string]application.DeviceInfo
}

func (d devices) Device(_ context.Context, id string) (application.DeviceInfo, bool, error) {
	x, ok := d.m[id]
	return x, ok, nil
}

type tickets struct {
	m map[string]application.TicketInfo
}

func (t tickets) Ticket(_ context.Context, id string) (application.TicketInfo, bool, error) {
	x, ok := t.m[id]
	return x, ok, nil
}

type holders struct{ m map[string]string }

func (h holders) UserHolders(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if u, ok := h.m[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

type noApprovals struct{}

func (noApprovals) RequestInTx(context.Context, pgx.Tx, audit.Actor, string, string, string, application.Approver, []string) (string, error) {
	return newID(), nil
}
func (noApprovals) CancelBySubjectInTx(context.Context, pgx.Tx, audit.Actor, string, string) error {
	return nil
}
func (noApprovals) ForSubject(context.Context, string) ([]application.ApprovalInfo, error) {
	return nil, nil
}

type approvers struct{}

func (approvers) Permissions(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{"tickets.manage": {}}, nil
}
func (approvers) TeamMemberIDs(context.Context, string) ([]string, error) { return nil, nil }
func (approvers) TeamIDsOfUser(context.Context, string) ([]string, error) { return nil, nil }

type setup struct {
	svc            *application.Service
	device, ticket string
	peer           string
	admin          string
}

func newSetup(t *testing.T) *setup {
	t.Helper()
	pool := dbtest.Pool(t)
	device, ticket, holder, asset := newID(), newID(), newID(), newID()
	checkin := time.Now().Add(-time.Hour)
	fake := remoteaccess.NewFake("rustdesk")
	svc := application.NewService(repository.New(pool),
		devices{map[string]application.DeviceInfo{device: {ID: device, AssetID: &asset, Ownership: "corporate", ObservedAt: time.Now(), LastCheckinAt: &checkin}}},
		tickets{map[string]application.TicketInfo{ticket: {ID: ticket, Open: true, AffectedUserID: holder, ReporterUserID: newID()}}},
		holders{map[string]string{asset: holder}}, activeDir{}, noApprovals{}, approvers{}, remoteaccess.NewRegistryOf(fake))
	s := &setup{svc: svc, device: device, ticket: ticket, admin: newID()}
	s.peer = "5" + strings.Repeat("1", 8)[:8]
	return s
}

func (s *setup) handler(auth fakeAuth) http.Handler {
	mux := http.NewServeMux()
	transport.Register(mux, s.svc, auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httpx.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)
}

func call(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func code(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	c, _ := out["code"].(string)
	return c
}

func TestHTTPFlowNoStoreAndStatusCodes(t *testing.T) {
	s := newSetup(t)
	tech := newID()
	techAuth := as(tech, application.PermStart, application.PermView)
	admin := as(s.admin, application.PermAdmin, application.PermView)
	// Mapping: administrators only; technicians get 403.
	peer := "7" + strings.Repeat("2", 8)
	peer = peer[:9]
	mapBody := map[string]any{"deviceId": s.device, "provider": "rustdesk", "peerId": peer, "reason": "initial_mapping"}
	if rec, _ := call(t, s.handler(techAuth), "PUT", "/api/v1/remote-access/peer-mappings", mapBody); rec.Code != http.StatusForbidden {
		t.Fatalf("technician mapping: %d", rec.Code)
	}
	rec, out := call(t, s.handler(admin), "PUT", "/api/v1/remote-access/peer-mappings", mapBody)
	if rec.Code != http.StatusOK && code(out) != "remoteaccess.peer_taken" {
		t.Fatalf("map: %d %v", rec.Code, out)
	}
	if rec.Code == http.StatusConflict {
		// The peer exists from an earlier run; use another one.
		mapBody["peerId"] = "8" + strings.Repeat("3", 8)
		if rec, out = call(t, s.handler(admin), "PUT", "/api/v1/remote-access/peer-mappings", mapBody); rec.Code != http.StatusOK {
			t.Fatalf("map retry: %d %v", rec.Code, out)
		}
	}
	// Capabilities: no-store, masked peer id.
	rec, out = call(t, s.handler(techAuth), "GET", "/api/v1/remote-access/capabilities?deviceId="+s.device, nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") || strings.Contains(rec.Body.String(), mapBody["peerId"].(string)) {
		t.Fatalf("capabilities: %d %s %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	// Request needs start_attended (route-level 403) and valid input (400).
	viewOnly := as(newID(), application.PermView)
	if rec, _ = call(t, s.handler(viewOnly), "POST", "/api/v1/remote-access/sessions", map[string]any{}); rec.Code != http.StatusForbidden {
		t.Fatalf("view only: %d", rec.Code)
	}
	if rec, _ = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions", map[string]any{"deviceId": "nope"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad input: %d", rec.Code)
	}
	if rec, _ = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions", map[string]any{"deviceId": s.device, "ticketId": s.ticket, "provider": "anydesk"}); rec.Code != http.StatusConflict {
		t.Fatalf("provider disabled: %d", rec.Code)
	}
	rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions", map[string]any{"deviceId": s.device, "ticketId": s.ticket, "provider": "rustdesk"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %v", rec.Code, out)
	}
	id, version := out["id"].(string), out["version"]
	if out["status"] != "authorized" || out["consent"] != "unknown" || out["observedConnectedAt"] != nil {
		t.Fatalf("session: %v", out)
	}
	// A second request for the device is refused with the stable code.
	if rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions", map[string]any{"deviceId": s.device, "ticketId": s.ticket, "provider": "rustdesk"}); rec.Code != http.StatusConflict || code(out) != "remoteaccess.session_open" {
		t.Fatalf("second request: %d %v", rec.Code, out)
	}
	// Launch requires expectedVersion.
	if rec, _ = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions/"+id+"/launch", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("launch without version: %d", rec.Code)
	}
	rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions/"+id+"/launch", map[string]any{"expectedVersion": version})
	if rec.Code != 200 || out["token"] == nil || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("launch: %d %v", rec.Code, out)
	}
	token := out["token"].(string)
	if strings.Contains(rec.Body.String(), "fake://") {
		t.Fatal("the launch response must not carry the link")
	}
	// Exchange: another user 404, the owner 200 once with the link in the body only, then 409.
	other := as(newID(), application.PermStart)
	if rec, _ = call(t, s.handler(other), "POST", "/api/v1/remote-access/launch-handles/exchange", map[string]any{"token": token}); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong user: %d", rec.Code)
	}
	rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/launch-handles/exchange", map[string]any{"token": token})
	if rec.Code != 200 || !strings.HasPrefix(out["launchUri"].(string), "fake://rustdesk/") || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") ||
		rec.Header().Get("Location") != "" {
		t.Fatalf("exchange: %d %v %v", rec.Code, out, rec.Header())
	}
	if rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/launch-handles/exchange", map[string]any{"token": token}); rec.Code != http.StatusConflict || code(out) != "remoteaccess.handle_used" {
		t.Fatalf("second exchange: %d %v", rec.Code, out)
	}
	// The record never contains the link.
	rec, out = call(t, s.handler(techAuth), "GET", "/api/v1/remote-access/sessions/"+id, nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "fake://") || len(out["transitions"].([]any)) != 3 {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ = call(t, s.handler(other), "GET", "/api/v1/remote-access/sessions/"+id, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("stranger detail: %d", rec.Code)
	}
	rec, out = call(t, s.handler(techAuth), "GET", "/api/v1/remote-access/sessions?status=launched", nil)
	if rec.Code != 200 || len(out["items"].([]any)) < 1 {
		t.Fatalf("list: %d %v", rec.Code, out)
	}
	// Consent then close.
	cur := out["items"].([]any)[0].(map[string]any)
	rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions/"+id+"/consent", map[string]any{"expectedVersion": cur["version"], "decision": "granted"})
	if rec.Code != 200 || out["consent"] != "granted" {
		t.Fatalf("consent: %d %v", rec.Code, out)
	}
	rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions/"+id+"/close", map[string]any{"expectedVersion": out["version"], "reason": "completed"})
	if rec.Code != 200 || out["status"] != "closed" {
		t.Fatalf("close: %d %v", rec.Code, out)
	}
	if rec, out = call(t, s.handler(techAuth), "POST", "/api/v1/remote-access/sessions/"+id+"/close", map[string]any{"expectedVersion": 1, "reason": "completed"}); rec.Code != http.StatusConflict {
		t.Fatalf("stale close: %d %v", rec.Code, out)
	}
	// Unmap by query, administrators only.
	q := "/api/v1/remote-access/peer-mappings?deviceId=" + s.device + "&provider=rustdesk&reason=wrong_device"
	if rec, _ = call(t, s.handler(techAuth), "DELETE", q, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("technician unmap: %d", rec.Code)
	}
	if rec, _ = call(t, s.handler(admin), "DELETE", q, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unmap: %d", rec.Code)
	}
	// Anonymous callers are rejected everywhere.
	if rec, _ = call(t, s.handler(as("")), "GET", "/api/v1/remote-access/sessions", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

type activeDir struct{}

func (activeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func TestObservationSummaryAndTicketUnavailableOverHTTP(t *testing.T) {
	s := newSetup(t)
	// The summary needs remote_access.view_sessions.
	if rec, _ := call(t, s.handler(as(newID(), application.PermStart, application.PermView)), "GET", "/api/v1/remote-access/observations/summary", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("summary without view_sessions: %d", rec.Code)
	}
	rec, out := call(t, s.handler(as(newID(), application.PermViewSessions)), "GET", "/api/v1/remote-access/observations/summary", nil)
	if _, ok := out["unattributedRecords"].(float64); rec.Code != http.StatusOK || !ok || out["byReason"] == nil || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("summary: %d %v", rec.Code, out)
	}
	// An unknown Ticket answers with the single ticket_unavailable code.
	peer := fmt.Sprintf("%09d", 100000000+time.Now().UnixNano()%900000000)
	if rec, out = call(t, s.handler(as(s.admin, application.PermAdmin)), "PUT", "/api/v1/remote-access/peer-mappings",
		map[string]any{"deviceId": s.device, "provider": "rustdesk", "peerId": peer, "reason": "initial_mapping"}); rec.Code != http.StatusOK {
		t.Fatalf("map: %d %v", rec.Code, out)
	}
	tech := as(newID(), application.PermStart, application.PermView)
	rec, out = call(t, s.handler(tech), "POST", "/api/v1/remote-access/sessions", map[string]any{"deviceId": s.device, "ticketId": newID(), "provider": "rustdesk"})
	if rec.Code != http.StatusConflict || code(out) != "remoteaccess.ticket_unavailable" {
		t.Fatalf("unknown ticket: %d %v", rec.Code, out)
	}
}
