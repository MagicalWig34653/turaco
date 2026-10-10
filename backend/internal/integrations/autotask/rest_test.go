package autotask

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// atServer reproduces the payload shapes of the Autotask REST documentation (read 2026-10-10, see rest.go):
// zone information, query answers with items and pageDetails, POST answering {"itemId": n}, PATCH answering 200.
type atServer struct {
	*httptest.Server
	mu        sync.Mutex
	tickets   map[int64]map[string]any
	nextID    int64
	requests  []string
	authSeen  http.Header
	failNext  int // status to answer once before behaving
	zoneCalls int
	unauth    bool
}

func newATServer(t *testing.T) *atServer {
	t.Helper()
	s := &atServer{tickets: map[int64]map[string]any{}, nextID: 100}
	mux := http.NewServeMux()
	mux.HandleFunc("/atservicesrest/v1.0/zoneInformation", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.zoneCalls++
		s.mu.Unlock()
		if r.Header.Get("Secret") != "" || r.Header.Get("UserName") != "" {
			t.Errorf("zone discovery must not carry credentials")
		}
		if r.URL.Query().Get("user") != "api@example.com" {
			t.Errorf("user %q", r.URL.Query().Get("user"))
		}
		// Documented answer shape: zoneName, url, webUrl, ci.
		_, _ = w.Write([]byte(`{"zoneName":"America East","url":"` + s.URL + `/atservicesrest/","webUrl":"https://ww3.autotask.net/","ci":20264}`))
	})
	mux.HandleFunc("/atservicesrest/v1.0/Tickets/query", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Filter []struct{ Op, Field, Value string } `json:"filter"`
		}
		_ = json.Unmarshal([]byte(r.URL.Query().Get("search")), &q)
		var items []map[string]any
		s.mu.Lock()
		for id, tk := range s.tickets {
			if len(q.Filter) == 1 && q.Filter[0].Op == "eq" && q.Filter[0].Field == "externalID" && tk["externalID"] == q.Filter[0].Value {
				items = append(items, map[string]any{"id": id, "externalID": tk["externalID"], "title": "must not matter"})
			}
		}
		s.mu.Unlock()
		out, _ := json.Marshal(map[string]any{"items": items, "pageDetails": map[string]any{"count": len(items), "requestCount": 500, "prevPageUrl": nil, "nextPageUrl": nil}})
		_, _ = w.Write(out)
	})
	mux.HandleFunc("/atservicesrest/v1.0/Tickets", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			if _, has := b["id"]; has {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errors":["id must not be set"]}`))
				return
			}
			s.nextID++
			s.tickets[s.nextID] = b
			_, _ = w.Write([]byte(`{"itemId": ` + jsonInt(s.nextID) + `}`))
		case http.MethodPatch:
			id := int64(b["id"].(float64))
			tk, ok := s.tickets[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["ticket 4711 of Contoso not found"]}`))
				return
			}
			for k, v := range b {
				tk[k] = v
			}
			_, _ = w.Write([]byte(`{"itemId": ` + jsonInt(id) + `}`))
		}
	})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.authSeen = r.Header.Clone()
		fail, unauth := s.failNext, s.unauth
		if fail != 0 {
			s.failNext = 0
		}
		s.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "zoneInformation") {
			mux.ServeHTTP(w, r)
			return
		}
		if fail != 0 {
			w.WriteHeader(fail)
			_, _ = w.Write([]byte(`{"errors":["secret detail about Contoso"]}`))
			return
		}
		if unauth || r.Header.Get("Secret") != "s3cr3t" || r.Header.Get("UserName") != "api@example.com" || r.Header.Get("ApiIntegrationCode") != "CODE123" ||
			r.Header.Get("Content-Type") != "" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

var (
	statusMap   = map[string]int{"new": 1, "open": 2, "in_progress": 8, "waiting": 7, "resolved": 5, "closed": 5, "cancelled": 5}
	priorityMap = map[string]int{"low": 4, "normal": 2, "high": 1, "urgent": 5}
)

func (s *atServer) client(t *testing.T, mod func(*RESTConfig)) *REST {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := RESTConfig{Username: "api@example.com", SecretFile: secret, IntegrationCode: "CODE123", CompanyID: 7, QueueID: 3,
		StatusMap: statusMap, PriorityMap: priorityMap, ZoneLookupBase: s.URL, Transport: s.Client().Transport, AllowInsecureHTTP: true}
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewREST(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ticket(ref string) Ticket {
	return Ticket{Reference: ref, Title: "Printer broken", Description: "It smokes", Status: "new", Priority: "high"}
}

func TestRESTCreateSendsDocumentedHeadersAndFieldsAndMapsPicklists(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	if st := c.Status(); st.State != providerstatus.Unverified {
		t.Fatalf("initial status %+v", st)
	}
	id, err := c.Upsert(context.Background(), ticket("TUR-000001"), "")
	if err != nil || id != "101" {
		t.Fatalf("id %q err %v", id, err)
	}
	tk := s.tickets[101]
	if tk["companyID"] != float64(7) || tk["queueID"] != float64(3) || tk["status"] != float64(1) || tk["priority"] != float64(1) ||
		tk["title"] != "Printer broken" || tk["externalID"] != "TUR-000001" || tk["description"] != "It smokes" {
		t.Fatalf("ticket %v", tk)
	}
	if _, has := tk["resolution"]; has {
		t.Fatal("empty resolution must not be sent")
	}
	if st := c.Status(); st.State != providerstatus.Verified {
		t.Fatalf("status %+v", st)
	}
	if s.authSeen.Get("UserName") != "api@example.com" || s.authSeen.Get("ApiIntegrationCode") != "CODE123" || s.authSeen.Get("Secret") != "s3cr3t" {
		t.Errorf("headers %v", s.authSeen)
	}
}

func TestRESTCreateIsIdempotentThroughTheReferenceLookup(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	first, err := c.Upsert(context.Background(), ticket("TUR-000002"), "")
	if err != nil {
		t.Fatal(err)
	}
	// The mapping was never stored (crash): the retry passes an empty external id again.
	second, err := c.Upsert(context.Background(), ticket("TUR-000002"), "")
	if err != nil || second != first || len(s.tickets) != 1 {
		t.Fatalf("second %q err %v tickets %d", second, err, len(s.tickets))
	}
}

func TestRESTUpdatePatchesWithTheRecordIDAndNeverSendsAutotaskOwnedFields(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	id, _ := c.Upsert(context.Background(), ticket("TUR-000003"), "")
	s.tickets[101]["assignedResourceID"] = float64(55) // set in Autotask
	t3 := ticket("TUR-000003")
	t3.Status, t3.Resolution = "resolved", "Replaced the toner"
	got, err := c.Upsert(context.Background(), t3, id)
	if err != nil || got != id {
		t.Fatalf("got %q err %v", got, err)
	}
	tk := s.tickets[101]
	if tk["status"] != float64(5) || tk["resolution"] != "Replaced the toner" || tk["assignedResourceID"] != float64(55) {
		t.Fatalf("ticket %v", tk)
	}
	if tk["companyID"] != float64(7) {
		t.Fatalf("company changed %v", tk)
	}
	if last := s.requests[len(s.requests)-1]; last != "PATCH /atservicesrest/v1.0/Tickets" {
		t.Fatalf("last request %s", last)
	}
}

func TestRESTCachesTheZone(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := c.Upsert(context.Background(), ticket("TUR-00000"+string(rune('4'+i))), ""); err != nil {
			t.Fatal(err)
		}
	}
	if s.zoneCalls != 1 {
		t.Fatalf("zone discovery called %d times", s.zoneCalls)
	}
}

func TestRESTRefreshesTheZoneOnceOn401ThenFailsPermanently(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	s.unauth = true
	_, err := c.Upsert(context.Background(), ticket("TUR-000010"), "")
	var ae *Error
	if !errors.As(err, &ae) || !ae.Permanent || !strings.Contains(ae.Message, "refused") {
		t.Fatalf("err %v", err)
	}
	if s.zoneCalls != 2 {
		t.Fatalf("zone discovery %d times, want 2 (initial + one refresh)", s.zoneCalls)
	}
	if st := c.Status(); st.State != providerstatus.Failing || st.LastErrorCode != "http_401" {
		t.Fatalf("status %+v", st)
	}
}

func TestRESTErrorClassificationAndNoLeakedBodies(t *testing.T) {
	for _, tc := range []struct {
		status    int
		permanent bool
	}{{429, false}, {503, false}, {500, false}, {400, true}, {404, true}, {403, true}} {
		s := newATServer(t)
		c := s.client(t, nil)
		if _, err := c.Upsert(context.Background(), ticket("TUR-000020"), "999"); err != nil { // warm the zone
			_ = err
		}
		s.mu.Lock()
		s.failNext = tc.status
		s.mu.Unlock()
		_, err := c.Upsert(context.Background(), ticket("TUR-000020"), "999")
		var ae *Error
		if !errors.As(err, &ae) || ae.Permanent != tc.permanent {
			t.Errorf("status %d: %v", tc.status, err)
			continue
		}
		if strings.Contains(err.Error(), "Contoso") || strings.Contains(err.Error(), "secret detail") || strings.Contains(err.Error(), s.URL) || strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("status %d leaks: %v", tc.status, err)
		}
	}
}

func TestRESTNetworkErrorIsTransientAndDoesNotLeakTheURL(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	s.Close()
	_, err := c.Upsert(context.Background(), ticket("TUR-000030"), "")
	var ae *Error
	if !errors.As(err, &ae) || ae.Permanent || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err %v", err)
	}
}

func TestRESTRejectsAmbiguousReferenceAndInvalidInput(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	s.tickets[1] = map[string]any{"externalID": "TUR-000040"}
	s.tickets[2] = map[string]any{"externalID": "TUR-000040"}
	if _, err := c.Upsert(context.Background(), ticket("TUR-000040"), ""); err == nil || !strings.Contains(err.Error(), "several tickets") {
		t.Fatalf("ambiguous: %v", err)
	}
	for name, tk := range map[string]Ticket{
		"bad reference": {Reference: "TUR 1", Title: "x", Status: "new", Priority: "low"},
		"bad status":    {Reference: "TUR-1", Title: "x", Status: "weird", Priority: "low"},
		"bad priority":  {Reference: "TUR-1", Title: "x", Status: "new", Priority: "weird"},
		"no title":      {Reference: "TUR-1", Status: "new", Priority: "low"},
	} {
		var ae *Error
		if _, err := c.Upsert(context.Background(), tk, ""); !errors.As(err, &ae) || !ae.Permanent {
			t.Errorf("%s: %v", name, err)
		}
	}
	var ae *Error
	if _, err := c.Upsert(context.Background(), ticket("TUR-000041"), "../x"); !errors.As(err, &ae) || !ae.Permanent {
		t.Errorf("hostile external id: %v", err)
	}
}

func TestRESTClipsFieldsToDocumentedLengths(t *testing.T) {
	s := newATServer(t)
	c := s.client(t, nil)
	tk := ticket("TUR-000050")
	tk.Title, tk.Description = strings.Repeat("ä", 400), strings.Repeat("d", 9000)
	if _, err := c.Upsert(context.Background(), tk, ""); err != nil {
		t.Fatal(err)
	}
	got := s.tickets[101]
	if n := len([]rune(got["title"].(string))); n != 255 {
		t.Errorf("title %d", n)
	}
	if n := len(got["description"].(string)); n != 8000 {
		t.Errorf("description %d", n)
	}
}

func TestRESTLocalRateCeiling(t *testing.T) {
	s := newATServer(t)
	now := time.Now()
	c := s.client(t, func(cfg *RESTConfig) { cfg.RatePerHour = 4; cfg.Now = func() time.Time { return now } })
	// Each create is zone (once) + lookup + create = 3 requests; the second attempt hits the ceiling.
	if _, err := c.Upsert(context.Background(), ticket("TUR-000060"), ""); err != nil {
		t.Fatal(err)
	}
	var ae *Error
	if _, err := c.Upsert(context.Background(), ticket("TUR-000061"), ""); !errors.As(err, &ae) || ae.Permanent || !strings.Contains(ae.Message, "ceiling") {
		t.Fatalf("err %v", err)
	}
	now = now.Add(61 * time.Minute)
	if _, err := c.Upsert(context.Background(), ticket("TUR-000061"), ""); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestZoneURLAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://webservices3.autotask.net/atservicesrest/": true,
		"https://webservices.autotask.net/atservicesrest/":  true,
		"https://webservices3.autotask.net:8443/x":          false,
		"http://webservices3.autotask.net/x":                false,
		"https://evil.example/atservicesrest/":              false,
		"https://webservices3.autotask.net.evil.example/x":  false,
		"https://autotask.net/x":                            false,
		"https://webservices3-evil.autotask.net/x":          false,
	} {
		u, _ := url.Parse(raw)
		if got := zoneURLAllowed(u); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}

func TestNewRESTValidation(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("x"), 0o600)
	base := RESTConfig{Username: "a@b.c", SecretFile: secret, IntegrationCode: "C", CompanyID: 1, StatusMap: statusMap, PriorityMap: priorityMap}
	if _, err := NewREST(base); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*RESTConfig){
		"no company":      func(c *RESTConfig) { c.CompanyID = 0 },
		"missing status":  func(c *RESTConfig) { c.StatusMap = map[string]int{"new": 1} },
		"missing prio":    func(c *RESTConfig) { c.PriorityMap = map[string]int{} },
		"no secret":       func(c *RESTConfig) { c.SecretFile = filepath.Join(filepath.Dir(secret), "missing") },
		"no integr. code": func(c *RESTConfig) { c.IntegrationCode = "" },
	} {
		cfg := base
		mod(&cfg)
		if _, err := NewREST(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
