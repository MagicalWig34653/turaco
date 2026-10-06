// Package cisakev reads the CISA Known Exploited Vulnerabilities catalog
// (https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json): a public JSON
// file without an account. The fetch is conditional (ETag / If-None-Match), size capped and time bounded;
// the result is enrichment data keyed by CVE id, not advisories. Terms of use: see
// docs/integrations/advisory-feeds.md (verify before production use).
package cisakev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// DefaultURL is the production catalog.
const DefaultURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

const (
	maxEntries        = 20000
	maxRequiredAction = 500
)

var cveIDPattern = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,12}$`)

// Config configures a Client. Zero values select the defaults.
type Config struct {
	URL            string
	Version        string // User-Agent "turaco/<version>", default "dev"
	HTTPClient     *http.Client
	RequestTimeout time.Duration // default 30 s
	MaxResponse    int64         // bytes, default 8 MiB
}

// Client reads the catalog.
type Client struct{ cfg Config }

var _ advisories.KEVSource = (*Client)(nil)

// New validates the configuration.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		cfg.URL = DefaultURL
	}
	if u, err := url.Parse(cfg.URL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("cisakev: invalid URL")
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
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = 8 << 20
	}
	return &Client{cfg: cfg}, nil
}

type catalog struct {
	Vulnerabilities []struct {
		CVEID          string `json:"cveID"`
		DateAdded      string `json:"dateAdded"`
		DueDate        string `json:"dueDate"`
		RequiredAction string `json:"requiredAction"`
		Ransomware     string `json:"knownRansomwareCampaignUse"`
	} `json:"vulnerabilities"`
}

// Catalog fetches the catalog. When etag is the value of the last successful read and the catalog has not
// changed the result has NotModified set. Entries with an invalid CVE id are skipped; invalid dates are
// left empty.
func (c *Client) Catalog(ctx context.Context, etag string) (advisories.KEVCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, nil)
	if err != nil {
		return advisories.KEVCatalog{}, advisories.ErrUnavailable
	}
	req.Header.Set("User-Agent", "turaco/"+c.cfg.Version)
	req.Header.Set("Accept", "application/json")
	if etag != "" && !strings.ContainsAny(etag, "\r\n") {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return advisories.KEVCatalog{}, advisories.ErrUnavailable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return advisories.KEVCatalog{ETag: etag, NotModified: true}, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return advisories.KEVCatalog{}, advisories.ErrRateLimited
	case resp.StatusCode != http.StatusOK:
		return advisories.KEVCatalog{}, advisories.ErrUnavailable
	}
	body, err := advisories.ReadLimited(resp.Body, c.cfg.MaxResponse)
	if err != nil {
		return advisories.KEVCatalog{}, err
	}
	var raw catalog
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.Vulnerabilities) > maxEntries {
		return advisories.KEVCatalog{}, advisories.ErrInvalidResponse
	}
	out := advisories.KEVCatalog{ETag: resp.Header.Get("ETag"), Entries: make([]advisories.KEVEntry, 0, len(raw.Vulnerabilities))}
	if len(out.ETag) > 200 || strings.ContainsAny(out.ETag, "\r\n") {
		out.ETag = ""
	}
	seen := map[string]bool{}
	for _, v := range raw.Vulnerabilities {
		id := strings.ToUpper(strings.TrimSpace(v.CVEID))
		if !cveIDPattern.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		out.Entries = append(out.Entries, advisories.KEVEntry{CVEID: id, DateAdded: date(v.DateAdded), DueDate: date(v.DueDate),
			RequiredAction: boundedLine(v.RequiredAction), KnownRansomwareUse: strings.EqualFold(strings.TrimSpace(v.Ransomware), "Known")})
	}
	if len(raw.Vulnerabilities) > 0 && len(out.Entries) == 0 {
		return advisories.KEVCatalog{}, advisories.ErrInvalidResponse
	}
	return out, nil
}

func date(s string) *time.Time {
	t, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(s), time.UTC)
	if err != nil || t.Year() < 2000 || t.Year() > 2200 {
		return nil
	}
	return &t
}

func boundedLine(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, " ")
	}
	var b strings.Builder
	for _, r := range s {
		if safetext.Unsafe(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > maxRequiredAction {
		out = string(r[:maxRequiredAction-3]) + "..."
	}
	return out
}
