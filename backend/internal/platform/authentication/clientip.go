package authentication

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedEntries = 32

// ClientIP returns the address used for per-client throttling. It is the host
// of RemoteAddr, unless RemoteAddr lies inside trusted: then it is the
// right-most X-Forwarded-For entry that is not itself trusted (every proxy
// appends the address it received the request from, so entries left of the
// first untrusted one may be forged by the client). A malformed or oversized
// header falls back to RemoteAddr so it cannot be used to pick a fresh key.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	remote, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !inPrefixes(remote, trusted) {
		return remote.String()
	}
	var entries []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(h, ",")...)
	}
	if len(entries) == 0 || len(entries) > maxForwardedEntries {
		return remote.String()
	}
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			return remote.String()
		}
		addr = normalizeAddr(addr)
		if !inPrefixes(addr, trusted) {
			return addr.String()
		}
	}
	// Every hop is a trusted proxy: the client is not distinguishable.
	return remote.String()
}

func parseHostAddr(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return normalizeAddr(addr), true
}

func normalizeAddr(a netip.Addr) netip.Addr { return a.Unmap().WithZone("") }

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
