package main

import (
	"go/parser"
	"go/token"
	"testing"
)

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
