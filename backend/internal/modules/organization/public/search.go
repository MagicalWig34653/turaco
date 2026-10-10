package public

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// UserSearcher is the platform Searcher of Users. It needs organization.view and runs the User list query of the
// Organization module (display name and e-mail address, active Users only), so HR fields never appear: a hit
// carries the name and the primary e-mail address, which the User list shows to every holder of organization.view.
type UserSearcher struct{ queries *application.Queries }

// NewUserSearcher builds the Searcher.
func NewUserSearcher(q *application.Queries) UserSearcher { return UserSearcher{queries: q} }

func (UserSearcher) Type() string   { return "user" }
func (UserSearcher) Module() string { return "organization" }

func (s UserSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	if !p.Has("organization.view") {
		return nil, search.ErrNotAllowed
	}
	if utf8.RuneCountInString(q) < query.MinSearchLength { // the trigram index serves three characters or more
		return nil, nil
	}
	page, err := s.queries.Users(ctx, application.QueryPrincipal{UserID: p.UserID, ViewDetails: p.Has("organization.users.view_details")},
		query.Request{Filter: query.And(nil, query.Cond("status", query.OpEquals, "active")), Search: q, Limit: limit})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	hits := make([]search.Hit, 0, len(page.Items))
	for _, u := range page.Items {
		h := search.Hit{ID: u.ID, Title: u.DisplayName}
		if u.PrimaryEmail != nil {
			h.Subtitle = *u.PrimaryEmail
		}
		hits = append(hits, h)
	}
	return hits, nil
}
