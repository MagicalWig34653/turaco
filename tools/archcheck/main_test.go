package main

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestPrincipalScopesRestricted(t *testing.T) {
	src := `package caller
import "example/backend/internal/platform/authorization"
func run(p authorization.Principal) bool {
	_ = authorization.Principal{Scopes: nil}
	return len(p.Scopes) > 0
}`
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "caller.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scopesViolations("backend/internal/modules/assets/application/caller.go", file, set); len(got) != 2 {
		t.Fatalf("module: %v", got)
	}
	if got := scopesViolations("backend/internal/platform/authentication/caller.go", file, set); len(got) != 0 {
		t.Fatalf("authentication: %v", got)
	}
	other, err := parser.ParseFile(set, "other.go", `package other
type cfg struct{ Scopes []string }
func run(c cfg) int { return len(c.Scopes) }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := scopesViolations("backend/internal/integrations/oauth/other.go", other, set); len(got) != 0 {
		t.Fatalf("file without the authorization import: %v", got)
	}
}

func TestAssistCallRestrictedToAI(t *testing.T) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "caller.go", `package caller
import marker "example/backend/internal/platform/audit"
func run() { marker.WithAssist(nil, marker.Assist{}) }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	aliases := map[string]bool{"marker": true}
	if got := assistViolations("backend/internal/modules/example/public/caller.go", file, set, aliases); len(got) != 1 {
		t.Fatalf("outside AI: %v", got)
	}
	if got := assistViolations("backend/internal/platform/ai/caller.go", file, set, aliases); len(got) != 0 {
		t.Fatalf("AI package: %v", got)
	}
}
