package config

import (
	"strings"
	"testing"
)

const guidA, guidB = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

func TestLoadGraphReadAndWrite(t *testing.T) {
	t.Setenv("MICROSOFT_HTTP_PROXY", "")
	g, err := LoadGraphRead()
	if err != nil || g.Configured {
		t.Fatalf("empty: %+v %v", g, err)
	}
	t.Setenv("MICROSOFT_GRAPH_TENANT_ID", guidA)
	if _, err := LoadGraphRead(); err == nil || !strings.Contains(err.Error(), "CLIENT_ID") {
		t.Fatalf("partial: %v", err)
	}
	t.Setenv("MICROSOFT_GRAPH_CLIENT_ID", guidB)
	if _, err := LoadGraphRead(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("no credential: %v", err)
	}
	t.Setenv("MICROSOFT_GRAPH_CLIENT_SECRET_FILE", "/run/secrets/graph")
	t.Setenv("MICROSOFT_GRAPH_CLIENT_SECRET_EXPIRES_AT", "2027-01-01T00:00:00Z")
	g, err = LoadGraphRead()
	if err != nil || !g.Configured || g.SecretFile != "/run/secrets/graph" || g.SecretExpiresAt == nil {
		t.Fatalf("%+v %v", g, err)
	}
	t.Setenv("MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE", "/c.pem")
	if _, err := LoadGraphRead(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both: %v", err)
	}
	// The write registration is independent of the read registration.
	if w, err := LoadGraphWrite(); err != nil || w.Configured {
		t.Fatalf("write: %+v %v", w, err)
	}
}

func TestLoadAutotask(t *testing.T) {
	if a, err := LoadAutotask(); err != nil || a.Configured {
		t.Fatalf("empty: %+v %v", a, err)
	}
	t.Setenv("AUTOTASK_API_USERNAME", "api@example.com")
	if _, err := LoadAutotask(); err == nil {
		t.Fatal("partial config accepted")
	}
	t.Setenv("AUTOTASK_API_SECRET_FILE", "/run/secrets/at")
	t.Setenv("AUTOTASK_INTEGRATION_CODE", "ABC123")
	t.Setenv("AUTOTASK_COMPANY_ID", "42")
	t.Setenv("AUTOTASK_STATUS_MAP", "new=1,open=1,in_progress=8,waiting=7,resolved=5,closed=5,cancelled=5")
	if _, err := LoadAutotask(); err == nil || !strings.Contains(err.Error(), "AUTOTASK_PRIORITY_MAP") {
		t.Fatalf("missing priority map: %v", err)
	}
	t.Setenv("AUTOTASK_PRIORITY_MAP", "low=3,normal=2,high=1,urgent=4")
	a, err := LoadAutotask()
	if err != nil || !a.Configured || a.CompanyID != 42 || a.StatusMap["in_progress"] != 8 || a.PriorityMap["urgent"] != 4 || a.RateLimitPerHour != 3000 {
		t.Fatalf("%+v %v", a, err)
	}
	t.Setenv("AUTOTASK_PRIORITY_MAP", "low=3,normal=2,high=1,urgent=4,extra=9")
	if _, err := LoadAutotask(); err == nil {
		t.Fatal("unknown key accepted")
	}
	t.Setenv("AUTOTASK_PRIORITY_MAP", "low=3,normal=2,high=1,urgent=4")
	t.Setenv("AUTOTASK_API_USERNAME", "not a login")
	if _, err := LoadAutotask(); err == nil {
		t.Fatal("bad username accepted")
	}
}
