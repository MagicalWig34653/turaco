package application

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// LookupHit is one candidate of the affected-resource picker.
type LookupHit struct {
	Type      string
	ID        string
	Reference string
	Name      string
	// Detail is a short secondary text: criticality of a Service, state of a Virtual Machine, product and status
	// of an Asset.
	Detail string
}

// hitSearcher is the optional capability of the Services, Infrastructure and Assets ports behind the
// affected-resource lookup. A port that does not provide it finds nothing for its type.
type hitSearcher interface {
	Search(ctx context.Context, text string, limit int) ([]LookupHit, error)
}

const (
	maxLookupText  = 100
	maxLookupLimit = 50
)

// LookupAffected finds the resources a Change may affect (Service, Virtual Machine, Asset) by text, for the picker
// of the change wizard. It needs the same visibility as adding the resource: a type the caller may not see answers
// with no hits at all (the same answer as for nothing found, so hidden records are never confirmed). Text that is
// empty or longer than 100 characters is refused; at most limit (default 20, at most 50) hits.
func (s *Service) LookupAffected(ctx context.Context, p Principal, targetType, text string, limit int) ([]LookupHit, error) {
	if !p.Manage {
		return nil, ErrForbidden
	}
	if !oneOf(targetType, []string{NodeService, NodeVM, NodeAsset}) {
		return nil, invalid("type must be one of %s, %s, %s", NodeService, NodeVM, NodeAsset)
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxLookupText || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, invalid("q must be 1 to %d characters", maxLookupText)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxLookupLimit {
		limit = maxLookupLimit
	}
	if p.hides(targetType) {
		return []LookupHit{}, nil
	}
	var port any
	switch targetType {
	case NodeService:
		port = s.services
	case NodeVM:
		port = s.infra
	case NodeAsset:
		port = s.assets
	}
	sr, ok := port.(hitSearcher)
	if !ok {
		return []LookupHit{}, nil
	}
	hits, err := sr.Search(ctx, text, limit)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", targetType, err)
	}
	return hits, nil
}
