package public

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// recordScan bounds how many of the newest records problem and incident searches inspect: their stores have no
// text index, so the module's authorized list is read once and matched in memory.
const recordScan = 200

// TicketSearcher is the platform Searcher of Tickets. It uses the same query as the ticket list (reference, title,
// device and person text; visibility by own tickets, Queue view and grants; unknown Queue numbers redacted), and a
// reference or earlier number the caller may know leads the result.
type TicketSearcher struct{ svc *application.Service }

// NewTicketSearcher builds the Searcher.
func NewTicketSearcher(svc *application.Service) TicketSearcher { return TicketSearcher{svc: svc} }

func (TicketSearcher) Type() string   { return "ticket" }
func (TicketSearcher) Module() string { return "servicedesk" }

func searchTicketPrincipal(p authorization.Principal) application.Principal {
	return application.Principal{UserID: p.UserID, View: p.Has("tickets.view"), Manage: p.Has("tickets.manage"),
		QueuesManage: p.Has(application.PermQueuesManage),
		ChangesView:  p.Has("changes.view") || p.Has("changes.manage") || p.Has("changes.execute")}
}

func (s TicketSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	ap := searchTicketPrincipal(p)
	var hits []search.Hit
	seen := map[string]bool{}
	if m, err := s.svc.FindByReference(ctx, ap, q); err == nil {
		hits = append(hits, search.Hit{ID: m.TicketID, Reference: m.Reference, Exact: true})
		seen[m.TicketID] = true
	} else if !errors.Is(err, application.ErrNotFound) {
		return nil, err
	}
	if utf8.RuneCountInString(q) >= query.MinSearchLength {
		page, err := s.svc.QueryScoped(ctx, ap, query.Request{Search: q, Limit: limit}, application.ScopeAuto, nil, "")
		if err != nil {
			return nil, err
		}
		for _, t := range page.Items {
			if seen[t.ID] {
				continue
			}
			hits = append(hits, search.Hit{ID: t.ID, Reference: t.Reference, Title: t.Title, Subtitle: t.Status})
		}
	}
	// The exact hit has no title yet; load it through the same authorized detail read.
	for i := range hits {
		if hits[i].Title == "" {
			d, err := s.svc.Get(ctx, ap, hits[i].ID)
			if err != nil {
				return nil, err
			}
			hits[i].Title, hits[i].Subtitle = d.Ticket.Title, d.Ticket.Status
		}
	}
	return hits, nil
}

// ProblemSearcher is the platform Searcher of Problems (staff and problems.manage, like the Problem list).
type ProblemSearcher struct{ svc *application.ProblemService }

// NewProblemSearcher builds the Searcher.
func NewProblemSearcher(svc *application.ProblemService) ProblemSearcher {
	return ProblemSearcher{svc: svc}
}

func (ProblemSearcher) Type() string   { return "problem" }
func (ProblemSearcher) Module() string { return "servicedesk" }

func (s ProblemSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.List(ctx, application.ProblemPrincipal{UserID: p.UserID, Staff: p.Has("tickets.view") || p.Has("tickets.manage"), Manage: p.Has("problems.manage")},
		"", application.Page{Limit: recordScan})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	var hits []search.Hit
	for _, pr := range res.Items {
		if len(hits) == limit {
			break
		}
		if search.Match(q, pr.Reference, pr.Title) {
			hits = append(hits, search.Hit{ID: pr.ID, Reference: pr.Reference, Title: pr.Title, Subtitle: pr.Status, Exact: search.ExactReference(q, pr.Reference)})
		}
	}
	return hits, nil
}

// MajorIncidentSearcher is the platform Searcher of Major Incidents (visible to every signed-in User; drills are
// left out of a global search).
type MajorIncidentSearcher struct{ svc *application.MajorService }

// NewMajorIncidentSearcher builds the Searcher.
func NewMajorIncidentSearcher(svc *application.MajorService) MajorIncidentSearcher {
	return MajorIncidentSearcher{svc: svc}
}

func (MajorIncidentSearcher) Type() string   { return "major_incident" }
func (MajorIncidentSearcher) Module() string { return "servicedesk" }

func (s MajorIncidentSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.List(ctx, p.UserID, false, false, application.Page{Limit: recordScan})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	var hits []search.Hit
	for _, m := range res.Items {
		if len(hits) == limit {
			break
		}
		if search.Match(q, m.Reference, m.Title) {
			hits = append(hits, search.Hit{ID: m.ID, Reference: m.Reference, Title: m.Title, Subtitle: m.Status, Exact: search.ExactReference(q, m.Reference)})
		}
	}
	return hits, nil
}
