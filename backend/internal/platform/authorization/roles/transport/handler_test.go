package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

type httpFixture struct {
	*fixture
	mux   *http.ServeMux
	admin string
}

func newHTTPFixture(t *testing.T, perms ...string) *httpFixture {
	t.Helper()
	f := newFixture(t)
	admin := f.user("Web admin")
	set := map[string]struct{}{}
	for _, p := range perms {
		set[p] = struct{}{}
	}
	// The guards evaluate the actor from the database, so the web admin really is a platform administrator.
	builtIn, err := f.svc.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AssignRole(context.Background(), audit.CLIActor("test"), f.pfx+"-c",
		roles.AssignInput{RoleID: builtIn.ID, SubjectType: roles.SubjectUser, SubjectID: admin}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.role_assignments WHERE subject_id = $1`, admin)
	})
	mux := http.NewServeMux()
	Register(mux, f.svc, fixed{p: authorization.Principal{UserID: admin, Permissions: set}, ok: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &httpFixture{fixture: f, mux: mux, admin: admin}
}

func (h *httpFixture) do(method, target, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	req.Header.Set("X-Request-ID", h.pfx+"-req")
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", h.pfx+"-req")
	h.mux.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct{ Code string }
	}
	decode(t, rec, &e)
	return e.Error.Code
}

func TestHTTPAuthentication(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name   string
		auth   authorization.Authenticator
		method string
		path   string
		want   int
	}{
		{"unauthenticated view", authorization.DenyAll{}, "GET", "/api/v1/roles", 401},
		{"unauthenticated manage", authorization.DenyAll{}, "POST", "/api/v1/roles", 401},
		{"view without permission", fixed{p: authorization.Principal{UserID: "u"}, ok: true}, "GET", "/api/v1/permissions", 403},
		{"manage with view only", fixed{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.roles.view": {}}}, ok: true}, "POST", "/api/v1/role-assignments", 403},
		{"revoke with view only", fixed{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.roles.view": {}}}, ok: true}, "POST", "/api/v1/role-assignments/x/revoke", 403},
		{"delete with view only", fixed{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.roles.view": {}}}, ok: true}, "DELETE", "/api/v1/roles/x", 403},
		{"manage cannot view", fixed{p: authorization.Principal{UserID: "u", Permissions: map[string]struct{}{"platform.roles.manage": {}}}, ok: true}, "GET", "/api/v1/roles", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			Register(mux, f.svc, tc.auth, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHTTPRoleFlow(t *testing.T) {
	h := newHTTPFixture(t, "platform.roles.view", "platform.roles.manage")

	rec := h.do("GET", "/api/v1/permissions", "")
	var pl struct{ Items []permissionDTO }
	decode(t, rec, &pl)
	if rec.Code != 200 || len(pl.Items) < 4 || pl.Items[0].Name > pl.Items[1].Name || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("permissions = %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}

	key := h.pfx + "-web"
	rec = h.do("POST", "/api/v1/roles", `{"key":"`+key+`","name":"Web","permissions":["tasks.view"]}`)
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var role roleDTO
	decode(t, rec, &role)
	if role.Key != key || role.BuiltIn || len(role.Permissions) != 1 || role.ActiveAssignments != 0 {
		t.Fatalf("role = %+v", role)
	}
	rows := h.wantAudit(role.ID, "authorization.role.created")
	if rows[0].actor == nil || *rows[0].actor != h.admin || rows[0].correlationID != h.pfx+"-req" {
		t.Fatalf("audit = %+v", rows[0])
	}

	if rec = h.do("POST", "/api/v1/roles", `{"key":"`+key+`","name":"Web"}`); rec.Code != 409 || errCode(t, rec) != "authorization.duplicate_key" {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct{ name, body, code string }{
		{"unknown field", `{"key":"` + h.pfx + `-u","name":"x","extra":1}`, "authorization.invalid_request"},
		{"malformed", `{`, "authorization.invalid_request"},
		{"trailing data", `{"key":"` + h.pfx + `-u","name":"x"}{}`, "authorization.invalid_request"},
		{"bad key", `{"key":"Bad Key","name":"x"}`, "authorization.invalid_request"},
		{"empty name", `{"key":"` + h.pfx + `-u","name":""}`, "authorization.invalid_request"},
		{"unknown permission", `{"key":"` + h.pfx + `-u","name":"x","permissions":["nope"]}`, "authorization.unknown_permission"},
		{"too large", `{"key":"` + h.pfx + `-u","name":"` + strings.Repeat("a", 70<<10) + `"}`, "authorization.invalid_request"},
	} {
		if rec = h.do("POST", "/api/v1/roles", tc.body); rec.Code != 400 || errCode(t, rec) != tc.code {
			t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	if rec = h.do("GET", "/api/v1/roles/"+role.ID, ""); rec.Code != 200 {
		t.Fatalf("get = %d", rec.Code)
	}
	if rec = h.do("GET", "/api/v1/roles/"+h.newID(), ""); rec.Code != 404 || errCode(t, rec) != "authorization.not_found" {
		t.Fatalf("get missing = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("GET", "/api/v1/roles", ""); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(key)) || !bytes.Contains(rec.Body.Bytes(), []byte(roles.AdministratorRoleKey)) {
		t.Fatalf("list = %d", rec.Code)
	}

	rec = h.do("PATCH", "/api/v1/roles/"+role.ID, `{"description":"changed","expectedVersion":1}`)
	decode(t, rec, &role)
	if rec.Code != 200 || role.Description != "changed" {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("PATCH", "/api/v1/roles/"+role.ID, `{"expectedVersion":2}`); rec.Code != 400 {
		t.Fatalf("empty patch = %d", rec.Code)
	}
	if rec = h.do("PUT", "/api/v1/roles/"+role.ID+"/permissions", `{}`); rec.Code != 400 {
		t.Fatalf("missing permissions = %d", rec.Code)
	}
	if rec = h.do("PUT", "/api/v1/roles/"+role.ID+"/permissions", `{"permissions":["x.y"],"expectedVersion":2}`); rec.Code != 400 || errCode(t, rec) != "authorization.unknown_permission" {
		t.Fatalf("unknown = %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do("PUT", "/api/v1/roles/"+role.ID+"/permissions", `{"permissions":["tickets.view","tasks.view"],"expectedVersion":2}`)
	decode(t, rec, &role)
	if rec.Code != 200 || len(role.Permissions) != 2 || role.Permissions[0] != "tasks.view" {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}

	builtIn, _ := h.svc.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	if rec = h.do("PATCH", "/api/v1/roles/"+builtIn.ID, `{"name":"x","expectedVersion":1}`); rec.Code != 409 || errCode(t, rec) != "authorization.built_in_role" {
		t.Fatalf("built-in patch = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("DELETE", "/api/v1/roles/"+builtIn.ID+"?expectedVersion=1", ""); rec.Code != 409 {
		t.Fatalf("built-in delete = %d", rec.Code)
	}

	// Assignments.
	user, group := h.user("Alice"), h.group("Staff")
	rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"user","subjectId":"`+user+`"}`)
	var as assignmentDTO
	decode(t, rec, &as)
	if rec.Code != 201 || as.SubjectDisplayName != "Alice" || as.CreatedBy != h.admin || as.Scope != "global" || as.RevokedAt != nil {
		t.Fatalf("assign = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"user","subjectId":"`+user+`"}`); rec.Code != 409 || errCode(t, rec) != "authorization.duplicate_assignment" {
		t.Fatalf("dup assign = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"directory_group","subjectId":"`+h.newID()+`"}`); rec.Code != 404 || errCode(t, rec) != "authorization.subject_not_found" {
		t.Fatalf("missing subject = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+h.newID()+`","subjectType":"user","subjectId":"`+user+`"}`); rec.Code != 404 || errCode(t, rec) != "authorization.not_found" {
		t.Fatalf("missing role = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"team","subjectId":"`+user+`"}`); rec.Code != 400 {
		t.Fatalf("bad type = %d", rec.Code)
	}
	if rec = h.do("POST", "/api/v1/role-assignments", `{"roleId":"`+role.ID+`","subjectType":"directory_group","subjectId":"`+group+`","scope":"global"}`); rec.Code != 400 {
		t.Fatalf("unknown field = %d", rec.Code)
	}
	if rec = h.do("DELETE", "/api/v1/roles/"+role.ID+"?expectedVersion=3", ""); rec.Code != 409 || errCode(t, rec) != "authorization.role_in_use" {
		t.Fatalf("in use = %d %s", rec.Code, rec.Body.String())
	}

	rec = h.do("GET", "/api/v1/role-assignments?roleId="+role.ID+"&limit=1", "")
	var list struct {
		Items      []assignmentDTO
		NextCursor string
	}
	decode(t, rec, &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.NextCursor != "" {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"limit=0", "limit=x"} {
		if rec = h.do("GET", "/api/v1/role-assignments?"+q, ""); rec.Code != 400 || errCode(t, rec) != "authorization.invalid_limit" {
			t.Fatalf("%s = %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec = h.do("GET", "/api/v1/role-assignments?cursor=zzz", ""); rec.Code != 400 || errCode(t, rec) != "authorization.invalid_cursor" {
		t.Fatalf("cursor = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("GET", "/api/v1/role-assignments?includeRevoked=maybe", ""); rec.Code != 400 {
		t.Fatalf("includeRevoked = %d", rec.Code)
	}
	if rec = h.do("GET", "/api/v1/role-assignments?subjectType=team", ""); rec.Code != 400 {
		t.Fatalf("subjectType = %d", rec.Code)
	}

	rec = h.do("POST", "/api/v1/role-assignments/"+as.ID+"/revoke", "")
	decode(t, rec, &as)
	if rec.Code != 200 || as.RevokedAt == nil || as.RevokedBy != h.admin {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if rec = h.do("POST", "/api/v1/role-assignments/"+as.ID+"/revoke", ""); rec.Code != 200 {
		t.Fatalf("revoke again = %d", rec.Code)
	}
	if rec = h.do("POST", "/api/v1/role-assignments/"+h.newID()+"/revoke", ""); rec.Code != 404 {
		t.Fatalf("revoke missing = %d", rec.Code)
	}
	if rec = h.do("DELETE", "/api/v1/roles/"+role.ID+"?expectedVersion=3", ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestNewReadRoutesNeedBothPermissions(t *testing.T) {
	f := newFixture(t)
	user := f.user("Someone")
	for _, tc := range []struct {
		name  string
		perms []string
		path  string
		want  int
	}{
		{"effective: view only", []string{"platform.roles.view"}, "/api/v1/users/" + user + "/effective-permissions", 403},
		{"effective: details only", []string{"organization.users.view_details"}, "/api/v1/users/" + user + "/effective-permissions", 403},
		{"effective: manage + details", []string{"platform.roles.manage", "organization.users.view_details"}, "/api/v1/users/" + user + "/effective-permissions", 403},
		{"effective: both", []string{"platform.roles.view", "organization.users.view_details"}, "/api/v1/users/" + user + "/effective-permissions", 200},
		{"holders: view only", []string{"platform.roles.view"}, "/api/v1/access/holders?permission=tickets.view", 403},
		{"holders: both", []string{"platform.roles.view", "organization.users.view_details"}, "/api/v1/access/holders?permission=tickets.view", 200},
		{"holders: unknown permission", []string{"platform.roles.view", "organization.users.view_details"}, "/api/v1/access/holders?permission=no.such", 400},
		{"templates: view", []string{"platform.roles.view"}, "/api/v1/role-templates", 200},
		{"templates: none", []string{"tasks.view"}, "/api/v1/role-templates", 403},
		{"members: view", []string{"platform.roles.view"}, "/api/v1/roles/" + f.newID() + "/members", 404},
		{"members: none", []string{"tasks.view"}, "/api/v1/roles/" + f.newID() + "/members", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := map[string]struct{}{}
			for _, p := range tc.perms {
				set[p] = struct{}{}
			}
			mux := http.NewServeMux()
			Register(mux, f.svc, fixed{p: authorization.Principal{UserID: user, Permissions: set}, ok: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestTemplatesPermissionPickerAndTemplateRoles(t *testing.T) {
	h := newHTTPFixture(t, "platform.roles.view", "platform.roles.manage", "organization.users.view_details")
	rec := h.do("GET", "/api/v1/role-templates", "")
	var tl struct {
		Items []templateDTO
	}
	decode(t, rec, &tl)
	keys := map[string]templateDTO{}
	for _, it := range tl.Items {
		keys[it.Key] = it
	}
	for _, want := range []string{"first-level-support", "it-specialist", "team-lead", "security-analyst", "infrastructure-engineer", "vendor-restricted", "employee-plus", "remote-support-attended"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("template %s missing", want)
		}
	}
	if rs := keys["remote-support-attended"]; !rs.AdministratorAssignOnly || keys["first-level-support"].AdministratorAssignOnly {
		t.Errorf("only the opt-in remote support template is administrator-only: %+v", rs)
	}
	for _, p := range keys["first-level-support"].Permissions {
		if p == "remote_access.start_attended" {
			t.Error("first level contains attended remote access")
		}
	}

	// The picker data: module, group, needs.
	rec = h.do("GET", "/api/v1/permissions", "")
	var pl struct{ Items []permissionDTO }
	decode(t, rec, &pl)
	for _, p := range pl.Items {
		if p.Module == "" || p.Group == "" || p.Needs == nil {
			t.Fatalf("incomplete picker data: %+v", p)
		}
		if p.Name == "remote_access.start_attended" && (len(p.Needs) != 2 || p.Module != "remoteaccess") {
			t.Errorf("remote access needs: %+v", p)
		}
	}

	// A role from a template records the template; the template never updates it.
	key := h.pfx + "-vendor"
	rec = h.do("POST", "/api/v1/roles", `{"key":"`+key+`","name":"Vendor","templateKey":"vendor-restricted"}`)
	var role roleDTO
	decode(t, rec, &role)
	if rec.Code != 201 || role.TemplateKey != "vendor-restricted" || role.TemplateVersion != 1 || len(role.Permissions) != 1 || role.Version != 1 {
		t.Fatalf("from template: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("POST", "/api/v1/roles", `{"key":"`+h.pfx+`-bad","name":"x","templateKey":"nope"}`); rec.Code != 400 || errCode(t, rec) != "authorization.unknown_template" {
		t.Errorf("unknown template: %d %s", rec.Code, rec.Body)
	}
	// Optimistic locking: the version is required and checked.
	if rec = h.do("PUT", "/api/v1/roles/"+role.ID+"/permissions", `{"permissions":["tasks.view"]}`); rec.Code != 400 {
		t.Errorf("missing version: %d", rec.Code)
	}
	if rec = h.do("PUT", "/api/v1/roles/"+role.ID+"/permissions", `{"permissions":["tasks.view"],"expectedVersion":9}`); rec.Code != 409 || errCode(t, rec) != "authorization.version_conflict" {
		t.Errorf("stale version: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("DELETE", "/api/v1/roles/"+role.ID, ""); rec.Code != 400 {
		t.Errorf("delete without version: %d", rec.Code)
	}
	// Separation of duties: a warning that needs acknowledgement with the rule keys, then passes.
	rec = h.do("POST", "/api/v1/roles", `{"key":"`+h.pfx+`-sod","name":"x","permissions":["changes.manage","changes.approve"]}`)
	var env struct {
		Error struct {
			Code    string
			Details struct{ Rules []string }
		}
	}
	decode(t, rec, &env)
	if rec.Code != 409 || env.Error.Code != "access.sod_acknowledgement_required" || len(env.Error.Details.Rules) != 1 || env.Error.Details.Rules[0] != "changes_manage_approve" {
		t.Fatalf("sod: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("POST", "/api/v1/roles", `{"key":"`+h.pfx+`-sod","name":"x","permissions":["changes.manage","changes.approve"],"acknowledgedRules":["changes_manage_approve"],"reason":"small team"}`); rec.Code != 201 {
		t.Errorf("acknowledged: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("GET", "/api/v1/access/sod-rules", ""); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("changes_manage_approve")) {
		t.Errorf("sod rules: %d", rec.Code)
	}
}

func TestEffectivePermissionsHoldersAndMembersEndpoints(t *testing.T) {
	h := newHTTPFixture(t, "platform.roles.view", "platform.roles.manage", "organization.users.view_details")
	role := h.mkRole("holder", "knowledge.manage")
	alice := h.user("Alice")
	group := h.group("Staff")
	for _, body := range []string{
		`{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + alice + `"}`,
		`{"roleId":"` + role.ID + `","subjectType":"directory_group","subjectId":"` + group + `"}`,
	} {
		if rec := h.do("POST", "/api/v1/role-assignments", body); rec.Code != 201 {
			t.Fatalf("assign: %d %s", rec.Code, rec.Body)
		}
	}
	rec := h.do("GET", "/api/v1/users/"+alice+"/effective-permissions", "")
	var ep struct {
		Roles []struct {
			RoleID, Source string
		}
		Permissions []struct {
			Name      string
			GrantedBy []string
		}
		Ceilings []string
	}
	decode(t, rec, &ep)
	if rec.Code != 200 || len(ep.Roles) != 1 || ep.Roles[0].Source != "direct" || len(ep.Permissions) != 1 || ep.Permissions[0].Name != "knowledge.manage" || len(ep.Permissions[0].GrantedBy) != 1 {
		t.Fatalf("effective: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("GET", "/api/v1/users/"+h.newID()+"/effective-permissions", ""); rec.Code != 404 {
		t.Errorf("unknown user: %d", rec.Code)
	}
	rec = h.do("GET", "/api/v1/access/holders?permission=knowledge.manage", "")
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(alice)) || !bytes.Contains(rec.Body.Bytes(), []byte(group)) {
		t.Errorf("holders: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do("GET", "/api/v1/roles/"+role.ID+"/members", ""); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(alice)) {
		t.Errorf("members: %d %s", rec.Code, rec.Body)
	}
	// An administrator assignment never expires, whoever asks.
	built, _ := h.svc.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	body := `{"roleId":"` + built.ID + `","subjectType":"user","subjectId":"` + alice + `","expiresAt":"2999-01-01T00:00:00Z"}`
	if rec = h.do("POST", "/api/v1/role-assignments", body); rec.Code != 409 || errCode(t, rec) != "access.admin_no_expiry" {
		t.Errorf("administrator with expiry: %d %s", rec.Code, rec.Body)
	}
	// Self assignment is refused for everyone, including administrators.
	self := `{"roleId":"` + role.ID + `","subjectType":"user","subjectId":"` + h.admin + `"}`
	if rec = h.do("POST", "/api/v1/role-assignments", self); rec.Code != 409 || errCode(t, rec) != "access.self_assignment" {
		t.Errorf("self assignment: %d %s", rec.Code, rec.Body)
	}
}

func (h *httpFixture) mkRole(suffix string, perms ...string) roleDTO {
	h.t.Helper()
	list, _ := json.Marshal(perms)
	rec := h.do("POST", "/api/v1/roles", `{"key":"`+h.pfx+`-`+suffix+`","name":"R","permissions":`+string(list)+`}`)
	var r roleDTO
	decode(h.t, rec, &r)
	if rec.Code != 201 {
		h.t.Fatalf("create role: %d %s", rec.Code, rec.Body)
	}
	return r
}
