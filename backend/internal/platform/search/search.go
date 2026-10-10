// Package search is the platform search concept (F14 section 5): one entry point that fans a text query out to the
// Searchers the modules register through their public contracts. A module owns what its records are, who may read
// them and how they match; the platform only bounds time and size, orders the groups and reports a slow or failing
// source as unavailable instead of failing the whole search.
//
// Authorization stays with the modules. A Searcher receives the authenticated Principal and must apply the same
// rules as the module's own list (permissions, queue scope, audience, redaction). The platform never filters hits
// itself and never caches results across callers. Queries are never logged and searches are not audited.
package search

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

const (
	// MinQueryLength is the shortest query in characters (a trigram needs three, but two catches short references).
	MinQueryLength = 2
	// MaxQueryLength bounds the query text.
	MaxQueryLength = 100
	// DefaultPerType and MaxPerType bound the hits of one source.
	DefaultPerType = 5
	MaxPerType     = 10
	// MaxTotal bounds the hits of one response.
	MaxTotal = 25
	// DefaultSourceTimeout is the time one source gets before it is reported as unavailable.
	DefaultSourceTimeout = 300 * time.Millisecond
)

// ErrNotAllowed is returned by a Searcher when the caller may not search that source at all (missing permission).
// The source is then left out silently: it is neither a hit list nor "unavailable".
var ErrNotAllowed = errors.New("search: source not allowed for caller")

// ErrQueryLength is returned for a query outside the length limits.
var ErrQueryLength = errors.New("search: query length out of range")

// ErrUnknownType is returned when a requested type is not registered.
var ErrUnknownType = errors.New("search: unknown type")

// Hit is one found record. Everything in it is already authorized and redacted by the owning module.
type Hit struct {
	// Type is the Searcher type, for example "ticket".
	Type string `json:"type"`
	ID   string `json:"id"`
	// Reference is the human number (T-1234); empty for records without one.
	Reference string `json:"reference,omitempty"`
	Title     string `json:"title"`
	// Subtitle is a short second line (status, e-mail address); the module decides what the caller may see.
	Subtitle string `json:"subtitle,omitempty"`
	// Exact marks a hit whose reference equals the query; the client may lead with it.
	Exact bool `json:"exact,omitempty"`
}

// Searcher is the contract a module registers.
type Searcher interface {
	// Type is the stable result type, singular and lower case.
	Type() string
	// Module is the module key; a disabled module is skipped.
	Module() string
	// Search returns at most limit hits the caller may read, best first, or ErrNotAllowed.
	Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]Hit, error)
}

// Request is one search.
type Request struct {
	Query string
	// Types restricts the sources; empty means all.
	Types []string
	// Limit is the maximum hits per type; 0 means DefaultPerType.
	Limit int
}

// Response is the result: hits grouped in registration order, and the types that did not answer in time.
type Response struct {
	Items       []Hit
	Unavailable []string
}

// Service fans searches out. Register all Searchers before serving.
type Service struct {
	searchers []Searcher
	// Enabled reports whether a module is switched on; nil means all are on.
	Enabled func(ctx context.Context, module string) bool
	// Timeout bounds each source; zero means DefaultSourceTimeout.
	Timeout time.Duration
}

// New returns an empty Service.
func New() *Service { return &Service{} }

// Register adds a Searcher. A duplicate type is a programming error.
func (s *Service) Register(searchers ...Searcher) {
	for _, x := range searchers {
		if slices.ContainsFunc(s.searchers, func(o Searcher) bool { return o.Type() == x.Type() }) {
			panic("search: duplicate type " + x.Type())
		}
		s.searchers = append(s.searchers, x)
	}
}

// Types lists the registered types in order.
func (s *Service) Types() []string {
	out := make([]string, len(s.searchers))
	for i, x := range s.searchers {
		out[i] = x.Type()
	}
	return out
}

// Search runs the query against the selected sources concurrently.
func (s *Service) Search(ctx context.Context, p authorization.Principal, req Request) (Response, error) {
	q := strings.TrimSpace(req.Query)
	if n := utf8.RuneCountInString(q); n < MinQueryLength || n > MaxQueryLength || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		return Response{}, ErrQueryLength
	}
	for _, t := range req.Types {
		if !slices.Contains(s.Types(), t) {
			return Response{}, ErrUnknownType
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultPerType
	}
	limit = min(limit, MaxPerType)
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultSourceTimeout
	}

	type outcome struct {
		hits        []Hit
		unavailable bool
	}
	selected := make([]Searcher, 0, len(s.searchers))
	for _, x := range s.searchers {
		if len(req.Types) > 0 && !slices.Contains(req.Types, x.Type()) {
			continue
		}
		if s.Enabled != nil && !s.Enabled(ctx, x.Module()) {
			continue
		}
		selected = append(selected, x)
	}
	results := make([]outcome, len(selected))
	var wg sync.WaitGroup
	for i, x := range selected {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			done := make(chan outcome, 1)
			go func() {
				defer func() {
					if recover() != nil { // a failing source must not take the process down
						done <- outcome{unavailable: true}
					}
				}()
				hits, err := x.Search(sctx, p, q, limit)
				switch {
				case errors.Is(err, ErrNotAllowed):
					done <- outcome{}
				case err != nil:
					done <- outcome{unavailable: true}
				default:
					done <- outcome{hits: hits}
				}
			}()
			select {
			case o := <-done:
				results[i] = o
			case <-sctx.Done():
				results[i] = outcome{unavailable: true}
			}
		}()
	}
	wg.Wait()

	var res Response
	for i, x := range selected {
		o := results[i]
		if o.unavailable {
			res.Unavailable = append(res.Unavailable, x.Type())
			continue
		}
		for _, h := range o.hits[:min(len(o.hits), limit)] {
			if len(res.Items) >= MaxTotal {
				break
			}
			h.Type = x.Type()
			res.Items = append(res.Items, h)
		}
	}
	return res, nil
}

// Match reports whether every word of q occurs in at least one of the fields, ignoring case. Searchers whose module
// has no indexed text search use it on a bounded page of records the module already authorized.
func Match(q string, fields ...string) bool {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return false
	}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}
	for _, w := range words {
		if !slices.ContainsFunc(lower, func(f string) bool { return strings.Contains(f, w) }) {
			return false
		}
	}
	return true
}

// ExactReference reports whether the query is the reference (case-insensitive).
func ExactReference(q, reference string) bool {
	return reference != "" && strings.EqualFold(strings.TrimSpace(q), reference)
}
