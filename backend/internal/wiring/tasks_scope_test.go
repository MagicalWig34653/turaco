package wiring_test

import (
	"context"
	"errors"
	"testing"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/scopetest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// tasksSubject drives the Task module through its real application layer over a real Location tree.
type tasksSubject struct {
	locFixture
	svc *tasksapp.Service
	adm tasksapp.Principal
}

func (s *tasksSubject) caller(userID string) tasksapp.Caller {
	return tasksapp.Caller{Actor: audit.UserActor(userID), CorrelationID: "corr-" + s.pfx}
}

func taskPrincipal(p authorization.Principal) tasksapp.Principal {
	return tasksapp.Principal{UserID: p.UserID, ViewAll: p.Has("tasks.view"), Manage: p.Has("tasks.manage"),
		ViewScope: p.ScopeOf("tasks.view"), ManageScope: p.ScopeOf("tasks.manage")}
}

func (s *tasksSubject) Create(t *testing.T, locationID string) string {
	t.Helper()
	in := tasksapp.CreateInput{Title: s.pfx + " task"}
	v, err := s.svc.Create(context.Background(), s.caller(s.adm.UserID), s.adm, in)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if locationID != "" {
		// Privileged placement: the fixture also needs tasks at an inactive Location.
		if _, err := s.pool.Exec(context.Background(), `UPDATE platform.tasks SET location_id = $2::uuid WHERE id = $1::uuid`, v.ID, locationID); err != nil {
			t.Fatalf("place task: %v", err)
		}
	}
	return v.ID
}

func (s *tasksSubject) List(t *testing.T, p authorization.Principal) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := s.svc.Query(context.Background(), taskPrincipal(p), query.Request{Search: s.pfx, Limit: 50, Cursor: cursor}, false, nil)
		if errors.Is(err, tasksapp.ErrForbidden) {
			return nil
		}
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, v := range page.Items {
			ids = append(ids, v.ID)
		}
		if page.NextCursor == "" {
			return ids
		}
		cursor = page.NextCursor
	}
}

func (s *tasksSubject) Count(t *testing.T, p authorization.Principal) int {
	t.Helper()
	page, err := s.svc.Query(context.Background(), taskPrincipal(p), query.Request{Search: s.pfx, Count: true, Limit: 5}, false, nil)
	if errors.Is(err, tasksapp.ErrForbidden) {
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

func (s *tasksSubject) Get(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	_, err := s.svc.Get(context.Background(), taskPrincipal(p), id)
	if err != nil && !errors.Is(err, tasksapp.ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	return err == nil
}

func (s *tasksSubject) version(t *testing.T, id string) *int {
	t.Helper()
	v, err := s.svc.Get(context.Background(), s.adm, id)
	if err != nil {
		t.Fatal(err)
	}
	return &v.Version
}

func (s *tasksSubject) Mutate(t *testing.T, p authorization.Principal, id string) bool {
	t.Helper()
	// The title stays (the harness finds the fixtures by it); the priority toggles.
	priority := tasksapp.PriorityHigh
	if cur, err := s.svc.Get(context.Background(), s.adm, id); err == nil && cur.Priority == tasksapp.PriorityHigh {
		priority = tasksapp.PriorityLow
	}
	_, err := s.svc.UpdateDetails(context.Background(), s.caller(p.UserID), taskPrincipal(p), id, s.version(t, id), tasksapp.UpdateInput{Priority: &priority})
	if err != nil && !errors.Is(err, tasksapp.ErrForbidden) && !errors.Is(err, tasksapp.ErrNotFound) {
		t.Fatalf("mutate: %v", err)
	}
	return err == nil
}

func (s *tasksSubject) Move(t *testing.T, p authorization.Principal, id, locationID string) bool {
	t.Helper()
	var loc *string
	if locationID != "" {
		loc = &locationID
	}
	_, err := s.svc.SetLocation(context.Background(), s.caller(p.UserID), taskPrincipal(p), id, s.version(t, id), loc)
	if err != nil && !errors.Is(err, tasksapp.ErrForbidden) && !errors.Is(err, tasksapp.ErrNotFound) && !errors.Is(err, tasksapp.ErrLocationOutOfScope) {
		t.Fatalf("move: %v", err)
	}
	return err == nil
}

func (s *tasksSubject) CreateAs(t *testing.T, p authorization.Principal, locationID string) bool {
	t.Helper()
	in := tasksapp.CreateInput{Title: s.pfx + " created"}
	if locationID != "" {
		in.LocationID = &locationID
	}
	_, err := s.svc.Create(context.Background(), s.caller(p.UserID), taskPrincipal(p), in)
	if err != nil && !errors.Is(err, tasksapp.ErrForbidden) {
		t.Fatalf("create as: %v", err)
	}
	return err == nil
}

// The Task module's adversarial Location scope run (ADR-0040, slice L-G).
func TestTasksLocationScope(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	org := orgrepository.New(pool)
	s := &tasksSubject{locFixture: locFixture{pool: pool, org: org, pfx: "TKS" + hex8()},
		svc: tasksapp.NewService(tasksrepository.New(pool), orgpublic.NewWorkDirectory(org), nil).WithQueryEngine(wiring.QueryEngine(pool))}
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&s.adm.UserID); err != nil {
		t.Fatal(err)
	}
	s.adm.ViewAll, s.adm.Manage = true, true
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, "corr-"+s.pfx)
		_, _ = pool.Exec(c, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, "corr-"+s.pfx)
		_, _ = pool.Exec(c, `DELETE FROM platform.tasks WHERE title LIKE $1 || '%'`, s.pfx)
		for i := 0; i < 5; i++ {
			_, _ = pool.Exec(c, `DELETE FROM organization.locations WHERE name LIKE $1 || '%' AND id NOT IN (SELECT parent_location_id FROM organization.locations WHERE parent_location_id IS NOT NULL)`, s.pfx)
		}
	})
	scopetest.Run(t, scopetest.Config{View: "tasks.view", Manage: "tasks.manage"}, s)
}
