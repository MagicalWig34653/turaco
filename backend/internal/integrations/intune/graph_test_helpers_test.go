package intune

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
)

// graphServer is a local stand-in for login.microsoftonline.com and graph.microsoft.com. Handlers reproduce the
// payload shapes of the pages named in graph_provider.go and graph_writer.go (read 2026-10-10).
type graphServer struct {
	*httptest.Server
	mux *http.ServeMux
	mu  sync.Mutex
	log []string
}

func newGraphServer(t *testing.T) *graphServer {
	t.Helper()
	s := &graphServer{mux: http.NewServeMux()}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.log = append(s.log, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		if r.URL.Path != "/tenant-1/oauth2/v2.0/token" && r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Handlers are registered without the API version; the log keeps the full path.
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1.0"), "/beta")
		r2.URL.RawPath = ""
		s.mux.ServeHTTP(w, r2)
	}))
	t.Cleanup(s.Close)
	s.mux.HandleFunc("/tenant-1/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3599,"access_token":"test-token"}`))
	})
	return s
}

func (s *graphServer) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *graphServer) graph(t *testing.T) *microsoft.Graph {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := microsoft.NewSecretCredential(secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := microsoft.NewGraph(microsoft.GraphConfig{
		TenantID: "tenant-1", ClientID: "client-1", Credential: cred, BaseURL: s.URL, AuthorityBase: s.URL,
		Transport: s.Client().Transport, AllowInsecureHTTP: true, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func json200(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
