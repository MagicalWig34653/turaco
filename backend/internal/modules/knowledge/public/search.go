package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// ArticleSearcher is the platform Searcher of Knowledge. It runs the module's own List as the caller, so audience
// and status rules are the article list's: published articles only (drafts and retired articles are not offered in
// a global search, even to authors), employee-audience articles for everybody, internal ones with knowledge.view.
type ArticleSearcher struct{ svc *application.Service }

// NewArticleSearcher builds the Searcher.
func NewArticleSearcher(svc *application.Service) ArticleSearcher { return ArticleSearcher{svc: svc} }

func (ArticleSearcher) Type() string   { return "knowledge" }
func (ArticleSearcher) Module() string { return "knowledge" }

func (s ArticleSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.List(ctx, application.Principal{UserID: p.UserID, View: p.Has("knowledge.view")}, q, application.StatusPublished, application.Page{Limit: limit})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	hits := make([]search.Hit, 0, len(res.Items))
	for _, a := range res.Items {
		hits = append(hits, search.Hit{ID: a.ID, Reference: a.Reference, Title: a.Title, Exact: search.ExactReference(q, a.Reference)})
	}
	return hits, nil
}
