package modules_test

import (
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

const sampleID = "0198a7c0-1111-7222-8333-444455556666"

// Every module states its external behaviour, and an unlisted catalog is refused (default deny).
func TestEveryModuleStatesItsExternalBehaviour(t *testing.T) {
	for _, m := range modules.Catalog() {
		if (len(m.ExternalPaths) > 0) == m.ExternalDenied {
			t.Errorf("module %q must declare exactly one of ExternalPaths and ExternalDenied", m.Key)
		}
	}
	undeclared := modules.Catalog()
	undeclared[len(undeclared)-1].ExternalPaths, undeclared[len(undeclared)-1].ExternalDenied = nil, false
	if _, err := modules.NewIndex(undeclared); err == nil || !strings.Contains(err.Error(), "external behaviour") {
		t.Errorf("a module without an external declaration must be refused, got %v", err)
	}
	both := modules.Catalog()
	both[0].ExternalPaths, both[0].ExternalDenied = []string{"GET /api/v1/meta"}, true
	if _, err := modules.NewIndex(both); err == nil {
		t.Error("a module with both declarations must be refused")
	}
	outside := modules.Catalog()
	for i := range outside {
		if outside[i].Key == "tasks" {
			outside[i].ExternalPaths = append(outside[i].ExternalPaths, "GET /api/v1/tickets/{id}")
		}
	}
	if _, err := modules.NewIndex(outside); err == nil {
		t.Error("an external pattern outside the module's route prefixes must be refused")
	}
}

func TestExternalAllowList(t *testing.T) {
	ix := modules.DefaultIndex()
	allowed := []string{
		"GET /api/v1/auth/session", "POST /api/v1/auth/logout", "GET /api/v1/meta", "GET /api/v1/modules/status",
		"GET /api/v1/tasks", "GET /api/v1/tasks/" + sampleID, "POST /api/v1/tasks/" + sampleID + "/complete", "GET /api/v1/my-work/counts",
		"GET /api/v1/notifications", "GET /api/v1/notifications/unread-count", "POST /api/v1/notifications/" + sampleID + "/read",
		"GET /api/v1/tickets/" + sampleID, "POST /api/v1/tickets/" + sampleID + "/comments",
	}
	for _, a := range allowed {
		method, path, _ := strings.Cut(a, " ")
		if !ix.ExternalAllowed(method, path) {
			t.Errorf("%s must be allowed for an external account", a)
		}
	}
	denied := []string{
		// the explicit exclusions of the design: search, views, query endpoints, AI
		"GET /api/v1/search", "GET /api/v1/views", "POST /api/v1/views", "POST /api/v1/tasks/query", "POST /api/v1/tickets/query",
		"POST /api/v1/users/query", "POST /api/v1/ai/sessions", "GET /api/v1/ai/status", "GET /api/v1/me/pins", "GET /api/v1/me/sidebar",
		// ids that are not UUIDs never match a record pattern
		"GET /api/v1/tickets/by-reference", "GET /api/v1/tickets/fields", "GET /api/v1/tasks/fields", "GET /api/v1/tasks/boards",
		"GET /api/v1/tickets/" + sampleID + "/history", "GET /api/v1/tickets/" + sampleID + "/participants", "POST /api/v1/tickets/" + sampleID + "/assign",
		"GET /api/v1/tickets", "POST /api/v1/tickets", "POST /api/v1/tasks", "PATCH /api/v1/tasks/" + sampleID, "POST /api/v1/tasks/" + sampleID + "/assign",
		// administration, directory, assets, knowledge, briefing
		"GET /api/v1/users", "GET /api/v1/external-parties", "GET /api/v1/roles", "GET /api/v1/admin/health", "GET /api/v1/people/lookup",
		"GET /api/v1/assets", "GET /api/v1/my-assets", "GET /api/v1/knowledge-articles", "GET /api/v1/briefing", "GET /api/v1/audit-events/export.csv",
		"GET /api/v1/notifications/preferences",
		// shape tricks
		"GET /api/v1/tickets/" + sampleID + "/", "GET /api/v1/tickets/" + sampleID + "/comments", "GET /api/v1/meta/x", "GET /health/live", "GET /api/v1/",
		"get /api/v1/meta",
	}
	for _, d := range denied {
		method, path, _ := strings.Cut(d, " ")
		if ix.ExternalAllowed(method, path) {
			t.Errorf("%s must not be allowed for an external account", d)
		}
	}
}
