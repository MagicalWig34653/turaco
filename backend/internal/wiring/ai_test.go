package wiring

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// The registered tools and their risk classes are fixed here: a new tool, a changed risk class or a new data class
// must be a reviewed change of this test, which also scans every field path against the prohibited-path list (A13).
// The only write tool (F12 A-C) has an internal effect only and returns nothing to the model.
func TestRegisteredAIToolsAreReviewedAndFieldSafe(t *testing.T) {
	risks := map[string]ai.Risk{"devices.context_summary": ai.RiskRead, "knowledge.search": ai.RiskRead, "tickets.summarize": ai.RiskRead,
		"tickets.draft_internal_note": ai.RiskWrite}
	svc, err := AI(nil, AIConfig{Enabled: true, TenantID: "t"})
	if err != nil {
		t.Fatalf("a tool fails the registry self-check: %v", err)
	}
	var names []string
	for _, tool := range svc.Registry().Tools() {
		names = append(names, tool.Name)
		if tool.Risk != risks[tool.Name] {
			t.Errorf("%s: risk %s, want %s", tool.Name, tool.Risk, risks[tool.Name])
		}
		if tool.Risk == ai.RiskWrite && (tool.Write == nil || tool.Write.Effect != ai.EffectInternal || len(tool.Output) != 0 || tool.Handler != nil) {
			t.Errorf("%s: a write tool must be internal-only, without output and handler", tool.Name)
		}
		known := false
		for _, p := range permissions.Registry {
			known = known || p.Name == tool.Permission
		}
		if !known {
			t.Errorf("%s: unregistered permission %s", tool.Name, tool.Permission)
		}
		for _, f := range tool.Output {
			if frag := ai.ProhibitedFragment(f.Path); frag != "" {
				t.Errorf("%s: output %s contains %q", tool.Name, f.Path, frag)
			}
			if !f.Class.Valid() {
				t.Errorf("%s: output %s has class %q", tool.Name, f.Path, f.Class)
			}
		}
		// Identities never travel in arguments: the caller comes from the session.
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		for p := range schema.Properties {
			l := strings.ToLower(p)
			for _, bad := range []string{"user", "tenant", "role", "system", "permission", "token", "secret", "url", "path", "sql"} {
				if strings.Contains(l, bad) {
					t.Errorf("%s: input property %q looks like an identity, URL or query channel", tool.Name, p)
				}
			}
		}
	}
	want := []string{"devices.context_summary", "knowledge.search", "tickets.draft_internal_note", "tickets.summarize"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("registered tools = %v, want %v", names, want)
	}
	if off, err := AI(nil, AIConfig{Enabled: false}); err != nil || len(off.Registry().Tools()) != 0 {
		t.Errorf("tools registered while AI is off: %v", err)
	}
}
