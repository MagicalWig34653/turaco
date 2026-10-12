package public_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// F17 S-E: the summary gains slaState and resolutionDueAt for staff only, only while the sla switch is on.
func TestTicketSummarizeToolAddsSLAFieldsForStaffOnlyWhileTheSwitchIsOn(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	newID := func() string {
		var id string
		if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice, agent := newID(), newID()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	corr := "ai-sla-" + hex.EncodeToString(b)
	enabled := true
	svc := application.NewService(repository.New(pool), dir{names: map[string]string{alice: "Alice Example"}}, noDevices{}).
		WithSLA(func(context.Context) (bool, error) { return enabled, nil })
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = $1::uuid`, alice)
	})
	tk, err := svc.Create(ctx, application.Caller{Actor: audit.UserActor(alice), CorrelationID: corr}, application.Principal{UserID: alice}, application.CreateInput{Title: "Badge reader", Description: "Door stays locked"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE servicedesk.tickets SET sla_resolution_due_at=$2, sla_warn_at=$3, sla_tracked=true WHERE id=$1::uuid`, tk.ID, due, due.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	tool := public.AITools(svc)[0]
	read := func(u string, perms ...string) map[string]any {
		t.Helper()
		m := map[string]struct{}{}
		for _, p := range perms {
			m[p] = struct{}{}
		}
		out, err := tool.Handler(ctx, ai.Caller{UserID: u, TenantID: "t", Permissions: m}, json.RawMessage(`{"ticketId":"`+tk.ID+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		data, _, err := ai.CheckOutput(tool, out)
		if err != nil {
			t.Fatal(err)
		}
		var res map[string]any
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	got := read(agent, "tickets.view", "tickets.manage")
	if got["slaState"] != "on_track" || got["resolutionDueAt"] != due.Format(time.RFC3339) {
		t.Fatalf("staff with the switch on: %v", got)
	}
	if got = read(alice); got["slaState"] != nil || got["resolutionDueAt"] != nil {
		t.Fatalf("the reporter must not get SLA fields: %v", got)
	}
	enabled = false
	if got = read(agent, "tickets.view", "tickets.manage"); got["slaState"] != nil || got["resolutionDueAt"] != nil {
		t.Fatalf("switch off: %v", got)
	}
}
