package roles

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Location helpers of the role service (ADR-0040). Location data is owned by Organization and read only through
// SubjectDirectory.LocationPaths.

// lockScope freezes the Location tree for the rest of tx and returns the path of the scope Location (nil when it is
// unknown). It runs before any coverage of the Location is evaluated (the guards expand the actor's scoped roots
// through LocationTree), so a concurrent Location move cannot change what the guards decided on before the
// assignment commits; a move waits for the assignment and then counts it (F16 L-B security review gate 1, slice L-C).
func (s *Service) lockScope(ctx context.Context, tx pgx.Tx, locationID string) ([]LocationRef, error) {
	path, err := s.subjects.LockLocationPath(ctx, tx, locationID)
	if err != nil {
		return nil, fmt.Errorf("lock scope location: %w", err)
	}
	return path, nil
}

// requireActivePath refuses a scope Location that does not exist or is inactive (F16 design L5, 422
// access.scope_location_invalid). Inactive Locations stay in existing scopes (L2); only new assignments are refused.
// AssignRole calls it after the actor guards, so the answer reveals nothing about Location ids to an actor who may
// not assign there (F16 L-B security review gate 4).
func requireActivePath(path []LocationRef, locationID string) error {
	if len(path) == 0 || !strings.EqualFold(path[len(path)-1].ID, locationID) || !path[len(path)-1].Active {
		return ErrScopeLocationInvalid
	}
	return nil
}

// scopeSnapshot is the Location name and path (Site first) of a scoped assignment at the time of an audit record, so
// the record stays explainable after the Location is renamed or moved (F16 L-B security review gate 5). Nothing is
// added for a global assignment or an unknown Location.
func scopeSnapshot(path []LocationRef, m map[string]any) map[string]any {
	if len(path) == 0 {
		return m
	}
	names := make([]string, len(path))
	for i, ref := range path {
		names[i] = ref.Name
	}
	m["scopeLocationName"] = path[len(path)-1].Name
	m["scopeLocationPath"] = names
	return m
}

// LocationLabel names a scope Location for display: its name and the names on its path from the Site down to it.
type LocationLabel struct {
	Name string
	// Path are the names from the Site (first) to the Location itself (last).
	Path []string
	// Active is false for a deactivated Location (its scoped assignments stay effective and are flagged).
	Active bool
}

// LocationLabels resolves scope Locations for display; unknown ids are absent. The transport calls it only for
// callers who may read Organization Locations.
func (s *Service) LocationLabels(ctx context.Context, ids []string) (map[string]LocationLabel, error) {
	ids = distinctNonEmpty(ids)
	out := map[string]LocationLabel{}
	if len(ids) == 0 {
		return out, nil
	}
	paths, err := s.subjects.LocationPaths(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve scope locations: %w", err)
	}
	for id, path := range paths {
		if len(path) == 0 {
			continue
		}
		l := LocationLabel{Name: path[len(path)-1].Name, Active: path[len(path)-1].Active, Path: make([]string, 0, len(path))}
		for _, ref := range path {
			l.Path = append(l.Path, ref.Name)
		}
		out[id] = l
	}
	return out, nil
}

// ancestorsOrSelf returns the Location and its ancestors (the scoped roots that cover it); nil for an unknown id.
func (s *Service) ancestorsOrSelf(ctx context.Context, locationID string) ([]string, error) {
	paths, err := s.subjects.LocationPaths(ctx, []string{locationID})
	if err != nil {
		return nil, fmt.Errorf("resolve location path: %w", err)
	}
	var out []string
	for _, ref := range paths[locationID] {
		out = append(out, strings.ToLower(ref.ID))
	}
	return out, nil
}

func distinctNonEmpty(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
