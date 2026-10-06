// Package nvd is the adapter for the NVD CVE API 2.0 (https://services.nvd.nist.gov/rest/json/cves/2.0).
// It needs no account; an optional API key raises the rate limit. The client is incremental (CVEs
// modified in a time window, at most 120 days per request window), paginated, rate limited, bounded in
// request time, response size and records per run, and retries throttling and server errors with backoff.
// Its DTOs stay in this package: it returns advisories.AdvisoryRecord. Terms of use and limits: see
// docs/integrations/advisory-feeds.md (verify before production use).
package nvd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// DefaultBaseURL is the production endpoint.
const DefaultBaseURL = "https://services.nvd.nist.gov/rest/json/cves/2.0"

const (
	// MaxWindow is the longest lastMod window NVD accepts.
	MaxWindow = 120 * 24 * time.Hour
	// MinWindow is the smallest window the client splits to when a window holds more records than the bound.
	MinWindow = time.Hour

	maxResultsPerPage = 2000
	rateWindow        = 30 * time.Second
	// maxRetryAfter bounds how long the client waits on a Retry-After before it gives up.
	maxRetryAfter = 2 * time.Minute
)

// Config configures a Client. Zero values select the documented defaults.
type Config struct {
	BaseURL string
	// APIKey is sent as the apiKey header and lifts the rate limit to 50 requests per 30 s. It is never logged.
	APIKey string
	// Version is shown in the User-Agent "turaco/<version>" (default "dev").
	Version        string
	HTTPClient     *http.Client
	Clock          Clock
	RequestTimeout time.Duration // default 30 s
	MaxResponse    int64         // bytes per response, default 32 MiB
	MaxRecords     int           // records per Sync, default 2000
	PageSize       int           // resultsPerPage, default 500, at most 2000
	MaxRetries     int           // retries per request, default 3
	BackoffBase    time.Duration // default 2 s, doubled per retry
}

// Client reads NVD. It is safe for concurrent use; all requests share one rate limiter.
type Client struct {
	cfg     Config
	limiter *limiter
}

// New validates the configuration and builds a Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("nvd: invalid base URL")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("nvd: invalid API key")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = 32 << 20
	}
	if cfg.MaxRecords <= 0 {
		cfg.MaxRecords = 2000
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 500
	}
	cfg.PageSize = min(cfg.PageSize, maxResultsPerPage)
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 2 * time.Second
	}
	limit := 5
	if cfg.APIKey != "" {
		limit = 50
	}
	return &Client{cfg: cfg, limiter: newLimiter(cfg.Clock, limit, rateWindow)}, nil
}

var _ advisories.Syncer = (*Client)(nil)
var _ advisories.Provider = (*Client)(nil)

// Advisories implements advisories.Provider: the records of one bounded Sync.
func (c *Client) Advisories(ctx context.Context, since time.Time) ([]advisories.AdvisoryRecord, error) {
	res, err := c.Sync(ctx, since)
	return res.Records, err
}

// Sync reads the CVEs modified since the given time (zero: the last MaxWindow) up to the present, window
// by window. A window holding more records than the remaining bound is halved (down to MinWindow); when
// the bound is reached the result is incomplete and Through marks where the next run continues.
func (c *Client) Sync(ctx context.Context, since time.Time) (advisories.SyncResult, error) {
	end := c.cfg.Clock.Now().UTC().Truncate(time.Second)
	if since.IsZero() || since.After(end) {
		since = end.Add(-MaxWindow)
	}
	start := since.UTC().Truncate(time.Second)
	res := advisories.SyncResult{Through: start}
	span := MaxWindow
	for start.Before(end) {
		winEnd := start.Add(span)
		if winEnd.After(end) {
			winEnd = end
		}
		first, err := c.page(ctx, url.Values{"lastModStartDate": {nvdTime(start)}, "lastModEndDate": {nvdTime(winEnd)},
			"resultsPerPage": {strconv.Itoa(c.cfg.PageSize)}, "startIndex": {"0"}})
		if err != nil {
			return res, err
		}
		if first.TotalResults > c.cfg.MaxRecords-len(res.Records) {
			if len(res.Records) > 0 {
				return res, nil
			}
			if winEnd.Sub(start) <= MinWindow {
				return res, fmt.Errorf("%w: a %s window holds %d records, above the bound of %d", advisories.ErrInvalidResponse, MinWindow, first.TotalResults, c.cfg.MaxRecords)
			}
			span = max(MinWindow, winEnd.Sub(start)/2)
			continue
		}
		page, fetched := first, 0
		for {
			for _, v := range page.Vulnerabilities {
				if rec, ok := toRecord(v.CVE); ok {
					res.Records = append(res.Records, rec)
				}
			}
			fetched += len(page.Vulnerabilities)
			if len(page.Vulnerabilities) == 0 || fetched >= first.TotalResults {
				break
			}
			page, err = c.page(ctx, url.Values{"lastModStartDate": {nvdTime(start)}, "lastModEndDate": {nvdTime(winEnd)},
				"resultsPerPage": {strconv.Itoa(c.cfg.PageSize)}, "startIndex": {strconv.Itoa(fetched)}})
			if err != nil {
				return res, err
			}
		}
		start, res.Through = winEnd, winEnd
		span = MaxWindow
	}
	res.Complete = true
	return res, nil
}

// ByID reads single CVEs by id; ids that are not valid CVE ids are skipped and unknown ids return nothing.
func (c *Client) ByID(ctx context.Context, ids []string) ([]advisories.AdvisoryRecord, error) {
	var out []advisories.AdvisoryRecord
	for _, id := range ids {
		id = strings.ToUpper(strings.TrimSpace(id))
		if !cveIDPattern.MatchString(id) {
			continue
		}
		r, err := c.page(ctx, url.Values{"cveId": {id}})
		if err != nil {
			return out, err
		}
		for _, v := range r.Vulnerabilities {
			if rec, ok := toRecord(v.CVE); ok && rec.ExternalID == id {
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func nvdTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") + "+00:00" }

// page performs one rate limited request with retries and decodes the response.
func (c *Client) page(ctx context.Context, q url.Values) (response, error) {
	u := c.cfg.BaseURL + "?" + q.Encode()
	var lastErr error = advisories.ErrUnavailable
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return response{}, err
		}
		resp, retryAfter, err := c.do(ctx, u)
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, errRetry) && !errors.Is(err, advisories.ErrRateLimited) {
			return response{}, err
		}
		lastErr = err
		if attempt == c.cfg.MaxRetries {
			break
		}
		wait := c.cfg.BackoffBase << attempt
		if wait > time.Minute {
			wait = time.Minute
		}
		if retryAfter > 0 {
			if retryAfter > maxRetryAfter {
				return response{}, advisories.ErrRateLimited
			}
			wait = retryAfter
		}
		if err := c.cfg.Clock.Sleep(ctx, wait); err != nil {
			return response{}, err
		}
	}
	if errors.Is(lastErr, advisories.ErrRateLimited) {
		return response{}, advisories.ErrRateLimited
	}
	return response{}, advisories.ErrUnavailable
}

// errRetry marks a failure worth retrying (server error, network error, timeout).
var errRetry = errors.New("retry")

func (c *Client) do(ctx context.Context, u string) (response, time.Duration, error) {
	rctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return response{}, 0, advisories.ErrUnavailable
	}
	req.Header.Set("User-Agent", "turaco/"+c.cfg.Version)
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("apiKey", c.cfg.APIKey)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, 0, ctx.Err()
		}
		return response{}, 0, errRetry
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return response{}, c.retryAfter(resp), advisories.ErrRateLimited
	case resp.StatusCode >= 500:
		return response{}, c.retryAfter(resp), errRetry
	default:
		return response{}, 0, advisories.ErrUnavailable
	}
	body, err := advisories.ReadLimited(resp.Body, c.cfg.MaxResponse)
	if err != nil {
		if rctx.Err() != nil && ctx.Err() == nil {
			return response{}, 0, errRetry
		}
		return response{}, 0, err
	}
	var out response
	if err := json.Unmarshal(body, &out); err != nil || out.TotalResults < 0 || len(out.Vulnerabilities) > maxResultsPerPage {
		return response{}, 0, advisories.ErrInvalidResponse
	}
	return out, 0, nil
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
