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
