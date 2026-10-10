package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// recordScan bounds how many of the newest Changes are inspected: the Change store has no text index, so the
// module's authorized list is read once and matched in memory.
const recordScan = 200

// ChangeSearcher is the platform Searcher of Changes. The list applies the module's visibility: changes.view,
// manage or execute read all Changes, everybody else only the Changes they requested or own.
type ChangeSearcher struct{ svc *application.Service }

// NewChangeSearcher builds the Searcher.
func NewChangeSearcher(svc *application.Service) ChangeSearcher { return ChangeSearcher{svc: svc} }

func (ChangeSearcher) Type() string   { return "change" }
func (ChangeSearcher) Module() string { return "changes" }

func (s ChangeSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.List(ctx, application.Principal{UserID: p.UserID, View: p.Has("changes.view"), Manage: p.Has("changes.manage"), Execute: p.Has("changes.execute")},
		application.Filter{Page: application.Page{Limit: recordScan}})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	var hits []search.Hit
	for _, c := range res.Items {
		if len(hits) == limit {
			break
		}
		if search.Match(q, c.Reference, c.Title) {
			hits = append(hits, search.Hit{ID: c.ID, Reference: c.Reference, Title: c.Title, Subtitle: c.Status, Exact: search.ExactReference(q, c.Reference)})
		}
	}
	return hits, nil
}
