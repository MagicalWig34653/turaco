package roles

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Location helpers of the role service (ADR-0040). Location data is owned by Organization and read only through
// SubjectDirectory.LocationPaths.

// requireActiveLocation refuses a scope Location that does not exist or is inactive (F16 design L5, 422
// access.scope_location_invalid). Inactive Locations stay in existing scopes (L2); only new assignments are refused.
func (s *Service) requireActiveLocation(ctx context.Context, locationID string) error {
	paths, err := s.subjects.LocationPaths(ctx, []string{locationID})
	if err != nil {
		return fmt.Errorf("check scope location: %w", err)
	}
	path := paths[locationID]
	if len(path) == 0 || !strings.EqualFold(path[len(path)-1].ID, locationID) || !path[len(path)-1].Active {
		return ErrScopeLocationInvalid
	}
	return nil
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
