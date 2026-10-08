package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

func main() {
	check := flag.Bool("check", false, "fail if generated documentation differs from files on disk")
	outDir := flag.String("out", "docs/reference", "output directory")
	flag.Parse()

	generated := map[string][]byte{
		"permissions.md":   renderPermissions(),
		"events.md":        renderEvents(),
		"configuration.md": renderConfiguration(),
		"ai-tools.md":      renderAITools(),
	}
	if err := apply(*outDir, generated, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func apply(dir string, files map[string][]byte, check bool) error {
	if !check {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for name, expected := range files {
		path := filepath.Join(dir, name)
		if check {
			actual, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("generated reference missing %s: %w", path, err)
			}
			if !bytes.Equal(actual, expected) {
				return fmt.Errorf("generated reference is stale: %s (run make docs)", path)
			}
			continue
		}
		if err := os.WriteFile(path, expected, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderPermissions() []byte {
	items := append([]permissions.Permission(nil), permissions.Registry...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	var b strings.Builder
	b.WriteString("# Permission Reference\n\n> Generated from code. Do not edit manually.\n\n| Permission | Risk | Description |\n|---|---|---|\n")
	for _, p := range items {
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.Name, p.Risk, p.Description)
	}
	return []byte(b.String())
}

func renderEvents() []byte {
	items := append([]events.Definition(nil), events.Registry...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	var b strings.Builder
	b.WriteString("# Event Catalog\n\n> Generated from code. Do not edit manually.\n\n| Event | Version | Owner | Description |\n|---|---:|---|---|\n")
	for _, e := range items {
		owner := e.Owner
		if e.Internal {
			owner += " (internal)"
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | %s |\n", e.Name, e.Version, owner, e.Description)
	}
	return []byte(b.String())
}

func renderConfiguration() []byte {
	items := append([]config.Descriptor(nil), config.Registry...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	var b strings.Builder
	b.WriteString("# Configuration Reference\n\n> Generated from code. Do not edit manually.\n\n| Variable | Type | Required | Secret | Default | Description |\n|---|---|---|---|---|---|\n")
	for _, c := range items {
		def := c.Default
		if c.Secret && def != "" {
			def = "(redacted)"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %t | %t | `%s` | %s |\n", c.Name, c.Type, c.Required, c.Secret, def, c.Description)
	}
	return []byte(b.String())
}

// renderAITools lists the AI Tools the modules contribute (F12), with the permission, risk class and the data
// classes derived from each tool's output fields, so a new tool or a wider output is a visible review item.
func renderAITools() []byte {
	svc, err := wiring.AI(nil, wiring.AIConfig{Enabled: true, TenantID: "docgen"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "register ai tools:", err)
		os.Exit(1)
	}
	var b strings.Builder
	b.WriteString("# AI Tool Reference\n\n> Generated from code. Do not edit manually.\n\nData classes are derived from each tool's output fields (F12 A13); a provider is offered a tool only if it is allowed every class listed. Output fields are the closed allowlist the egress filter enforces.\n\n| Tool | Permission | Risk | Data classes | Reads one record | Output fields |\n|---|---|---|---|---|---|\n")
	for _, t := range svc.Registry().Tools() {
		classes := make([]string, 0)
		for _, c := range t.Classes() {
			classes = append(classes, "`"+string(c)+"`")
		}
		fields := make([]string, 0, len(t.Output))
		for _, f := range t.Output {
			fields = append(fields, "`"+f.Path+"`")
		}
		target := "no"
		if t.Target != nil {
			target = "yes (`" + t.Target.Type + "`, needs the User to have named it)"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s |\n", t.Name, t.Permission, t.Risk, strings.Join(classes, ", "), target, strings.Join(fields, ", "))
	}
	return []byte(b.String())
}
