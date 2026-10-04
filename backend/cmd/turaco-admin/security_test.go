package main

import (
	"bytes"
	"context"
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
