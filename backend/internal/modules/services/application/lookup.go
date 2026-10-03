package application

import (
	"context"
	"strings"
)

// Lookup returns the Services among ids (at most MaxLookupIDs), keyed by id
// (lower case), including retired ones. It performs no permission check: it is
// the contract other modules use after they have authorized their own caller
// (Changes validates affected Services and finds the owners to notify).
func (s *App) Lookup(ctx context.Context, ids []string) (map[string]Service, error) {
	if len(ids) > MaxLookupIDs {
		return nil, invalid("at most %d services can be looked up at once", MaxLookupIDs)
	}
	norm := make([]string, 0, len(ids))
	for _, id := range ids {
		if uuidPattern.MatchString(id) {
			norm = append(norm, strings.ToLower(id))
		}
	}
	found, err := s.store.ByIDs(ctx, norm)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Service, len(found))
	for _, v := range found {
		out[v.ID] = v
	}
	return out, nil
}
