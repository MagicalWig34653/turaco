package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Regression tests of the F13 Q-B/Q-C security review.

func cond(field string, op query.Op, v any) *query.Filter {
	n := query.Cond(field, op, v)
	return &query.Filter{V: 1, Root: &n}
}

func ids(items []application.Ticket) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[it.ID] = true
	}
	return out
}

// A requester whose Ticket moved to an internal desk must not be able to match, order or find it by the internal
// number, neither by filter, search nor sort, nor by looking the number up.
func TestHiddenReferenceCannotBeMatchedByARequester(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	pub, internal := e.queue(application.QueuePublic), e.queue(application.QueueInternal)
	alice := e.employee(e.alice)
	hidden := e.raise(alice, pub)
	visible := e.raise(alice, pub)
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, hidden.ID, &hidden.Version, internal.ID, "misrouted")
	if err != nil {
		t.Fatal(err)
	}
	run := func(p application.Principal, req query.Request) map[string]bool {
		t.Helper()
		req.Limit = 100
		page, err := e.svc.Query(ctx, p, req, false, nil)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		return ids(page.Items)
	}
	for name, req := range map[string]query.Request{
		"equals":   {Filter: cond("reference", query.OpEquals, moved.Reference)},
		"contains": {Filter: cond("reference", query.OpContains, internal.Prefix)},
		"starts":   {Filter: cond("reference", query.OpStartsWith, internal.Prefix)},
		"search":   {Search: internal.Prefix},
		"not":      {Filter: cond("reference", query.OpNotContains, internal.Prefix)},
	} {
		got := run(alice, req)
		if got[hidden.ID] {
			t.Errorf("%s: the Ticket in the internal desk matched for the requester", name)
		}
	}
	// Sorting by reference orders only Tickets whose number the requester may know.
	got := run(alice, query.Request{Sort: []query.SortSpec{{Field: "reference", Dir: "asc"}}})
	if got[hidden.ID] || !got[visible.ID] {
		t.Errorf("sort by reference: %v", got)
	}
	// The visible number still works for the requester, and staff see everything.
	if got := run(alice, query.Request{Filter: cond("reference", query.OpEquals, visible.Reference)}); !got[visible.ID] {
		t.Error("the requester finds a Ticket by a number they know")
	}
	staff, err := e.svc.Query(ctx, e.global, query.Request{Limit: 100, Filter: cond("reference", query.OpEquals, moved.Reference)}, true, nil)
	if err != nil || len(staff.Items) != 1 || staff.Items[0].ID != hidden.ID {
		t.Errorf("staff query = %+v %v", staff.Items, err)
	}
	// Without the reference, the requester still sees the Ticket (shaped: the number they know).
	if got := run(alice, query.Request{}); !got[hidden.ID] {
		t.Error("the requester keeps seeing their own Ticket")
	}
	// The owner cannot confirm the internal number through the lookup either; the answer is the unknown-number answer.
	_, errHidden := e.svc.FindByReference(ctx, alice, moved.Reference)
	_, errUnknown := e.svc.FindByReference(ctx, alice, "ZZZZ-999999")
	if !errors.Is(errHidden, application.ErrNotFound) || errHidden.Error() != errUnknown.Error() {
		t.Errorf("owner resolves the internal number: %v / %v", errHidden, errUnknown)
	}
	if m, err := e.svc.FindByReference(ctx, alice, hidden.Reference); err != nil || m.TicketID != hidden.ID || m.Reference != hidden.Reference {
		t.Errorf("the owner resolves the number they know: %+v %v", m, err)
	}
	// A viewer of the internal desk can resolve its number.
	e.grant(internal, user(e.u1, "view"))
	if m, err := e.svc.FindByReference(ctx, e.employee(e.u1), moved.Reference); err != nil || m.TicketID != hidden.ID {
		t.Errorf("a viewer resolves the desk number: %+v %v", m, err)
	}
}

type capture struct{ intents []notifications.Intent }

func (c *capture) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	c.intents = append(c.intents, in)
	return true, nil
}

// Notification text names the number the recipient may know, not the current number of an internal desk.
func TestNotificationsUseTheReferenceTheRecipientMayKnow(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	pub, internal := e.queue(application.QueuePublic), e.queue(application.QueueInternal)
	e.mem.perms[e.global.UserID] = map[string]struct{}{"tickets.view": {}, "tickets.manage": {}}
	tk := e.raise(e.employee(e.alice), pub)
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &tk.Version, internal.ID, "misrouted")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.ReferenceFor(ctx, e.alice, moved); err != nil || got != tk.Reference {
		t.Errorf("requester reference = %q %v, want %q", got, err, tk.Reference)
	}
	if got, err := e.svc.ReferenceFor(ctx, e.global.UserID, moved); err != nil || got != moved.Reference {
		t.Errorf("staff reference = %q %v, want %q", got, err, moved.Reference)
	}
	n := &capture{}
	cons := application.NewConsumers(repository.New(e.pool), dir{active: map[string]bool{e.alice: true, e.global.UserID: true}}, n).WithReferences(e.svc.ReferenceFor)
	payload, _ := json.Marshal(map[string]any{"ticketId": tk.ID, "internal": false, "authorId": e.global.UserID})
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := cons.OnCommentAdded(ctx, tx, events.OutboxEvent{ID: "ev-" + e.corr, EventType: "TicketCommentAdded", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(n.intents) == 0 {
		t.Fatal("nobody was notified")
	}
	for _, in := range n.intents {
		title, _ := in.Params["title"].(string)
		if strings.Contains(title, internal.Prefix) || !strings.HasPrefix(title, tk.Reference) {
			t.Errorf("notification for %s names %q", in.RecipientUserID, title)
		}
	}
}

// Problems and Major Incidents reach Tickets of every Queue; linking and listing check the User's Queue access.
func TestProblemsAndIncidentsRespectQueueAccessOfTickets(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	hr := e.queue(application.QueueInternal)
	e.mem.perms[e.global.UserID] = map[string]struct{}{"tickets.view": {}, "tickets.manage": {}}
	tk := e.raise(e.global, hr)
	problems := application.NewProblemService(repository.New(e.pool), dir{active: map[string]bool{e.u1: true, e.u2: true, e.global.UserID: true}}).WithTicketAccess(e.svc)
	major := application.NewMajorService(repository.New(e.pool)).WithTicketAccess(e.svc)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.problems WHERE created_by = $1::uuid`, e.global.UserID)
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.major_incidents WHERE declared_by = $1::uuid`, e.global.UserID)
	})
	pr, err := problems.CreateProblem(ctx, e.c(e.global.UserID), application.ProblemPrincipal{UserID: e.global.UserID, Staff: true, Manage: true}, "Printers", "")
	if err != nil {
		t.Fatal(err)
	}
	mi, err := major.Declare(ctx, e.c(e.global.UserID), true, "Print outage", "All printers are down")
	if err != nil {
		t.Fatal(err)
	}
	const unknown = "00000000-0000-7000-8000-000000000009"
	manager := application.ProblemPrincipal{UserID: e.u1, Staff: true, Manage: true}
	// u1 manages problems and incidents but has no access to the desk: same answer as for an unknown Ticket.
	errHidden := problems.LinkTicket(ctx, e.c(e.u1), manager, pr.ID, tk.ID, true)
	errUnknown := problems.LinkTicket(ctx, e.c(e.u1), manager, pr.ID, unknown, true)
	if !errors.Is(errHidden, application.ErrNotFound) || errHidden.Error() != errUnknown.Error() {
		t.Errorf("problem link: %v / %v", errHidden, errUnknown)
	}
	errHidden = major.LinkTicket(ctx, e.c(e.u1), true, mi.ID, tk.ID)
	errUnknown = major.LinkTicket(ctx, e.c(e.u1), true, mi.ID, unknown)
	if !errors.Is(errHidden, application.ErrNotFound) || errHidden.Error() != errUnknown.Error() {
		t.Errorf("major link: %v / %v", errHidden, errUnknown)
	}
	if e.count(`SELECT count(*) FROM servicedesk.problem_tickets WHERE ticket_id = $1::uuid`, tk.ID) != 0 ||
		e.count(`SELECT count(*) FROM servicedesk.tickets WHERE id = $1::uuid AND major_incident_id IS NOT NULL`, tk.ID) != 0 {
		t.Error("a Ticket of a hidden desk was linked")
	}
	// Staff with global ticket permissions link it; the problem then lists it for them but not for u1.
	if err := problems.LinkTicket(ctx, e.c(e.global.UserID), application.ProblemPrincipal{UserID: e.global.UserID, Staff: true, Manage: true}, pr.ID, tk.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := major.LinkTicket(ctx, e.c(e.global.UserID), true, mi.ID, tk.ID); err != nil {
		t.Fatal(err)
	}
	d, err := problems.Get(ctx, manager, pr.ID)
	if err != nil || len(d.Tickets) != 0 || d.Problem.Tickets != 0 {
		t.Errorf("a manager without desk access sees %+v (count %d) %v", d.Tickets, d.Problem.Tickets, err)
	}
	// Counts are shaped like the lists: no hidden count on the detail, the list or a Major Incident.
	counts := func(who string, staff bool) (int, int, int) {
		var pd application.ProblemDetail
		listed := 0
		if staff { // Problems are staff only
			var err error
			if pd, err = problems.Get(ctx, application.ProblemPrincipal{UserID: who, Staff: true}, pr.ID); err != nil {
				t.Fatal(err)
			}
			pl, err := problems.List(ctx, application.ProblemPrincipal{UserID: who, Staff: true}, "", application.Page{})
			if err != nil {
				t.Fatal(err)
			}
			listed = -1
			for _, x := range pl.Items {
				if x.ID == pr.ID {
					listed = x.Tickets
				}
			}
		}
		md, err := major.Get(ctx, who, false, mi.ID)
		if err != nil {
			t.Fatal(err)
		}
		ml, err := major.List(ctx, who, true, application.Page{})
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range ml.Items {
			if x.ID == mi.ID && x.Tickets != md.Incident.Tickets {
				t.Errorf("incident list count %d differs from detail %d", x.Tickets, md.Incident.Tickets)
			}
		}
		if md.Incident.Tickets != len(md.Tickets) || pd.Problem.Tickets != len(pd.Tickets) {
			t.Errorf("count differs from the listed tickets: incident %d/%d problem %d/%d", md.Incident.Tickets, len(md.Tickets), pd.Problem.Tickets, len(pd.Tickets))
		}
		return pd.Problem.Tickets, listed, md.Incident.Tickets
	}
	if a, b, c := counts(e.u1, true); a != 0 || b != 0 || c != 0 {
		t.Errorf("counts without desk access = %d %d %d, want 0", a, b, c)
	}
	if a, b, c := counts(e.u2, false); a != 0 || b != 0 || c != 0 {
		t.Errorf("counts for an external user = %d %d %d, want 0", a, b, c)
	}
	d, err = problems.Get(ctx, application.ProblemPrincipal{UserID: e.global.UserID, Staff: true}, pr.ID)
	if err != nil || len(d.Tickets) != 1 {
		t.Errorf("staff with desk access sees %+v %v", d.Tickets, err)
	}
	if a, b, c := counts(e.global.UserID, true); a != 1 || b != 1 || c != 1 {
		t.Errorf("counts with global access = %d %d %d, want 1", a, b, c)
	}
	// A view grant on the desk opens linking and listing.
	e.grant(hr, user(e.u1, "view"))
	if err := problems.LinkTicket(ctx, e.c(e.u1), manager, pr.ID, tk.ID, true); err != nil {
		t.Errorf("link with a view grant: %v", err)
	}
	if d, _ = problems.Get(ctx, manager, pr.ID); len(d.Tickets) != 1 || d.Problem.Tickets != 1 {
		t.Errorf("a viewer of the desk sees %+v", d.Tickets)
	}
	if a, b, c := counts(e.u1, true); a != 1 || b != 1 || c != 1 {
		t.Errorf("counts with a view grant = %d %d %d, want 1", a, b, c)
	}
	// Without a Ticket authorization nothing is linked or listed.
	bare := application.NewProblemService(repository.New(e.pool), dir{})
	if err := bare.LinkTicket(ctx, e.c(e.u1), manager, pr.ID, tk.ID, true); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("link without access port: %v", err)
	}
	if d, _ = bare.Get(ctx, manager, pr.ID); len(d.Tickets) != 0 {
		t.Errorf("list without access port: %+v", d.Tickets)
	}
}

// The audit entry of a grant replacement names who gained and lost access.
func TestGrantChangesAreAuditedWithSubjectsAndLevels(t *testing.T) {
	e := newQEnv(t)
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "view"), user(e.u2, "work"))
	e.grant(q, user(e.u1, "view"), user(e.u2, "manage"))
	rows, err := e.pool.Query(context.Background(), `SELECT metadata FROM platform.audit_events
		WHERE correlation_id = $1 AND action = 'servicedesk.queue.grants_replaced' AND target_id = $2 ORDER BY occurred_at, id`, e.corr, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var metas []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, m)
	}
	if len(metas) != 2 {
		t.Fatalf("grants_replaced entries = %d", len(metas))
	}
	tuple := func(list any, subject, level string) bool {
		arr, _ := list.([]any)
		for _, x := range arr {
			m, _ := x.(map[string]any)
			if m["subjectType"] == "user" && m["subjectId"] == subject && m["level"] == level {
				return true
			}
		}
		return false
	}
	if !tuple(metas[0]["added"], e.u1, "view") || !tuple(metas[0]["added"], e.u2, "work") {
		t.Errorf("first change added = %v", metas[0]["added"])
	}
	if !tuple(metas[1]["added"], e.u2, "manage") || !tuple(metas[1]["removed"], e.u2, "work") ||
		tuple(metas[1]["added"], e.u1, "view") || tuple(metas[1]["removed"], e.u1, "view") {
		t.Errorf("second change = added %v removed %v", metas[1]["added"], metas[1]["removed"])
	}
}

// The database refuses a Queue change whose number was not issued for the target Queue in this transaction.
func TestMoveGuardChecksPrefixAndIssuance(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	a, b := e.queue(application.QueuePublic), e.queue(application.QueuePublic)
	tk := e.raise(e.employee(e.alice), a)
	try := func(name, sql string, args ...any) {
		t.Helper()
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, sql, args...); err == nil || !strings.Contains(err.Error(), "SD409") {
			t.Errorf("%s: want SD409, got %v", name, err)
		}
	}
	try("forged number", `UPDATE servicedesk.tickets SET queue_id = $2::uuid, number = 77, reference = $3 WHERE id = $1::uuid`, tk.ID, b.ID, b.Prefix+"-0077")
	try("prefix of another queue", `UPDATE servicedesk.tickets SET queue_id = $2::uuid, number = 77, reference = $3 WHERE id = $1::uuid`, tk.ID, b.ID, a.Prefix+"-0077")
	try("number does not match", `UPDATE servicedesk.tickets SET queue_id = $2::uuid, number = 78, reference = $3 WHERE id = $1::uuid`,
		tk.ID, b.ID, b.Prefix+"-0077")
	try("issued for another queue", `WITH n AS (SELECT o_number, o_reference FROM servicedesk.issue_reference($3::uuid))
		UPDATE servicedesk.tickets AS tk SET queue_id = $2::uuid, number = n.o_number, reference = n.o_reference FROM n WHERE tk.id = $1::uuid`,
		tk.ID, b.ID, a.ID)
	// The real operation passes.
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &tk.Version, b.ID, "misrouted"); err != nil {
		t.Errorf("move: %v", err)
	}
}

// ticketAccess is the Ticket authorization of Problems and Major Incidents in the legacy test environment:
// the agent holds tickets.manage, the viewer tickets.view; Queue grants stay empty.
func (e *env) ticketAccess() *application.Service {
	mem := &fakeMem{teams: map[string][]string{}, roles: map[string][]string{}, perms: map[string]map[string]struct{}{
		e.agent:  {"tickets.view": {}, "tickets.manage": {}},
		e.viewer: {"tickets.view": {}},
	}}
	return application.NewService(repository.New(e.pool), dir{active: map[string]bool{e.alice: true, e.bob: true, e.agent: true, e.viewer: true}}, device{}).WithMemberships(mem)
}
