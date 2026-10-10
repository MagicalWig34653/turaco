package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

type noMail struct{}

func (noMail) BaseURLConfigured() bool { return true }
func (noMail) MailConfigured() bool    { return false }
func (noMail) Link(token, purpose string) string {
	return "https://turaco.example.test/set-password#token=" + token + "&purpose=" + purpose
}
func (noMail) Send(context.Context, string, string, string, string) error { return nil }

type peopleHTTP struct {
	t     *testing.T
	pool  *pgxpool.Pool
	mux   *http.ServeMux
	pfx   string
	actor string
	perms map[string]struct{}
}

// as authenticates requests as userID holding perms.
type as struct {
	h *peopleHTTP
}

func (a as) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	if a.h.actor == "" {
		return authorization.Principal{}, false, nil
	}
	return authorization.Principal{UserID: a.h.actor, Permissions: a.h.perms}, true, nil
}

func newPeopleHTTP(t *testing.T) *peopleHTTP {
	pool := dbtest.Pool(t)
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	h := &peopleHTTP{t: t, pool: pool, pfx: "zt" + hex.EncodeToString(b), mux: http.NewServeMux()}
	subjects := public.NewAuthorizationSubjects(repository.New(pool))
	repo := repository.New(pool).WithGuards(roles.NewGuards(pool, subjects), authentication.SessionRevoker{}).
		WithCredentials(authentication.NewLocalCredentials(nil), noMail{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := as{h}
	RegisterTeams(h.mux, application.NewTeams(repo), auth, logger)
	RegisterPeople(h.mux, application.NewPeople(repo), application.NewQueries(repo, query.NewEphemeralEngine().WithLimiter(query.NewLimiter(1000, 1000))), repo, auth, logger)
	Register(h.mux, repo, repo, "", auth, logger)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`,
			`DELETE FROM organization.team_memberships WHERE team_id IN (SELECT id FROM organization.teams WHERE name LIKE $1 || '%')`,
			`DELETE FROM organization.teams WHERE name LIKE $1 || '%'`,
			`DELETE FROM platform.credential_tokens WHERE user_id IN (SELECT id FROM organization.users WHERE display_name LIKE $1 || '%')`,
			`DELETE FROM platform.local_credentials WHERE user_id IN (SELECT id FROM organization.users WHERE display_name LIKE $1 || '%')`,
			`UPDATE organization.users SET department_id = NULL, primary_location_id = NULL WHERE display_name LIKE $1 || '%'`,
			`DELETE FROM organization.users WHERE display_name LIKE $1 || '%'`,
			`DELETE FROM organization.departments WHERE name LIKE $1 || '%'`,
			`DELETE FROM organization.locations WHERE name LIKE $1 || '%'`,
		} {
			if _, err := pool.Exec(ctx, q, h.pfx); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return h
}

func (h *peopleHTTP) as(actor string, perms ...string) {
	h.actor = actor
	h.perms = map[string]struct{}{}
	for _, p := range perms {
		h.perms[p] = struct{}{}
	}
}

func (h *peopleHTTP) do(method, path, body string) (int, map[string]any, http.Header) {
	h.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", h.pfx+"-req")
	httpx.NoStore(h.mux).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func errCodeOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (h *peopleHTTP) createUser(name string) string {
	h.t.Helper()
	h.as("", "")
	h.actor = h.mustActor()
	code, body, _ := h.do("POST", "/api/v1/users", fmt.Sprintf(`{"displayName":"%s %s","primaryEmail":"%s-%s@example.test"}`, h.pfx, name, h.pfx, strings.ToLower(name)))
	if code != 201 {
		h.t.Fatalf("create %s: %d %v", name, code, body)
	}
	return body["id"].(string)
}

// mustActor makes a fixed actor with the create permission (set up).
func (h *peopleHTTP) mustActor() string {
	var id string
	if err := h.pool.QueryRow(context.Background(), `INSERT INTO organization.users(display_name) VALUES ($1) RETURNING id::text`, h.pfx+" Setup").Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	h.perms = map[string]struct{}{"organization.users.manage": {}, "organization.view": {}}
	return id
}

func TestPeopleRoutesAuthorizationMatrix(t *testing.T) {
	h := newPeopleHTTP(t)
	id := "00000000-0000-7000-8000-000000000001"
	routes := []struct{ method, path, body, perm string }{
		{"POST", "/api/v1/users", `{"displayName":"x"}`, "organization.users.manage"},
		{"PATCH", "/api/v1/users/" + id + "/profile", `{"expectedVersion":1,"givenName":"x"}`, "organization.users.manage"},
		{"PUT", "/api/v1/users/" + id + "/department", `{"expectedVersion":1,"departmentId":null}`, "organization.users.manage"},
		{"PUT", "/api/v1/users/" + id + "/primary-location", `{"expectedVersion":1,"locationId":null}`, "organization.users.manage"},
		{"PUT", "/api/v1/users/" + id + "/manager", `{"expectedVersion":1,"managerUserId":null}`, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/deactivate", `{"expectedVersion":1,"reason":"no_longer_needed"}`, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/reactivate", `{"expectedVersion":1,"reason":"returned"}`, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/mark-departed", `{"expectedVersion":1,"reason":"retired"}`, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/send-invitation", ``, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/reset-password", ``, "organization.users.manage"},
		{"POST", "/api/v1/locations", `{"name":"x"}`, "organization.locations.manage"},
		{"PATCH", "/api/v1/locations/" + id, `{"expectedVersion":1,"name":"x"}`, "organization.locations.manage"},
		{"POST", "/api/v1/locations/" + id + "/move", `{"expectedVersion":1,"parentId":null}`, "organization.locations.manage"},
		{"POST", "/api/v1/locations/" + id + "/deactivate", `{"expectedVersion":1}`, "organization.locations.manage"},
		{"POST", "/api/v1/locations/" + id + "/activate", `{"expectedVersion":1}`, "organization.locations.manage"},
		{"POST", "/api/v1/departments", `{"name":"x"}`, "organization.departments.manage"},
		{"PATCH", "/api/v1/departments/" + id, `{"expectedVersion":1,"name":"x"}`, "organization.departments.manage"},
		{"POST", "/api/v1/departments/" + id + "/move", `{"expectedVersion":1,"parentId":null}`, "organization.departments.manage"},
		{"POST", "/api/v1/departments/" + id + "/deactivate", `{"expectedVersion":1}`, "organization.departments.manage"},
		{"POST", "/api/v1/departments/" + id + "/activate", `{"expectedVersion":1}`, "organization.departments.manage"},
		{"PUT", "/api/v1/teams/" + id + "/members/" + id + "/role", `{"role":"lead"}`, "organization.teams.manage"},
		{"GET", "/api/v1/users/fields", ``, "organization.view"},
		{"POST", "/api/v1/users/query", `{}`, "organization.view"},
		{"POST", "/api/v1/teams/query", `{}`, "organization.view"},
		{"POST", "/api/v1/locations/query", `{}`, "organization.view"},
		{"POST", "/api/v1/departments/query", `{}`, "organization.view"},
		{"GET", "/api/v1/departments", ``, "organization.view"},
		{"GET", "/api/v1/users/" + id + "/teams", ``, "organization.view"},
	}
	others := []string{"organization.view", "organization.users.manage", "organization.locations.manage", "organization.departments.manage", "organization.teams.manage", "tasks.manage", "platform.roles.manage"}
	for _, rt := range routes {
		h.as("")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 401 {
			t.Errorf("%s %s unauthenticated = %d", rt.method, rt.path, code)
		}
		h.as(id, "tasks.manage", "platform.admin")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 403 {
			t.Errorf("%s %s without any organization permission = %d", rt.method, rt.path, code)
		}
		var wrong []string
		for _, p := range others {
			if p != rt.perm {
				wrong = append(wrong, p)
			}
		}
		if rt.perm != "organization.view" { // every other permission must not open a manage route
			wrong = wrong[:0]
			for _, p := range others {
				if p != rt.perm && p != "organization.view" {
					wrong = append(wrong, p)
				}
			}
			h.as(id, wrong...)
			if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 403 {
				t.Errorf("%s %s with other permissions = %d", rt.method, rt.path, code)
			}
		}
		h.as(id, rt.perm)
		if code, body, _ := h.do(rt.method, rt.path, rt.body); code == 401 || code == 403 {
			t.Errorf("%s %s with %s = %d %v", rt.method, rt.path, rt.perm, code, body)
		}
	}
}

func TestUserOperationsRequireVersionAndReportOwnership(t *testing.T) {
	h := newPeopleHTTP(t)
	h.as("", "")
	setup := h.mustActor()
	h.actor = setup
	h.perms["organization.users.manage"] = struct{}{}
	id := h.createUser("Dana")
	h.actor = setup
	// No expectedVersion, no change.
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/users/" + id + "/profile", `{"givenName":"Dana"}`},
		{"PUT", "/api/v1/users/" + id + "/department", `{"departmentId":null}`},
		{"POST", "/api/v1/users/" + id + "/deactivate", `{"reason":"no_longer_needed"}`},
	} {
		if code, body, _ := h.do(c.method, c.path, c.body); code != 400 || errCodeOf(body) != "organization.invalid_request" {
			t.Errorf("%s %s without version: %d %v", c.method, c.path, code, body)
		}
	}
	// The reason code is a closed list.
	if code, _, _ := h.do("POST", "/api/v1/users/"+id+"/deactivate", `{"expectedVersion":1,"reason":"because I said so"}`); code != 400 {
		t.Errorf("free-text reason: %d", code)
	}
	// Unknown fields are rejected (no mass assignment of status or kind).
	for _, body := range []string{`{"expectedVersion":1,"status":"inactive"}`, `{"expectedVersion":1,"accountKind":"external"}`, `{"expectedVersion":1,"origin":"directory"}`} {
		if code, _, _ := h.do("PATCH", "/api/v1/users/"+id+"/profile", body); code != 400 {
			t.Errorf("profile body %s: %d", body, code)
		}
	}
	// An external account cannot be created before the External Party slice exists.
	if code, _, _ := h.do("POST", "/api/v1/users", `{"displayName":"`+h.pfx+` X","accountKind":"external"}`); code != 400 {
		t.Errorf("external creation: %d", code)
	}
	// Directory-owned fields answer with the field names.
	var dir string
	_ = h.pool.QueryRow(context.Background(), `INSERT INTO organization.users(display_name) VALUES ($1) RETURNING id::text`, h.pfx+" Directory Dave").Scan(&dir)
	code, body, _ := h.do("PATCH", "/api/v1/users/"+dir+"/profile", `{"expectedVersion":1,"primaryEmail":"x@example.test"}`)
	details, _ := body["error"].(map[string]any)["details"].(map[string]any)
	if code != 409 || errCodeOf(body) != "organization.field_directory_owned" || fmt.Sprint(details["fields"]) != "[primaryEmail]" {
		t.Errorf("directory-owned: %d %v", code, body)
	}
	// The detail shows who owns what.
	h.perms["organization.view"] = struct{}{}
	code, body, _ = h.do("GET", "/api/v1/users/"+dir, ``)
	fields, _ := body["fields"].([]any)
	var ownerOfEmail string
	for _, f := range fields {
		if m := f.(map[string]any); m["key"] == "primaryEmail" {
			ownerOfEmail, _ = m["owner"].(string)
		}
	}
	if code != 200 || ownerOfEmail != "directory" || body["source"] != "directory" || body["accountKind"] != "employee" {
		t.Errorf("detail: %d %v", code, body)
	}
	if _, hasNumber := body["employeeNumber"]; hasNumber {
		t.Error("the employee number needs view_details")
	}
}

func TestInvitationLinkIsShownOnceAndNeverForResets(t *testing.T) {
	h := newPeopleHTTP(t)
	h.as("", "")
	setup := h.mustActor()
	h.actor = setup
	id := h.createUser("Erin")
	h.actor = setup
	h.perms["platform.admin"] = struct{}{} // only an administrator is shown an unmailed link
	code, body, hdr := h.do("POST", "/api/v1/users/"+id+"/send-invitation", ``)
	link, _ := body["link"].(string)
	if code != 200 || !strings.HasPrefix(link, "https://turaco.example.test/set-password#token=") || hdr.Get("Cache-Control") != "no-store" || hdr.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("invitation: %d %v %v", code, body, hdr)
	}
	if strings.Contains(link, "?") || strings.Contains(strings.SplitN(link, "#", 2)[0], "token") {
		t.Errorf("the token must travel in the fragment only: %s", link)
	}
	// Without a mail channel a caller that is no platform administrator gets no link, and no token is issued.
	other := h.createUser("Frank")
	delete(h.perms, "platform.admin")
	if code, body, _ := h.do("POST", "/api/v1/users/"+other+"/send-invitation", ``); code != 409 || errCodeOf(body) != "auth.mail_not_configured" {
		t.Errorf("invitation by a non-administrator without mail: %d %v", code, body)
	}
	h.perms["platform.admin"] = struct{}{}
	// A reset needs mail and is never returned in the response.
	if code, body, _ := h.do("POST", "/api/v1/users/"+id+"/reset-password", ``); code != 409 || errCodeOf(body) != "auth.mail_not_configured" {
		t.Errorf("reset without mail: %d %v", code, body)
	}
	// The audit record never contains the token.
	var leaked int
	token := strings.SplitN(strings.SplitN(link, "#token=", 2)[1], "&", 2)[0]
	_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.audit_events WHERE metadata::text LIKE '%' || $1 || '%' OR after_data::text LIKE '%' || $1 || '%'`, token).Scan(&leaked)
	if leaked != 0 {
		t.Error("the token is in the audit trail")
	}
}

func TestQueryCatalogHidesHRFieldsWithoutViewDetails(t *testing.T) {
	h := newPeopleHTTP(t)
	h.as("", "")
	setup := h.mustActor()
	h.actor = setup
	h.createUser("Fay")
	h.actor = setup

	keys := func(perms ...string) map[string]bool {
		h.as(setup, perms...)
		code, body, _ := h.do("GET", "/api/v1/users/fields", ``)
		if code != 200 {
			t.Fatalf("fields: %d %v", code, body)
		}
		out := map[string]bool{}
		for _, f := range body["fields"].([]any) {
			out[f.(map[string]any)["key"].(string)] = true
		}
		return out
	}
	plain := keys("organization.view")
	detailed := keys("organization.view", "organization.users.view_details")
	for _, hr := range []string{"manager", "department", "location", "employee_number", "access_expires_at"} {
		if plain[hr] || !detailed[hr] {
			t.Errorf("field %s: plain=%v detailed=%v", hr, plain[hr], detailed[hr])
		}
	}
	// Filtering on a hidden field is refused (no probing by bisection) and the item has no HR data.
	h.as(setup, "organization.view")
	filter := `{"filter":{"v":1,"root":{"type":"condition","field":"employee_number","op":"equals","value":"1"}}}`
	if code, body, _ := h.do("POST", "/api/v1/users/query", filter); code != 400 {
		t.Errorf("filter on a hidden field: %d %v", code, body)
	}
	code, body, _ := h.do("POST", "/api/v1/users/query", `{"search":"`+h.pfx+`"}`)
	items, _ := body["items"].([]any)
	if code != 200 || len(items) == 0 {
		t.Fatalf("query: %d %v", code, body)
	}
	for _, it := range items {
		for _, hr := range []string{"managerUserId", "departmentId", "primaryLocationId", "employeeNumber", "accessExpiresAt"} {
			if _, ok := it.(map[string]any)[hr]; ok {
				t.Errorf("list item exposes %s without view_details", hr)
			}
		}
	}
	h.as(setup, "organization.view", "organization.users.view_details")
	if code, body, _ := h.do("POST", "/api/v1/users/query", `{"search":"`+h.pfx+`","sort":[{"field":"display_name","dir":"asc"}],"count":true}`); code != 200 || body["count"] == nil {
		t.Errorf("detailed query: %d %v", code, body)
	}
}

func TestLocationAndDepartmentEndpoints(t *testing.T) {
	h := newPeopleHTTP(t)
	h.as("", "")
	setup := h.mustActor()
	h.as(setup, "organization.locations.manage", "organization.departments.manage", "organization.view", "organization.users.manage")
	code, site, _ := h.do("POST", "/api/v1/locations", fmt.Sprintf(`{"kind":"site","name":"%s Site"}`, h.pfx))
	if code != 201 || site["kind"] != "site" || site["version"] != float64(1) {
		t.Fatalf("site: %d %v", code, site)
	}
	if code, body, _ := h.do("POST", "/api/v1/locations", fmt.Sprintf(`{"kind":"area","name":"%s Bad"}`, h.pfx)); code != 400 {
		t.Errorf("area without parent: %d %v", code, body)
	}
	code, area, _ := h.do("POST", "/api/v1/locations", fmt.Sprintf(`{"kind":"area","parentId":"%s","name":"%s Area"}`, site["id"], h.pfx))
	if code != 201 || area["parentId"] != site["id"] {
		t.Fatalf("area: %d %v", code, area)
	}
	if code, body, _ := h.do("POST", "/api/v1/locations/"+site["id"].(string)+"/move", fmt.Sprintf(`{"expectedVersion":1,"parentId":"%s"}`, area["id"])); code != 409 || errCodeOf(body) != "organization.invalid_hierarchy" {
		t.Errorf("cycle: %d %v", code, body)
	}
	if code, body, _ := h.do("POST", "/api/v1/locations/"+area["id"].(string)+"/move", `{"expectedVersion":1}`); code != 400 {
		t.Errorf("move without parentId key: %d %v", code, body)
	}
	// Deactivating a site with an active area needs confirmImpact.
	code, body, _ := h.do("POST", "/api/v1/locations/"+site["id"].(string)+"/deactivate", `{"expectedVersion":1}`)
	counts, _ := body["error"].(map[string]any)["details"].(map[string]any)["counts"].(map[string]any)
	if code != 409 || errCodeOf(body) != "organization.impact_confirmation_required" || counts["areas"] != float64(1) {
		t.Errorf("impact: %d %v", code, body)
	}
	if code, body, _ := h.do("POST", "/api/v1/locations/"+site["id"].(string)+"/deactivate", `{"expectedVersion":1,"confirmImpact":true}`); code != 200 || body["active"] != false {
		t.Errorf("confirmed: %d %v", code, body)
	}
	// Departments.
	code, dept, _ := h.do("POST", "/api/v1/departments", fmt.Sprintf(`{"name":"%s Dept","code":"%s-d"}`, h.pfx, h.pfx))
	if code != 201 {
		t.Fatalf("department: %d %v", code, dept)
	}
	if code, body, _ := h.do("POST", "/api/v1/departments", fmt.Sprintf(`{"name":"%s Dept2","code":"%s-D"}`, h.pfx, strings.ToUpper(h.pfx))); code != 409 || errCodeOf(body) != "organization.conflict" {
		t.Errorf("duplicate code: %d %v", code, body)
	}
	if code, body, _ := h.do("GET", "/api/v1/departments/"+dept["id"].(string), ``); code != 200 || body["code"] != h.pfx+"-d" {
		t.Errorf("get department: %d %v", code, body)
	}
}

func TestTeamEndpointsMembershipAndDescription(t *testing.T) {
	h := newPeopleHTTP(t)
	h.as("", "")
	setup := h.mustActor()
	h.as(setup, "organization.teams.manage", "organization.view", "organization.users.manage")
	code, team, _ := h.do("POST", "/api/v1/teams", fmt.Sprintf(`{"name":"%s Team"}`, h.pfx))
	if code != 201 {
		t.Fatalf("team: %d %v", code, team)
	}
	tid := team["id"].(string)
	uid := h.createUser("Gus")
	h.as(setup, "organization.teams.manage", "organization.view")
	if code, body, _ := h.do("POST", "/api/v1/teams/"+tid+"/members", fmt.Sprintf(`{"userId":"%s","role":"Boss"}`, uid)); code != 400 {
		t.Errorf("free-text member role: %d %v", code, body)
	}
	if code, _, _ := h.do("POST", "/api/v1/teams/"+tid+"/members", fmt.Sprintf(`{"userId":"%s"}`, uid)); code != 201 {
		t.Fatalf("add member: %d", code)
	}
	code, body, _ := h.do("PUT", "/api/v1/teams/"+tid+"/members/"+uid+"/role", `{"role":"lead"}`)
	if code != 200 || body["role"] != "lead" {
		t.Errorf("set lead: %d %v", code, body)
	}
	code, body, _ = h.do("GET", "/api/v1/teams/"+tid, ``)
	leads, _ := body["leads"].([]any)
	if code != 200 || len(leads) != 1 {
		t.Errorf("team leads: %d %v", code, body)
	}
	// A description change needs the version; name and description are separate requests.
	if code, _, _ := h.do("PATCH", "/api/v1/teams/"+tid, `{"description":"x"}`); code != 400 {
		t.Errorf("description without version: %d", code)
	}
	if code, _, _ := h.do("PATCH", "/api/v1/teams/"+tid, `{"name":"y","description":"x","expectedVersion":1}`); code != 400 {
		t.Errorf("name and description together: %d", code)
	}
	cur := body["version"]
	if code, body, _ := h.do("PATCH", "/api/v1/teams/"+tid, fmt.Sprintf(`{"description":"Handles things","expectedVersion":%v}`, cur)); code != 200 || body["description"] != "Handles things" {
		t.Errorf("description: %d %v", code, body)
	}
	// Self membership answers its own code, whoever the actor is.
	h.as(uid, "organization.teams.manage")
	if code, body, _ := h.do("PUT", "/api/v1/teams/"+tid+"/members/"+uid+"/role", `{"role":"member"}`); code != 409 || errCodeOf(body) != "access.team_membership_self" {
		t.Errorf("self role change: %d %v", code, body)
	}
	if code, body, _ := h.do("DELETE", "/api/v1/teams/"+tid+"/members/"+uid, ``); code != 409 || errCodeOf(body) != "access.team_membership_self" {
		t.Errorf("self removal: %d %v", code, body)
	}
}
