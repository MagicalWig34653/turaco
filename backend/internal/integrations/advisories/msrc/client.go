// Package msrc reads Microsoft security updates from the public MSRC CVRF API v3.0 (no account, no key):
//
//	GET {base}/updates          the monthly security update documents (id, release dates, CvrfUrl)
//	GET {base}/cvrf/{id}        one CVRF document as JSON (Accept: application/json)
//
// Documentation: https://github.com/microsoft/MSRC-Microsoft-Security-Updates-API and
// https://api.msrc.microsoft.com/cvrf/v3.0/swagger (read 2026-10-10). Implemented from the documentation and
// not yet verified against the live service: the field names below follow the published CVRF 1.2 JSON shape
// and every field is optional. Mapping, bounds and validation are conservative; unknown fields are ignored.
package msrc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://api.msrc.microsoft.com/cvrf/v3.0"

// SourceKey is the advisory source key.
const SourceKey = "msrc"

const (
	maxDocsPerRun   = 3
	maxVulnsPerDoc  = 6000
	maxProductsEach = 40
	maxTitle        = 300
	maxSummary      = 4000
	defaultMaxBody  = 64 << 20 // a monthly document is large (tens of MiB)
)

var (
	cveIDPattern  = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,12}$`)
	docIDPattern  = regexp.MustCompile(`^[0-9]{4}-[A-Za-z]{3}$`)
	buildPattern  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,4}$`)
	productIDText = regexp.MustCompile(`^[0-9A-Za-z._-]{1,40}$`)
)

// Config configures a Client. Zero values select the defaults.
type Config struct {
	BaseURL        string
	Version        string // User-Agent "turaco/<version>", default "dev"
	HTTPClient     *http.Client
	RequestTimeout time.Duration // default 120 s (large documents)
	MaxResponse    int64         // bytes, default 64 MiB
	Now            func() time.Time
}

// Client reads the MSRC CVRF API.
type Client struct{ cfg Config }

var _ advisories.Syncer = (*Client)(nil)

// New validates the configuration.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if u, err := url.Parse(cfg.BaseURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("msrc: invalid base URL")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
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
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 120 * time.Second
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = defaultMaxBody
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg}, nil
}

type updateList struct {
	Value []struct {
		ID                 string `json:"ID"`
		InitialReleaseDate string `json:"InitialReleaseDate"`
		CurrentReleaseDate string `json:"CurrentReleaseDate"`
	} `json:"value"`
}

// Sync reads the monthly documents released or revised since the given time (zero: the latest document). At
// most maxDocsPerRun documents are read per run, oldest first; Through is the CurrentReleaseDate of the last
// document read completely and Complete is false while newer documents remain.
func (c *Client) Sync(ctx context.Context, since time.Time) (advisories.SyncResult, error) {
	var list updateList
	if err := c.get(ctx, "/updates", &list); err != nil {
		return advisories.SyncResult{}, err
	}
	type doc struct {
		id      string
		revised time.Time
	}
	var docs []doc
	for _, u := range list.Value {
		revised := parseTime(u.CurrentReleaseDate)
		if !docIDPattern.MatchString(u.ID) || revised == nil {
			continue
		}
		docs = append(docs, doc{id: u.ID, revised: *revised})
	}
	// Oldest first.
	for i := 0; i < len(docs); i++ {
		for j := i + 1; j < len(docs); j++ {
			if docs[j].revised.Before(docs[i].revised) {
				docs[i], docs[j] = docs[j], docs[i]
			}
		}
	}
	var due []doc
	for _, d := range docs {
		if since.IsZero() || d.revised.After(since) {
			due = append(due, d)
		}
	}
	if since.IsZero() && len(due) > 1 {
		due = due[len(due)-1:]
	}
	result := advisories.SyncResult{Complete: true}
	for i, d := range due {
		if i >= maxDocsPerRun {
			result.Complete = false
			break
		}
		recs, err := c.document(ctx, d.id)
		if err != nil {
			result.Complete = false
			return result, err
		}
		result.Records = append(result.Records, recs...)
		result.Through = d.revised
	}
	return result, nil
}

// ByID is not supported: MSRC documents are monthly and the NVD feed covers CVE lookups.
func (c *Client) ByID(context.Context, []string) ([]advisories.AdvisoryRecord, error) {
	return nil, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return advisories.ErrUnavailable
	}
	req.Header.Set("User-Agent", "turaco/"+c.cfg.Version)
	req.Header.Set("Accept", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return advisories.ErrUnavailable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return advisories.ErrRateLimited
	case resp.StatusCode != http.StatusOK:
		return advisories.ErrUnavailable
	}
	body, err := advisories.ReadLimited(resp.Body, c.cfg.MaxResponse)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return advisories.ErrInvalidResponse
	}
	return nil
}

func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
