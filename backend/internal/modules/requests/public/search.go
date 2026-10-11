// Package public is the Requests module's public contract.
package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// RequestSearcher is the platform Searcher of Requests. Holders of requests.view or requests.manage search all
// Requests; everybody else only the Requests they made or that were made for them.
type RequestSearcher struct{ svc *application.Service }

// NewRequestSearcher builds the Searcher.
func NewRequestSearcher(svc *application.Service) RequestSearcher { return RequestSearcher{svc: svc} }

func (RequestSearcher) Type() string   { return "request" }
func (RequestSearcher) Module() string { return "requests" }

func (s RequestSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	ap := application.Principal{UserID: p.UserID, View: p.Has("requests.view"), Manage: p.Has("requests.manage")}
	res, err := s.svc.Search(ctx, ap, ap.View || ap.Manage, q, application.Page{Limit: limit})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	var hits []search.Hit
	for _, r := range res.Items {
		hits = append(hits, search.Hit{ID: r.ID, Reference: r.Reference, Title: r.CatalogItemTitle, Subtitle: r.Status, Exact: search.ExactReference(q, r.Reference)})
	}
	return hits, nil
}
