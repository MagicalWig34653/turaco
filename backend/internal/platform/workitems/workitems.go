// Package workitems is the My Work source contract (ADR-0033 V8, docs/product/f13-workbench-views-design.md).
// Modules contribute work for the signed-in User through a Source implemented in their own public package and
// registered in the composition root. My Work merges the sources into one read model and creates no task, state or
// notification of its own.
//
// Every source authorizes the User for every item and for the count (a source never trusts the merge), returns
// items in the shared order and a continuation cursor per item, and is skipped while its module is switched off
// (ADR-0032). A source that fails is reported as unavailable; it is never treated as empty or as zero.
//
// The package imports no business module (make archcheck).
package workitems

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Limits.
const (
	DefaultLimit = 50
	MaxLimit     = 100
	// CountCap is the largest count reported exactly; larger ones are reported as capped.
	CountCap  = 1000
	maxCursor = 4096
)

// ErrInvalidCursor is returned for a cursor that is malformed or was not issued for the requested sources.
var ErrInvalidCursor = errors.New("workitems: invalid cursor")

// InvalidError is a request validation failure whose message is safe to show.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "workitems: " + e.Message }

// Item is one entry of My Work. It carries only what the owning module decided the User may see.
type Item struct {
	ID     string
	Source string
	// Kind names the sort of item ("task", "ticket"); the UI chooses the badge and label from it.
	Kind          string
	Title         string
	Reference     string
	Status        string
	WaitingReason string
	Priority      string
	DueAt         *time.Time
	UpdatedAt     time.Time
	// Href is the in-app path that opens the item.
	Href string
}

// Entry is an Item with the data the merge needs: its rank in the shared order (smaller first; due date comes
// before rank) and the source's own cursor that continues after this entry.
type Entry struct {
	Item   Item
	Rank   int
	Cursor string
}

// Count is a capped number of items.
type Count struct {
	N      int
	Capped bool
}

// Source is one module's contribution.
type Source interface {
	// Key is the stable source id ("tasks", "tickets").
	Key() string
	// Module is the ADR-0032 module key that must be effectively on for the source to run.
	Module() string
	// Items returns up to limit entries after cursor, ordered by (due date with none last, rank, item id), for
	// the User only. A User who may not use the source gets no entries and no error. A bad cursor is
	// ErrInvalidCursor.
	Items(ctx context.Context, p authorization.Principal, cursor string, limit int) ([]Entry, error)
	// Count returns the number of items the User would see, capped at CountCap.
	Count(ctx context.Context, p authorization.Principal) (Count, error)
}

// ModuleGate reports whether a module is effectively on.
type ModuleGate interface {
	Enabled(ctx context.Context, key string) (bool, error)
}

// Service merges the registered sources.
type Service struct {
	sources []Source
	gate    ModuleGate
}

// New registers the sources in display order.
func New(gate ModuleGate, sources ...Source) (*Service, error) {
	seen := map[string]bool{}
	for _, s := range sources {
		if s.Key() == "" || seen[s.Key()] {
			return nil, fmt.Errorf("workitems: empty or duplicate source key %q", s.Key())
		}
		seen[s.Key()] = true
	}
	return &Service{sources: slices.Clone(sources), gate: gate}, nil
}

// Keys lists the registered source keys.
func (s *Service) Keys() []string {
	out := make([]string, len(s.sources))
	for i, src := range s.sources {
		out[i] = src.Key()
	}
	return out
}

// pick resolves the requested keys (all sources when empty) and drops the sources of disabled modules.
func (s *Service) pick(ctx context.Context, keys []string) ([]Source, error) {
	var out []Source
	enabled := map[string]bool{}
	for _, src := range s.sources {
		if len(keys) > 0 && !slices.Contains(keys, src.Key()) {
			continue
		}
		on, known := enabled[src.Module()]
		if !known {
			var err error
			if on, err = s.gate.Enabled(ctx, src.Module()); err != nil {
				return nil, fmt.Errorf("check module %s: %w", src.Module(), err)
			}
			enabled[src.Module()] = on
		}
		if on {
			out = append(out, src)
		}
	}
	return out, nil
}

func (s *Service) validKeys(keys []string) error {
	for _, k := range keys {
		if !slices.ContainsFunc(s.sources, func(src Source) bool { return src.Key() == k }) {
			return &InvalidError{Message: "unknown source " + k}
		}
	}
	return nil
}

// Page is one page of the merged feed.
type Page struct {
	Items      []Item
	NextCursor string
	// Unavailable lists the sources that failed; their items are missing from this page and the cursor keeps their
	// position, so the next page retries them.
	Unavailable []string
}

func before(a, b Entry) bool {
	switch {
	case a.Item.DueAt == nil && b.Item.DueAt != nil:
		return false
	case a.Item.DueAt != nil && b.Item.DueAt == nil:
		return true
	case a.Item.DueAt != nil && !a.Item.DueAt.Equal(*b.Item.DueAt):
		return a.Item.DueAt.Before(*b.Item.DueAt)
	case a.Rank != b.Rank:
		return a.Rank < b.Rank
	}
	if a.Item.ID != b.Item.ID {
		return a.Item.ID < b.Item.ID
	}
	return a.Item.Source < b.Item.Source
}

func decodeCursor(raw string, requested map[string]bool) (map[string]string, error) {
	pos := map[string]string{}
	if raw == "" {
		return pos, nil
	}
	if len(raw) > maxCursor {
		return nil, ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pos); err != nil {
		return nil, ErrInvalidCursor
	}
	for k, v := range pos {
		if !requested[k] || len(v) > 512 {
			return nil, ErrInvalidCursor
		}
	}
	return pos, nil
}

func encodeCursor(pos map[string]string) string {
	b, _ := json.Marshal(pos)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Items returns the merged feed of the requested sources (all when none are named) for the User. The merge takes
// the smallest head of the per-source streams, so the result is in the shared order; the cursor holds one position
// per source, advanced only by the entries that were returned.
func (s *Service) Items(ctx context.Context, p authorization.Principal, keys []string, cursor string, limit int) (Page, error) {
	if p.UserID == "" {
		return Page{}, &InvalidError{Message: "a signed-in user is required"}
	}
	if err := s.validKeys(keys); err != nil {
		return Page{}, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		return Page{}, &InvalidError{Message: "limit is too large"}
	}
	srcs, err := s.pick(ctx, keys)
	if err != nil {
		return Page{}, err
	}
	requested := map[string]bool{}
	for _, src := range srcs {
		requested[src.Key()] = true
	}
	pos, err := decodeCursor(cursor, requested)
	if err != nil {
		return Page{}, err
	}
	streams := map[string][]Entry{}
	var page Page
	for _, src := range srcs {
		entries, err := src.Items(ctx, p, pos[src.Key()], limit+1)
		if errors.Is(err, ErrInvalidCursor) {
			return Page{}, ErrInvalidCursor
		}
		if err != nil {
			page.Unavailable = append(page.Unavailable, src.Key())
			continue
		}
		streams[src.Key()] = entries
	}
	next := map[string]string{}
	for k, v := range pos {
		next[k] = v
	}
	page.Items = []Item{}
	for len(page.Items) < limit {
		best := ""
		for _, src := range srcs {
			head := streams[src.Key()]
			if len(head) == 0 {
				continue
			}
			if best == "" || before(head[0], streams[best][0]) {
				best = src.Key()
			}
		}
		if best == "" {
			break
		}
		e := streams[best][0]
		streams[best] = streams[best][1:]
		e.Item.Source = best
		page.Items = append(page.Items, e.Item)
		next[best] = e.Cursor
	}
	more := false
	for _, rest := range streams {
		more = more || len(rest) > 0
	}
	if more {
		page.NextCursor = encodeCursor(next)
	}
	return page, nil
}

// Count states.
const (
	CountOK          = "ok"
	CountUnavailable = "unavailable"
)

// CountResult is the count of one source.
type CountResult struct {
	Source string
	Count
	Status string
	// Err is the reason of an unavailable source; it is logged by the transport and never sent to the client.
	Err error
}

// Counts returns the capped count of each requested source (all when none are named); disabled modules are left
// out and a failing source is reported as unavailable, never as zero.
func (s *Service) Counts(ctx context.Context, p authorization.Principal, keys []string) ([]CountResult, error) {
	if p.UserID == "" {
		return nil, &InvalidError{Message: "a signed-in user is required"}
	}
	if err := s.validKeys(keys); err != nil {
		return nil, err
	}
	srcs, err := s.pick(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make([]CountResult, 0, len(srcs))
	for _, src := range srcs {
		c, err := src.Count(ctx, p)
		if err != nil {
			out = append(out, CountResult{Source: src.Key(), Status: CountUnavailable, Err: err})
			continue
		}
		if c.N > CountCap {
			c = Count{N: CountCap, Capped: true}
		}
		out = append(out, CountResult{Source: src.Key(), Count: c, Status: CountOK})
	}
	return out, nil
}
