// Package microsoft is the shared HTTP foundation for calls to Microsoft cloud hosts (Entra ID, later Graph and Teams;
// docs/product/f15-microsoft-integration-design.md, slice M-0). It enforces what every Microsoft call needs: https
// only, a per-feature host allow-list checked before anything is sent, no redirects, response size and time caps, an
// explicit optional proxy and CA file, and a dial-time address check when no proxy is used. It never logs tokens,
// bodies or query strings.
package microsoft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrHostNotAllowed is returned before any I/O when the URL is not https or its host is not on the allow-list.
	ErrHostNotAllowed = errors.New("microsoft: host is not allowed")
	// ErrResponseTooLarge is returned when a response exceeds the size cap.
	ErrResponseTooLarge = errors.New("microsoft: response too large")
)

const (
	defaultTimeout  = 15 * time.Second
	defaultMaxBytes = 1 << 20
)

// TransientError marks failures worth retrying: network errors, 5xx and 429.
type TransientError struct {
	Status     int
	RetryAfter time.Duration
	Err        error
}

func (e *TransientError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("microsoft: transient failure: %v", e.Err)
	}
	return fmt.Sprintf("microsoft: transient failure: status %d", e.Status)
}

func (e *TransientError) Unwrap() error { return e.Err }

// Config configures a Client.
type Config struct {
	// AllowedHosts lists the host names (no port) the client may call, for example login.microsoftonline.com.
	AllowedHosts []string
	// HTTPProxy is an explicit proxy URL (HTTP CONNECT). Environment proxy variables are never honoured.
	HTTPProxy string
	// CAFile adds CA certificates to the system pool (TLS-inspecting proxies).
	CAFile string
	// Timeout bounds one request; MaxBodyBytes bounds one response.
	Timeout      time.Duration
	MaxBodyBytes int64
	// Transport replaces the network layer and AllowInsecureHTTP permits http URLs. Both are for tests with a local
	// fake server only; production code never sets them.
	Transport         http.RoundTripper
	AllowInsecureHTTP bool
}

// Client calls allow-listed Microsoft hosts.
type Client struct {
	http     *http.Client
	allowed  map[string]struct{}
	maxBytes int64
	insecure bool
}

// New builds a Client.
func New(cfg Config) (*Client, error) {
	if len(cfg.AllowedHosts) == 0 {
		return nil, errors.New("microsoft: at least one allowed host is required")
	}
	c := &Client{allowed: map[string]struct{}{}, maxBytes: cfg.MaxBodyBytes, insecure: cfg.AllowInsecureHTTP}
	for _, h := range cfg.AllowedHosts {
		c.allowed[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	if c.maxBytes <= 0 {
		c.maxBytes = defaultMaxBytes
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	rt := cfg.Transport
	if rt == nil {
		t, err := transport(cfg)
		if err != nil {
			return nil, err
		}
		rt = t
	}
	c.http = &http.Client{
		Transport: rt,
		Timeout:   timeout,
		// A redirect is never followed: a response that redirects is a failure.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

func transport(cfg Config) (*http.Transport, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read MICROSOFT_CA_FILE: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("MICROSOFT_CA_FILE holds no PEM certificate")
		}
		tlsCfg.RootCAs = pool
	}
	t := &http.Transport{
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if cfg.HTTPProxy != "" {
		u, err := url.Parse(cfg.HTTPProxy)
		if err != nil {
			return nil, fmt.Errorf("parse MICROSOFT_HTTP_PROXY: %w", err)
		}
		t.Proxy = http.ProxyURL(u)
		return t, nil
	}
	// Without a proxy the address that is actually dialled must be a public one (DNS rebinding and allow-list
	// bypass by resolver tricks); with a proxy the proxy resolves.
	d := &net.Dialer{Timeout: 10 * time.Second, Control: publicAddressOnly}
	t.DialContext = d.DialContext
	return t, nil
}

// publicAddressOnly rejects loopback, private, link-local and unspecified destinations at connect time.
func publicAddressOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !publicIP(ip) {
		return fmt.Errorf("%w: destination address is not public", ErrHostNotAllowed)
	}
	return nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

// CheckURL applies the https and host allow-list rules without sending anything.
func (c *Client) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, ErrHostNotAllowed
	}
	if u.Scheme != "https" && !(c.insecure && u.Scheme == "http") {
		return nil, ErrHostNotAllowed
	}
	host := strings.ToLower(u.Hostname())
	if c.insecure {
		host = strings.ToLower(u.Host)
	}
	if _, ok := c.allowed[host]; !ok {
		return nil, ErrHostNotAllowed
	}
	return u, nil
}

// Response is a completed call. Bodies are capped; headers are returned for Retry-After style handling.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// Do sends one request. A 429 or 5xx answer and network failures are returned as *TransientError; other statuses
// are returned to the caller, who decides (for example 400 and 401 from the token endpoint).
func (c *Client) Do(ctx context.Context, method, rawURL string, body io.Reader, header http.Header) (Response, error) {
	u, err := c.CheckURL(rawURL)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return Response{}, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrHostNotAllowed) {
			return Response{}, ErrHostNotAllowed
		}
		return Response{}, &TransientError{Err: sanitize(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return Response{}, &TransientError{Err: sanitize(err)}
	}
	if int64(len(data)) > c.maxBytes {
		return Response{}, ErrResponseTooLarge
	}
	out := Response{Status: resp.StatusCode, Body: data, Header: resp.Header}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return out, &TransientError{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header)}
	}
	return out, nil
}

// sanitize drops the URL (which can carry query values) from a transport error.
func sanitize(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		// The HTTP-date form is allowed by RFC 9110.
		d = time.Until(t)
	}
	if d <= 0 {
		return 0
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}
