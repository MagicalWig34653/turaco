package views

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Route names the owning module's query endpoints of one resource.
type Route struct {
	FieldsPath string // GET, for example /api/v1/tickets/fields
	QueryPath  string // POST, for example /api/v1/tickets/query
}

// HTTPRunner is the Runner of the API process: it calls the owning module's own query endpoints in process, with
// the request's session cookie, so the module authenticates the follow-up call as the same User and applies its
// own permission check, row scope, field redaction, rate limit and error handling. The views platform therefore
// cannot widen what a direct POST /<resource>/query would return, and it never reads module tables.
//
// The handler is the API router below the module gate: the views platform checks the module switch itself.
type HTTPRunner struct {
	handler http.Handler
	routes  map[string]Route
}

// NewHTTPRunner builds the runner over the API router and the registered routes.
func NewHTTPRunner(handler http.Handler, routes map[string]Route) *HTTPRunner {
	return &HTTPRunner{handler: handler, routes: routes}
}

const maxRunnerResponse = 8 << 20

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}
func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.body.Len()+len(p) > maxRunnerResponse {
		r.over = true
		return len(p), nil
	}
	return r.body.Write(p)
}

func (h *HTTPRunner) call(ctx context.Context, c Caller, method, path string, body []byte) (*recorder, error) {
	u := url.URL{Scheme: "http", Host: "internal", Path: path}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build internal request: %w", err)
	}
	// Only the session credential is forwarded; the module authenticates it exactly as for a direct request.
	for _, ck := range c.Header.Values("Cookie") {
		req.Header.Add("Cookie", ck)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:0"
	rec := &recorder{header: http.Header{}}
	h.handler.ServeHTTP(rec, req)
	if rec.over {
		return nil, fmt.Errorf("internal response for %s is too large", path)
	}
	return rec, nil
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// failure turns a non-2xx module answer into a RunError (4xx) or an internal error (5xx and unreadable bodies).
func failure(rec *recorder, path string) error {
	var env errorEnvelope
	if rec.status >= 400 && rec.status < 500 && json.Unmarshal(rec.body.Bytes(), &env) == nil && env.Error.Code != "" {
		return &RunError{Status: rec.status, Code: env.Error.Code, Message: env.Error.Message}
	}
	return fmt.Errorf("internal call %s failed with status %d", path, rec.status)
}

// Fields implements Runner.
func (h *HTTPRunner) Fields(ctx context.Context, c Caller, resource string) (query.Info, error) {
	route, ok := h.routes[resource]
	if !ok {
		return query.Info{}, fmt.Errorf("views: no route for resource %q", resource)
	}
	rec, err := h.call(ctx, c, http.MethodGet, route.FieldsPath, nil)
	if err != nil {
		return query.Info{}, err
	}
	if rec.status != http.StatusOK {
		return query.Info{}, failure(rec, route.FieldsPath)
	}
	var info query.Info
	if err := json.Unmarshal(rec.body.Bytes(), &info); err != nil {
		return query.Info{}, fmt.Errorf("decode fields of %s: %w", resource, err)
	}
	return info, nil
}

type envelope struct {
	Items       json.RawMessage `json:"items"`
	NextCursor  string          `json:"nextCursor"`
	Count       *int            `json:"count"`
	CountCapped bool            `json:"countCapped"`
	Warnings    []query.Warning `json:"warnings"`
}

// Query implements Runner.
func (h *HTTPRunner) Query(ctx context.Context, c Caller, resource string, req query.Request) (Result, error) {
	route, ok := h.routes[resource]
	if !ok {
		return Result{}, fmt.Errorf("views: no route for resource %q", resource)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("encode query: %w", err)
	}
	rec, err := h.call(ctx, c, http.MethodPost, route.QueryPath, body)
	if err != nil {
		return Result{}, err
	}
	if rec.status != http.StatusOK {
		return Result{}, failure(rec, route.QueryPath)
	}
	var env envelope
	if err := json.Unmarshal(rec.body.Bytes(), &env); err != nil {
		return Result{}, fmt.Errorf("decode result of %s: %w", resource, err)
	}
	return Result{Items: env.Items, NextCursor: env.NextCursor, Count: env.Count, CountCapped: env.CountCapped, Warnings: env.Warnings}, nil
}
