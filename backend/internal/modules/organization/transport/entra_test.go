package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// adminActor creates a user who really holds the built-in administrator role (the dominance rule reads the database)
// and makes the next requests run as that user with platform.admin.
func (h *peopleHTTP) adminActor() string {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(context.Background(), `INSERT INTO organization.users(display_name) VALUES ($1) RETURNING id::text`, h.pfx+" Admin").Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	svc := roles.NewService(h.pool, public.NewAuthorizationSubjects(repository.New(h.pool)))
	built, err := svc.GetRoleByKey(context.Background(), roles.AdministratorRoleKey)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := svc.AssignRole(context.Background(), audit.CLIActor("test"), h.pfx+"-assign", roles.AssignInput{RoleID: built.ID, SubjectType: roles.SubjectUser, SubjectID: id}); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM platform.role_assignments WHERE subject_id = $1`, id)
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM platform.audit_events WHERE correlation_id = $1`, h.pfx+"-assign")
	})
	h.as(id, "platform.admin", "organization.view", "organization.users.manage")
	return id
}

func (h *peopleHTTP) newUserVersion(name string) (string, int) {
	h.t.Helper()
	admin := h.actor
	perms := h.perms
	id := h.createUser(name)
	h.actor, h.perms = admin, perms
	return id, 1
}

func TestEntraRoutesAuthorizationMatrix(t *testing.T) {
	h := newPeopleHTTP(t)
	id := "00000000-0000-7000-8000-000000000001"
	oid := "0f0f0f0f-0000-1111-2222-333333333333"
	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/users/entra-linking", ``},
		{"POST", "/api/v1/users/" + id + "/external-identities/entra", fmt.Sprintf(`{"expectedVersion":1,"tenantId":%q,"objectId":%q}`, testEntraTenant, oid)},
		{"DELETE", "/api/v1/users/" + id + "/external-identities/" + id, ``},
	}
	others := []string{"organization.view", "organization.users.manage", "organization.locations.manage", "organization.departments.manage", "organization.teams.manage",
		"organization.import", "organization.external_parties.manage", "platform.roles.manage", "platform.health.view", "tasks.manage"}
	for _, rt := range routes {
		h.as("")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 401 {
			t.Errorf("%s %s unauthenticated = %d", rt.method, rt.path, code)
		}
		h.as(id, others...)
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 403 {
			t.Errorf("%s %s with every other permission = %d, want 403", rt.method, rt.path, code)
		}
		h.as(id, "platform.admin")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code == 401 || code == 403 {
			t.Errorf("%s %s as administrator = %d", rt.method, rt.path, code)
		}
	}
}

func TestAdministratorLinksListsAndUnlinksAnEntraIdentity(t *testing.T) {
	h := newPeopleHTTP(t)
	admin := h.adminActor()
	_ = admin
	userID, _ := h.newUserVersion("Linda")
	oid := "0f0f0f0f-0000-1111-2222-3333333300ab"
	link := fmt.Sprintf(`{"expectedVersion":1,"tenantId":%q,"objectId":%q}`, testEntraTenant, strings.ToUpper(oid))

	// The tenant default is offered to the UI.
	code, info, _ := h.do("GET", "/api/v1/users/entra-linking", "")
	if code != 200 || info["configured"] != true || info["tenantId"] != testEntraTenant {
		t.Fatalf("info %d %v", code, info)
	}

	code, body, _ := h.do("POST", "/api/v1/users/"+userID+"/external-identities/entra", link)
	if code != 201 {
		t.Fatalf("link = %d %v", code, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["version"] != float64(2) || body["noticeSent"] != true || body["credentialDeleted"] != false {
		t.Fatalf("link result %v", body)
	}
	if len(h.notices.events) != 1 || !strings.HasPrefix(h.notices.events[0], "linked ") {
		t.Fatalf("notices %v", h.notices.events)
	}

	// The detail lists the identity: provider, last four characters, linked-at and route; never the whole subject.
	code, detail, _ := h.do("GET", "/api/v1/users/"+userID, "")
	ids, _ := detail["externalIdentities"].([]any)
	if code != 200 || len(ids) != 1 {
		t.Fatalf("detail %d %v", code, detail)
	}
	first, _ := ids[0].(map[string]any)
	if first["providerKey"] != "entra:"+testEntraTenant || first["subjectSuffix"] != "00ab" || first["via"] != "administrator" || first["linkedAt"] == "" || first["id"] == "" {
		t.Fatalf("identity %v", first)
	}
	if strings.Contains(fmt.Sprint(detail), strings.ToLower(oid)) {
		t.Fatalf("the detail leaks the whole subject: %v", detail)
	}
	identityID, _ := first["id"].(string)

	// The same object id cannot be linked to a second user, a second link for the user is refused.
	other, _ := h.newUserVersion("Otto")
	if code, b, _ := h.do("POST", "/api/v1/users/"+other+"/external-identities/entra", link); code != 409 || errCodeOf(b) != "organization.entra_identity_in_use" {
		t.Errorf("taken = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/"+userID+"/external-identities/entra", strings.Replace(link, `"expectedVersion":1`, `"expectedVersion":2`, 1)); code != 409 || errCodeOf(b) != "organization.entra_tenant_already_linked" {
		t.Errorf("second identity = %d %v", code, b)
	}

	code, un, _ := h.do("DELETE", "/api/v1/users/"+userID+"/external-identities/"+identityID, "")
	if code != 200 || un["noticeSent"] != true {
		t.Fatalf("unlink = %d %v", code, un)
	}
	if code, b, _ := h.do("DELETE", "/api/v1/users/"+userID+"/external-identities/"+identityID, ""); code != 404 || errCodeOf(b) != "organization.not_found" {
		t.Errorf("second unlink = %d %v", code, b)
	}
}

func TestEntraLinkRefusalsOverHTTP(t *testing.T) {
	h := newPeopleHTTP(t)
	admin := h.adminActor()
	userID, _ := h.newUserVersion("Rita")
	oid := "0f0f0f0f-0000-1111-2222-3333333300cd"
	post := func(id, body string) (int, map[string]any) {
		code, b, _ := h.do("POST", "/api/v1/users/"+id+"/external-identities/entra", body)
		return code, b
	}
	good := fmt.Sprintf(`{"expectedVersion":1,"tenantId":%q,"objectId":%q}`, testEntraTenant, oid)

	cases := map[string]struct {
		id, body string
		code     int
		err      string
	}{
		"missing version":      {userID, fmt.Sprintf(`{"tenantId":%q,"objectId":%q}`, testEntraTenant, oid), 400, "organization.invalid_request"},
		"unknown field":        {userID, fmt.Sprintf(`{"expectedVersion":1,"tenantId":%q,"objectId":%q,"origin":"directory"}`, testEntraTenant, oid), 400, "organization.invalid_request"},
		"object id not a guid": {userID, fmt.Sprintf(`{"expectedVersion":1,"tenantId":%q,"objectId":"anna@example.org"}`, testEntraTenant), 400, "organization.invalid_request"},
		"tenant not a guid":    {userID, fmt.Sprintf(`{"expectedVersion":1,"tenantId":"common","objectId":%q}`, oid), 400, "organization.invalid_request"},
		"foreign tenant":       {userID, fmt.Sprintf(`{"expectedVersion":1,"tenantId":"99999999-2222-3333-4444-555555555555","objectId":%q}`, oid), 409, "organization.entra_tenant_not_allowed"},
		"consumer tenant":      {userID, fmt.Sprintf(`{"expectedVersion":1,"tenantId":"9188040d-6c67-4c5b-b112-36a304b66dad","objectId":%q}`, oid), 409, "organization.entra_tenant_not_allowed"},
		"stale version":        {userID, strings.Replace(good, `"expectedVersion":1`, `"expectedVersion":7`, 1), 409, "organization.version_conflict"},
		"unknown user":         {"00000000-0000-7000-8000-00000000dead", good, 404, "organization.not_found"},
		"oneself":              {admin, strings.Replace(good, `"expectedVersion":1`, `"expectedVersion":2`, 1), 409, "organization.self_operation"},
	}
	for name, tc := range cases {
		if code, b := post(tc.id, tc.body); code != tc.code || errCodeOf(b) != tc.err {
			t.Errorf("%s = %d %v", name, code, b)
		}
	}

	// Inactive target.
	inactive, _ := h.newUserVersion("Ina")
	if _, err := h.pool.Exec(context.Background(), `UPDATE organization.users SET status = 'inactive' WHERE id = $1`, inactive); err != nil {
		t.Fatal(err)
	}
	if code, b := post(inactive, good); code != 409 || errCodeOf(b) != "organization.invalid_state" {
		t.Errorf("inactive = %d %v", code, b)
	}
	// Emergency account.
	var emergency string
	if err := h.pool.QueryRow(context.Background(), `INSERT INTO organization.users (display_name, origin) VALUES ($1, 'emergency') RETURNING id::text`, h.pfx+" Emergency").Scan(&emergency); err != nil {
		t.Fatal(err)
	}
	if code, b := post(emergency, good); code != 409 || errCodeOf(b) != "organization.emergency_account" {
		t.Errorf("emergency = %d %v", code, b)
	}
	// A local account that holds a role cannot have its password replaced (R5).
	holder, _ := h.newUserVersion("Hilda")
	h.grantRole(holder, "tickets.view")
	h.invite(holder)
	if code, b := post(holder, good); code != 409 || errCodeOf(b) != "organization.directory_link_refused_roles" {
		t.Errorf("local credential with role = %d %v", code, b)
	}
	// A local account without roles is linked and loses its password atomically.
	plain, _ := h.newUserVersion("Paula")
	h.invite(plain)
	code, b := post(plain, strings.Replace(good, "00cd", "00ce", 1))
	if code != 201 || b["credentialDeleted"] != true {
		t.Errorf("local credential replaced = %d %v", code, b)
	}
	var creds int
	_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.local_credentials WHERE user_id = $1`, plain).Scan(&creds)
	if creds != 0 {
		t.Errorf("credentials left: %d", creds)
	}
	// Mail failure never fails a committed change.
	h.notices.err = errors.New("relay down")
	mailFail, _ := h.newUserVersion("Mia")
	code, b = post(mailFail, strings.Replace(good, "00cd", "00cf", 1))
	if code != 201 || b["noticeSent"] != false {
		t.Errorf("mail failure = %d %v", code, b)
	}
}

// grantRole assigns a custom role with perms to the user.
func (h *peopleHTTP) grantRole(userID string, perms ...string) {
	h.t.Helper()
	svc := roles.NewService(h.pool, public.NewAuthorizationSubjects(repository.New(h.pool)))
	var keys []string
	for _, r := range roles.SoDRules() {
		keys = append(keys, r.Key)
	}
	role, err := svc.CreateRole(context.Background(), audit.CLIActor("test"), h.pfx+"-role", roles.CreateRoleInput{Key: h.pfx + "-r-" + userID[:8], Name: "Test " + h.pfx, Permissions: perms,
		Acknowledgement: roles.Acknowledgement{Rules: keys, Reason: "test"}})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := svc.AssignRole(context.Background(), audit.CLIActor("test"), h.pfx+"-role", roles.AssignInput{RoleID: role.ID, SubjectType: roles.SubjectUser, SubjectID: userID,
		Acknowledgement: roles.Acknowledgement{Rules: keys, Reason: "test"}}); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.pool.Exec(ctx, `DELETE FROM platform.role_assignments WHERE role_id = $1`, role.ID)
		_, _ = h.pool.Exec(ctx, `DELETE FROM platform.roles WHERE id = $1`, role.ID)
		_, _ = h.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, h.pfx+"-role")
	})
}

// invite gives the user a local credential through the invitation route (as an administrator).
func (h *peopleHTTP) invite(userID string) {
	h.t.Helper()
	if code, b, _ := h.do("POST", "/api/v1/users/"+userID+"/send-invitation", ""); code != 200 && code != 201 {
		h.t.Fatalf("invite = %d %v", code, b)
	}
}
