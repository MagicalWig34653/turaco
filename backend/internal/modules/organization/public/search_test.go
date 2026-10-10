package public

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// Without organization.view the source is skipped before any query runs (nil Queries here); a query shorter than
// the trigram minimum finds nothing instead of failing.
func TestUserSearcherNeedsOrganizationView(t *testing.T) {
	s := NewUserSearcher(nil)
	_, err := s.Search(context.Background(), authorization.Principal{UserID: "u"}, "anna", 5)
	if !errors.Is(err, search.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	p := authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"organization.view": {}}}
	if hits, err := s.Search(context.Background(), p, "an", 5); err != nil || len(hits) != 0 {
		t.Errorf("short query: %v %v", hits, err)
	}
}
