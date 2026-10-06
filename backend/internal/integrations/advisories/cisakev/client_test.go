package cisakev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

func serve(t *testing.T, h http.HandlerFunc, mod func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL, HTTPClient: srv.Client(), Version: "9.9"}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogParsesEntriesAndETag(t *testing.T) {
	body, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotUA, gotINM string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotINM = r.Header.Get("User-Agent"), r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write(body)
	}, nil)
	got, err := c.Catalog(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if gotUA != "turaco/9.9" || gotINM != "" || got.ETag != `"abc"` || got.NotModified {
		t.Fatalf("ua=%q inm=%q %+v", gotUA, gotINM, got)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("invalid ids must be skipped: %+v", got.Entries)
	}
	e := got.Entries[0]
	if e.CVEID != "CVE-2026-1001" || !e.KnownRansomwareUse || e.RequiredAction != "Apply updates per vendor instructions." ||
		e.DateAdded == nil || e.DateAdded.Format(time.DateOnly) != "2026-09-30" || e.DueDate == nil || e.DueDate.Format(time.DateOnly) != "2026-10-21" {
		t.Fatalf("entry %+v", e)
	}
	if e2 := got.Entries[1]; e2.CVEID != "CVE-2026-1002" || e2.KnownRansomwareUse || strings.ContainsRune(e2.RequiredAction, '‮') {
		t.Fatalf("entry %+v", e2)
	}
	if e3 := got.Entries[2]; e3.DateAdded != nil || e3.DueDate != nil {
		t.Fatalf("invalid dates must be empty: %+v", e3)
	}
}

func TestConditionalFetch(t *testing.T) {
	var gotINM string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}, nil)
	got, err := c.Catalog(context.Background(), `"abc"`)
	if err != nil || !got.NotModified || got.ETag != `"abc"` || len(got.Entries) != 0 || gotINM != `"abc"` {
		t.Fatalf("%+v %v inm=%q", got, err, gotINM)
	}
	// A 304 without a conditional request is not a valid answer.
	if _, err := c.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	big := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 2000))) }, func(c *Config) { c.MaxResponse = 1000 })
	if _, err := big.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("oversized: %v", err)
	}
	bad := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"vulnerabilities": [`)) }, nil)
	if _, err := bad.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("malformed: %v", err)
	}
	only := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"vulnerabilities":[{"cveID":"x"}]}`))
	}, nil)
	if _, err := only.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("no valid entry: %v", err)
	}
	limited := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, nil)
	if _, err := limited.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrRateLimited) {
		t.Fatalf("429: %v", err)
	}
	down := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, nil)
	if _, err := down.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("503: %v", err)
	}
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, func(c *Config) { c.RequestTimeout = 50 * time.Millisecond })
	if _, err := slow.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}, func(cfg *Config) { cfg.HTTPClient = &http.Client{} })
	if _, err := c.Catalog(context.Background(), ""); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if hit {
		t.Fatal("the redirect was followed")
	}
}
