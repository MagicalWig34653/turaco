package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type testAuth struct {
	permissions   map[string]struct{}
	authenticated bool
}

func (a testAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return authorization.Principal{UserID: "00000000-0000-7000-8000-000000000001", Permissions: a.permissions}, a.authenticated, nil
}

func TestSecurityRoutesAuthorizeBeforeResourceLookup(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/security/advisories", 403},
		{"GET", "/api/v1/security/findings/00000000-0000-7000-8000-000000000002", 403},
		{"POST", "/api/v1/security/advisories", 403},
		{"POST", "/api/v1/security/findings/00000000-0000-7000-8000-000000000002/accept-risk", 403},
		{"GET", "/api/v1/security/overview", 403},
		{"GET", "/api/v1/security/advisories/00000000-0000-7000-8000-000000000002/progress", 403},
		{"GET", "/api/v1/security/findings/00000000-0000-7000-8000-000000000002/tasks", 403},
		{"POST", "/api/v1/security/advisories/00000000-0000-7000-8000-000000000002/tasks", 403},
		{"POST", "/api/v1/security/advisories/00000000-0000-7000-8000-000000000002/changes", 403},
	} {
		mux := http.NewServeMux()
		Register(mux, nil, testAuth{authenticated: true, permissions: map[string]struct{}{}}, nil)
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestImportRejectsOversizedAndOverCountBeforeService(t *testing.T) {
	h := &handler{}
	for _, tt := range []struct{ name, body string }{
		{"too many records", `{"records":[` + strings.Repeat(`{},`, application.MaxImportRecords) + `{}]}`},
		{"oversized body", `{"records":[{"title":"` + strings.Repeat("x", maxImportBody) + `"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/security/advisories/import", strings.NewReader(tt.body))
			h.importAdvisories(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCriterionInputUsesCamelCaseFields(t *testing.T) {
	var b advisoryBody
	raw := `{"source":"vendor","externalId":"CVE-1","criteria":[{"softwareProductId":"123","productName":"Example","osPlatform":"windows","rules":[{"kind":"fixed","version":"2.0"}]}]}`
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	in := b.input()
	if len(in.Criteria) != 1 || in.Criteria[0].SoftwareProductID != "123" || in.Criteria[0].OSPlatform != "windows" || len(in.Criteria[0].Rules) != 1 || in.Criteria[0].Rules[0].Kind != "fixed" {
		t.Fatalf("bad criteria mapping: %+v", in.Criteria)
	}
}

func TestFindingDTOUsesRedactedDevice(t *testing.T) {
	f := application.FindingView{Finding: application.Finding{DeviceID: "hidden-1"}, DeviceHidden: true}
	out := findingDTO(f)
	if out["deviceId"] != "hidden-1" || out["deviceHidden"] != true || out["deviceName"].(*string) != nil {
		t.Fatalf("bad redaction DTO: %+v", out)
	}
}

func TestResolvingAnAdvisoryWithIncompleteCriteriaWarns(t *testing.T) {
	h := &handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/security/advisories/x/not-applicable", nil)
	h.advisoryResponse(rec, req, application.Advisory{ID: "x", CriteriaIncomplete: true, CriteriaSkipped: 3}, nil)
	var body struct {
		Warnings           []string `json:"warnings"`
		CriteriaIncomplete bool     `json:"criteriaIncomplete"`
		CriteriaSkipped    int      `json:"criteriaSkipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.CriteriaIncomplete || body.CriteriaSkipped != 3 || len(body.Warnings) != 1 || body.Warnings[0] != "criteria_incomplete" {
		t.Fatalf("%s", rec.Body.String())
	}
}
