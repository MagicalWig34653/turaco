package views

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Resource registers one queryable resource with the views platform. The composition root declares it; the
// owning module's query endpoint does the work.
type Resource struct {
	// Key is the catalog resource key (tickets, devices, tasks).
	Key string
	// Module is the ADR-0032 module key. The resource is only usable while that module is effectively on.
	Module string
	// Group is the default sidebar group of the resource's Views.
	Group string
	// Use lists permissions of which the caller needs at least one to read the resource at all (as the module's own
	// query endpoint requires). Empty means any signed-in User (Tickets: everyone sees their own).
	Use []string
}

// Result is one page of a query as the owning module returned it. Items stay the module's own JSON.
type Result struct {
	Items       json.RawMessage
	NextCursor  string
	Count       *int
	CountCapped bool
	Warnings    []query.Warning
}

// RunError is a client-facing error of the module's query endpoint (a 4xx the caller can act on, such as
// query.invalid_filter), passed through unchanged.
type RunError struct {
	Status  int
	Code    string
	Message string
}

func (e *RunError) Error() string {
	return fmt.Sprintf("views: query failed (%d %s)", e.Status, e.Code)
}

// Runner runs queries as the viewer. The implementation must authenticate the call as c.UserID and apply the
// module's own authorization, scope and redaction; the views platform adds nothing to it and removes nothing.
type Runner interface {
	// Fields returns the resource's catalog as the viewer may use it.
	Fields(ctx context.Context, c Caller, resource string) (query.Info, error)
	// Query runs a request over the resource as the viewer.
	Query(ctx context.Context, c Caller, resource string, req query.Request) (Result, error)
}

// Directory answers questions about Users, Teams and Directory Groups. Organization's public contracts implement it.
type Directory interface {
	// CurrentTeamIDs returns the Teams the User currently belongs to.
	CurrentTeamIDs(ctx context.Context, userID string) ([]string, error)
	// GroupIDsOfUser returns the Directory Groups the User belongs to (transitively); roles are assigned to them.
	GroupIDsOfUser(ctx context.Context, userID string) ([]string, error)
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
}

// ModuleGate reports whether a module is effectively on (platform/modules implements it).
type ModuleGate interface {
	Enabled(ctx context.Context, key string) (bool, error)
}

// resourceSet is the validated registry.
type resourceSet struct {
	byKey  map[string]Resource
	groups map[string]bool
}

func newResourceSet(list []Resource) (resourceSet, error) {
	rs := resourceSet{byKey: map[string]Resource{}, groups: map[string]bool{"work": true}}
	for _, r := range list {
		if !columnPattern.MatchString(r.Key) || !columnPattern.MatchString(r.Module) || !groupKeyPattern.MatchString(r.Group) {
			return resourceSet{}, fmt.Errorf("views: invalid resource %q", r.Key)
		}
		if _, dup := rs.byKey[r.Key]; dup {
			return resourceSet{}, fmt.Errorf("views: duplicate resource %q", r.Key)
		}
		r.Use = slices.Clone(r.Use)
		rs.byKey[r.Key] = r
		rs.groups[r.Group] = true
	}
	return rs, nil
}

// usable lists the resource keys the caller may read.
func (rs resourceSet) usable(c Caller) []string {
	out := make([]string, 0, len(rs.byKey))
	for k, r := range rs.byKey {
		if c.hasAny(r.Use) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func (rs resourceSet) canUse(c Caller, key string) (Resource, bool) {
	r, ok := rs.byKey[key]
	if !ok || !c.hasAny(r.Use) {
		return Resource{}, false
	}
	return r, true
}
