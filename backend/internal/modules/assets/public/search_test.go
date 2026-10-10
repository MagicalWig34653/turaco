package public

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// Without assets.view or assets.manage the source is skipped before the store is touched (nil here).
func TestAssetSearcherNeedsAssetRights(t *testing.T) {
	s := NewAssetSearcher(application.NewService(nil, nil, nil, nil))
	_, err := s.Search(context.Background(), authorization.Principal{UserID: "u"}, "kis-ws", 5)
	if !errors.Is(err, search.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
}
