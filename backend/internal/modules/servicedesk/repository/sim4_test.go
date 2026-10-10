package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// locDir adds the optional active-location check to the test directory.
type locDir struct {
	dir
	locations map[string]bool
}

func (d locDir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = d.locations[id]
	}
	return out, nil
}

// fakeChanges serves Change summaries from memory.
type fakeChanges map[string]changespublic.ChangeInfo

func (f fakeChanges) Lookup(_ context.Context, ids []string, _ changespublic.ReadScope) (map[string]changespublic.ChangeInfo, error) {
	out := map[string]changespublic.ChangeInfo{}
	for _, id := range ids {
		if c, ok := f[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

type recordedNotifier struct{ intents []notifications.Intent }

func (n *recordedNotifier) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.intents = append(n.intents, in)
	return true, nil
}

func (e *qenv) uuid() string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// History entries made from one audit event get distinct ids (the client keys rows by them), and resuming an
// unassigned ticket makes the actor the assignee.
func TestHistoryEntriesHaveUniqueIDsAndResumeAssignsTheActor(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"))
	tk := e.raise(e.global, q)
	u1 := e.employee(e.u1)
	if _, err := e.svc.Transition(ctx, e.c(e.u1), u1, tk.ID, nil, application.OpStart, application.Params{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u1), u1, tk.ID, nil, application.OpWait, application.Params{Reason: "vendor"}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, tk.ID, nil, &empty, nil); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Transition(ctx, e.c(e.u1), u1, tk.ID, nil, application.OpResume, application.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeID == nil || *got.AssigneeID != e.u1 || got.Status != application.StatusInProgress {
		t.Fatalf("resume of an unassigned ticket = status %s assignee %v, want in_progress by the actor", got.Status, got.AssigneeID)
	}
	entries, _, err := e.svc.History(ctx, e.admin, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, en := range entries {
		if seen[en.ID] {
			t.Errorf("duplicate history id %s (kind %s)", en.ID, en.Kind)
		}
		seen[en.ID] = true
	}
	if len(entries) < 5 {
		t.Errorf("entries = %d, want the start to yield an assignment and a status entry", len(entries))
	}
}

// A colleague's ticket keeps its assignee on resume.
func TestResumeKeepsAnExistingAssignee(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"), user(e.u2, "work"))
	tk := e.raise(e.global, q)
	if _, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, application.OpStart, application.Params{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, application.OpWait, application.Params{Reason: "vendor"}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Transition(ctx, e.c(e.u2), e.employee(e.u2), tk.ID, nil, application.OpResume, application.Params{})
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeID == nil || *got.AssigneeID != e.u1 {
		t.Fatalf("assignee = %v, want the colleague to stay", got.AssigneeID)
	}
}

func TestSetLocationIsGuardedAuditedAndInTheHistory(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	loc, gone := e.uuid(), e.uuid()
	active := map[string]bool{}
	for _, id := range []string{e.alice, e.bob, e.u1, e.u2, e.admin.UserID} {
		active[id] = true
	}
	e.svc = application.NewService(repository.New(e.pool), locDir{dir: dir{active: active}, locations: map[string]bool{loc: true}}, device{}).WithMemberships(e.mem)
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.global, q)
	u1, u2 := e.employee(e.u1), e.employee(e.u2)

	if _, err := e.svc.SetLocation(ctx, e.c(e.u2), u2, tk.ID, &tk.Version, &loc); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("a view grant must not set the location: %v", err)
	}
	if _, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, nil, &loc); err == nil {
		t.Error("the expected version is required")
	}
	stale := tk.Version + 5
	if _, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, &stale, &loc); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, &tk.Version, &gone); !errors.As(err, &inv) {
		t.Errorf("an unknown location must be refused: %v", err)
	}
	if _, err := e.svc.SetLocation(ctx, e.c(e.alice), e.employee(e.alice), tk.ID, &tk.Version, &loc); !errors.Is(err, application.ErrForbidden) && !errors.Is(err, application.ErrNotFound) {
		t.Errorf("the reporter must not set the location: %v", err)
	}
	out, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, &tk.Version, &loc)
	if err != nil {
		t.Fatal(err)
	}
	if out.AffectedLocationID == nil || *out.AffectedLocationID != loc || out.Version != tk.Version+1 {
		t.Fatalf("location = %v version %d", out.AffectedLocationID, out.Version)
	}
	d, err := e.svc.Get(ctx, u1, tk.ID)
	if err != nil || !d.Abilities.SetLocation {
		t.Fatalf("ability = %+v %v", d.Abilities, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.location_changed' AND target_id = $2`, e.corr, tk.ID) != 1 {
		t.Error("the change must be audited once")
	}
	entries, _, err := e.svc.History(ctx, e.admin, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, en := range entries {
		if en.Kind == application.HistoryLocationChanged && en.ToLocationID == loc && en.ActorID == e.u1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no location_changed history entry in %+v", entries)
	}
	// Setting the same value again is a no-op without a version bump.
	again, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, &out.Version, &loc)
	if err != nil || again.Version != out.Version {
		t.Errorf("repeat = version %d, %v", again.Version, err)
	}
	// Clearing is allowed.
	cleared, err := e.svc.SetLocation(ctx, e.c(e.u1), u1, tk.ID, &again.Version, nil)
	if err != nil || cleared.AffectedLocationID != nil {
		t.Errorf("clear = %v, %v", cleared.AffectedLocationID, err)
	}
}

func TestMentionsInInternalNotesReachOnlyPeopleWhoMayViewTheQueue(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.global, q)
	u1 := e.employee(e.u1)

	var inv *application.InvalidInputError
	if _, err := e.svc.AddCommentWithMentions(ctx, e.c(e.u1), u1, tk.ID, "ping", false, []string{e.u2}); !errors.As(err, &inv) {
		t.Errorf("a public comment cannot mention: %v", err)
	}
	if _, err := e.svc.AddCommentWithMentions(ctx, e.c(e.u1), u1, tk.ID, "ping", true, []string{"not-an-id"}); !errors.As(err, &inv) {
		t.Errorf("malformed id: %v", err)
	}
	// u2 may view the Queue, bob may not, the author is never notified about themselves.
	c, err := e.svc.AddCommentWithMentions(ctx, e.c(e.u1), u1, tk.ID, "Can you check this?", true, []string{e.u2, e.bob, e.u1, e.u2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.MentionedUserIDs, []string{e.u2}) {
		t.Fatalf("stored mentions = %v, want only the person who may view the queue", c.MentionedUserIDs)
	}
	d, err := e.svc.Get(ctx, u1, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.Comments); n != 1 || !slices.Equal(d.Comments[0].MentionedUserIDs, []string{e.u2}) {
		t.Fatalf("comments = %+v", d.Comments)
	}

	var payload []byte
	if err := e.pool.QueryRow(ctx, `SELECT payload FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TicketCommentAdded' ORDER BY id DESC LIMIT 1`, e.corr).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	n := &recordedNotifier{}
	active := map[string]bool{e.u1: true, e.u2: true, e.bob: true}
	consumers := application.NewConsumers(repository.New(e.pool), dir{active: active}, n)
	author := e.u1
	if err := consumers.OnCommentAdded(ctx, nil, events.OutboxEvent{ID: e.uuid(), EventType: "TicketCommentAdded", Payload: payload, ActorID: &author}); err != nil {
		t.Fatal(err)
	}
	if len(n.intents) != 1 || n.intents[0].RecipientUserID != e.u2 || n.intents[0].Category != "ticket.mention" {
		t.Fatalf("notifications = %+v, want one ticket.mention for the viewer", n.intents)
	}
	if raw, _ := json.Marshal(n.intents[0].Params); len(raw) > 0 && string(raw) != "" {
		for _, secret := range []string{"Can you check this?"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("the notification must not carry the note text: %s", raw)
			}
		}
	}
}

func TestTicketsLinkToChangesThroughTheRelationshipGraph(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	change := e.uuid()
	owner := e.u2
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	info := changespublic.ChangeInfo{ID: change, Reference: "CHG-000042", Title: "Patch ORBIS", Status: "scheduled", RequesterID: e.alice, OwnerID: &owner}
	e.svc = e.svc.WithChanges(fakeChanges{change: info}, relationships.New(reg), e.pool)
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.global, q)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_id = $1::uuid OR target_id = $2::uuid`, tk.ID, change)
	})

	viewer := application.Principal{UserID: e.u1, ChangesView: true}
	if _, err := e.svc.LinkChange(ctx, e.c(e.u2), e.employee(e.u2), tk.ID, change); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("a view grant must not link: %v", err)
	}
	if _, err := e.svc.LinkChange(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, change); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a caller who may not read the change gets not found: %v", err)
	}
	if _, err := e.svc.LinkChange(ctx, e.c(e.u1), viewer, tk.ID, e.uuid()); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown change: %v", err)
	}
	l1, err := e.svc.LinkChange(ctx, e.c(e.u1), viewer, tk.ID, change)
	if err != nil || l1.Reference != "CHG-000042" || l1.Title != "Patch ORBIS" {
		t.Fatalf("link = %+v %v", l1, err)
	}
	l2, err := e.svc.LinkChange(ctx, e.c(e.u1), viewer, tk.ID, change)
	if err != nil || l2.ID != l1.ID {
		t.Fatalf("linking twice must be idempotent: %+v %v", l2, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.change_linked'`, e.corr) != 1 {
		t.Error("one audit event for the link")
	}

	links, err := e.svc.LinkedChanges(ctx, viewer, tk.ID)
	if err != nil || len(links) != 1 || links[0].Hidden || links[0].Status != "scheduled" {
		t.Fatalf("linked = %+v %v", links, err)
	}
	// Without the Changes read right the link is listed without data.
	links, err = e.svc.LinkedChanges(ctx, e.employee(e.u1), tk.ID)
	if err != nil || len(links) != 1 || !links[0].Hidden || links[0].Reference != "" || links[0].Title != "" {
		t.Fatalf("hidden link = %+v %v", links, err)
	}
	if _, err := e.svc.LinkedChanges(ctx, e.employee(e.alice), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("the reporter does not list change links: %v", err)
	}

	// The Change side lists the tickets the caller may see.
	tickets, err := e.svc.TicketsOfChange(ctx, viewer, change)
	if err != nil || len(tickets) != 1 || tickets[0].TicketID != tk.ID {
		t.Fatalf("tickets of change = %+v %v", tickets, err)
	}
	stranger := application.Principal{UserID: e.bob, ChangesView: true}
	if tickets, err = e.svc.TicketsOfChange(ctx, stranger, change); err != nil || len(tickets) != 0 {
		t.Fatalf("a person without queue access sees no tickets: %+v %v", tickets, err)
	}
	if _, err := e.svc.TicketsOfChange(ctx, e.employee(e.bob), change); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("an unreadable change is not found: %v", err)
	}

	if err := e.svc.UnlinkChange(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, change); err != nil {
		t.Fatal(err)
	}
	if links, _ = e.svc.LinkedChanges(ctx, viewer, tk.ID); len(links) != 0 {
		t.Errorf("after unlink: %+v", links)
	}
	if err := e.svc.UnlinkChange(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, change); err != nil {
		t.Errorf("unlink twice is a no-op: %v", err)
	}
}
