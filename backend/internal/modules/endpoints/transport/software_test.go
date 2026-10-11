package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

const unknownID = "00000000-0000-7000-8000-0000000000e5"

func TestRegisterSoftwareProduct(t *testing.T) {
	name := "Transport alias product " + time.Now().UTC().Format("20060102150405.000000000")
	h := serve(t, as(admin, "endpoints.manage"), nil, false)
	input, _ := json.Marshal(map[string]any{"name": name, "publisher": "Example", "aliases": []string{name + " alias"}})
	rec := do(h, "POST", "/api/v1/software/products", string(input))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"relinkedInstallations":0`) {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatalf("response: %+v %v", result, err)
	}
	pool := dbtest.Pool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM endpoints.software_aliases WHERE software_product_id = $1::uuid`, result.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM endpoints.software_products WHERE id = $1::uuid`, result.ID)
	})
	if rec := do(h, "POST", "/api/v1/software/products", string(input)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSoftwareRoutesRequirePermissions(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		perms                    []string
		want                     int
	}{
		{"products without permission", "GET", "/api/v1/software/products", "", []string{"endpoints.manage"}, http.StatusForbidden},
		{"products with view", "GET", "/api/v1/software/products", "", []string{"software.view"}, http.StatusOK},
		{"products with package", "GET", "/api/v1/software/products?status=approved", "", []string{"software.package"}, http.StatusOK},
		{"products invalid status", "GET", "/api/v1/software/products?status=x", "", []string{"software.view"}, http.StatusBadRequest},
		{"register product with software view", "POST", "/api/v1/software/products", `{"name":"Example"}`, []string{"software.view"}, http.StatusForbidden},
		{"register product with security manage", "POST", "/api/v1/software/products", `{"name":"Example"}`, []string{"security.manage"}, http.StatusForbidden},
		{"register product invalid with endpoints manage", "POST", "/api/v1/software/products", `{"name":""}`, []string{"endpoints.manage"}, http.StatusBadRequest},
		{"versions with approve", "GET", "/api/v1/software/versions", "", []string{"software.approve"}, http.StatusOK},
		{"packages with view", "GET", "/api/v1/software/packages", "", []string{"software.view"}, http.StatusOK},
		{"unknown version", "GET", "/api/v1/software/versions/" + unknownID, "", []string{"software.view"}, http.StatusNotFound},
		{"malformed version id", "GET", "/api/v1/software/versions/not-a-uuid", "", []string{"software.view"}, http.StatusNotFound},
		{"register with view", "POST", "/api/v1/software/versions", `{}`, []string{"software.view"}, http.StatusForbidden},
		{"register with approve", "POST", "/api/v1/software/versions", `{}`, []string{"software.approve"}, http.StatusForbidden},
		{"register unknown product", "POST", "/api/v1/software/versions", `{"productId":"` + unknownID + `","version":"1","installerSha256":"` +
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" + `","installerUrl":"https://x.example.com/a.msi","installCommand":"a","detectionRule":"b"}`,
			[]string{"software.package"}, http.StatusNotFound},
		{"register invalid hash", "POST", "/api/v1/software/versions", `{"productId":"` + unknownID + `","version":"1","installerSha256":"x"}`,
			[]string{"software.package"}, http.StatusBadRequest},
		{"approve with package", "POST", "/api/v1/software/versions/" + unknownID + "/approve", `{"expectedVersion":1}`, []string{"software.package"}, http.StatusForbidden},
		{"approve unknown", "POST", "/api/v1/software/versions/" + unknownID + "/approve", `{"expectedVersion":1}`, []string{"software.approve"}, http.StatusNotFound},
		{"approve with reason", "POST", "/api/v1/software/versions/" + unknownID + "/approve", `{"expectedVersion":1,"reason":"other"}`, []string{"software.approve"}, http.StatusBadRequest},
		{"reject without version", "POST", "/api/v1/software/versions/" + unknownID + "/reject", `{"reason":"other"}`, []string{"software.approve"}, http.StatusBadRequest},
		{"request with approve", "POST", "/api/v1/software/versions/" + unknownID + "/request-approval", `{"expectedVersion":1}`, []string{"software.approve"}, http.StatusForbidden},
		{"product op with view", "POST", "/api/v1/software/products/" + unknownID + "/approve", `{"expectedVersion":1}`, []string{"software.view"}, http.StatusForbidden},
		{"product op unknown", "POST", "/api/v1/software/products/" + unknownID + "/block", `{"expectedVersion":1,"reason":"policy"}`, []string{"software.approve"}, http.StatusNotFound},
		{"product op invalid", "POST", "/api/v1/software/products/" + unknownID + "/destroy", `{"expectedVersion":1}`, []string{"software.approve"}, http.StatusNotFound},
		{"package with approve", "POST", "/api/v1/software/versions/" + unknownID + "/package", `{"expectedVersion":1}`, []string{"software.approve"}, http.StatusForbidden},
		{"publish unknown", "POST", "/api/v1/software/packages/" + unknownID + "/publish", `{"expectedVersion":1}`, []string{"software.package"}, http.StatusNotFound},
		{"sync with view", "POST", "/api/v1/software/packages/sync", `{}`, []string{"software.view"}, http.StatusForbidden},
		{"sync disabled", "POST", "/api/v1/software/packages/sync", `{}`, []string{"software.package"}, http.StatusConflict},
		{"catalog not configured", "GET", "/api/v1/software/catalog/search?q=firefox", "", []string{"software.view"}, http.StatusConflict},
		{"catalog without permission", "GET", "/api/v1/software/catalog/search?q=firefox", "", []string{"endpoints.view"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := serve(t, as(admin, tc.perms...), nil, false)
			if rec := do(h, tc.method, tc.path, tc.body); rec.Code != tc.want {
				t.Fatalf("%s %s: got %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestSoftwareCatalogSearchIsRateLimitedPerUser(t *testing.T) {
	h := serve(t, as(admin, "software.view"), nil, false)
	for i := 0; i < 10; i++ {
		if rec := do(h, "GET", "/api/v1/software/catalog/search?q=firefox", ""); rec.Code != http.StatusConflict {
			t.Fatalf("search %d: %d", i, rec.Code)
		}
	}
	rec := do(h, "GET", "/api/v1/software/catalog/search?q=firefox", "")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "endpoints.software_rate_limited") {
		t.Fatalf("11th search: %d %s", rec.Code, rec.Body.String())
	}
}
