// Package safehttp is the dedicated HTTP transport for AI Provider calls (F12 decision A14). A provider endpoint is
// administrator configuration, but it is still an SSRF primitive, so the destination is enforced where it cannot be
// bypassed: at dial time, on the resolved IP of every connection. DNS rebinding therefore cannot change the
// destination after validation, because there is no earlier validation to outlive.
//
// Rules: scheme https (http only for a local provider); the request host must equal the provider's host; no
// userinfo; no proxy from the environment; redirects are never followed; the response size and the time are capped.
// An external provider may never resolve to loopback, private, link-local, metadata or other special ranges. A local
// provider (an administrator-marked Ollama host) may use loopback and private ranges, never link-local, metadata,
// unspecified or multicast addresses.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Errors returned by the transport. They never contain response content.
var (
	ErrBlockedDestination = errors.New("safehttp: destination address is not allowed")
	ErrBlockedHost        = errors.New("safehttp: host is not the provider host")
	ErrRedirect           = errors.New("safehttp: redirects are not followed")
	ErrResponseTooLarge   = errors.New("safehttp: response exceeds the size limit")
	ErrInvalidEndpoint    = errors.New("safehttp: invalid provider endpoint")
)

// Policy describes what one provider's client may reach.
type Policy struct {
	// Endpoint is the provider's base URL. Only its scheme, host and port are reachable.
	Endpoint string
	// Local permits loopback and private ranges (and http), for providers an administrator marked local.
	Local bool
	// MaxResponseBytes caps the response body; default 1 MiB.
	MaxResponseBytes int64
	// Timeout caps one request including the body; default 60s.
	Timeout time.Duration
	// Resolver overrides name resolution (tests). Default: the system resolver.
	Resolver func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dialer overrides the final TCP dial (tests); it receives the already validated IP address and port.
	Dialer func(ctx context.Context, network, address string) (net.Conn, error)
}

// ValidateEndpoint checks a provider endpoint URL syntactically (on save). It does not resolve names: the
// resolved address is checked on every connection.
func ValidateEndpoint(raw string, local bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, ErrInvalidEndpoint
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalidEndpoint
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !local {
			return nil, fmt.Errorf("%w: http is only allowed for a local provider", ErrInvalidEndpoint)
		}
	default:
		return nil, ErrInvalidEndpoint
	}
	if port := u.Port(); port != "" {
		var n int
		if _, err := fmt.Sscanf(port, "%d", &n); err != nil || n < 1 || n > 65535 {
			return nil, ErrInvalidEndpoint
		}
	}
	// A literal IP is checked now so the administrator gets an early error; the dial check still applies.
	if ip, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil {
		if err := checkAddr(ip.Unmap(), local); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidEndpoint, err)
		}
	}
	return u, nil
}

// checkAddr applies the address policy.
func checkAddr(ip netip.Addr, local bool) error {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return ErrBlockedDestination
	case ip.IsUnspecified(), ip.IsMulticast(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		// Includes the cloud metadata address 169.254.169.254 and fe80::/10, also for local providers.
		return ErrBlockedDestination
	}
	for _, p := range alwaysBlocked {
		if p.Contains(ip) {
			return ErrBlockedDestination
		}
	}
	if local {
		// A local provider may only be a host inside the installation's network: loopback or private ranges.
		// A public address is not "local" whatever the flag says; such a provider must be external (https + DPA).
		if ip.IsLoopback() || ip.IsPrivate() {
			return nil
		}
		return ErrBlockedDestination
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return ErrBlockedDestination
	}
	for _, p := range externalBlocked {
		if p.Contains(ip) {
			return ErrBlockedDestination
		}
	}
	return nil
}

var alwaysBlocked = mustPrefixes(
	"0.0.0.0/8",          // "this network"
	"169.254.0.0/16",     // link-local, cloud metadata
	"192.0.0.0/24",       // IETF protocol assignments
	"224.0.0.0/4",        // multicast
	"240.0.0.0/4",        // reserved and broadcast
	"fd00:ec2::/32",      // AWS IPv6 metadata
	"64:ff9b::/96",       // NAT64: would hide an IPv4 destination
	"100.100.100.200/32", // Alibaba metadata
)

var externalBlocked = mustPrefixes(
	"100.64.0.0/10",   // carrier-grade NAT
	"198.18.0.0/15",   // benchmarking
	"192.0.2.0/24",    // documentation
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"2001:db8::/32",   // documentation
	"fc00::/7",        // unique local
	"::1/128",
)

func mustPrefixes(in ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(in))
	for i, s := range in {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Client is an HTTP client bound to one provider.
type Client struct {
	hc        *http.Client
	host      string // host:port of the provider
	scheme    string
	base      *url.URL
	maxBytes  int64
	transport *http.Transport
}

// New builds the client for one provider.
func New(p Policy) (*Client, error) {
	base, err := ValidateEndpoint(p.Endpoint, p.Local)
	if err != nil {
		return nil, err
	}
	if p.MaxResponseBytes <= 0 {
		p.MaxResponseBytes = 1 << 20
	}
	if p.Timeout <= 0 {
		p.Timeout = 60 * time.Second
	}
	resolve := p.Resolver
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	dial := p.Dialer
	if dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		dial = d.DialContext
	}
	local := p.Local
	tr := &http.Transport{
		// No environment proxy: a proxy would make the proxy, not this policy, choose the destination.
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: p.Timeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, ErrBlockedDestination
			}
			var addrs []netip.Addr
			if ip, perr := netip.ParseAddr(host); perr == nil {
				addrs = []netip.Addr{ip}
			} else if addrs, err = resolve(ctx, host); err != nil {
				return nil, fmt.Errorf("safehttp: resolve provider host: %w", err)
			}
			if len(addrs) == 0 {
				return nil, ErrBlockedDestination
			}
			// Every resolved address must be allowed: a mixed answer is refused as a whole, so a rebinding
			// answer cannot smuggle an internal address in next to a public one.
			for _, a := range addrs {
				if err := checkAddr(a, local); err != nil {
					return nil, err
				}
			}
			// Dial the validated IP itself (pinned), never the name again.
			var lastErr error
			for _, a := range addrs {
				conn, err := dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	c := &Client{host: strings.ToLower(base.Host), scheme: base.Scheme, base: base, maxBytes: p.MaxResponseBytes, transport: tr}
	c.hc = &http.Client{
		Transport: tr,
		Timeout:   p.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c, nil
}

// Close releases idle connections.
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// Do sends req. The URL must have the provider's scheme and host. It returns the body (at most MaxResponseBytes)
// and the status; a redirect response is an error.
func (c *Client) Do(req *http.Request) (status int, body []byte, err error) {
	u := req.URL
	if u == nil || strings.ToLower(u.Scheme) != c.scheme || strings.ToLower(u.Host) != c.host || u.User != nil {
		return 0, nil, ErrBlockedHost
	}
	if req.Host != "" && strings.ToLower(req.Host) != c.host {
		return 0, nil, ErrBlockedHost
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, nil, ErrRedirect
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(data)) > c.maxBytes {
		return resp.StatusCode, nil, ErrResponseTooLarge
	}
	return resp.StatusCode, data, nil
}

// URL joins a path to the provider endpoint (path only; query and host cannot be injected).
func (c *Client) URL(path string) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	return u.String()
}
