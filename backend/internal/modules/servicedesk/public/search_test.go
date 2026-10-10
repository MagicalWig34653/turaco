package public

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// A caller who may not read Problems is skipped before any record is loaded (the store is nil here).
func TestProblemSearcherNeedsStaffRights(t *testing.T) {
	s := NewProblemSearcher(application.NewProblemService(nil, nil))
	_, err := s.Search(context.Background(), authorization.Principal{UserID: "u"}, "etikett", 5)
	if !errors.Is(err, search.ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if s.Type() != "problem" || s.Module() != "servicedesk" {
		t.Errorf("identity %s/%s", s.Type(), s.Module())
	}
}
