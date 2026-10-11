package transport

import (
	"fmt"
	"testing"
	"time"
)

func TestExternalPartyRoutesAuthorizationMatrix(t *testing.T) {
	h := newPeopleHTTP(t)
	id := "00000000-0000-7000-8000-000000000001"
	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/external-parties", ""},
		{"POST", "/api/v1/external-parties", `{"kind":"vendor","name":"x","sponsorUserId":"` + id + `"}`},
		{"GET", "/api/v1/external-parties/" + id, ""},
		{"PATCH", "/api/v1/external-parties/" + id, `{"expectedVersion":1,"notes":"x"}`},
	}
	others := []string{"organization.view", "organization.users.manage", "organization.teams.manage", "organization.import", "platform.admin", "tasks.manage"}
	for _, rt := range routes {
		h.as("")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 401 {
			t.Errorf("%s %s unauthenticated = %d", rt.method, rt.path, code)
		}
		h.as(id, others...)
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 403 {
			t.Errorf("%s %s with every other permission = %d, want 403", rt.method, rt.path, code)
		}
	}
}

func TestExternalPartiesAndAccountsOverHTTP(t *testing.T) {
	h := newPeopleHTTP(t)
	actor := h.mustActor()
	sponsor := h.createUser("Sponsor")
	h.as(actor, "organization.external_parties.manage", "organization.users.manage", "organization.view")

	body := fmt.Sprintf(`{"kind":"vendor","name":"%s Acme","sponsorUserId":%q,"notes":"printer maintenance"}`, h.pfx, sponsor)
	code, party, _ := h.do("POST", "/api/v1/external-parties", body)
	if code != 201 {
		t.Fatalf("create party = %d %v", code, party)
	}
	partyID := party["id"].(string)
	if party["sponsorName"] == "" || party["active"] != true || party["version"].(float64) != 1 {
		t.Errorf("party = %v", party)
	}
	if code, b, _ := h.do("POST", "/api/v1/external-parties", body); code != 409 || errCodeOf(b) != "organization.party_name_taken" {
		t.Errorf("duplicate = %d %v", code, b)
	}
	bad := fmt.Sprintf(`{"kind":"vendor","name":"%s Other","sponsorUserId":%q}`, h.pfx, "00000000-0000-7000-8000-000000000009")
	if code, b, _ := h.do("POST", "/api/v1/external-parties", bad); code != 409 || errCodeOf(b) != "organization.sponsor_invalid" {
		t.Errorf("unknown sponsor = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/external-parties", `{"kind":"supplier","name":"x","sponsorUserId":"`+sponsor+`"}`); code != 400 {
		t.Errorf("bad kind = %d %v", code, b)
	}
	if code, b, _ := h.do("PATCH", "/api/v1/external-parties/"+partyID, `{"expectedVersion":5,"notes":"x"}`); code != 409 || errCodeOf(b) != "organization.version_conflict" {
		t.Errorf("version = %d %v", code, b)
	}
	if code, b, _ := h.do("PATCH", "/api/v1/external-parties/"+partyID, `{"expectedVersion":1,"notes":"x","status":"gone"}`); code != 400 {
		t.Errorf("unknown field = %d %v", code, b)
	}

	// An external account needs a party, an email and an end of access of at most 365 days.
	until := time.Now().UTC().AddDate(0, 0, 60).Format(time.RFC3339)
	acct := fmt.Sprintf(`{"displayName":"%s Vera","primaryEmail":"%s-vera@vendor.example.test","accountKind":"external","externalPartyId":%q,"accessExpiresAt":%q}`, h.pfx, h.pfx, partyID, until)
	code, u, _ := h.do("POST", "/api/v1/users", acct)
	if code != 201 || u["accountKind"] != "external" || u["externalPartyId"] != partyID {
		t.Fatalf("create account = %d %v", code, u)
	}
	userID := u["id"].(string)
	tooLong := time.Now().UTC().AddDate(0, 0, 400).Format(time.RFC3339)
	if code, b, _ := h.do("POST", "/api/v1/users", fmt.Sprintf(`{"displayName":"%s Too","primaryEmail":"%s-too@vendor.example.test","accountKind":"external","externalPartyId":%q,"accessExpiresAt":%q}`, h.pfx, h.pfx, partyID, tooLong)); code != 400 {
		t.Errorf("400 days = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/users", fmt.Sprintf(`{"displayName":"%s NoParty","accountKind":"external","accessExpiresAt":%q}`, h.pfx, until)); code != 400 {
		t.Errorf("without a party = %d %v", code, b)
	}
	// The party shows its accounts; the detail DTO of the user names the party.
	code, detail, _ := h.do("GET", "/api/v1/external-parties/"+partyID, "")
	accounts, _ := detail["accounts"].([]any)
	if code != 200 || len(accounts) != 1 || detail["accountCount"].(float64) != 1 {
		t.Errorf("party detail = %d %v", code, detail)
	}
	code, list, _ := h.do("GET", "/api/v1/external-parties", "")
	if items, _ := list["items"].([]any); code != 200 || len(items) == 0 || list["kinds"] == nil {
		t.Errorf("list = %d %v", code, list)
	}

	// Creating an external account needs the external parties permission on top of users.manage.
	h.as(actor, "organization.users.manage", "organization.view")
	if code, b, _ := h.do("POST", "/api/v1/users", acct); code != 403 || errCodeOf(b) != "access.external_party_permission_required" {
		t.Errorf("without external_parties.manage = %d %v", code, b)
	}

	// The sponsor extends without any permission; somebody else cannot; the extension is bounded.
	later := time.Now().UTC().AddDate(0, 0, 120).Format(time.RFC3339)
	extend := fmt.Sprintf(`{"expectedVersion":1,"accessExpiresAt":%q,"reason":"sponsor_request"}`, later)
	h.as(actor, "organization.view")
	if code, b, _ := h.do("POST", "/api/v1/users/"+userID+"/extend-access", extend); code != 403 {
		t.Errorf("stranger extends = %d %v", code, b)
	}
	h.as(sponsor)
	if code, b, _ := h.do("POST", "/api/v1/users/"+userID+"/extend-access", extend); code != 200 || b["version"].(float64) != 2 {
		t.Errorf("sponsor extends = %d %v", code, b)
	}
	h.as(sponsor)
	if code, b, _ := h.do("GET", "/api/v1/external-parties", ""); code != 403 {
		t.Errorf("sponsor without the permission lists parties = %d %v", code, b)
	}

	// The sponsor's own page: no permission needed, exactly the sponsored parties, with versions to extend against.
	h.as(sponsor)
	code, own, _ := h.do("GET", "/api/v1/external-parties/sponsored", "")
	items, _ := own["items"].([]any)
	if code != 200 || len(items) != 1 || own["extendReasons"] == nil {
		t.Fatalf("sponsored = %d %v", code, own)
	}
	first := items[0].(map[string]any)
	accts, _ := first["accounts"].([]any)
	if first["id"] != partyID || len(accts) != 1 || accts[0].(map[string]any)["userId"] != userID || accts[0].(map[string]any)["version"].(float64) != 2 {
		t.Errorf("sponsored party = %v", first)
	}
	if _, leaked := accts[0].(map[string]any)["primaryEmail"]; leaked {
		t.Error("the sponsor's view must not carry the email address")
	}
	// Somebody who sponsors nothing sees an empty list, even with every administration permission; no session is 401.
	h.as(actor, "organization.external_parties.manage", "organization.users.manage", "organization.view", "platform.admin")
	if code, own, _ := h.do("GET", "/api/v1/external-parties/sponsored", ""); code != 200 || len(own["items"].([]any)) != 0 {
		t.Errorf("non-sponsor = %d %v", code, own)
	}
	h.as("")
	if code, _, _ := h.do("GET", "/api/v1/external-parties/sponsored", ""); code != 401 {
		t.Errorf("unauthenticated sponsored = %d", code)
	}
}
