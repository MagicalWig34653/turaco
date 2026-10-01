package authentication

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedEntries = 32

// ClientIP returns the address used for per-client throttling. It is the host
// of RemoteAddr, unless RemoteAddr lies inside trusted: then it is taken from
// X-Forwarded-For. Every proxy appends the address it received the request
// from, so only the right-most entries are trustworthy: at most the right-most
// maxForwardedEntries entries are considered and walked from the right,
// skipping trusted proxies and unparseable entries; the first untrusted
// address is the client. If every considered entry is trusted, the left-most
// parsed one is used. A trusted RemoteAddr is never used as the client when an
// X-Forwarded-For header is present (an oversized or garbled header must not
// select a fresh, shared key); without any parseable entry the client is
// "unknown" for an oversized header and RemoteAddr otherwise.
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
	if len(entries) == 0 {
		return remote.String()
	}
	oversized := len(entries) > maxForwardedEntries
	if oversized {
		entries = entries[len(entries)-maxForwardedEntries:]
	}
	var leftmost netip.Addr
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			continue
		}
		addr = normalizeAddr(addr)
		if !inPrefixes(addr, trusted) {
			return addr.String()
		}
		leftmost = addr
	}
	if leftmost.IsValid() {
		// Every hop is a trusted proxy: the left-most is the best guess.
		return leftmost.String()
	}
	if oversized {
		return "unknown"
	}
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
