package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// upload posts a multipart import request as the current actor.
func (h *peopleHTTP) upload(fields map[string]string, file string) (int, map[string]any) {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != "" {
		fw, _ := mw.CreateFormFile("file", "people.csv")
		_, _ = fw.Write([]byte(file))
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/import-batches", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", h.pfx+"-req")
	httpx.NoStore(h.mux).ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestImportAndBulkRoutesAuthorizationMatrix(t *testing.T) {
	h := newPeopleHTTP(t)
	id := "00000000-0000-7000-8000-000000000001"
	routes := []struct{ method, path, body, perm string }{
		{"POST", "/api/v1/users/bulk-operations", `{"dryRun":true,"operation":"set_department","userIds":["` + id + `"]}`, "organization.users.manage"},
		{"POST", "/api/v1/users/" + id + "/link-directory-identity", `{"expectedVersion":1,"runId":"` + id + `","externalId":"x"}`, "platform.admin"},
		{"POST", "/api/v1/users/" + id + "/extend-access", `{"expectedVersion":1,"accessExpiresAt":"2030-01-01T00:00:00Z","reason":"contract_renewed"}`, "organization.external_parties.manage"},
		{"POST", "/api/v1/import-batches", ``, "organization.import"},
	}
	all := []string{"organization.view", "organization.users.manage", "organization.locations.manage", "organization.departments.manage", "organization.teams.manage",
		"organization.import", "organization.external_parties.manage", "platform.admin", "tasks.manage"}
	for _, rt := range routes {
		h.as("")
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 401 {
			t.Errorf("%s %s unauthenticated = %d", rt.method, rt.path, code)
		}
		var others []string
		for _, p := range all {
			if p != rt.perm {
				others = append(others, p)
			}
		}
		h.as(id, others...)
		if code, _, _ := h.do(rt.method, rt.path, rt.body); code != 403 {
			t.Errorf("%s %s with every other permission = %d, want 403", rt.method, rt.path, code)
		}
	}
	// Reading and applying a batch only needs a signed-in user; the batch is bound to its creator, so a stranger gets 404.
	h.as(id, "tasks.manage")
	for _, p := range []string{"/api/v1/import-batches/" + id, "/api/v1/import-batches/" + id + "/rows", "/api/v1/import-batches/" + id + "/rejected.csv"} {
		if code, _, _ := h.do("GET", p, ""); code != 404 {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
	}
	if code, _, _ := h.do("POST", "/api/v1/import-batches/"+id+"/apply", `{"previewHash":"`+strings.Repeat("a", 64)+`","expectedRejects":0}`); code != 404 {
		t.Errorf("apply of a foreign batch = %d, want 404", code)
	}
	h.as("")
	if code, _, _ := h.do("GET", "/api/v1/import-batches/"+id, ""); code != 401 {
		t.Errorf("unauthenticated read = %d", code)
	}
}

func TestImportFlowOverHTTP(t *testing.T) {
	h := newPeopleHTTP(t)
	setup := h.mustActor()
	h.actor = setup
	h.perms = map[string]struct{}{"organization.users.manage": {}, "organization.import": {}}
	fields := map[string]string{"kind": "users", "matchKey": "primary_email", "mode": "upsert"}
	email := h.pfx + "-imp@example.test"
	csv := "display_name,primary_email\n=SUM(A1)," + email + "\nBroken,not-an-address\n"
	code, body := h.upload(fields, csv)
	if code != 201 {
		t.Fatalf("preview = %d %v", code, body)
	}
	counts := body["counts"].(map[string]any)
	if counts["create"] != float64(1) || counts["reject"] != float64(1) {
		t.Fatalf("counts = %v", counts)
	}
	id, hash := body["id"].(string), body["previewHash"].(string)
	rows := body["rows"].([]any)
	if w := rows[0].(map[string]any)["warnings"].([]any); len(w) != 1 {
		t.Errorf("a leading = is kept with a warning: %v", rows[0])
	}

	// Rejected rows are listed and exported through the one CSV writer.
	code, page, _ := h.do("GET", "/api/v1/import-batches/"+id+"/rows?action=reject", "")
	if code != 200 || len(page["items"].([]any)) != 1 {
		t.Fatalf("rows = %d %v", code, page)
	}
	req := httptest.NewRequest("GET", "/api/v1/import-batches/"+id+"/rejected.csv", nil)
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-ID", h.pfx+"-req")
	httpx.NoStore(h.mux).ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "\ufeffrow,key,field,reason") || !strings.Contains(rec.Body.String(), "primary_email,invalid_value") {
		t.Errorf("rejected.csv = %d %q", rec.Code, rec.Body.String())
	}

	wrong := `{"previewHash":"` + strings.Repeat("0", 64) + `","expectedRejects":1}`
	if code, body, _ := h.do("POST", "/api/v1/import-batches/"+id+"/apply", wrong); code != 409 || errCodeOf(body) != "organization.import_preview_mismatch" {
		t.Errorf("wrong hash = %d %v", code, body)
	}
	// The manage permission is checked again at apply.
	h.perms = map[string]struct{}{"organization.import": {}}
	if code, body, _ := h.do("POST", "/api/v1/import-batches/"+id+"/apply", fmt.Sprintf(`{"previewHash":%q,"expectedRejects":1}`, hash)); code != 404 && code != 403 {
		t.Errorf("apply without the manage permission = %d %v", code, body)
	}
	h.perms = map[string]struct{}{"organization.users.manage": {}, "organization.import": {}}
	code, res, _ := h.do("POST", "/api/v1/import-batches/"+id+"/apply", fmt.Sprintf(`{"previewHash":%q,"expectedRejects":1}`, hash))
	if code != 200 || res["status"] != "applied" {
		t.Fatalf("apply = %d %v", code, res)
	}
	var n int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM organization.users WHERE primary_email = $1`, email).Scan(&n); err != nil || n != 1 {
		t.Errorf("imported users = %d (%v)", n, err)
	}
	if code, res, _ := h.do("POST", "/api/v1/import-batches/"+id+"/apply", fmt.Sprintf(`{"previewHash":%q,"expectedRejects":1}`, hash)); code != 200 || res["replayed"] != true {
		t.Errorf("second apply = %d %v", code, res)
	}

	// Request errors.
	if code, body := h.upload(fields, ""); code != 400 {
		t.Errorf("missing file = %d %v", code, body)
	}
	if code, body := h.upload(map[string]string{"kind": "teams", "mode": "upsert"}, csv); code != 400 {
		t.Errorf("unknown kind = %d %v", code, body)
	}
	h.perms = map[string]struct{}{"organization.import": {}}
	if code, body := h.upload(fields, csv); code != 403 {
		t.Errorf("preview without organization.users.manage = %d %v", code, body)
	}
	h.perms = map[string]struct{}{"organization.users.manage": {}, "organization.import": {}}
	if code, body := h.upload(fields, "primary_email\n"+strings.Repeat("a", 2<<20)+"\n"); code != 413 {
		t.Errorf("oversized file = %d %v", code, body)
	}
}

func TestBulkLinkAndExtendOverHTTP(t *testing.T) {
	h := newPeopleHTTP(t)
	setup := h.mustActor()
	h.actor = setup
	dept := h.pfx + "-D1"
	h.perms = map[string]struct{}{"organization.users.manage": {}, "organization.departments.manage": {}, "organization.view": {}}
	code, d, _ := h.do("POST", "/api/v1/departments", fmt.Sprintf(`{"name":"%s Dept","code":"%s"}`, h.pfx, dept))
	if code != 201 {
		t.Fatalf("department = %d %v", code, d)
	}
	deptID := d["id"].(string)
	u1, u2 := h.createUser("Bulkone"), h.createUser("Bulktwo")
	h.perms = map[string]struct{}{"organization.users.manage": {}}
	body := fmt.Sprintf(`{"dryRun":true,"operation":"set_department","departmentId":%q,"userIds":[%q,%q,"00000000-0000-7000-8000-00000000dead"]}`, deptID, u1, u2)
	code, p, _ := h.do("POST", "/api/v1/users/bulk-operations", body)
	if code != 201 || p["counts"].(map[string]any)["update"] != float64(2) || p["counts"].(map[string]any)["reject"] != float64(1) {
		t.Fatalf("bulk preview = %d %v", code, p)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/bulk-operations", `{"operation":"set_department","userIds":["`+u1+`"]}`); code != 400 {
		t.Errorf("dryRun is required: %d %v", code, b)
	}
	apply := fmt.Sprintf(`{"dryRun":false,"batchId":%q,"previewHash":%q,"expectedRejects":1}`, p["id"], p["previewHash"])
	code, res, _ := h.do("POST", "/api/v1/users/bulk-operations", apply)
	if code != 200 || res["status"] != "applied" || len(res["rows"].([]any)) != 3 {
		t.Fatalf("bulk apply = %d %v", code, res)
	}

	// Extend access: only external accounts, RFC 3339 dates, expectedVersion.
	h.perms = map[string]struct{}{"organization.external_parties.manage": {}}
	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	var ext string
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO organization.users(display_name, primary_email, origin, account_kind, access_expires_at)
		VALUES ($1, $2, 'local', 'external', $3) RETURNING id::text`, h.pfx+" Vendor", h.pfx+"-vendor@example.test", expires).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	until := expires.Add(72 * time.Hour).Format(time.RFC3339)
	ok := fmt.Sprintf(`{"expectedVersion":1,"accessExpiresAt":%q,"reason":"contract_renewed"}`, until)
	if code, b, _ := h.do("POST", "/api/v1/users/"+ext+"/extend-access", `{"expectedVersion":1,"accessExpiresAt":"tomorrow","reason":"contract_renewed"}`); code != 400 {
		t.Errorf("bad date = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/"+ext+"/extend-access", `{"expectedVersion":1,"accessExpiresAt":"`+until+`","reason":"because"}`); code != 400 {
		t.Errorf("bad reason = %d %v", code, b)
	}
	code, got, _ := h.do("POST", "/api/v1/users/"+ext+"/extend-access", ok)
	if code != 200 || got["version"] != float64(2) {
		t.Errorf("extend = %d %v", code, got)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/"+ext+"/extend-access", ok); code != 409 || errCodeOf(b) != "organization.version_conflict" {
		t.Errorf("replayed extend = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/"+u1+"/extend-access", ok); code != 409 || errCodeOf(b) != "organization.invalid_state" {
		t.Errorf("employee extend = %d %v", code, b)
	}

	// Link: administrators only; the identity must come from a sync run conflict.
	h.perms = map[string]struct{}{"platform.admin": {}}
	if code, b, _ := h.do("POST", "/api/v1/users/"+u2+"/link-directory-identity", `{"expectedVersion":2,"runId":"00000000-0000-7000-8000-00000000dead","externalId":"x"}`); code != 404 {
		t.Errorf("unknown run = %d %v", code, b)
	}
	if code, b, _ := h.do("POST", "/api/v1/users/"+u2+"/link-directory-identity", `{"expectedVersion":1}`); code != 400 {
		t.Errorf("missing run = %d %v", code, b)
	}
}
