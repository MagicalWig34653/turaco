package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	catalogapp "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

func TestDemoItemsAreValidDefinitions(t *testing.T) {
	const id = "0194f0a0-0000-7000-8000-000000000001"
	items := demoItems(id, id, id)
	if len(items) != 4 {
		t.Fatalf("%d demo items, want 4", len(items))
	}
	for _, it := range items {
		raw, err := json.Marshal(it.Definition)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalogapp.ParseDefinition(raw); err != nil {
			t.Errorf("%s: %v", it.Key, err)
		}
	}
}

func TestDemoSeedRefusesOutsideDevelopment(t *testing.T) {
	err := runDemo(context.Background(), env{cfg: config.Config{Environment: "production"}}, "seed", nil)
	if err == nil || !strings.Contains(err.Error(), "APP_ENV=development") {
		t.Errorf("err = %v", err)
	}
	if err := runDemo(context.Background(), env{}, "other", nil); err == nil {
		t.Error("unknown demo command accepted")
	}
}
