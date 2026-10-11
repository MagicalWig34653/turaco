package transport_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type overviewBody struct {
	Totals struct{ Open, Unassigned, Waiting int }
	Queues []struct {
		Queue      struct{ ID string }
		Level      string
		Open       int
		Unassigned int
		Waiting    int
	}
	Assignees []struct {
		UserID string
		Open   int
	}
	OldestUnassigned []struct{ ID, Title string }
	OldestWaiting    []struct{ ID, Title string }
}

func createTicketIn(t *testing.T, h http.Handler, queueID, title string) string {
	t.Helper()
	rec := do(h, "POST", "/api/v1/tickets", `{"title":"`+title+`","queueId":"`+queueID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q = %d %s", title, rec.Code, rec.Body)
	}
	var tk struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	return tk.ID
}

// TestTicketOverviewOverHTTP covers the manager overview: counts per Queue and assignee for the viewable Queues
// only, never the Tickets of a Queue the caller may not view, and a clean refusal for people who view none.
func TestTicketOverviewOverHTTP(t *testing.T) {
	admin := serve(t, as(agent, "servicedesk.queues.manage", "tickets.manage", "tickets.view"))
	staff := serve(t, as(agent, "tickets.manage", "tickets.view"))
	employee := serve(t, as(alice))
	lead := serve(t, as(bob)) // a view grant on one Queue only

	pub := createQueue(t, admin, "public")
	hidden := createQueue(t, admin, "internal")

	assigned := createTicketIn(t, staff, pub.ID, "ovw-assigned")
	waiting := createTicketIn(t, staff, pub.ID, "ovw-waiting")
	createTicketIn(t, staff, pub.ID, "ovw-unassigned")
	createTicketIn(t, staff, hidden.ID, "ovw-secret-desk")
	// A ticket the lead reported themselves in the hidden desk must not be counted either.
	createTicketIn(t, lead, pub.ID, "ovw-own-by-lead")

	for _, id := range []string{assigned, waiting} {
		if rec := do(staff, "POST", "/api/v1/tickets/"+id+"/assign", `{"assigneeId":"`+agent+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("assign = %d %s", rec.Code, rec.Body)
		}
	}
	if rec := do(staff, "POST", "/api/v1/tickets/"+waiting+"/wait", `{"reason":"customer"}`); rec.Code != http.StatusOK {
		t.Fatalf("wait = %d %s", rec.Code, rec.Body)
	}

	// Denials: a requester without any Queue view, and an anonymous caller.
	if rec := do(employee, "GET", "/api/v1/tickets/overview", ""); rec.Code != http.StatusForbidden {
		t.Errorf("requester overview = %d, want 403", rec.Code)
	}
	if rec := do(lead, "GET", "/api/v1/tickets/overview", ""); rec.Code != http.StatusForbidden {
		t.Errorf("overview without a Queue view = %d, want 403", rec.Code)
	}
	if rec := do(serve(t, fakeAuth{}), "GET", "/api/v1/tickets/overview", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous overview = %d, want 401", rec.Code)
	}

	// A view grant opens exactly that Queue.
	rec := do(admin, "PUT", "/api/v1/service-desk/queues/"+pub.ID+"/grants", `{"expectedVersion":`+itoa(pub.Version)+`,"grants":[{"subjectType":"user","subjectId":"`+bob+`","level":"view"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant = %d %s", rec.Code, rec.Body)
	}
	rec = do(lead, "GET", "/api/v1/tickets/overview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lead overview = %d %s", rec.Code, rec.Body)
	}
	for _, leak := range []string{hidden.ID, hidden.Prefix, "ovw-secret-desk"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("overview of a Queue-scoped lead leaks %q: %s", leak, rec.Body)
		}
	}
	var o overviewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Queues) != 1 || o.Queues[0].Queue.ID != pub.ID || o.Queues[0].Level != "view" {
		t.Fatalf("queues = %+v", o.Queues)
	}
	// Four open tickets in the Queue: assigned, waiting, unassigned and the lead's own.
	if got := o.Queues[0]; got.Open != 4 || got.Unassigned != 2 || got.Waiting != 1 {
		t.Errorf("queue counts = %+v", got)
	}
	if o.Totals.Open != 4 || o.Totals.Unassigned != 2 || o.Totals.Waiting != 1 {
		t.Errorf("totals = %+v", o.Totals)
	}
	if len(o.Assignees) != 1 || o.Assignees[0].UserID != agent || o.Assignees[0].Open != 2 {
		t.Errorf("assignees = %+v", o.Assignees)
	}
	if len(o.OldestUnassigned) != 2 || o.OldestUnassigned[0].Title != "ovw-unassigned" {
		t.Errorf("oldest unassigned = %+v", o.OldestUnassigned)
	}
	if len(o.OldestWaiting) != 1 || o.OldestWaiting[0].ID != waiting {
		t.Errorf("oldest waiting = %+v", o.OldestWaiting)
	}

	// A global viewer sees the hidden desk as well, with the level the global permission implies.
	rec = do(staff, "GET", "/api/v1/tickets/overview", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("staff overview = %d %s", rec.Code, rec.Body)
	}
	o = overviewBody{}
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	found := map[string]int{}
	for _, q := range o.Queues {
		found[q.Queue.ID] = q.Open
	}
	if found[pub.ID] != 4 || found[hidden.ID] != 1 {
		t.Errorf("staff queue counts = %+v", found)
	}
}
