package modules_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

func TestDefaultCatalogIsValid(t *testing.T) {
	ix, err := modules.NewIndex(modules.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"platform", "access", "organization", "audit", "tasks", "approvals", "notifications"} {
		if m, ok := ix.Get(key); !ok || !m.Core {
			t.Errorf("%s must be a core module", key)
		}
	}
	for _, key := range []string{"presence", "ai"} {
		if m, _ := ix.Get(key); m.DefaultEnabled || len(m.AlwaysRunJobs) == 0 {
			t.Errorf("%s: default off with retention jobs expected, got %+v", key, m)
		}
	}
}

func TestNewIndexRejectsBrokenCatalogs(t *testing.T) {
	m := func(key string, requires ...string) modules.Module {
		return modules.Module{Key: key, DefaultEnabled: true, Requires: requires, RoutePrefixes: []string{key}}
	}
	core := modules.Module{Key: "base", Core: true, DefaultEnabled: true}
	tests := map[string][]modules.Module{
		"duplicate key":      {m("a"), m("a")},
		"unknown dependency": {m("a", "zzz")},
		"cycle":              {m("a", "b"), m("b", "a")},
		"self cycle":         {m("a", "a")},
		"core dependency":    {core, m("a", "base")},
		"bad key":            {{Key: "Bad-Key", DefaultEnabled: true}},
		"duplicate prefix":   {m("a"), {Key: "b", DefaultEnabled: true, RoutePrefixes: []string{"a"}}},
		"core with requires": {{Key: "c", Core: true, DefaultEnabled: true, Requires: []string{"a"}}, m("a")},
	}
	for name, list := range tests {
		if _, err := modules.NewIndex(list); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestForPathAndForJob(t *testing.T) {
	ix := modules.DefaultIndex()
	paths := map[string]string{
		"/api/v1/tickets": "servicedesk", "/api/v1/tickets/1/comments": "servicedesk", "/api/v1/remote-access/sessions": "remoteaccess",
		"/api/v1/software/versions": "endpoints", "/api/v1/ai/conversations": "ai", "/api/v1/presence/entries": "presence",
		"/api/v1/briefing/feed": "briefing", "/api/v1/impact": "services", "/api/v1/maintenance-calendar": "planning",
		"/api/v1/ai/status": "", "/api/v1/ai/status/": "", "/api/v1/presence/status": "",
		"/api/v1/presence/settings": "", "/api/v1/presence/settings/purge": "", "/api/v1/presence/settingsx": "presence",
		"/api/v1/ai/settings": "", "/api/v1/ai/providers": "", "/api/v1/ai/providers/1/test": "", "/api/v1/ai/usage": "", "/api/v1/ai/conversations/messages": "ai",
		"/api/v1/tasks": "", "/api/v1/roles/1": "", "/api/v1/auth/login": "", "/api/v1/modules/status": "", "/api/v1/admin/modules": "",
		"/health/live": "", "/api/v1": "", "/api/v1/unknown": "", "/api/v1/ticketsx": "",
	}
	for path, want := range paths {
		if got, ok := ix.ForPath(path); got != want || ok != (want != "") {
			t.Errorf("ForPath(%s) = %q, %v; want %q", path, got, ok, want)
		}
	}
	jobs := map[string]struct {
		key    string
		policy modules.JobPolicy
	}{
		"security.match": {"security", modules.JobDefer}, "servicedesk.external.push": {"servicedesk", modules.JobDefer},
		"services.vm_link_backfill": {"services", modules.JobDefer}, "security.match_all": {"security", modules.JobDrop},
		"endpoints.software_package_sync": {"endpoints", modules.JobDrop}, "changes.reminders": {"changes", modules.JobDrop},
		"remoteaccess.observe": {"remoteaccess", modules.JobDrop}, "security.deferred_event": {"security", modules.JobDefer},
		"endpoints.deployment_tick": {}, "remoteaccess.expire_sessions": {},
		"presence.purge": {}, "ai.retention.purge": {}, "tasks.recurrence.generate": {}, "organization.directory_sync": {}, "nodot": {}, "x.y": {},
	}
	for typ, want := range jobs {
		got, policy, ok := ix.ForJob(typ)
		if got != want.key || ok != (want.key != "") || (ok && policy != want.policy) {
			t.Errorf("ForJob(%s) = %q, %v, %v; want %+v", typ, got, policy, ok, want)
		}
	}
	for name, want := range map[string]string{"changes.approval": "changes", "knowledge.runbook-task-finished": "knowledge",
		"tasks.notify-assigned": "", "approvals.notify-requested": "", "nodot": ""} {
		if got, ok := ix.ForConsumer(name); got != want || ok != (want != "") {
			t.Errorf("ForConsumer(%s) = %q, %v", name, got, ok)
		}
	}
}

// Every route a transport package registers below /api/v1 must either belong to a module of the catalog or be a
// known platform path. A new route family without a catalog entry would otherwise stay reachable after its module is
// switched off.
func TestEveryRegisteredRouteFamilyIsInTheCatalog(t *testing.T) {
	ix := modules.DefaultIndex()
	platformSegments := map[string]bool{"auth": true, "roles": true, "role-assignments": true, "permissions": true, "modules": true, "admin": true,
		"audit-events": true, "users": true, "teams": true, "locations": true, "departments": true, "people": true, "role-templates": true, "access": true, "directory-groups": true, "directory-sync-runs": true, "import-batches": true,
		"tasks": true, "recurring-task-definitions": true, "my-work": true, "approvals": true, "notifications": true, "meta": true,
		// Saved Views, shares and pins (ADR-0033) are a core platform capability: always on like My Work. A View of an
		// optional module is gated by the views service itself (views.module_disabled), and /me/* is the caller's own presentation state.
		"views": true, "me": true,
		// Attachments (ADR-0037) are a core platform capability; the owning module authorizes every request through its
		// registered owner, and a switched-off module's records answer not found.
		"attachments": true,
		// The platform search fans out to module Searchers that each respect the module switches themselves.
		"search": true}
	routeRE := regexp.MustCompile(`"(?:GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"{ ]*)`)
	root := filepath.Join("..", "..")
	seen := 0
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || !strings.Contains(filepath.ToSlash(path), "/transport/") && !strings.HasSuffix(path, "authentication/handler.go") && !strings.Contains(path, "authentication") {
			return err
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range routeRE.FindAllStringSubmatch(string(src), -1) {
			seen++
			seg := strings.SplitN(strings.TrimPrefix(m[1], "/api/v1/"), "/", 2)[0]
			if _, owned := ix.ForPath("/api/v1/" + seg); !owned && !platformSegments[seg] {
				t.Errorf("%s: route family %q is neither owned by a catalog module nor a known platform path", path, seg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 100 {
		t.Fatalf("only %d routes found; the scan is broken", seen)
	}
}
