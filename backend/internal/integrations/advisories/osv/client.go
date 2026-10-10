// Package osv is the adapter for the OSV.dev API (https://google.github.io/osv.dev/api/). It needs no
// account and sends no credentials. OSV has no "modified since" query, so the client is query based: it
// asks POST /v1/query for the vulnerabilities of configured packages (ecosystem and name, all versions,
// following next_page_token) and GET /v1/vulns/{id} for single records. It is bounded in request time,
// response size, pages and records per run and retries throttling and server errors with backoff.
// Its DTOs stay in this package: it returns advisories.AdvisoryRecord. Terms of use and limits: see
// docs/integrations/advisory-feeds.md (verify before production use).
package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// DefaultBaseURL is the production endpoint.
const DefaultBaseURL = "https://api.osv.dev"

const (
	// maxRetryAfter bounds how long the client waits on a Retry-After before it gives up.
	maxRetryAfter = 2 * time.Minute
	// maxPagesPerPackage bounds the next_page_token loop of one package.
	maxPagesPerPackage = 20
	maxQueryBody       = 1 << 10
)

// Config configures a Client. Zero values select the documented defaults.
type Config struct {
	BaseURL string
	// Version is shown in the User-Agent "turaco/<version>" (default "dev").
	Version string
	// AllowInsecureHTTP permits a plain http base URL (tests with httptest only).
	AllowInsecureHTTP bool
	HTTPClient        *http.Client
	Clock             Clock
	RequestTimeout    time.Duration // default 30 s
	MaxResponse       int64         // bytes per response, default 16 MiB
	MaxRecords        int           // records per run, default 2000
	MaxRetries        int           // retries per request, default 3
	BackoffBase       time.Duration // default 2 s, doubled per retry
}

// Client reads OSV. It is safe for concurrent use.
type Client struct {
	cfg Config
}

// New validates the configuration and builds a Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("osv: invalid base URL")
	}
	if u.Scheme != "https" && !cfg.AllowInsecureHTTP {
		return nil, errors.New("osv: the base URL must use https")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		cp := *cfg.HTTPClient
		hc = &cp
	}
	hc.CheckRedirect = advisories.SameHostHTTPS
	cfg.HTTPClient = hc
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = 16 << 20
	}
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = 2000
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 2 * time.Second
	}
	return &Client{cfg: cfg}, nil
}

var _ advisories.PackageSource = (*Client)(nil)

// Packages reads the vulnerabilities of the given packages, package by package. Records are deduplicated
// by id. When the per-run bound is reached the result is incomplete; on an error it holds the records read
// so far. A package with an invalid ecosystem or name is skipped.
func (c *Client) Packages(ctx context.Context, pkgs []advisories.PackageQuery) (advisories.SyncResult, error) {
	var res advisories.SyncResult
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !validPackage(p) {
			continue
		}
		token := ""
		for page := 0; ; page++ {
			if page == maxPagesPerPackage {
				return res, nil
			}
			out, err := c.query(ctx, p, token)
			if err != nil {
				return res, err
			}
			for _, v := range out.Vulns {
				rec, ok := toRecord(v)
				if !ok || seen[rec.ExternalID] {
					continue
				}
				if len(res.Records) == c.cfg.MaxRecords {
					return res, nil
				}
				seen[rec.ExternalID] = true
				res.Records = append(res.Records, rec)
			}
			token = out.NextPageToken
			if token == "" || len(out.Vulns) == 0 {
				break
			}
		}
	}
	res.Complete = true
	return res, nil
}

// ByID reads single records by OSV id; ids with unusual characters are skipped and unknown ids return nothing.
func (c *Client) ByID(ctx context.Context, ids []string) ([]advisories.AdvisoryRecord, error) {
	var out []advisories.AdvisoryRecord
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !idPattern.MatchString(id) {
			continue
		}
		body, status, err := c.send(ctx, http.MethodGet, c.cfg.BaseURL+"/v1/vulns/"+url.PathEscape(id), nil)
		if err != nil {
			return out, err
		}
		if status == http.StatusNotFound {
			continue
		}
		var v vuln
		if err := json.Unmarshal(body, &v); err != nil {
			return out, advisories.ErrInvalidResponse
		}
		if rec, ok := toRecord(v); ok && rec.ExternalID == id {
			out = append(out, rec)
		}
	}
	return out, nil
}

type queryRequest struct {
	Package   queryPackage `json:"package"`
	PageToken string       `json:"page_token,omitempty"`
}

type queryPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

func (c *Client) query(ctx context.Context, p advisories.PackageQuery, token string) (queryResponse, error) {
	body, err := json.Marshal(queryRequest{Package: queryPackage{Name: p.Name, Ecosystem: p.Ecosystem}, PageToken: token})
	if err != nil || len(body) > maxQueryBody {
		return queryResponse{}, advisories.ErrInvalidResponse
	}
	raw, status, err := c.send(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/query", body)
	if err != nil {
		return queryResponse{}, err
	}
	if status == http.StatusNotFound {
		return queryResponse{}, nil
	}
	var out queryResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return queryResponse{}, advisories.ErrInvalidResponse
	}
	return out, nil
}

// errRetry marks a failure worth retrying (server error, network error, timeout).
var errRetry = errors.New("retry")

// send performs one request with retries. It returns the body of a 200 response, or status 404 with an
// empty body; every other outcome is an error.
func (c *Client) send(ctx context.Context, method, u string, payload []byte) ([]byte, int, error) {
	var lastErr error = advisories.ErrUnavailable
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		body, status, retryAfter, err := c.do(ctx, method, u, payload)
		if err == nil {
			return body, status, nil
		}
		if !errors.Is(err, errRetry) && !errors.Is(err, advisories.ErrRateLimited) {
			return nil, 0, err
		}
		lastErr = err
		if attempt == c.cfg.MaxRetries {
			break
		}
		wait := min(c.cfg.BackoffBase<<attempt, time.Minute)
		if retryAfter > 0 {
			if retryAfter > maxRetryAfter {
				return nil, 0, advisories.ErrRateLimited
			}
			wait = retryAfter
		}
		if err := c.cfg.Clock.Sleep(ctx, wait); err != nil {
			return nil, 0, err
		}
	}
	if errors.Is(lastErr, advisories.ErrRateLimited) {
		return nil, 0, advisories.ErrRateLimited
	}
	return nil, 0, advisories.ErrUnavailable
}

func (c *Client) do(ctx context.Context, method, u string, payload []byte) ([]byte, int, time.Duration, error) {
	rctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	var rd *bytes.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	var req *http.Request
	var err error
	if rd != nil {
		req, err = http.NewRequestWithContext(rctx, method, u, rd)
	} else {
		req, err = http.NewRequestWithContext(rctx, method, u, nil)
	}
	if err != nil {
		return nil, 0, 0, advisories.ErrUnavailable
	}
	req.Header.Set("User-Agent", "turaco/"+c.cfg.Version)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, 0, ctx.Err()
		}
		return nil, 0, 0, errRetry
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, http.StatusNotFound, 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, 0, c.retryAfter(resp), advisories.ErrRateLimited
	case resp.StatusCode >= 500:
		return nil, 0, c.retryAfter(resp), errRetry
	default:
		return nil, 0, 0, advisories.ErrUnavailable
	}
	body, err := advisories.ReadLimited(resp.Body, c.cfg.MaxResponse)
	if err != nil {
		if rctx.Err() != nil && ctx.Err() == nil {
			return nil, 0, 0, errRetry
		}
		return nil, 0, 0, err
	}
	return body, http.StatusOK, 0, nil
}

// retryAfter reads a Retry-After header in seconds or as an HTTP date; 0 when absent or invalid.
func (c *Client) retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(c.cfg.Clock.Now()); d > 0 {
			return d
		}
	}
	return 0
}
