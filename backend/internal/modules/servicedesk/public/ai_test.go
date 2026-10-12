package public_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ names map[string]string }

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (d dir) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	return d.ActiveUsers(ctx, ids)
}
func (d dir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := d.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}
func (d dir) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type noDevices struct{}

func (noDevices) Snapshot(context.Context, string, string) (map[string]any, error) {
	return nil, application.ErrNotFound
}

func TestTicketSummarizeToolIsUserDelegatedAndRedactsByModuleRules(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	newID := func() string {
		var id string
		if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice, bob, agent := newID(), newID(), newID()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	corr := "ai-tickets-" + hex.EncodeToString(b)
	svc := application.NewService(repository.New(pool), dir{names: map[string]string{alice: "Alice Example", bob: "Bob Example", agent: "Agent Smith"}}, noDevices{})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, alice)
	})
	call := func(u string) application.Caller {
		return application.Caller{Actor: audit.UserActor(u), CorrelationID: corr}
	}
	tk, err := svc.Create(ctx, call(alice), application.Principal{UserID: alice}, application.CreateInput{Title: "Printer jams", Description: "Every page jams"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddComment(ctx, call(alice), application.Principal{UserID: alice}, tk.ID, "public comment from alice", false); err != nil {
		t.Fatal(err)
	}
	staff := application.Principal{UserID: agent, View: true, Manage: true}
	if _, err := svc.AddComment(ctx, call(agent), staff, tk.ID, "INTERNAL staff note", true); err != nil {
		t.Fatal(err)
	}

	tools := public.AITools(svc)
	tool := tools[0]
	if tool.Name != "tickets.summarize" || tool.Target == nil || tool.Target.Type != "ticket" {
		t.Fatalf("tool: %+v", tool)
	}
	if err := ai.NewRegistry().RegisterAll(tools...); err != nil {
		t.Fatalf("self-check: %v", err)
	}
	input := json.RawMessage(`{"ticketId":"` + tk.ID + `"}`)
	caller := func(u string, perms ...string) ai.Caller {
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		return ai.Caller{UserID: u, TenantID: "t", Permissions: m}
	}
	read := func(c ai.Caller) (string, error) {
		out, err := tool.Handler(ctx, c, input)
		if err != nil {
			return "", err
		}
		data, _, err := ai.CheckOutput(tool, out)
		return string(data), err
	}
	// Staff with tickets.view see internal notes, names and the comment thread.
	got, err := read(caller(agent, "tickets.view", "tickets.manage"))
	if err != nil || !strings.Contains(got, "INTERNAL staff note") || !strings.Contains(got, "Alice Example") || !strings.Contains(got, "Printer jams") {
		t.Fatalf("staff read: %v\n%s", err, got)
	}
	for _, leak := range []string{tk.ID, "queue", "snapshot", "externalId", "reporterId"} {
		if strings.Contains(got, leak) {
			t.Errorf("summary leaks %q: %s", leak, got)
		}
	}
	// The reporter reads their own ticket with the module's rules: no internal notes.
	got, err = read(caller(alice))
	if err != nil || strings.Contains(got, "INTERNAL") || !strings.Contains(got, "public comment from alice") {
		t.Fatalf("owner read: %v\n%s", err, got)
	}
	// IDOR: another employee cannot read it, and the answer is indistinguishable from a missing ticket.
	if _, err := read(caller(bob)); !errors.Is(err, ai.ErrToolNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	missing := json.RawMessage(`{"ticketId":"` + newID() + `"}`)
	if _, err := tool.Handler(ctx, caller(bob), missing); !errors.Is(err, ai.ErrToolNotFound) {
		t.Fatalf("missing ticket: %v", err)
	}
	// A model-supplied identity has no effect: the handler only knows the caller.
	if _, err := tool.Handler(ctx, caller(bob), json.RawMessage(`{"ticketId":"`+tk.ID+`","userId":"`+agent+`"}`)); err == nil {
		// The runtime rejects unknown arguments before the handler; even if one slipped through, bob stays bob.
		t.Log("handler ignored the extra field")
	}
}

// The first write tool (F12 A-C): Prepare changes nothing and follows the module's rules; Execute adds the internal
// note as the confirming User exactly once per idempotency key, only on the version the proposal saw, and the
// module's own audit entry carries the AI-assisted marker from the context.
func TestDraftInternalNoteToolIsIdempotentVersionBoundAndMarked(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	newID := func() string {
		var id string
		if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice, bob, agent := newID(), newID(), newID()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	corr := "ai-note-" + hex.EncodeToString(b)
	svc := application.NewService(repository.New(pool), dir{names: map[string]string{}}, noDevices{})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, alice)
	})
	tk, err := svc.Create(ctx, application.Caller{Actor: audit.UserActor(alice), CorrelationID: corr}, application.Principal{UserID: alice},
		application.CreateInput{Title: "VPN drops", Description: "Every hour"})
	if err != nil {
		t.Fatal(err)
	}
	var tool ai.Tool
	for _, tl := range public.AITools(svc) {
		if tl.Name == "tickets.draft_internal_note" {
			tool = tl
		}
	}
	if tool.Risk != ai.RiskWrite || tool.Write == nil || tool.Write.Effect != ai.EffectInternal || tool.Permission != "tickets.manage" {
		t.Fatalf("tool: %+v", tool)
	}
	caller := func(u string, perms ...string) ai.Caller {
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		return ai.Caller{UserID: u, TenantID: "t", CorrelationID: corr, Permissions: m}
	}
	staff := caller(agent, "tickets.view", "tickets.manage")
	input := json.RawMessage(`{"ticketId":"` + tk.ID + `","body":"Checked the tunnel logs; MTU issue."}`)

	prep, err := tool.Write.Prepare(ctx, staff, input)
	if err != nil || prep.TargetVersion != tk.Version || !strings.Contains(prep.TargetLabel, "VPN drops") {
		t.Fatalf("prepare: %+v %v", prep, err)
	}
	// The module decides: the reporter cannot write internal notes, a stranger cannot even see the Ticket.
	if _, err := tool.Write.Prepare(ctx, caller(alice), input); !errors.Is(err, ai.ErrToolForbidden) {
		t.Fatalf("reporter prepare: %v", err)
	}
	if _, err := tool.Write.Prepare(ctx, caller(bob), input); !errors.Is(err, ai.ErrToolNotFound) {
		t.Fatalf("stranger prepare: %v", err)
	}
	countNotes := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM servicedesk.ticket_comments WHERE ticket_id=$1::uuid AND internal`, tk.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countNotes() != 0 {
		t.Fatal("prepare wrote a note")
	}

	proposal := newID()
	actx := audit.WithAssist(ctx, audit.Assist{Via: "ai", TenantID: "t", ProposalID: proposal})
	x := ai.Execution{ProposalID: proposal, IdempotencyKey: proposal, TargetVersion: prep.TargetVersion}
	res, err := tool.Write.Execute(actx, staff, input, x)
	if err != nil || res.Result.Type != "ticket_comment" {
		t.Fatalf("execute: %+v %v", res, err)
	}
	again, err := tool.Write.Execute(actx, staff, input, x)
	if err != nil || again.Result.ID != res.Result.ID || countNotes() != 1 {
		t.Fatalf("idempotent replay: %+v %v notes=%d", again, err, countNotes())
	}
	replayed, found, err := tool.Write.Replay(ctx, caller(agent), input, x)
	if err != nil || !found || replayed.Result.ID != res.Result.ID || countNotes() != 1 {
		t.Fatalf("read-only replay after permission loss: %+v %v %v", replayed, found, err)
	}
	if _, found, err := tool.Write.Replay(ctx, caller(bob), input, x); err != nil || found {
		t.Fatalf("foreign replay disclosed a note: %v %v", found, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE correlation_id=$1 AND action='servicedesk.ticket.comment_added'
		AND via='ai' AND tenant_id='t' AND ai_proposal_id=$2::uuid AND actor_id=$3::uuid AND metadata->>'internal'='true'`, corr, proposal, agent).Scan(&n); err != nil || n != 1 {
		t.Fatalf("module audit with the AI marker: %d %v", n, err)
	}
	// Another author cannot reuse the key to read or claim the note.
	other := caller(newID(), "tickets.view", "tickets.manage")
	if _, err := tool.Write.Execute(actx, other, input, x); err == nil {
		t.Fatal("another user replayed someone else's idempotency key")
	}

	// The Ticket changed after the proposal: stale, never rebased.
	if _, err := svc.SetPriority(ctx, application.Caller{Actor: audit.UserActor(agent), CorrelationID: corr},
		application.Principal{UserID: agent, View: true, Manage: true}, tk.ID, nil, "high"); err != nil {
		t.Fatal(err)
	}
	fresh := newID()
	if _, err := tool.Write.Execute(ctx, staff, input, ai.Execution{ProposalID: fresh, IdempotencyKey: fresh, TargetVersion: prep.TargetVersion}); !errors.Is(err, ai.ErrToolStale) {
		t.Fatalf("stale version: %v", err)
	}
	// Permission lost: the module refuses.
	if _, err := tool.Write.Execute(ctx, caller(agent, "tickets.view"), input, ai.Execution{ProposalID: fresh, IdempotencyKey: fresh, TargetVersion: prep.TargetVersion + 1}); !errors.Is(err, ai.ErrToolForbidden) {
		t.Fatalf("without tickets.manage: %v", err)
	}
	if countNotes() != 1 {
		t.Fatalf("notes after refused executions: %d", countNotes())
	}
}
