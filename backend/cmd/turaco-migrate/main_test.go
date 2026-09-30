package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverOrdersMigrations(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000002_second.up.sql", "ignore.txt", "000001_first.up.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 {
		t.Fatalf("unexpected migration order: %+v", got)
	}
}
