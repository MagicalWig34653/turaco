package osv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// fakeClock moves only when something sleeps, so tests run instantly and deterministically.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newClient(t *testing.T, srv *httptest.Server, mod func(*Config)) (*Client, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: t0}
	cfg := Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Clock: clock, Version: "1.2.3", BackoffBase: time.Second, AllowInsecureHTTP: true}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, clock
}

var examplePkg = []advisories.PackageQuery{{Ecosystem: "npm", Name: "example-lib"}}

func TestPackagesSendsQueryFollowsPagesAndMaps(t *testing.T) {
	var mu sync.Mutex
	var bodies []queryRequest
	var gotUA, gotCT, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var q queryRequest
		_ = json.Unmarshal(raw, &q)
		mu.Lock()
		bodies = append(bodies, q)
		gotUA, gotCT, gotMethod, gotPath = r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), r.Method, r.URL.Path
		mu.Unlock()
		if q.PageToken == "page-2" {
			_, _ = w.Write(fixture(t, "query_page2.json"))
			return
		}
		_, _ = w.Write(fixture(t, "query_page1.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	res, err := c.Packages(context.Background(), examplePkg)
	if err != nil || !res.Complete {
		t.Fatalf("complete=%v err=%v", res.Complete, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/query" || gotUA != "turaco/1.2.3" || gotCT != "application/json" {
		t.Fatalf("request %s %s ua=%q ct=%q", gotMethod, gotPath, gotUA, gotCT)
	}
	if len(bodies) != 2 || bodies[0].Package != (queryPackage{Name: "example-lib", Ecosystem: "npm"}) || bodies[0].PageToken != "" || bodies[1].PageToken != "page-2" {
		t.Fatalf("bodies %+v", bodies)
	}
	if len(res.Records) != 2 {
		t.Fatalf("withdrawn, malformed and duplicate records must be skipped: %d", len(res.Records))
	}
	r := res.Records[0]
	if r.Source != "osv" || r.ExternalID != "GHSA-aaaa-bbbb-cccc" || r.Severity != "high" || r.SourceURL != "https://osv.dev/vulnerability/GHSA-aaaa-bbbb-cccc" {
		t.Fatalf("record %+v", r)
	}
	if r.Title != "GHSA-aaaa-bbbb-cccc: Prototype pollution in example-lib" {
		t.Fatalf("title %q", r.Title)
	}
	if !strings.HasPrefix(r.Summary, "Aliases: CVE-2026-2001\n\n") || strings.ContainsRune(r.Summary, '‮') || !strings.Contains(r.Summary, "Second line.") {
		t.Fatalf("summary %q", r.Summary)
	}
	if r.PublishedAt == nil || !r.PublishedAt.Equal(time.Date(2026, 9, 1, 10, 15, 7, 0, time.UTC)) || r.ModifiedAt == nil || r.ModifiedAt.Nanosecond() != 123456000 {
		t.Fatalf("times %v %v", r.PublishedAt, r.ModifiedAt)
	}
	if !slices.Equal(r.References, []string{"https://github.com/advisories/GHSA-aaaa-bbbb-cccc"}) {
		t.Fatalf("references %v", r.References)
	}
	want := []advisories.VersionRule{
		{Kind: advisories.RuleIntroduced, Version: "0"}, {Kind: advisories.RuleFixed, Version: "1.2.3"},
		{Kind: advisories.RuleIntroduced, Version: "2.0.0"}, {Kind: advisories.RuleFixed, Version: "2.4.0"},
		{Kind: advisories.RuleEQ, Version: "1.0.0"},
	}
	if len(r.Criteria) != 1 || r.Criteria[0].ProductName != "example-lib" || !slices.Equal(r.Criteria[0].Rules, want) {
		t.Fatalf("criteria %+v", r.Criteria)
	}
	// One GIT range and one unusual version cannot be carried and are counted, never dropped silently.
	if r.CriteriaSkipped != 2 {
		t.Fatalf("criteriaSkipped = %d", r.CriteriaSkipped)
	}

	py := res.Records[1]
	if py.ExternalID != "PYSEC-2026-7" || py.Severity != "none" || py.Title != "PYSEC-2026-7: Denial of service in example-lib" {
		t.Fatalf("record %+v", py)
	}
	got := map[string]advisories.Criteria{}
	for _, cr := range py.Criteria {
		got[cr.ProductName] = cr
	}
	if rules := got["example-lib"].Rules; !slices.Equal(rules, []advisories.VersionRule{{Kind: advisories.RuleLE, Version: "3.1.5"}}) {
		t.Fatalf("last_affected keeps only the upper bound: %+v", rules)
	}
	if rules := got["other-lib"].Rules; len(rules) != 0 || len(py.Criteria) != 3 {
		t.Fatalf("a package without ranges affects every version: %+v", py.Criteria)
	}
	if rules := got["tail-lib"].Rules; !slices.Equal(rules, []advisories.VersionRule{{Kind: advisories.RuleIntroduced, Version: "4.0.0"}}) {
		t.Fatalf("open range: %+v", rules)
	}
}

func TestPackagesBoundsRecordsAndSkipsInvalidPackages(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(fixture(t, "query_page2.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.MaxRecords = 1 })
	res, err := c.Packages(context.Background(), []advisories.PackageQuery{{Ecosystem: "bad eco", Name: "x"}, {Ecosystem: "npm", Name: ""}, {Ecosystem: "npm", Name: "example-lib"}})
	if err != nil || res.Complete || len(res.Records) != 1 {
		t.Fatalf("the bound ends the run incomplete: complete=%v records=%d err=%v", res.Complete, len(res.Records), err)
	}
	if calls != 1 {
		t.Fatalf("invalid packages must not be queried: %d calls", calls)
	}
}

func TestPackagesStopsAtPageBound(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprintf(w, `{"vulns":[{"id":"GHSA-page-%04d","modified":"2026-09-20T00:00:00Z"}],"next_page_token":"more"}`, calls)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	res, err := c.Packages(context.Background(), examplePkg)
	if err != nil || res.Complete || calls != maxPagesPerPackage {
		t.Fatalf("complete=%v calls=%d err=%v", res.Complete, calls, err)
	}
}

func TestPackagesRetriesThrottlingAndServerErrors(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-retry-0001","modified":"2026-09-20T00:00:00Z"}]}`))
		}
	}))
	defer srv.Close()
	c, clock := newClient(t, srv, nil)
	res, err := c.Packages(context.Background(), examplePkg)
	if err != nil || len(res.Records) != 1 || !res.Complete {
		t.Fatalf("%+v %v", res, err)
	}
	if !slices.Equal(clock.slept, []time.Duration{7 * time.Second, 2 * time.Second}) {
		t.Fatalf("Retry-After first, then the doubled backoff: %v", clock.slept)
	}
}

func TestErrorsAreConstantAndRetriesBounded(t *testing.T) {
	tests := map[string]struct {
		status  int
		retry   string
		body    string
		want    error
		retried bool
	}{
		"throttled":        {status: http.StatusTooManyRequests, want: advisories.ErrRateLimited, retried: true},
		"retry after long": {status: http.StatusTooManyRequests, retry: "600", want: advisories.ErrRateLimited},
		"server error":     {status: http.StatusInternalServerError, want: advisories.ErrUnavailable, retried: true},
		"bad request":      {status: http.StatusBadRequest, want: advisories.ErrUnavailable},
		"malformed":        {status: http.StatusOK, body: "malformed.json", want: advisories.ErrInvalidResponse},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				if tc.body != "" {
					_, _ = w.Write(fixture(t, tc.body))
				}
			}))
			defer srv.Close()
			c, _ := newClient(t, srv, nil)
			_, err := c.Packages(context.Background(), examplePkg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("the error must not carry the URL: %v", err)
			}
			if tc.retried && calls != 4 || !tc.retried && calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestPackagesKeepsProgressOnError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-first-0001","modified":"2026-09-20T00:00:00Z"}]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	res, err := c.Packages(context.Background(), []advisories.PackageQuery{{Ecosystem: "npm", Name: "a"}, {Ecosystem: "npm", Name: "b"}})
	if !errors.Is(err, advisories.ErrUnavailable) || res.Complete || len(res.Records) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "query_page1.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.MaxResponse = 100 })
	if _, err := c.Packages(context.Background(), examplePkg); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestByID(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/vulns/GHSA-aaaa-bbbb-cccc":
			_, _ = w.Write([]byte(`{"id":"GHSA-aaaa-bbbb-cccc","modified":"2026-09-20T14:00:00Z","summary":"One"}`))
		case "/v1/vulns/GHSA-other-0001":
			// A record with another id than asked for is not trusted.
			_, _ = w.Write([]byte(`{"id":"GHSA-zzzz-0001","modified":"2026-09-20T14:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	got, err := c.ByID(context.Background(), []string{"GHSA-aaaa-bbbb-cccc", "unknown-0001", "../etc/passwd", "GHSA-other-0001", " "})
	if err != nil || len(got) != 1 || got[0].ExternalID != "GHSA-aaaa-bbbb-cccc" {
		t.Fatalf("%+v %v", got, err)
	}
	want := []string{"GET /v1/vulns/GHSA-aaaa-bbbb-cccc", "GET /v1/vulns/unknown-0001", "GET /v1/vulns/GHSA-other-0001"}
	if !slices.Equal(paths, want) {
		t.Fatalf("invalid ids must not be requested: %v", paths)
	}
}

func TestNewValidatesBaseURL(t *testing.T) {
	for name, url := range map[string]string{"plain http": "http://api.osv.dev", "no host": "https://", "scheme": "ftp://api.osv.dev"} {
		if _, err := New(Config{BaseURL: url}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := New(Config{}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}

func TestRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target must not be called")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.Packages(context.Background(), examplePkg); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestCriteriaBounds(t *testing.T) {
	var in []affected
	for i := 0; i < maxCriteria+3; i++ {
		in = append(in, affected{Package: pkg{Name: fmt.Sprintf("pkg-%d", i)}})
	}
	out, skipped := criteria(in)
	if len(out) != maxCriteria || skipped != 3 {
		t.Fatalf("criteria=%d skipped=%d", len(out), skipped)
	}
	var versions []string
	for i := 0; i < maxRules+2; i++ {
		versions = append(versions, fmt.Sprintf("1.0.%d", i))
	}
	out, skipped = criteria([]affected{{Package: pkg{Name: "many"}, Versions: versions}})
	if len(out) != 1 || len(out[0].Rules) != maxRules || skipped != 2 {
		t.Fatalf("rules=%d skipped=%d", len(out[0].Rules), skipped)
	}
}

func TestSeverityLabels(t *testing.T) {
	for in, want := range map[string]string{"CRITICAL": "critical", "high": "high", "MODERATE": "medium", "Low": "low", "": "none", "weird": "none"} {
		if got := severity(in); got != want {
			t.Errorf("severity(%q) = %q, want %q", in, got, want)
		}
	}
}
