package authentication

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func ipReq(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8", "fd00::/8")
	tests := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"direct", "203.0.113.7:5555", nil, nil, "203.0.113.7"},
		{"direct ignores XFF without trusted proxies", "203.0.113.7:5555", []string{"1.2.3.4"}, nil, "203.0.113.7"},
		{"direct ignores XFF from untrusted peer", "203.0.113.7:5555", []string{"1.2.3.4"}, trusted, "203.0.113.7"},
		{"trusted proxy single entry", "10.0.0.5:1", []string{"198.51.100.9"}, trusted, "198.51.100.9"},
		{"trusted proxy no header", "10.0.0.5:1", nil, trusted, "10.0.0.5"},
		{"forged leftmost entry is ignored", "10.0.0.5:1", []string{"6.6.6.6, 198.51.100.9"}, trusted, "198.51.100.9"},
		{"trusted hops are skipped", "10.0.0.5:1", []string{"198.51.100.9, 10.1.1.1, 10.2.2.2"}, trusted, "198.51.100.9"},
		{"several header lines", "10.0.0.5:1", []string{"6.6.6.6", "198.51.100.9, 10.1.1.1"}, trusted, "198.51.100.9"},
		{"all hops trusted uses the left-most", "10.0.0.5:1", []string{"10.1.1.1, 10.2.2.2"}, trusted, "10.1.1.1"},
		{"all hops trusted across header lines", "10.0.0.5:1", []string{"10.3.3.3", "10.1.1.1, 10.2.2.2"}, trusted, "10.3.3.3"},
		{"malformed right-most entry is skipped", "10.0.0.5:1", []string{"198.51.100.9, garbage"}, trusted, "198.51.100.9"},
		{"empty entry is skipped", "10.0.0.5:1", []string{"198.51.100.9,"}, trusted, "198.51.100.9"},
		{"only unparseable entries fall back to peer", "10.0.0.5:1", []string{"garbage, , nope"}, trusted, "10.0.0.5"},
		{"ipv6 client behind ipv4 proxy", "10.0.0.5:1", []string{"2001:db8::1"}, trusted, "2001:db8::1"},
		{"ipv6 proxy", "[fd00::1]:443", []string{"2001:db8::2"}, trusted, "2001:db8::2"},
		{"ipv4-mapped is normalized", "[::ffff:203.0.113.7]:1", nil, nil, "203.0.113.7"},
		{"mapped proxy is trusted", "[::ffff:10.0.0.5]:1", []string{"198.51.100.9"}, trusted, "198.51.100.9"},
		{"zone is dropped", "[fe80::1%eth0]:1", nil, nil, "fe80::1"},
		{"unparseable remote", "garbage", []string{"1.2.3.4"}, trusted, "unknown"},
		{"remote without port", "203.0.113.7", nil, nil, "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClientIP(ipReq(tt.remote, tt.xff...), tt.trusted); got != tt.want {
				t.Fatalf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientIPOversizedForwardedChain(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	repeat := func(entry string, n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = entry
		}
		return strings.Join(out, ", ")
	}
	tests := []struct {
		name, xff, want string
	}{
		// Only the right-most 32 entries count; forged ones further left never do.
		{"right-most untrusted wins", repeat("6.6.6.6", 20) + ", " + repeat("10.1.1.1", 31) + ", 198.51.100.9", "198.51.100.9"},
		{"forged entries beyond the window are ignored", repeat("6.6.6.6", 40) + ", " + repeat("10.1.1.1", 32), "10.1.1.1"},
		{"never the trusted peer", repeat("10.2.2.2", 40), "10.2.2.2"},
		{"all garbage and oversized is unknown, not the peer", repeat("garbage", 40), "unknown"},
	}
	for _, tt := range tests {
		if got := ClientIP(ipReq("10.0.0.5:1", tt.xff), trusted); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
	// Exactly 32 entries are not oversized.
	if got := ClientIP(ipReq("10.0.0.5:1", repeat("garbage", maxForwardedEntries)), trusted); got != "10.0.0.5" {
		t.Fatalf("32 garbage entries: got %q", got)
	}
	// The left-most of the considered window is used when all are trusted.
	var distinct []string
	for i := 1; i <= 40; i++ {
		distinct = append(distinct, fmt.Sprintf("10.0.0.%d", i))
	}
	if got := ClientIP(ipReq("10.0.0.250:1", strings.Join(distinct, ",")), trusted); got != "10.0.0.9" {
		t.Fatalf("all trusted, oversized: got %q, want the left-most of the last 32 (10.0.0.9)", got)
	}
}
