package wiring_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/scopetest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// ticketsSubject drives the Service Desk through its real application layer over a real Location tree. A ticket's
// scope Location is its affected Location.
type ticketsSubject struct {
	locFixture
	svc      *servicedeskapp.Service
	reporter string
	staff    servicedeskapp.Principal
}

func (s *ticketsSubject) caller() servicedeskapp.Caller {
	return servicedeskapp.Caller{Actor: audit.UserActor(s.reporter), CorrelationID: "corr-" + s.pfx}
}

func ticketPrincipal(p authorization.Principal) servicedeskapp.Principal {
	return servicedeskapp.Principal{UserID: p.UserID, View: p.Has("tickets.view"), Manage: p.Has("tickets.manage"),
		ViewScope: p.ScopeOf("tickets.view"), ManageScope: p.ScopeOf("tickets.manage")}
}

func (s *ticketsSubject) Create(t *testing.T, locationID string) string {
	t.Helper()
	ctx := context.Background()
	tk, err := s.svc.Create(ctx, s.caller(), s.staff, servicedeskapp.CreateInput{Title: s.pfx + " ticket"})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if locationID != "" {
		// The Location is set with privileged SQL: the fixture needs tickets at an inactive Location too.
		if _, err := s.pool.Exec(ctx, `UPDATE servicedesk.tickets SET affected_location_id = $2::uuid WHERE id = $1::uuid`, tk.ID, locationID); err != nil {
			t.Fatalf("place ticket: %v", err)
		}
	} else if _, err := s.pool.Exec(ctx, `UPDATE servicedesk.tickets SET affected_location_id = NULL WHERE id = $1::uuid`, tk.ID); err != nil {
		t.Fatalf("clear ticket location: %v", err)
	}
	return tk.ID
}

func (s *ticketsSubject) List(t *testing.T, p authorization.Principal) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := s.svc.QueryScoped(context.Background(), ticketPrincipal(p), query.Request{Search: s.pfx, Limit: 50, Cursor: cursor}, servicedeskapp.ScopeAll, nil, "")
		if errors.Is(err, servicedeskapp.ErrForbidden) {
			return nil
		}
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, tk := range page.Items {
			ids = append(ids, tk.ID)
		}
		if page.NextCursor == "" {
			return ids
		}
		cursor = page.NextCursor
	}
}

func (s *ticketsSubject) Count(t *testing.T, p authorization.Principal) int {
	t.Helper()
	page, err := s.svc.QueryScoped(context.Background(), ticketPrincipal(p), query.Request{Search: s.pfx, Count: true, Limit: 5}, servicedeskapp.ScopeAll, nil, "")
	if errors.Is(err, servicedeskapp.ErrForbidden) {
		return 0
	}
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if page.Count == nil {
		t.Fatal("query returned no count")
	}
	return *page.Count
}

func (s *ticketsSubject) Get(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	_, err := s.svc.Get(context.Background(), ticketPrincipal(p), id)
	if err != nil && !errors.Is(err, servicedeskapp.ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	return err == nil
}

func (s *ticketsSubject) version(t *testing.T, id string) *int {
	t.Helper()
	d, err := s.svc.Get(context.Background(), s.staff, id)
	if err != nil {
		t.Fatal(err)
	}
	v := d.Ticket.Version
	return &v
}

func (s *ticketsSubject) Mutate(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	priority := "high"
	if d, err := s.svc.Get(context.Background(), s.staff, id); err == nil && d.Ticket.Priority == "high" {
		priority = "normal"
	}
	_, err := s.svc.SetPriority(context.Background(), servicedeskapp.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "corr-" + s.pfx}, ticketPrincipal(p), id, s.version(t, id), priority)
	if err != nil && !errors.Is(err, servicedeskapp.ErrForbidden) && !errors.Is(err, servicedeskapp.ErrNotFound) {
		t.Fatalf("mutate: %v", err)
	}
	return err == nil
}

func (s *ticketsSubject) Move(t *testing.T, p authorization.Principal, id, locationID string) bool {
	t.Helper()
	var loc *string
	if locationID != "" {
		loc = &locationID
	}
	_, err := s.svc.SetLocation(context.Background(), servicedeskapp.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "corr-" + s.pfx}, ticketPrincipal(p), id, s.version(t, id), loc)
	if err != nil && !errors.Is(err, servicedeskapp.ErrForbidden) && !errors.Is(err, servicedeskapp.ErrNotFound) && !errors.Is(err, servicedeskapp.ErrLocationOutOfScope) {
		t.Fatalf("move: %v", err)
	}
	return err == nil
}

// The Service Desk's adversarial Location scope run (ADR-0040, slice L-D): the ticket query, its capped count, the
// legacy list, detail, mutation and Location change apply the caller's scope on the ticket's affected Location.
func TestServiceDeskLocationScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	s := &ticketsSubject{locFixture: locFixture{pool: pool, org: orgrepository.New(pool), pfx: "TSC" + hex8()}, svc: wiring.ServiceDesk(pool)}
	// The ticket fixtures need a real active User (the operation checks the reporter).
	if err := pool.QueryRow(ctx, `INSERT INTO organization.users(display_name, primary_email) VALUES ($1, $2) RETURNING id::text`, s.pfx+" reporter", s.pfx+"@example.test").Scan(&s.reporter); err != nil {
		t.Fatal(err)
	}
	s.staff = servicedeskapp.Principal{UserID: s.reporter, View: true, Manage: true}
	t.Cleanup(func() { cleanupTicketScope(pool, s) })
	scopetest.Run(t, scopetest.Config{
		View: "tickets.view", Manage: "tickets.manage",
		Surfaces: []scopetest.Surface{{
			Name: "legacy ticket list",
			Visible: func(t *testing.T, p authorization.Principal) []string {
				res, err := s.svc.List(ctx, ticketPrincipal(p), true, servicedeskapp.Filter{Page: servicedeskapp.Page{Limit: 200}})
				if errors.Is(err, servicedeskapp.ErrForbidden) {
					return nil
				}
				if err != nil {
					t.Fatalf("legacy list: %v", err)
				}
				var ids []string
				for _, tk := range res.Items {
					if tk.Title == s.pfx+" ticket" {
						ids = append(ids, tk.ID)
					}
				}
				return ids
			},
		}},
	}, s)
}

func cleanupTicketScope(pool *pgxpool.Pool, s *ticketsSubject) {
	c := context.Background()
	_, _ = pool.Exec(c, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, "corr-"+s.pfx)
	_, _ = pool.Exec(c, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, "corr-"+s.pfx)
	_, _ = pool.Exec(c, `DELETE FROM servicedesk.reference_registry WHERE ticket_id IN (SELECT id FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid)`, s.reporter)
	_, _ = pool.Exec(c, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, s.reporter)
	_, _ = pool.Exec(c, `DELETE FROM organization.users WHERE id = $1::uuid`, s.reporter)
	for i := 0; i < 5; i++ {
		_, _ = pool.Exec(c, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL)`, s.pfx)
	}
}
