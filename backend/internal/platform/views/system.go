package views

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// System Views are built-in definitions that modules offer per caller (Tickets: "My open tickets", "Unassigned",
// one per Queue the caller views). They are code, not rows: they cannot be edited, shared or archived, and they run
// through exactly the same path as a Saved View, the owning module's own query endpoint as the viewer. A provider
// decides which entries a caller gets; the viewer's own scope still applies to every execution, so an entry can
// show an empty list but never more than the viewer may read.

// SystemKeyPrefix starts the id of every System View.
const SystemKeyPrefix = "system:"

// SystemView is one built-in View for one caller.
type SystemView struct {
	// Key is the stable id, "system:<resource>:<name>[:<ref>]".
	Key string
	// Handle is an optional second id the counts endpoint accepts ("queue:<id>").
	Handle   string
	Resource string
	Group    string
	// NameKey is the i18n key of the display name; Name is set instead when the name is data (a Queue's name).
	NameKey string
	Name    string
	// Ref identifies the object the entry stands for (the Queue id), empty otherwise.
	Ref        string
	Definition Definition
	Position   int
	// ScopeKey digests everything of the caller's scope that changes what the entry selects (for Tickets the set of
	// viewable Queues). It is part of the count cache key.
	ScopeKey string
}

// SystemProvider offers System Views of one module. It must not return entries the caller cannot use at all.
type SystemProvider interface {
	SystemViews(ctx context.Context, c Caller) ([]SystemView, error)
}

// WithSystemProviders registers the providers of System Views.
func (s *Service) WithSystemProviders(p ...SystemProvider) *Service {
	s.providers = append(s.providers, p...)
	return s
}

// IsSystemKey reports whether the id names a System View (or one of its handles).
func IsSystemKey(id string) bool {
	return strings.HasPrefix(id, SystemKeyPrefix) || strings.HasPrefix(id, "queue:")
}

// SystemViews returns the System Views the caller has: resource readable, module on, in provider order.
func (s *Service) SystemViews(ctx context.Context, c Caller) ([]SystemView, error) {
	if !isUUID(c.UserID) {
		return nil, ErrForbidden
	}
	var out []SystemView
	enabled := map[string]bool{}
	for _, p := range s.providers {
		list, err := p.SystemViews(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("system views: %w", err)
		}
		for _, sv := range list {
			res, ok := s.res.canUse(c, sv.Resource)
			if !ok || !strings.HasPrefix(sv.Key, SystemKeyPrefix) {
				continue
			}
			on, known := enabled[res.Module]
			if !known {
				var err error
				if on, err = s.gate.Enabled(ctx, res.Module); err != nil {
					return nil, fmt.Errorf("check module %s: %w", res.Module, err)
				}
				enabled[res.Module] = on
			}
			if on {
				out = append(out, sv)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func (s *Service) systemView(ctx context.Context, c Caller, id string) (SystemView, error) {
	list, err := s.SystemViews(ctx, c)
	if err != nil {
		return SystemView{}, err
	}
	return findSystem(list, id)
}

// findSystem picks the System View with the key or handle from a list the caller already has.
func findSystem(list []SystemView, id string) (SystemView, error) {
	for _, sv := range list {
		if sv.Key == id || (sv.Handle != "" && sv.Handle == id) {
			return sv, nil
		}
	}
	return SystemView{}, ErrNotFound
}

func (s *Service) runSystem(ctx context.Context, c Caller, sv SystemView, in ResultsInput) (ResultsOutput, error) {
	res, ok := s.res.canUse(c, sv.Resource)
	if !ok {
		return ResultsOutput{}, ErrNotFound
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return ResultsOutput{}, err
	}
	req := query.Request{Cursor: in.Cursor, Limit: in.Limit, Count: in.Count, Filter: sv.Definition.Filter}
	out, err := s.runner.Query(ctx, c, res.Key, req)
	if err != nil {
		return ResultsOutput{}, err
	}
	return ResultsOutput{View: Reference{ID: sv.Key, Name: sv.Name, NameKey: sv.NameKey, Resource: sv.Resource}, Result: out}, nil
}

// resultsSystem runs a System View for the caller; an id the caller does not have is ErrNotFound.
func (s *Service) resultsSystem(ctx context.Context, c Caller, id string, in ResultsInput) (ResultsOutput, error) {
	sv, err := s.systemView(ctx, c, id)
	if err != nil {
		return ResultsOutput{}, err
	}
	return s.runSystem(ctx, c, sv, in)
}

// ---------------------------------------------------------------- counts

// CountStatus values of a Count.
const (
	CountOK          = "ok"
	CountUnavailable = "unavailable"
)

// MaxCountIDs bounds one counts request.
const MaxCountIDs = 30

// Count is the capped number of rows of one View or System View for the caller. An unavailable count is never
// reported as zero.
type Count struct {
	ID     string
	Count  int
	Capped bool
	Status string
}

// countCache keeps capped counts for CacheTTL. The key holds the principal, the permissions and the module scope
// (see CacheKey), so a count is only ever answered to the caller and scope it was computed for. It is bounded.
type countCache struct {
	mu      sync.Mutex
	entries map[string]cachedCount
	now     func() time.Time
}

type cachedCount struct {
	n      int
	capped bool
	until  time.Time
}

const countCacheMax = 5000

func newCountCache() *countCache {
	return &countCache{entries: map[string]cachedCount{}, now: time.Now}
}

func (c *countCache) get(key string) (cachedCount, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.until) {
		delete(c.entries, key)
		return cachedCount{}, false
	}
	return e, true
}

func (c *countCache) put(key string, n int, capped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= countCacheMax {
		for k, e := range c.entries {
			if !now.Before(e.until) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < countCacheMax {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = cachedCount{n: n, capped: capped, until: now.Add(CacheTTL)}
}

func permissionKeys(c Caller) []string {
	out := make([]string, 0, len(c.Permissions))
	for p := range c.Permissions {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// countSystem counts a System View, from the cache when the same caller and scope asked within CacheTTL.
func (s *Service) countSystem(ctx context.Context, c Caller, sv SystemView) Count {
	key := CacheKey(sv.Key, 0, c.UserID, permissionKeys(c), nil, nil, sv.ScopeKey)
	if e, ok := s.counts.get(key); ok {
		return Count{ID: sv.Key, Count: e.n, Capped: e.capped, Status: CountOK}
	}
	out, err := s.runSystem(ctx, c, sv, ResultsInput{Limit: 1, Count: true})
	if err != nil || out.Count == nil {
		return Count{ID: sv.Key, Status: CountUnavailable}
	}
	s.counts.put(key, *out.Count, out.CountCapped)
	return Count{ID: sv.Key, Count: *out.Count, Capped: out.CountCapped, Status: CountOK}
}

func (s *Service) countSaved(ctx context.Context, c Caller, id string) (Count, bool) {
	out, err := s.Results(ctx, c, id, ResultsInput{Limit: 1, Count: true})
	switch {
	case err == nil && out.Count != nil:
		return Count{ID: id, Count: *out.Count, Capped: out.CountCapped, Status: CountOK}, true
	case errors.Is(err, ErrNotFound) || errors.Is(err, ErrArchived) || errors.Is(err, ErrModuleDisabled):
		return Count{}, false
	}
	return Count{ID: id, Status: CountUnavailable}, true
}

// Counts returns the capped counts of Views and System Views for the caller. Ids the caller does not have are left
// out, so the answer does not tell whether such a View exists. A count that cannot be computed (module error, rate
// limit, timeout) is returned as unavailable, never as zero.
func (s *Service) Counts(ctx context.Context, c Caller, ids []string) ([]Count, error) {
	if !isUUID(c.UserID) {
		return nil, ErrForbidden
	}
	if len(ids) == 0 || len(ids) > MaxCountIDs {
		return nil, invalid("Give between 1 and 30 ids.")
	}
	seen := map[string]bool{}
	var out []Count
	// The System Views of the caller (providers resolve the caller's Queue scope) are loaded once per request,
	// not once per id.
	var system []SystemView
	systemLoaded := false
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		switch {
		case IsSystemKey(id):
			if !systemLoaded {
				var err error
				if system, err = s.SystemViews(ctx, c); err != nil {
					return nil, err
				}
				systemLoaded = true
			}
			sv, err := findSystem(system, id)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return nil, err
			}
			cnt := s.countSystem(ctx, c, sv)
			cnt.ID = id
			out = append(out, cnt)
		case isUUID(id):
			if cnt, ok := s.countSaved(ctx, c, id); ok {
				out = append(out, cnt)
			}
		default:
			return nil, invalid("An id is neither a view id nor a system view handle.")
		}
	}
	return out, nil
}
