package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func get(t *testing.T, c *Client, rawURL string) (int, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func TestCheckAddrPolicy(t *testing.T) {
	cases := []struct {
		ip              string
		external, local bool // allowed?
	}{
		{"93.184.216.34", true, true},
		{"2606:2800:220:1:248:1893:25c8:1946", true, true},
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"10.1.2.3", false, true},
		{"172.16.5.5", false, true},
		{"192.168.1.1", false, true},
		{"fd12:3456::1", false, true},
		{"100.64.0.1", false, true},
		{"169.254.169.254", false, false},
		{"::ffff:169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"224.0.0.1", false, false},
		{"ff02::1", false, false},
		{"255.255.255.255", false, false},
		{"100.100.100.200", false, false},
		{"fd00:ec2::254", false, false},
		{"192.0.0.192", false, false},
		{"64:ff9b::7f00:1", false, false},
	}
	for _, c := range cases {
		ip := netip.MustParseAddr(c.ip)
		if got := checkAddr(ip, false) == nil; got != c.external {
			t.Errorf("external policy %s: allowed=%v, want %v", c.ip, got, c.external)
		}
		if got := checkAddr(ip, true) == nil; got != c.local {
			t.Errorf("local policy %s: allowed=%v, want %v", c.ip, got, c.local)
		}
	}
}

func TestValidateEndpoint(t *testing.T) {
	bad := []struct {
		url   string
		local bool
	}{
		{"http://provider.example/v1", false},
		{"ftp://provider.example", false},
		{"https://user:pw@provider.example/v1", false},
		{"https://provider.example/v1?x=1", false},
		{"https://provider.example/v1#f", false},
		{"https://127.0.0.1/v1", false},
		{"https://[::1]/v1", false},
		{"https://169.254.169.254/latest", false},
		{"http://169.254.169.254/latest", true},
		{"http://[fe80::1]/v1", true},
		{"https://provider.example:99999/v1", false},
		{"", false},
		{"//provider.example", false},
	}
	for _, c := range bad {
		if _, err := ValidateEndpoint(c.url, c.local); err == nil {
			t.Errorf("ValidateEndpoint(%q, local=%v) accepted", c.url, c.local)
		}
	}
	for _, c := range []struct {
		url   string
		local bool
	}{{"https://provider.example/v1", false}, {"http://127.0.0.1:11434/v1", true}, {"http://localhost:11434/v1", true}, {"https://10.0.0.5/v1", true}} {
		if _, err := ValidateEndpoint(c.url, c.local); err != nil {
			t.Errorf("ValidateEndpoint(%q, local=%v): %v", c.url, c.local, err)
		}
	}
}

func TestExternalProviderCannotReachLoopbackEvenViaDNS(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	c, err := New(Policy{Endpoint: "https://provider.example:" + port,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = get(t, c, c.URL("x"))
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("expected blocked destination, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the loopback server was reached")
	}
}

func TestLocalProviderMayUseLoopbackButNotRedirects(t *testing.T) {
	var target atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { target.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/redirect" {
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c, err := New(Policy{Endpoint: srv.URL + "/v1", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	status, body, err := get(t, c, c.URL("chat"))
	if err != nil || status != 200 || string(body) != "ok" {
		t.Fatalf("local request: %d %q %v", status, body, err)
	}
	_, _, err = get(t, c, c.URL("redirect"))
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect must be refused, got %v", err)
	}
	if target.Load() != 0 {
		t.Fatal("the redirect target was contacted")
	}
}

func TestLocalProviderStillBlocksMetadataAndMixedAnswers(t *testing.T) {
	c, err := New(Policy{Endpoint: "http://ollama.internal:11434/v1", Local: true,
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("169.254.169.254")}, nil
		},
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dialed despite a blocked address in the answer")
			return nil, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(t, c, c.URL("x")); !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("mixed answer with a metadata address must be refused, got %v", err)
	}
}

// A hostname that resolves to a public address on one lookup and to an internal address on the next (DNS
// rebinding) cannot change the destination: the check is on the dial-time answer, and the validated IP itself is dialed.
func TestDNSRebindingIsDefeatedAtDialTime(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	calls := 0
	c, err := New(Policy{Endpoint: "https://rebind.example/v1",
		Resolver: func(context.Context, string) ([]netip.Addr, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		Dialer: func(_ context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, address)
			mu.Unlock()
			return nil, errors.New("stub dial")
		}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err1 := get(t, c, c.URL("x"))
	if err1 == nil || errors.Is(err1, ErrBlockedDestination) {
		t.Fatalf("first lookup is public and reaches the (stub) dial: %v", err1)
	}
	_, _, err2 := get(t, c, c.URL("x"))
	if !errors.Is(err2, ErrBlockedDestination) {
		t.Fatalf("second lookup resolves to loopback and must be refused: %v", err2)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("only the validated IP may be dialed, never the name: %v", dialed)
	}
}

func TestRequestMustTargetTheProviderHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	c, err := New(Policy{Endpoint: srv.URL, Local: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://127.0.0.1:1/x", "http://evil.example/x", "https://" + strings.TrimPrefix(srv.URL, "http://") + "/x", "http://u:p@" + strings.TrimPrefix(srv.URL, "http://") + "/x"} {
		if _, _, err := get(t, c, u); !errors.Is(err, ErrBlockedHost) {
			t.Errorf("%s: expected ErrBlockedHost, got %v", u, err)
		}
	}
}

func TestResponseSizeAndTimeoutAreCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = w.Write([]byte(strings.Repeat("a", 2048)))
	}))
	defer srv.Close()
	c, err := New(Policy{Endpoint: srv.URL, Local: true, MaxResponseBytes: 1024, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := get(t, c, c.URL("big")); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("oversized response: %v", err)
	}
	if _, _, err := get(t, c, c.URL("slow")); err == nil {
		t.Error("slow response must time out")
	}
}
