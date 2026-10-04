// Package advisories is the adapter boundary to security advisory feeds
// (docs/product/f8-security-briefing-design.md, decision B1).
//
// Only the internal side exists so far: the normalized record types the Security module imports, the
// Provider contract a feed adapter implements, an in-memory Fake for tests and local development and a
// placeholder that reports "not configured". Real feeds (NVD, CISA KEV, vendor bulletins, MSRC) are later
// adapters; their DTOs never cross this boundary: providers return the normalized records below. The
// same records are accepted by the bounded JSON import (POST /api/v1/security/advisories/import).
//
// Every field is untrusted input: the consumer validates lengths, characters, enumerations and URLs.
package advisories

import (
	"context"
	"errors"
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
