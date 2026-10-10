package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// AssetSearcher is the platform Searcher of Assets. It runs the asset list search as the caller (assets.view or
// assets.manage; device hostnames only with endpoints.view), so the result equals what the asset list would show.
type AssetSearcher struct{ svc *application.Service }

// NewAssetSearcher builds the Searcher.
func NewAssetSearcher(svc *application.Service) AssetSearcher { return AssetSearcher{svc: svc} }

func (AssetSearcher) Type() string   { return "asset" }
func (AssetSearcher) Module() string { return "assets" }

func (s AssetSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.List(ctx, application.Principal{UserID: p.UserID, View: p.Has("assets.view"), Manage: p.Has("assets.manage"), Hostnames: p.Has("endpoints.view")},
		application.Filter{Query: q, Page: application.Page{Limit: limit}})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	hits := make([]search.Hit, 0, len(res.Items))
	for _, a := range res.Items {
		h := search.Hit{ID: a.ID, Reference: a.Reference, Title: a.Reference, Exact: search.ExactReference(q, a.Reference)}
		switch {
		case a.AssetTag != nil && *a.AssetTag != "":
			h.Subtitle = *a.AssetTag
		case a.SerialNumber != nil:
			h.Subtitle = *a.SerialNumber
		}
		hits = append(hits, h)
	}
	return hits, nil
}
