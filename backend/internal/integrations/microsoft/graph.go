package microsoft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

const (
	// GraphBaseURL and AuthorityBaseURL are the global-cloud endpoints. The sovereign clouds are not built.
	GraphBaseURL     = "https://graph.microsoft.com"
	AuthorityBaseURL = "https://login.microsoftonline.com"

	graphScope           = "https://graph.microsoft.com/.default"
	defaultGraphMaxBytes = 8 << 20
	defaultMaxAttempts   = 4
	defaultMaxInlineWait = 60 * time.Second
	defaultMaxPages      = 2000
	defaultMaxItems      = 200000
)

// ErrNextLinkRejected is returned when an @odata.nextLink does not point to the Graph host of this client.
var ErrNextLinkRejected = errors.New("microsoft: next link is not on the Graph host")

// ErrTooManyResults is returned when a collection exceeds the page or item cap.
var ErrTooManyResults = errors.New("microsoft: collection exceeds the configured cap")

// GraphError is a non-success answer of Graph (4xx). It carries the status, the Graph error code (for example
// Authorization_RequestDenied) and the request id for support; never the message or body, which can echo tenant data.
type GraphError struct {
	Status    int
	Code      string
	RequestID string
}

func (e *GraphError) Error() string {
	s := fmt.Sprintf("microsoft: graph request failed: status %d", e.Status)
	if e.Code != "" {
		s += " code " + e.Code
	}
	if e.RequestID != "" {
		s += " request-id " + e.RequestID
	}
	return s
}

// IsGraphStatus reports whether err is a *GraphError with the given status.
func IsGraphStatus(err error, status int) bool {
	var ge *GraphError
	return errors.As(err, &ge) && ge.Status == status
}

// GraphConfig configures a Graph client for one app registration (the read registration and the write registration
// are separate clients).
type GraphConfig struct {
	TenantID   string
	ClientID   string
	Credential Credential

	HTTPProxy string
	CAFile    string
	Timeout   time.Duration
	// MaxBodyBytes bounds one response page (default 8 MiB).
	MaxBodyBytes int64
	// MaxPages and MaxItems bound one List call.
	MaxPages int
	MaxItems int
	// MaxAttempts bounds attempts per request (429, 5xx and network errors are retried); MaxInlineWait is the longest
	// Retry-After honoured inside the call. A longer wait is returned as *TransientError for the job runner.
	MaxAttempts   int
	MaxInlineWait time.Duration

	// BaseURL and AuthorityBase default to the global cloud. Tests point them at a local server together with
	// Transport and AllowInsecureHTTP; production code never sets them.
	BaseURL           string
	AuthorityBase     string
	Transport         http.RoundTripper
	AllowInsecureHTTP bool
	// Now and Sleep are test seams.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Graph is a Microsoft Graph client with application (client credentials) authentication. It is shared by the
// Intune read client, the Intune assignment writer and later Entra/Teams features, each with its own registration.
type Graph struct {
	client      *Client
	tokens      *TokenSource
	base        *url.URL
	tracker     *providerstatus.Tracker
	maxPages    int
	maxItems    int
	maxAttempts int
	maxWait     time.Duration
	sleep       func(ctx context.Context, d time.Duration) error
	cred        Credential
}

// NewGraph builds a client. It sends nothing.
func NewGraph(cfg GraphConfig) (*Graph, error) {
	if cfg.TenantID == "" || cfg.ClientID == "" || cfg.Credential == nil {
		return nil, errors.New("microsoft: graph tenant id, client id and credential are required")
	}
	base, authority := cfg.BaseURL, cfg.AuthorityBase
	if base == "" {
		base = GraphBaseURL
	}
	if authority == "" {
		authority = AuthorityBaseURL
	}
	bu, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || bu.Host == "" {
		return nil, errors.New("microsoft: invalid graph base url")
	}
	au, err := url.Parse(authority)
	if err != nil || au.Host == "" {
		return nil, errors.New("microsoft: invalid authority base url")
	}
	hostOf := func(u *url.URL) string {
		if cfg.AllowInsecureHTTP {
			return u.Host
		}
		return u.Hostname()
	}
	c, err := New(Config{
		AllowedHosts: []string{hostOf(bu), hostOf(au)}, HTTPProxy: cfg.HTTPProxy, CAFile: cfg.CAFile, Timeout: cfg.Timeout,
		MaxBodyBytes: orInt64(cfg.MaxBodyBytes, defaultGraphMaxBytes), Transport: cfg.Transport, AllowInsecureHTTP: cfg.AllowInsecureHTTP,
	})
	if err != nil {
		return nil, err
	}
	g := &Graph{
		client: c, base: bu, cred: cfg.Credential, tracker: providerstatus.NewTracker(cfg.Now),
		tokens:   NewTokenSource(c, authority, cfg.TenantID, cfg.ClientID, graphScope, cfg.Credential, cfg.Now),
		maxPages: orInt(cfg.MaxPages, defaultMaxPages), maxItems: orInt(cfg.MaxItems, defaultMaxItems),
		maxAttempts: orInt(cfg.MaxAttempts, defaultMaxAttempts), maxWait: cfg.MaxInlineWait, sleep: cfg.Sleep,
	}
	if g.maxWait <= 0 {
		g.maxWait = defaultMaxInlineWait
	}
	if g.sleep == nil {
		g.sleep = sleepContext
	}
	return g, nil
}

func orInt(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func orInt64(v, d int64) int64 {
	if v > 0 {
		return v
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status implements providerstatus.Reporter: unverified until a Graph call succeeded.
func (g *Graph) Status() providerstatus.Snapshot { return g.tracker.Status() }

// Credential exposes the credential for expiry reporting.
func (g *Graph) Credential() Credential { return g.cred }

// Request is one Graph call. Target is a path with query starting with "/" (for example
// "/v1.0/deviceManagement/managedDevices?$top=100") or, for paging, an absolute @odata.nextLink.
type Request struct {
	Method string
	Target string
	// Body is marshalled as JSON when not nil.
	Body any
	// Header adds request headers (for example ConsistencyLevel: eventual).
	Header http.Header
}

// Call sends one request and decodes a JSON answer into out (when not nil). Non-2xx answers are *GraphError,
// exhausted retries *TransientError, refused credentials *AuthError. It returns the HTTP status.
func (g *Graph) Call(ctx context.Context, r Request, out any) (int, error) {
	var payload []byte
	if r.Body != nil {
		b, err := json.Marshal(r.Body)
		if err != nil {
			return 0, err
		}
		payload = b
	}
	resp, err := g.send(ctx, r, payload)
	if err != nil {
		g.tracker.Failure(failureCode(err))
		return 0, err
	}
	if resp.Status < 200 || resp.Status > 299 {
		ge := &GraphError{Status: resp.Status, RequestID: safeRequestID(resp.Header.Get("request-id"))}
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(resp.Body, &e) == nil {
			ge.Code = providerstatus.SafeCode(e.Error.Code)
			if e.Error.Code == "" {
				ge.Code = ""
			}
		}
		// A 404 on a looked-up resource is an answer, not a malfunction; it still proves credentials and reachability.
		if resp.Status == http.StatusNotFound {
			g.tracker.Success()
		} else {
			g.tracker.Failure(fmt.Sprintf("http_%d", resp.Status))
		}
		return resp.Status, ge
	}
	g.tracker.Success()
	if out != nil && len(bytes.TrimSpace(resp.Body)) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp.Status, errors.New("microsoft: graph answer is not valid json")
		}
	}
	return resp.Status, nil
}

// Get is Call with GET.
func (g *Graph) Get(ctx context.Context, target string, out any) error {
	_, err := g.Call(ctx, Request{Method: http.MethodGet, Target: target}, out)
	return err
}

// List reads a collection, following @odata.nextLink (restricted to the Graph host of this client) and calling
// visit for every element of "value". Unknown fields are the caller's to ignore.
func (g *Graph) List(ctx context.Context, target string, header http.Header, visit func(json.RawMessage) error) error {
	items := 0
	next := target
	for page := 0; next != ""; page++ {
		if page >= g.maxPages {
			return ErrTooManyResults
		}
		var body struct {
			Value []json.RawMessage `json:"value"`
			Next  string            `json:"@odata.nextLink"`
		}
		if _, err := g.Call(ctx, Request{Method: http.MethodGet, Target: next, Header: header}, &body); err != nil {
			return err
		}
		for _, v := range body.Value {
			items++
			if items > g.maxItems {
				return ErrTooManyResults
			}
			if err := visit(v); err != nil {
				return err
			}
		}
		next = body.Next
	}
	return nil
}

// resolve turns a path target into a URL and validates absolute nextLinks.
func (g *Graph) resolve(target string) (string, error) {
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return g.base.String() + target, nil
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || !strings.EqualFold(u.Scheme, g.base.Scheme) || !strings.EqualFold(u.Host, g.base.Host) {
		return "", ErrNextLinkRejected
	}
	return u.String(), nil
}

func (g *Graph) send(ctx context.Context, r Request, payload []byte) (Response, error) {
	full, err := g.resolve(r.Target)
	if err != nil {
		return Response{}, err
	}
	refreshed, skipWait := false, false
	var lastErr error
	for attempt := 0; attempt < g.maxAttempts; attempt++ {
		if attempt > 0 && !skipWait {
			if err := g.wait(ctx, lastErr, attempt); err != nil {
				return Response{}, err
			}
		}
		skipWait = false
		token, err := g.tokens.Token(ctx)
		if err != nil {
			if isTransient(err) {
				lastErr = err
				continue
			}
			return Response{}, err
		}
		header := http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}}
		if payload != nil {
			header.Set("Content-Type", "application/json")
		}
		for k, v := range r.Header {
			header[k] = v
		}
		var body *bytes.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		var resp Response
		if body != nil {
			resp, err = g.client.Do(ctx, r.Method, full, body, header)
		} else {
			resp, err = g.client.Do(ctx, r.Method, full, nil, header)
		}
		if err != nil {
			if isTransient(err) {
				lastErr = err
				if !safeToRetry(r.Method, err) {
					// An ambiguous failure of a non-idempotent write (timeout, 500, 502, 504) may have been
					// committed: the caller rereads the target and reconciles before it writes again.
					return Response{}, err
				}
				continue
			}
			return Response{}, err
		}
		if resp.Status == http.StatusUnauthorized && !refreshed {
			// The cached token may have been revoked or the permissions changed; ask once more.
			g.tokens.Invalidate()
			refreshed, skipWait = true, true
			attempt--
			continue
		}
		return resp, nil
	}
	return Response{}, lastErr
}

// safeToRetry reports whether the request may be sent again after err. Reads, PUT and DELETE are idempotent. POST and
// PATCH are retried only after an explicit throttle answer (429, 503), where the service states it did not process
// the request; a lost response or a 500, 502 or 504 is ambiguous.
func safeToRetry(method string, err error) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	var te *TransientError
	return errors.As(err, &te) && (te.Status == http.StatusTooManyRequests || te.Status == http.StatusServiceUnavailable)
}

func isTransient(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}

// wait sleeps before the next attempt: the Retry-After of a 429/503 (graph throttling guidance: wait the
// advertised time, https://learn.microsoft.com/en-us/graph/throttling, read 2026-10-10) or an exponential back-off.
// A Retry-After longer than MaxInlineWait ends the call with the transient error so the job runner retries later.
func (g *Graph) wait(ctx context.Context, last error, attempt int) error {
	d := time.Second << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	var te *TransientError
	if errors.As(last, &te) && te.RetryAfter > 0 {
		d = te.RetryAfter
	}
	if d > g.maxWait {
		return last
	}
	return g.sleep(ctx, d)
}

func failureCode(err error) string {
	var ae *AuthError
	var te *TransientError
	switch {
	case errors.As(err, &ae):
		return "auth_" + ae.Code
	case errors.As(err, &te):
		if te.Status > 0 {
			return fmt.Sprintf("http_%d", te.Status)
		}
		return "network"
	case errors.Is(err, ErrHostNotAllowed), errors.Is(err, ErrNextLinkRejected):
		return "host_not_allowed"
	case errors.Is(err, ErrResponseTooLarge):
		return "response_too_large"
	}
	return "error"
}

func safeRequestID(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c == '-') {
			return ""
		}
	}
	return s
}
