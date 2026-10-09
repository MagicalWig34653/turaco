package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query/querytest"
)

func qNode(field string, op query.Op, value any) query.Node { return query.Cond(field, op, value) }

func qFilter(n ...query.Node) *query.Filter {
	return &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "and", Children: n}}
}

func qIDs(page query.Page[application.Ticket]) map[string]bool {
	out := map[string]bool{}
	for _, tk := range page.Items {
		out[tk.ID] = true
	}
	return out
}

func TestTicketCatalogMatchesTheSchema(t *testing.T) {
	e := newEnv(t)
	querytest.CheckSchema(t, e.pool, application.TicketCatalog())
}

func TestTicketQueryScopeIsNotWidenableAndNoIDOR(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mine := e.raise()
	theirs, err := e.svc.Create(ctx, e.c(e.bob), application.Principal{UserID: e.bob}, application.CreateInput{Title: "Bob's printer"})
	if err != nil {
		t.Fatal(err)
	}
	all := query.Request{Limit: 100}
	// An employee sees only own tickets, whatever the filter says.
	for name, f := range map[string]*query.Filter{
		"no filter":      nil,
		"other reporter": qFilter(qNode("reporter", query.OpEquals, e.bob)),
		"or true":        {V: 1, Root: &query.Node{Type: "group", Logic: "or", Children: []query.Node{qNode("reporter", query.OpEquals, e.bob), qNode("status", query.OpNotEquals, "closed")}}},
		"not equals me":  qFilter(qNode("reporter", query.OpNotEquals, e.alice)),
		"by reference":   qFilter(qNode("reference", query.OpEquals, theirs.Reference)),
	} {
		req := all
		req.Filter = f
		page, err := e.svc.Query(ctx, e.user(), req, false, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := qIDs(page); got[theirs.ID] || (name == "no filter" && !got[mine.ID]) {
			t.Errorf("%s: leaked or missing rows %v", name, got)
		}
		if name == "by reference" && len(page.Items) != 0 {
			t.Error("a reference probe must not reveal another user's ticket")
		}
	}
	// Employees cannot use the all-scope.
	if _, err := e.svc.Query(ctx, e.user(), all, true, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("all scope for an employee: %v", err)
	}
	// Staff see both.
	page, err := e.svc.Query(ctx, e.staff(), all, true, nil)
	if err != nil || !qIDs(page)[mine.ID] || !qIDs(page)[theirs.ID] {
		t.Errorf("staff: %v %v", qIDs(page), err)
	}
	// Staff on the own-scope still see only their own tickets.
	page, err = e.svc.Query(ctx, e.staff(), all, false, nil)
	if err != nil || qIDs(page)[mine.ID] || qIDs(page)[theirs.ID] {
		t.Errorf("staff own scope: %v %v", qIDs(page), err)
	}
	// Counts use the same scope.
	req := all
	req.Count = true
	page, err = e.svc.Query(ctx, e.user(), req, false, nil)
	if err != nil || page.Count == nil || *page.Count != 1 {
		t.Errorf("employee count must be 1: %v %v", page.Count, err)
	}
}

func TestTicketRoutingFieldsAreAbsentForEmployees(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	if _, err := e.svc.Transition(ctx, e.c(e.agent), e.staff(), tk.ID, &tk.Version, application.OpStart, application.Params{}); err != nil {
		t.Fatal(err)
	}
	fields := func(p application.Principal) map[string]bool {
		info, err := e.svc.QueryFields(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, f := range info.Fields {
			out[f.Key] = true
		}
		return out
	}
	emp, staff := fields(e.user()), fields(e.staff())
	for _, k := range []string{"queue", "routing_team", "assignee"} {
		if emp[k] || !staff[k] {
			t.Errorf("field %s: employee=%v staff=%v", k, emp[k], staff[k])
		}
	}
	// The probe an employee could use to learn whether a ticket is assigned fails exactly like an unknown field.
	probe := func(field string) string {
		_, err := e.svc.Query(ctx, e.user(), query.Request{Filter: qFilter(qNode(field, query.OpIsEmpty, nil))}, false, nil)
		raw, _ := json.Marshal(err.Error())
		return string(raw)
	}
	if probe("assignee") != probe("nonexistent") || probe("queue") != probe("nonexistent") || probe("routing_team") != probe("nonexistent") {
		t.Errorf("routing fields answer differently from unknown fields: %s %s", probe("assignee"), probe("nonexistent"))
	}
	if _, err := e.svc.Query(ctx, e.user(), query.Request{Sort: []query.SortSpec{{Field: "assignee", Dir: "asc"}}}, false, nil); err == nil {
		t.Error("employee sorted by assignee")
	}
	// The plain parameters keep their old meaning: an employee's routing filters are ignored, staff's apply.
	compat, _ := e.svc.CompatNodes(ctx, e.user(), application.Filter{AssigneeID: e.agent, QueueID: e.team})
	if len(compat) != 0 {
		t.Errorf("employee compat nodes %v", compat)
	}
	byAgent, _ := e.svc.CompatNodes(ctx, e.staff(), application.Filter{AssigneeID: e.agent})
	page, err := e.svc.Query(ctx, e.staff(), query.Request{Limit: 100}, true, byAgent)
	if err != nil || !qIDs(page)[tk.ID] {
		t.Errorf("staff filter by assignee: %v %v", qIDs(page), err)
	}
	closed, _ := e.svc.CompatNodes(ctx, e.staff(), application.Filter{Status: "closed"})
	page, err = e.svc.Query(ctx, e.staff(), query.Request{Limit: 100}, true, closed)
	if err != nil || qIDs(page)[tk.ID] {
		t.Errorf("status compat: %v %v", qIDs(page), err)
	}
	// Employees never receive the queue in results.
	page, _ = e.svc.Query(ctx, e.user(), query.Request{Limit: 10}, false, nil)
	for _, it := range page.Items {
		if it.QueueTeamID != nil {
			t.Error("queue disclosed to an employee")
		}
	}
}

func TestTicketQueryKeysetOverRealTickets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		e.raise()
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := e.svc.Query(ctx, e.user(), query.Request{Limit: 3, Cursor: cursor, Sort: []query.SortSpec{{Field: "priority", Dir: "asc"}, {Field: "created_at", Dir: "desc"}}}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tk := range page.Items {
			if seen[tk.ID] {
				t.Fatalf("duplicate %s", tk.ID)
			}
			seen[tk.ID] = true
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 7 {
		t.Errorf("paged %d of 7 tickets", len(seen))
	}
}

func TestTicketDefaultQueriesUseIndexes(t *testing.T) {
	e := newEnv(t)
	plan := mustPrepare(t, e, query.Request{Limit: 50})
	sql, args := plan.Statement(query.Select{Columns: "t.id"})
	querytest.ExplainUsesIndex(t, e.pool, sql, args)
}

func mustPrepare(t *testing.T, e *env, req query.Request) *query.Plan {
	t.Helper()
	plan, err := query.NewEphemeralEngine().Prepare(application.TicketCatalog(), query.Subject{UserID: e.agent, Permissions: map[string]bool{"tickets.view": true}}, req, "all", query.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
