package public_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

const editor = "00000000-0000-7000-8000-0000000000f1"

func TestKnowledgeSearchToolRespectsAudienceDraftsAndEgress(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tag := "aitool" + hex.EncodeToString(b)
	corr := "knowledge-" + tag
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM knowledge.articles WHERE title LIKE $1`, tag+"%")
	})
	svc := application.NewService(repository.New(pool))
	manage := application.Principal{UserID: editor, Manage: true}
	c := application.Caller{Actor: audit.UserActor(editor), CorrelationID: corr}
	mk := func(title, audience string, publish bool) {
		a, err := svc.Create(ctx, c, manage, application.Input{Title: tag + " " + title, Summary: "summary of " + title, Body: "body of " + title, Audience: audience})
		if err != nil {
			t.Fatal(err)
		}
		if publish {
			if _, err := svc.Publish(ctx, c, manage, a.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("staffonly", "internal", true)
	mk("everyone", "employee", true)
	mk("draft", "employee", false)

	tools := public.AITools(svc)
	if len(tools) != 1 || tools[0].Name != "knowledge.search" || tools[0].Risk != ai.RiskRead {
		t.Fatalf("tools: %+v", tools)
	}
	reg := ai.NewRegistry()
	if err := reg.RegisterAll(tools...); err != nil {
		t.Fatalf("self-check: %v", err)
	}
	run := func(perms ...string) (titles []string, raw string) {
		perm := map[string]struct{}{}
		for _, p := range perms {
			perm[p] = struct{}{}
		}
		out, err := tools[0].Handler(ctx, ai.Caller{UserID: editor, Permissions: perm}, json.RawMessage(`{"query":"`+tag+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		data, classes, err := ai.CheckOutput(tools[0], out)
		if err != nil {
			t.Fatalf("DTO violates its own allowlist: %v", err)
		}
		if len(classes) == 0 {
			t.Fatal("no data classes derived")
		}
		var res struct{ Items []struct{ Title string } }
		_ = json.Unmarshal(data, &res)
		for _, i := range res.Items {
			titles = append(titles, i.Title)
		}
		return titles, string(data)
	}
	// Staff with knowledge.view see employee and internal articles; the draft is never offered, not even to a manager.
	titles, raw := run("knowledge.view", "knowledge.manage")
	if len(titles) != 2 || strings.Contains(raw, "draft") {
		t.Fatalf("staff search: %v", titles)
	}
	// Without knowledge.view only employee-audience articles come back (module rule, not the tool's).
	titles, _ = run()
	if len(titles) != 1 || !strings.Contains(titles[0], "everyone") {
		t.Fatalf("plain search: %v", titles)
	}
	if strings.Contains(raw, "body of") {
		t.Error("article bodies must not be returned")
	}
}
