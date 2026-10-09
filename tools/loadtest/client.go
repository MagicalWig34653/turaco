package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "turaco_session"

// cookieNames are the session cookie names: the secure variant carries the __Host- prefix.
var cookieNames = []string{sessionCookie, "__Host-" + sessionCookie}

// Persona is one simulated login.
type Persona struct {
	Login    string
	Class    string
	Password string
}

// Session is a logged-in persona. The cookie is kept by hand so that one
// shared transport (bounded connection pool) serves every user.
type Session struct {
	P Persona

	mu          sync.Mutex
	cookie      string
	cookieName  string
	userID      string
	permissions map[string]bool
	loginAt     time.Time
	loginMu     sync.Mutex // serializes re-login of this session
}

// UserID is the user id reported by /auth/session.
func (s *Session) UserID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID
}

// Has reports whether the session has a permission.
func (s *Session) Has(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissions[p]
}

func (s *Session) snapshot() (cookie string, loginAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cookie, s.loginAt
}

func (s *Session) cookieHeader() *http.Cookie {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie == "" {
		return nil
	}
	name := s.cookieName
	if name == "" {
		name = sessionCookie
	}
	return &http.Cookie{Name: name, Value: s.cookie}
}

// Client talks to the API through one shared HTTP transport.
type Client struct {
	base      string // http://host:port
	origin    string
	http      *http.Client
	timeout   time.Duration
	rec       *Recorder
	loginSem  chan struct{}
	maxAge    time.Duration
	reloginMu sync.Mutex
	logins    uint64
	relogins  uint64
	statMu    sync.Mutex
}

// NewClient creates the client; maxConns bounds the connection pool.
func NewClient(base string, maxConns int, timeout time.Duration, rec *Recorder) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid --base-url %q", base)
	}
	origin := u.Scheme + "://" + u.Host
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          maxConns,
		MaxIdleConnsPerHost:   maxConns,
		MaxConnsPerHost:       maxConns,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: timeout,
	}
	return &Client{
		base:     strings.TrimRight(origin+strings.TrimRight(u.Path, "/"), "/"),
		origin:   origin,
		http:     &http.Client{Transport: tr, Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		timeout:  timeout,
		rec:      rec,
		loginSem: make(chan struct{}, 4),
		maxAge:   55 * time.Minute,
	}, nil
}

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Result is the outcome of one API call.
type Result struct {
	Status  int
	Code    string // error.code of an error envelope
	Err     error
	Class   Class
	Body    []byte
	Latency time.Duration
	// Missing is a 404 without an error envelope: the route does not exist in this API build.
	Missing bool
}

// OK reports a 2xx answer.
func (r Result) OK() bool { return r.Err == nil && r.Status >= 200 && r.Status < 300 }

// JobCtx carries the schedule information of one arrival into the calls it makes.
// Only the first call is measured from the scheduled start (coordinated
// omission); later calls of the same operation start when the previous one ended.
type JobCtx struct {
	Stage int
	Sched time.Time
}

func (j *JobCtx) take() time.Time {
	if j == nil || j.Sched.IsZero() {
		return time.Time{}
	}
	t := j.Sched
	j.Sched = time.Time{}
	return t
}

func (j *JobCtx) stage() int {
	if j == nil {
		return -1
	}
	return j.Stage
}

// Call performs one measured request. out may be nil. A 401 triggers one
// re-login and one retry; the 401 itself is recorded as class reauth.
func (c *Client) Call(ctx context.Context, jc *JobCtx, s *Session, op, method, path string, body, out any) Result {
	sched := jc.take()
	r := c.do(ctx, jc.stage(), s, op, method, path, body, out, sched)
	if r.Status == http.StatusUnauthorized && s != nil {
		if err := c.Login(ctx, s, true); err == nil {
			return c.do(ctx, jc.stage(), s, op, method, path, body, out, time.Time{})
		}
	}
	return r
}

func (c *Client) do(ctx context.Context, stage int, s *Session, op, method, path string, body, out any, sched time.Time) Result {
	start := time.Now()
	if sched.IsZero() {
		sched = start
	}
	res := c.raw(ctx, s, method, path, body)
	end := time.Now()
	if res.OK() && out != nil && len(res.Body) > 0 {
		if err := json.Unmarshal(res.Body, out); err != nil {
			res.Err = fmt.Errorf("decode %s: %w", op, err)
			res.Class = ClassInvalid
		}
	}
	c.rec.Add(Sample{Stage: stage, Op: op, Response: end.Sub(sched), Service: end.Sub(start), Class: res.Class, Status: res.Status, Bytes: len(res.Body), Missing: res.Missing})
	res.Latency = end.Sub(sched)
	return res
}

func (c *Client) raw(ctx context.Context, s *Session, method, path string, body any) Result {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Result{Err: err, Class: ClassInvalid}
		}
		rd = bytes.NewReader(b)
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return Result{Err: err, Class: ClassInvalid}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet && method != http.MethodHead {
		// Same-origin rule (platform.csrf_rejected): the browser would send these.
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if s != nil {
		if ck := s.cookieHeader(); ck != nil {
			req.AddCookie(ck)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if rctx.Err() != nil || IsTimeout(err) {
			err = fmt.Errorf("timeout: %w", err)
		}
		return Result{Err: err, Class: ClassNetwork}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		if rctx.Err() != nil || IsTimeout(err) {
			err = fmt.Errorf("timeout: %w", err)
		}
		return Result{Status: resp.StatusCode, Err: err, Class: ClassNetwork}
	}
	r := Result{Status: resp.StatusCode, Body: b, Class: ClassifyStatus(resp.StatusCode, nil)}
	if resp.StatusCode >= 400 {
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil {
			r.Code = env.Error.Code
		}
		r.Missing = resp.StatusCode == http.StatusNotFound && r.Code == ""
	}
	return r
}

// Login signs the persona in (emergency login) and loads the session facts.
// force re-logs in even when a recent session exists (after a 401).
func (c *Client) Login(ctx context.Context, s *Session, force bool) error {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cookie, at := s.snapshot()
	if cookie != "" && !at.IsZero() {
		if !force && time.Since(at) < c.maxAge {
			return nil
		}
		// Another goroutine may have re-logged in while this one waited for the lock.
		if force && time.Since(at) < 2*time.Second {
			return nil
		}
	}
	select {
	case c.loginSem <- struct{}{}:
		defer func() { <-c.loginSem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		start := time.Now()
		sched := start
		rctx, cancel := context.WithTimeout(ctx, c.timeout)
		b, _ := json.Marshal(map[string]string{"login": s.P.Login, "password": s.P.Password})
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, c.base+"/api/v1/auth/emergency-login", bytes.NewReader(b))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			c.rec.Add(Sample{Stage: -1, Op: "auth.login", Response: time.Since(sched), Service: time.Since(start), Class: ClassNetwork})
			lastErr = err
			if !sleepCtx(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		class := ClassifyStatus(resp.StatusCode, nil)
		c.rec.Add(Sample{Stage: -1, Op: "auth.login", Response: time.Since(sched), Service: time.Since(start), Class: class, Status: resp.StatusCode, Bytes: len(bodyBytes)})
		switch {
		case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK:
			var val, name string
			for _, ck := range resp.Cookies() {
				for _, n := range cookieNames {
					if ck.Name == n && ck.Value != "" {
						val, name = ck.Value, ck.Name
					}
				}
			}
			if val == "" {
				return errors.New("login answered success without a session cookie")
			}
			s.mu.Lock()
			s.cookie, s.cookieName, s.loginAt = val, name, time.Now()
			s.mu.Unlock()
			if err := c.loadSession(ctx, s); err != nil {
				return err
			}
			c.statMu.Lock()
			if force {
				c.relogins++
			} else {
				c.logins++
			}
			c.statMu.Unlock()
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
			wait := time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(ra) * time.Second
			}
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			lastErr = fmt.Errorf("login throttled (%d), retry after %s", resp.StatusCode, wait)
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
		case resp.StatusCode == http.StatusNotFound:
			return errors.New("emergency login is disabled (404): start the API with AUTH_EMERGENCY_LOGIN_ENABLED=true")
		case resp.StatusCode == http.StatusUnauthorized:
			return fmt.Errorf("login %s: invalid credentials (is the hospital simulation seeded?)", s.P.Login)
		case resp.StatusCode == http.StatusForbidden:
			return errors.New("login rejected by the same-origin rule (403): --base-url must equal the origin the API sees")
		default:
			return fmt.Errorf("login %s: unexpected status %d", s.P.Login, resp.StatusCode)
		}
	}
	return fmt.Errorf("login %s: %w", s.P.Login, lastErr)
}

func (c *Client) loadSession(ctx context.Context, s *Session) error {
	var sess struct {
		UserID      string   `json:"userId"`
		Permissions []string `json:"permissions"`
	}
	r := c.do(ctx, -1, s, "auth.session", http.MethodGet, "/auth/session", nil, &sess, time.Time{})
	if !r.OK() {
		return fmt.Errorf("session of %s: status %d %v", s.P.Login, r.Status, r.Err)
	}
	perms := map[string]bool{}
	for _, p := range sess.Permissions {
		perms[p] = true
	}
	s.mu.Lock()
	s.userID, s.permissions = sess.UserID, perms
	s.mu.Unlock()
	return nil
}

// LoginCounts returns initial logins and re-logins.
func (c *Client) LoginCounts() (logins, relogins uint64) {
	c.statMu.Lock()
	defer c.statMu.Unlock()
	return c.logins, c.relogins
}

// RawGet fetches an unauthenticated path (for /meta) without recording.
func (c *Client) RawGet(ctx context.Context, path string, out any) error {
	r := c.raw(ctx, nil, http.MethodGet, path, nil)
	if !r.OK() {
		return fmt.Errorf("GET %s: status %d %v", path, r.Status, r.Err)
	}
	return json.Unmarshal(r.Body, out)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
