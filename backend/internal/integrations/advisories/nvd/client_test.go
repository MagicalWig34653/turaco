package nvd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// fakeClock moves only when something sleeps, so tests run instantly and deterministically.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	slept  []time.Duration
	failOn int
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
	cfg := Config{BaseURL: srv.URL, HTTPClient: srv.Client(), Clock: clock, Version: "1.2.3", BackoffBase: time.Second}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, clock
}

func TestRateLimiterRollingWindow(t *testing.T) {
	clock := &fakeClock{now: t0}
	l := newLimiter(clock, 5, 30*time.Second)
	for i := 0; i < 5; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(clock.slept) != 0 {
		t.Fatalf("the first five requests must not wait: %v", clock.slept)
	}
	if err := l.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 30*time.Second {
		t.Fatalf("the sixth request waits for the window to roll: %v", clock.slept)
	}
	// Spread requests: four immediately, then 20 s later one more is allowed, the next must wait for the first to expire.
	clock2 := &fakeClock{now: t0}
	l2 := newLimiter(clock2, 2, 30*time.Second)
	_ = l2.wait(context.Background())
	clock2.now = clock2.now.Add(20 * time.Second)
	_ = l2.wait(context.Background())
	_ = l2.wait(context.Background())
	if len(clock2.slept) != 1 || clock2.slept[0] != 10*time.Second {
		t.Fatalf("wait until the oldest request leaves the window: %v", clock2.slept)
	}
}

func TestRateLimiterHonorsCancellation(t *testing.T) {
	clock := &fakeClock{now: t0}
	l := newLimiter(clock, 1, time.Minute)
	_ = l.wait(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestLimitDependsOnKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a, _ := newClient(t, srv, nil)
	b, _ := newClient(t, srv, func(c *Config) { c.APIKey = "k" })
	if a.limiter.limit != 5 || b.limiter.limit != 50 {
		t.Fatalf("limits %d / %d", a.limiter.limit, b.limiter.limit)
	}
}

func TestSyncMapsFixtureAndSendsHeaders(t *testing.T) {
	var gotKey, gotUA, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotUA, gotQuery = r.Header.Get("apiKey"), r.Header.Get("User-Agent"), r.URL.RawQuery
		_, _ = w.Write(fixture(t, "page_mixed.json"))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.APIKey = "secret-key" })
	res, err := c.Sync(context.Background(), t0.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" || gotUA != "turaco/1.2.3" {
		t.Fatalf("headers apiKey=%q ua=%q", gotKey, gotUA)
	}
	q, _ := url.ParseQuery(gotQuery)
	if q.Get("lastModStartDate") != "2026-09-30T12:00:00.000+00:00" || q.Get("lastModEndDate") != "2026-10-01T12:00:00.000+00:00" || q.Get("resultsPerPage") != "500" {
		t.Fatalf("query %v", q)
	}
	if !res.Complete || !res.Through.Equal(t0) {
		t.Fatalf("result %+v", res)
	}
	if len(res.Records) != 2 {
		t.Fatalf("the rejected CVE must be skipped: %d records", len(res.Records))
	}
	r := res.Records[0]
	if r.Source != "nvd" || r.ExternalID != "CVE-2026-1001" || r.Severity != "critical" || r.SourceURL != "https://nvd.nist.gov/vuln/detail/CVE-2026-1001" {
		t.Fatalf("record %+v", r)
	}
	if !strings.HasPrefix(r.Title, "CVE-2026-1001: Heap overflow in Example Viewer") || strings.ContainsRune(r.Title, '‮') || strings.Contains(r.Title, "\n") {
		t.Fatalf("title %q", r.Title)
	}
	if strings.ContainsRune(r.Summary, '‮') || !strings.Contains(r.Summary, "\nSecond line.") || strings.Contains(r.Summary, "espanol") {
		t.Fatalf("summary %q", r.Summary)
	}
	if r.PublishedAt == nil || !r.PublishedAt.Equal(time.Date(2026, 9, 1, 10, 15, 7, 123000000, time.UTC)) || r.ModifiedAt == nil {
		t.Fatalf("times %v %v", r.PublishedAt, r.ModifiedAt)
	}
	if !slices.Equal(r.References, []string{"https://example.org/advisory/1001"}) {
		t.Fatalf("references %v", r.References)
	}
	want := []advisories.Criteria{
		{ProductName: "example viewer", Publisher: "example corp", Rules: []advisories.VersionRule{
			{Kind: "introduced", Version: "2.0.0"}, {Kind: "fixed", Version: "2.5.0"}, {Kind: "eq", Version: "1.9"}}},
		{ProductName: "example viewer", Publisher: "example corp", OSPlatform: "macos", Rules: []advisories.VersionRule{{Kind: "le", Version: "1.8.1"}}},
		{ProductName: "other tool", Publisher: "other vendor", Rules: []advisories.VersionRule{{Kind: "lt", Version: "7.1"}}},
	}
	if fmt.Sprint(r.Criteria) != fmt.Sprint(want) {
		t.Fatalf("criteria\n got %v\nwant %v", r.Criteria, want)
	}
	last := res.Records[1]
	if last.ExternalID != "CVE-2026-1003" || last.Severity != "none" || len(last.Criteria) != 0 {
		t.Fatalf("unanalyzed record %+v", last)
	}
}

func TestNoKeyHeaderWithoutKey(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Apikey"]
		_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("the apiKey header must be absent without a key")
	}
}

func TestPaginationAndWindows(t *testing.T) {
	var mu sync.Mutex
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		start, _ := strconv.Atoi(q.Get("startIndex"))
		per, _ := strconv.Atoi(q.Get("resultsPerPage"))
		total := 5
		if q.Get("lastModStartDate") != "2026-04-04T12:00:00.000+00:00" { // later windows are empty
			total = 0
		}
		var items []string
		for i := start; i < min(start+per, total); i++ {
			items = append(items, fmt.Sprintf(`{"cve":{"id":"CVE-2026-%04d","lastModified":"2026-05-01T00:00:00.000","descriptions":[{"lang":"en","value":"d"}]}}`, 2000+i))
		}
		_, _ = fmt.Fprintf(w, `{"totalResults":%d,"vulnerabilities":[%s]}`, total, strings.Join(items, ","))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.PageSize = 2 })
	// 180 days: a 120-day window, then a 60-day window; the first holds five records in three pages.
	res, err := c.Sync(context.Background(), t0.Add(-180*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 5 || !res.Complete {
		t.Fatalf("records %d complete %v", len(res.Records), res.Complete)
	}
	var starts []string
	for _, q := range queries {
		if q.Get("lastModStartDate") == "2026-04-04T12:00:00.000+00:00" {
			starts = append(starts, q.Get("startIndex"))
		}
	}
	if !slices.Equal(starts, []string{"0", "2", "4"}) {
		t.Fatalf("pages %v", starts)
	}
	if len(queries) != 4 {
		t.Fatalf("expected 3 pages + 1 second window, got %d requests", len(queries))
	}
	last := queries[len(queries)-1]
	if last.Get("lastModStartDate") != "2026-08-02T12:00:00.000+00:00" || last.Get("lastModEndDate") != "2026-10-01T12:00:00.000+00:00" {
		t.Fatalf("second window %v", last)
	}
}

func TestWindowIsSplitWhenOverBoundAndRunStopsAtBound(t *testing.T) {
	// The server holds records in the last two hours only; the bound of 3 forces the window to be halved
	// until a window fits, then the following window would exceed the remaining bound and the run stops.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s, _ := time.Parse("2006-01-02T15:04:05.000-07:00", q.Get("lastModStartDate"))
		e, _ := time.Parse("2006-01-02T15:04:05.000-07:00", q.Get("lastModEndDate"))
		// 2 records per hour of the window; ids derived from the start so records are distinct.
		hours := int(e.Sub(s) / time.Hour)
		total := hours * 2
		if hours == 0 {
			total = 0
		}
		var items []string
		for i := 0; i < total && i < 500; i++ {
			items = append(items, fmt.Sprintf(`{"cve":{"id":"CVE-%d-%04d","descriptions":[{"lang":"en","value":"d"}]}}`, 2000+int(s.Unix()%1000), i))
		}
		_, _ = fmt.Fprintf(w, `{"totalResults":%d,"vulnerabilities":[%s]}`, total, strings.Join(items, ","))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.MaxRecords = 3 })
	res, err := c.Sync(context.Background(), t0.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete || len(res.Records) == 0 || len(res.Records) > 3 {
		t.Fatalf("expected an incomplete run within the bound: %+v", res)
	}
	if !res.Through.After(t0.Add(-24 * time.Hour)) {
		t.Fatalf("the cursor must advance past completed windows: %v", res.Through)
	}
}

func TestWindowTooLargeAtMinimumFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalResults":50,"vulnerabilities":[]}`))
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.MaxRecords = 10 })
	if _, err := c.Sync(context.Background(), t0.Add(-30*time.Minute)); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterAndBackoff(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		switch n {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusForbidden)
		case 3:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
		}
	}))
	defer srv.Close()
	c, clock := newClient(t, srv, nil)
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("requests = %d", n)
	}
	// Retry-After 7 s, then backoff base<<1 = 2 s, then base<<2 = 4 s.
	if !slices.Equal(clock.slept, []time.Duration{7 * time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("sleeps %v", clock.slept)
	}
}

func TestGivesUpAfterRetriesAndOnLongRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.MaxRetries = 2 })
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer long.Close()
	c2, clock := newClient(t, long, nil)
	if _, err := c2.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
	if len(clock.slept) != 0 {
		t.Fatalf("a Retry-After above the cap must not be waited for: %v", clock.slept)
	}
	srv5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv5.Close()
	c3, _ := newClient(t, srv5, nil)
	if _, err := c3.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(404) }))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrUnavailable) || n != 1 {
		t.Fatalf("err = %v requests = %d", err, n)
	}
}

func TestMalformedAndOversizedResponses(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "malformed.json"))
	}))
	defer bad.Close()
	c, _ := newClient(t, bad, nil)
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("malformed: %v", err)
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "page_mixed.json"))
	}))
	defer big.Close()
	c2, _ := newClient(t, big, func(cfg *Config) { cfg.MaxResponse = 512 })
	if _, err := c2.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrInvalidResponse) {
		t.Fatalf("oversized: %v", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c, _ := newClient(t, srv, func(cfg *Config) { cfg.RequestTimeout = 50 * time.Millisecond; cfg.MaxRetries = -1 })
	if _, err := c.Sync(context.Background(), t0.Add(-time.Hour)); !errors.Is(err, advisories.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Sync(ctx, t0.Add(-time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestByID(t *testing.T) {
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("cveId")
		ids = append(ids, id)
		if id == "CVE-2026-9999" {
			_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"totalResults":1,"vulnerabilities":[{"cve":{"id":%q,"descriptions":[{"lang":"en","value":"x"}]}}]}`, id)
	}))
	defer srv.Close()
	c, _ := newClient(t, srv, nil)
	got, err := c.ByID(context.Background(), []string{"cve-2026-1001", "../etc/passwd", "CVE-2026-9999", "CVE-2026-1002"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ExternalID != "CVE-2026-1001" || got[1].ExternalID != "CVE-2026-1002" || len(ids) != 3 {
		t.Fatalf("got %v ids %v", got, ids)
	}
}

func TestParseCPEAndRules(t *testing.T) {
	c, ok := parseCPE23(`cpe:2.3:a:ven\:dor:pro_duct:1.0:*:*:*:*:windows:*:*`)
	if !ok || c.vendor != "ven:dor" || c.product != "pro_duct" || c.version != "1.0" || c.targetSW != "windows" {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := parseCPE23("cpe:2.3:a:only:three"); ok {
		t.Fatal("short CPE accepted")
	}
	tests := []struct {
		name     string
		m        cpeMatch
		version  string
		want     []advisories.VersionRule
		unconstr bool
		ok       bool
	}{
		{"start incl + end excl", cpeMatch{VersionStartIncluding: "1.0", VersionEndExcluding: "2.0"}, "*", []advisories.VersionRule{{Kind: "introduced", Version: "1.0"}, {Kind: "fixed", Version: "2.0"}}, false, true},
		{"start excl + end excl", cpeMatch{VersionStartExcluding: "1.0", VersionEndExcluding: "2.0"}, "*", []advisories.VersionRule{{Kind: "introduced", Version: "1.0"}, {Kind: "fixed", Version: "2.0"}}, false, true},
		{"end excl only", cpeMatch{VersionEndExcluding: "2.0"}, "*", []advisories.VersionRule{{Kind: "lt", Version: "2.0"}}, false, true},
		{"end incl only", cpeMatch{VersionEndIncluding: "2.0"}, "*", []advisories.VersionRule{{Kind: "le", Version: "2.0"}}, false, true},
		{"start incl + end incl", cpeMatch{VersionStartIncluding: "1.0", VersionEndIncluding: "2.0"}, "*", []advisories.VersionRule{{Kind: "le", Version: "2.0"}}, false, true},
		{"start only", cpeMatch{VersionStartIncluding: "3.1"}, "*", []advisories.VersionRule{{Kind: "introduced", Version: "3.1"}}, false, true},
		{"exact version", cpeMatch{}, "4.2.1", []advisories.VersionRule{{Kind: "eq", Version: "4.2.1"}}, false, true},
		{"any version", cpeMatch{}, "*", nil, true, true},
		{"not applicable version", cpeMatch{}, "-", nil, true, true},
		{"unusable version skips the match", cpeMatch{VersionEndExcluding: "2 0"}, "*", nil, false, false},
		{"unusable exact version", cpeMatch{}, "1.0 beta", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, unconstr, ok := rulesOf(tt.m, cpe{version: tt.version})
			if ok != tt.ok || unconstr != tt.unconstr || fmt.Sprint(rules) != fmt.Sprint(tt.want) {
				t.Fatalf("got %v %v %v", rules, unconstr, ok)
			}
		})
	}
}

func TestUnconstrainedMatchWidensTheProduct(t *testing.T) {
	cfg := []configuration{{Nodes: []node{{CPEMatch: []cpeMatch{
		{Vulnerable: true, Criteria: "cpe:2.3:a:v:p:*:*:*:*:*:*:*:*", VersionEndExcluding: "2.0"},
		{Vulnerable: true, Criteria: "cpe:2.3:a:v:p:*:*:*:*:*:*:*:*"},
	}}}}}
	got := criteria(cfg)
	if len(got) != 1 || len(got[0].Rules) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestSeverityBands(t *testing.T) {
	score := func(v float64) metrics {
		return metrics{V31: []metric{{Type: "Primary", CVSSData: struct {
			BaseScore *float64 `json:"baseScore"`
		}{&v}}}}
	}
	for v, want := range map[float64]string{0: "none", 0.1: "low", 3.9: "low", 4.0: "medium", 6.9: "medium", 7.0: "high", 8.9: "high", 9.0: "critical", 10: "critical"} {
		if got := severity(score(v)); got != want {
			t.Errorf("score %v: %s, want %s", v, got, want)
		}
	}
	v4 := 5.0
	m := score(9.8)
	m.V40 = []metric{{CVSSData: struct {
		BaseScore *float64 `json:"baseScore"`
	}{&v4}}}
	if severity(m) != "medium" {
		t.Error("v4.0 is preferred")
	}
}

func TestTitleAndSummaryAreBounded(t *testing.T) {
	long := strings.Repeat("word ", 2000)
	if got := title("CVE-2026-1", long); len([]rune(got)) > maxTitle || !strings.HasSuffix(got, "...") {
		t.Fatalf("title %d runes", len([]rune(got)))
	}
	if got := summary(long); len([]rune(got)) > maxSummary {
		t.Fatalf("summary %d runes", len([]rune(got)))
	}
	if title("CVE-2026-1", "") != "CVE-2026-1" {
		t.Fatal("empty description")
	}
}

func TestInvalidConfig(t *testing.T) {
	if _, err := New(Config{BaseURL: "ftp://x"}); err == nil {
		t.Fatal("bad base URL accepted")
	}
	if _, err := New(Config{APIKey: "a\nb"}); err == nil {
		t.Fatal("bad key accepted")
	}
}
