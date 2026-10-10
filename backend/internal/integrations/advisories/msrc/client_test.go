package msrc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

func serve(t *testing.T, hits *atomic.Int32) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "turaco/") {
			t.Errorf("headers %v", r.Header)
		}
		var file string
		switch r.URL.Path {
		case "/updates":
			file = "testdata/updates.json"
		case "/cvrf/2026-Oct":
			file = "testdata/2026-Oct.json"
		case "/cvrf/2026-Sep":
			_, _ = w.Write([]byte(`{"Vulnerability":[]}`))
			return
		case "/throttled":
			w.WriteHeader(http.StatusTooManyRequests)
			return
		default:
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSyncMapsCVRFDocument(t *testing.T) {
	c := serve(t, nil)
	res, err := c.Sync(context.Background(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !res.Complete || len(res.Records) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	r := res.Records[0]
	if r.Source != "msrc" || r.ExternalID != "CVE-2026-12345" || r.Severity != "critical" || r.Title != "Windows Kernel Elevation of Privilege Vulnerability" {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.Summary, "<b>") || !strings.Contains(r.Summary, "SYSTEM") {
		t.Errorf("summary %q", r.Summary)
	}
	if r.PublishedAt == nil || r.ModifiedAt == nil || !r.ModifiedAt.After(*r.PublishedAt) || !strings.HasPrefix(r.SourceURL, "https://msrc.microsoft.com/update-guide/vulnerability/CVE-2026-12345") {
		t.Errorf("dates/url %+v", r)
	}
	// Only products of the product tree become criteria (99999 is unknown); the fixed build becomes a fixed rule.
	if len(r.Criteria) != 2 {
		t.Fatalf("criteria %+v", r.Criteria)
	}
	var win advisories.Criteria
	for _, cr := range r.Criteria {
		if strings.HasPrefix(cr.ProductName, "Windows 11") {
			win = cr
		}
	}
	if win.OSPlatform != "windows" || len(win.Rules) != 1 || win.Rules[0] != (advisories.VersionRule{Kind: advisories.RuleFixed, Version: "10.0.26100.3194"}) {
		t.Errorf("windows criteria %+v", win)
	}
	if !res.Through.Equal(time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("through %v", res.Through)
	}
}

func TestSyncWithoutCursorReadsOnlyTheLatestDocument(t *testing.T) {
	var hits atomic.Int32
	c := serve(t, &hits)
	res, err := c.Sync(context.Background(), time.Time{})
	if err != nil || len(res.Records) != 1 || hits.Load() != 2 {
		t.Fatalf("%+v %v hits %d", res, err, hits.Load())
	}
}

func TestSyncSkipsDocumentsNotNewerThanTheCursor(t *testing.T) {
	var hits atomic.Int32
	c := serve(t, &hits)
	res, err := c.Sync(context.Background(), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(res.Records) != 0 || !res.Complete || hits.Load() != 1 {
		t.Fatalf("%+v %v hits %d", res, err, hits.Load())
	}
}

func TestErrorsAreClassified(t *testing.T) {
	c := serve(t, nil)
	var out updateList
	if err := c.get(context.Background(), "/throttled", &out); !errors.Is(err, advisories.ErrRateLimited) {
		t.Errorf("429: %v", err)
	}
	if err := c.get(context.Background(), "/missing", &out); !errors.Is(err, advisories.ErrUnavailable) {
		t.Errorf("404: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer bad.Close()
	bc, _ := New(Config{BaseURL: bad.URL})
	if _, err := bc.Sync(context.Background(), time.Time{}); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Errorf("html: %v", err)
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat(" ", 200))) }))
	defer big.Close()
	small, _ := New(Config{BaseURL: big.URL, MaxResponse: 100})
	if _, err := small.Sync(context.Background(), time.Time{}); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Errorf("oversized: %v", err)
	}
}

func TestRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Error("redirect target reached") }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer redir.Close()
	c, _ := New(Config{BaseURL: redir.URL})
	if _, err := c.Sync(context.Background(), time.Time{}); !errors.Is(err, advisories.ErrUnavailable) {
		t.Errorf("%v", err)
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New(Config{BaseURL: "ftp://x"}); err == nil {
		t.Fatal("accepted")
	}
}
