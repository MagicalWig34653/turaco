// Package advisories is the adapter boundary to security advisory feeds
// (docs/product/f8-security-briefing-design.md, decision B1).
//
// The port holds the normalized record types the Security module imports, the Provider contract a feed
// adapter implements, an in-memory Fake for tests and local development and a placeholder that reports
// "not configured". Real accountless adapters live in the sub-packages nvd (NVD API 2.0) and cisakev
// (CISA Known Exploited Vulnerabilities catalog); OSV, vendor bulletins and MSRC are later adapters.
// Provider DTOs never cross this boundary: adapters return the normalized records below. The same
// records are accepted by the bounded JSON import (POST /api/v1/security/advisories/import).
//
// Every field is untrusted input: the consumer validates lengths, characters, enumerations and URLs.
package advisories

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// ErrNotConfigured is returned while no advisory feed is implemented or configured.
var ErrNotConfigured = errors.New("advisories: no advisory feed is configured (not implemented yet)")

// Version rule kinds of a Criteria (see VersionRule).
const (
	RuleIntroduced = "introduced"
	RuleFixed      = "fixed"
	RuleLT         = "lt"
	RuleLE         = "le"
	RuleEQ         = "eq"
)

// RuleKinds lists every version rule kind.
var RuleKinds = []string{RuleIntroduced, RuleFixed, RuleLT, RuleLE, RuleEQ}

// VersionRule is one condition on the installed version. introduced/fixed pairs describe ranges
// (affected from introduced, up to but excluding fixed; a fixed without introduced starts at the lowest
// version), lt/le describe "every version below (or up to) X" and eq names single affected versions.
type VersionRule struct {
	Kind    string
	Version string
}

// Criteria names affected software: a product (by name and optional publisher, matched to Turaco Software
// Products by exact name or alias), an optional OS platform (windows, macos, ios, android, linux, other)
// and version rules. No rules means every version of the product is affected.
type Criteria struct {
	ProductName string
	Publisher   string
	OSPlatform  string
	Rules       []VersionRule
}

// AdvisoryRecord is one advisory as reported by a source, already normalized. Source is a short key of
// the feed (for example "nvd" or "vendor-msrc"); ExternalID is the source's stable id (a CVE id or a
// bulletin id) and unique per source. Severity is one of none, low, medium, high, critical. SourceURL
// must be an https URL.
type AdvisoryRecord struct {
	Source      string
	ExternalID  string
	Title       string
	Summary     string
	Severity    string
	PublishedAt *time.Time
	ModifiedAt  *time.Time
	SourceURL   string
	Criteria    []Criteria
	// CriteriaSkipped counts affected software the adapter had to leave out of Criteria (too many products
	// or rules, versions it cannot carry); above zero the criteria are incomplete and analysts must not
	// treat them as complete.
	CriteriaSkipped int `json:",omitempty"`
	// References are https URLs the source lists for the advisory (bounded by the adapter).
	References []string `json:",omitempty"`
}

// Errors adapters return; messages never contain URLs, keys or response bodies.
var (
	// ErrRateLimited reports that the source throttled the client beyond the retry budget.
	ErrRateLimited = errors.New("advisories: the source rate limit was exceeded")
	// ErrUnavailable reports a network failure, timeout or an unexpected status of the source.
	ErrUnavailable = errors.New("advisories: the source is unavailable")
	// ErrInvalidResponse reports a malformed, oversized or implausible response.
	ErrInvalidResponse = errors.New("advisories: the source returned an invalid response")
)

// SyncResult is one bounded incremental read of a feed. Through is the end of the last window that was
// read completely: the next run continues from it. Complete is false when the record bound stopped the
// read before the present was reached or an error ended the read early (Records and Through then hold the
// progress made so far).
type SyncResult struct {
	Records  []AdvisoryRecord
	Through  time.Time
	Complete bool
}

// SameHostHTTPS is an http.Client CheckRedirect that follows a redirect only to the same host over https
// (at most 3 hops); every other redirect ends with the redirect response itself, which adapters treat as
// "unavailable". Request headers such as an API key therefore never reach another host.
func SameHostHTTPS(req *http.Request, via []*http.Request) error {
	if len(via) > 3 || req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

// Syncer is an incremental advisory feed (NVD).
type Syncer interface {
	// Sync reads advisories modified since the given time (zero: the adapter's default window).
	Sync(ctx context.Context, since time.Time) (SyncResult, error)
	// ByID reads the advisories with the given ids (CVE ids); unknown ids are skipped.
	ByID(ctx context.Context, ids []string) ([]AdvisoryRecord, error)
}

// KEVEntry is the CISA Known Exploited Vulnerabilities enrichment of one CVE.
type KEVEntry struct {
	CVEID              string
	DateAdded          *time.Time
	DueDate            *time.Time
	RequiredAction     string
	KnownRansomwareUse bool
}

// KEVCatalog is a conditional read of the KEV catalog. NotModified is true when the ETag the caller sent
// is still current (Entries is then empty).
type KEVCatalog struct {
	Entries     []KEVEntry
	ETag        string
	NotModified bool
}

// KEVSource reads the Known Exploited Vulnerabilities catalog.
type KEVSource interface {
	Catalog(ctx context.Context, etag string) (KEVCatalog, error)
}

// ReadLimited reads at most max bytes; a longer body is ErrInvalidResponse.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if int64(len(b)) > max {
		return nil, ErrInvalidResponse
	}
	return b, nil
}

// Provider reads advisories from a feed.
type Provider interface {
	// Advisories returns the advisories published or modified since the given time (zero: all).
	Advisories(ctx context.Context, since time.Time) ([]AdvisoryRecord, error)
}

// NotConfigured is the placeholder provider: every call fails with ErrNotConfigured.
type NotConfigured struct{}

func (NotConfigured) Advisories(context.Context, time.Time) ([]AdvisoryRecord, error) {
	return nil, ErrNotConfigured
}

// Fake is an in-memory provider for tests and local development.
type Fake struct {
	mu      sync.Mutex
	records []AdvisoryRecord
	err     error
}

func NewFake() *Fake { return &Fake{} }

// Set replaces the reported advisories.
func (f *Fake) Set(r ...AdvisoryRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = clone(r)
}

// FailWith makes every following call fail with err; nil restores normal behavior.
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Advisories returns the records modified (or, without a modification time, published) at or after
// since; records without either time are always returned.
func (f *Fake) Advisories(_ context.Context, since time.Time) ([]AdvisoryRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []AdvisoryRecord
	for _, r := range f.records {
		at := r.ModifiedAt
		if at == nil {
			at = r.PublishedAt
		}
		if since.IsZero() || at == nil || !at.Before(since) {
			out = append(out, r)
		}
	}
	return clone(out), nil
}

func clone(in []AdvisoryRecord) []AdvisoryRecord {
	out := make([]AdvisoryRecord, len(in))
	for i, r := range in {
		r.Criteria = slices.Clone(r.Criteria)
		for j := range r.Criteria {
			r.Criteria[j].Rules = slices.Clone(r.Criteria[j].Rules)
		}
		out[i] = r
	}
	return out
}
