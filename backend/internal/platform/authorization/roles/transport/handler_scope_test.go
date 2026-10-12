package transport

import (
	"context"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// Location-scoped assignments over HTTP (F16 design L11): request field, response fields, filters, error codes and
// the effective-permissions shape.
func TestHTTPScopedAssignments(t *testing.T) {
	h := newHTTPFixture(t, "platform.roles.view", "platform.roles.manage", "organization.users.view_details", "organization.view")
	role := h.mkRole("scoped", "knowledge.manage")
	site := h.location("Site North", "", true)
	area := h.location("Ward 3", site, true)
	closed := h.location("Closed wing", site, false)
	alice := h.user("Alice")

	rec := h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"user","subjectId":"`+alice+`","scopeLocationId":"`+area+`"}`)
	var a struct {
		ID, Scope, ScopeLocationID, ScopeLocationName string
		ScopeLocationPath                             []string
		ScopeLocationActive                           bool
	}
	decode(t, rec, &a)
	if rec.Code != 201 || a.Scope != "location" || a.ScopeLocationID != area || a.ScopeLocationName != "Ward 3" ||
		len(a.ScopeLocationPath) != 2 || a.ScopeLocationPath[0] != "Site North" || !a.ScopeLocationActive {
		t.Fatalf("assign scoped: %d %s", rec.Code, rec.Body)
	}
	h.wantAudit(a.ID, "authorization.role.assigned")

	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"inactive Location", `{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + alice + `","scopeLocationId":"` + closed + `"}`, 422, "access.scope_location_invalid"},
		{"unknown Location", `{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + alice + `","scopeLocationId":"` + h.newID() + `"}`, 422, "access.scope_location_invalid"},
		{"malformed Location", `{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + alice + `","scopeLocationId":"x"}`, 400, "authorization.invalid_request"},
		{"duplicate", `{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + alice + `","scopeLocationId":"` + area + `"}`, 409, "authorization.duplicate_assignment"},
	} {
		if rec := h.do("POST", "/api/v1/role-assignments", tc.body); rec.Code != tc.status || errCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	built, _ := h.svc.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+built.ID+`","subjectType":"user","subjectId":"`+alice+`","scopeLocationId":"`+site+`"}`)
	if rec.Code != 409 || errCode(t, rec) != "access.admin_scope_global" {
		t.Errorf("administrator scoped: %d %s", rec.Code, rec.Body)
	}

	// List filters.
	var list struct {
		Items []struct{ ID, Scope, ScopeLocationName string }
	}
	rec = h.do("GET", "/api/v1/role-assignments?subjectId="+alice+"&scope=location&scopeLocationId="+area, "")
	decode(t, rec, &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != a.ID || list.Items[0].ScopeLocationName != "Ward 3" {
		t.Fatalf("filtered list: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("GET", "/api/v1/role-assignments?scope=site", ""); rec.Code != 400 {
		t.Errorf("bad scope filter: %d", rec.Code)
	}

	// Effective permissions: no permission is scopable yet, so the whole scoped assignment is inert.
	var ep struct {
		Roles []struct {
			AssignmentID, Scope, ScopeLocationID string
			InertPermissions                     []string
		}
		Permissions       []struct{ Name, Scope string }
		ScopedPermissions []struct{ Name string }
	}
	rec = h.do("GET", "/api/v1/users/"+alice+"/effective-permissions", "")
	decode(t, rec, &ep)
	if rec.Code != 200 || len(ep.Roles) != 1 || ep.Roles[0].Scope != "location" || ep.Roles[0].ScopeLocationID != area ||
		len(ep.Roles[0].InertPermissions) != 1 || ep.Roles[0].InertPermissions[0] != "knowledge.manage" || len(ep.Permissions) != 0 || ep.ScopedPermissions == nil {
		t.Fatalf("effective permissions: %d %s", rec.Code, rec.Body)
	}

	// Holders by Location: a non-scopable permission is held only by global holders.
	rec = h.do("GET", "/api/v1/access/holders?permission=knowledge.manage&locationId="+area, "")
	var holders struct{ Items []struct{ SubjectID string } }
	decode(t, rec, &holders)
	for _, x := range holders.Items {
		if x.SubjectID == alice {
			t.Errorf("inert scoped assignment listed as holder: %s", rec.Body)
		}
	}

	// Members carry the scope.
	var members struct {
		Items []struct {
			UserID, Scope    string
			ScopeLocationIDs []string
		}
	}
	rec = h.do("GET", "/api/v1/roles/"+role.ID+"/members", "")
	decode(t, rec, &members)
	if rec.Code != 200 || len(members.Items) != 1 || members.Items[0].Scope != "location" || len(members.Items[0].ScopeLocationIDs) != 1 {
		t.Fatalf("members: %d %s", rec.Code, rec.Body)
	}

	// Revocation answers with the scope.
	rec = h.do("POST", "/api/v1/role-assignments/"+a.ID+"/revoke", "")
	decode(t, rec, &a)
	if rec.Code != 200 || a.ScopeLocationID != area {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
}

// Without organization.view a response carries the scope Location id but no names.
func TestHTTPScopeLabelsNeedOrganizationView(t *testing.T) {
	h := newHTTPFixture(t, "platform.roles.view", "platform.roles.manage")
	role := h.mkRole("nolabel", "knowledge.manage")
	site := h.location("Site South", "", true)
	rec := h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"user","subjectId":"`+h.user("Bob")+`","scopeLocationId":"`+site+`"}`)
	var a map[string]any
	decode(t, rec, &a)
	if rec.Code != 201 || a["scopeLocationId"] != site {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body)
	}
	if _, named := a["scopeLocationName"]; named {
		t.Fatalf("Location name disclosed without organization.view: %s", rec.Body)
	}
}
