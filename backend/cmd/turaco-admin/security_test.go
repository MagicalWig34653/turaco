package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSecurityImportBoundsAndSyntax(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"oversize", strings.Repeat("x", (1<<20)+1)},
		{"empty", `{"records":[]}`},
		{"too many", `{"records":[` + strings.Repeat(`{},`, 500) + `{}` + `]}`},
		{"unknown field", `{"records":[],"unexpected":true}`},
		{"second object", `{"records":[]} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runSecurity(context.Background(), env{stdin: strings.NewReader(tc.body), stdout: &bytes.Buffer{}}, "import", nil); err == nil {
				t.Fatal("accepted invalid import")
			}
		})
	}
	if err := runSecurity(context.Background(), env{}, "unknown", nil); err != errUsage {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestSecurityImportCLIActorCarriesOSUser(t *testing.T) {
	actor := env{actor: json.RawMessage(`{"actor":"cli","osUser":"operator"}`)}.auditActor()
	if actor.System != "cli" || actor.OSUser != "operator" {
		t.Fatalf("unexpected CLI actor: %+v", actor)
	}
}
