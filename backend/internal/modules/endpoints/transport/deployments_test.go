package transport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestDeploymentRoutesRequirePermissions(t *testing.T) {
	ring := `{"name":"Pilot","targetSetId":"` + unknownID + `","successThresholdPercent":90,"noWindowRequired":true,"expectedVersion":1}`
	cases := []struct {
		name, method, path, body string
		perms                    []string
		want                     int
	}{
		{"target sets without permission", "GET", "/api/v1/target-sets", "", []string{"endpoints.manage"}, http.StatusForbidden},
		{"target sets with view", "GET", "/api/v1/target-sets", "", []string{"deployments.view"}, http.StatusOK},
		{"target set unknown", "GET", "/api/v1/target-sets/" + unknownID, "", []string{"deployments.view"}, http.StatusNotFound},
		{"evaluate unknown", "GET", "/api/v1/target-sets/" + unknownID + "/evaluate", "", []string{"deployments.manage"}, http.StatusNotFound},
		{"explain without permission", "GET", "/api/v1/target-sets/" + unknownID + "/explain?deviceId=" + unknownID, "", []string{"endpoints.view"}, http.StatusForbidden},
		{"create set with view", "POST", "/api/v1/target-sets", `{"name":"x","definition":{"filters":{}}}`, []string{"deployments.view"}, http.StatusForbidden},
		{"create set unknown field", "POST", "/api/v1/target-sets", `{"name":"x","definition":{"filters":{"colour":"red"}}}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"create set unknown top field", "POST", "/api/v1/target-sets", `{"name":"x","definition":{},"sql":"1=1"}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"create set bad platform", "POST", "/api/v1/target-sets", `{"name":"x","definition":{"filters":{"platform":["os2"]}}}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"create set oversize", "POST", "/api/v1/target-sets", `{"name":"x","definition":{"filters":{"model":["` + strings.Repeat("m", 100<<10) + `"]}}}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"archive without version", "POST", "/api/v1/target-sets/" + unknownID + "/archive", `{}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"deployments for any session", "GET", "/api/v1/deployments", "", nil, http.StatusOK},
		{"deployments invalid status", "GET", "/api/v1/deployments?status=running", "", []string{"deployments.view"}, http.StatusBadRequest},
		{"deployment unknown", "GET", "/api/v1/deployments/" + unknownID, "", []string{"deployments.view"}, http.StatusNotFound},
		{"deployment of another user", "GET", "/api/v1/deployments/" + unknownID, "", nil, http.StatusNotFound},
		{"create with view", "POST", "/api/v1/deployments", `{"name":"x","softwareVersionId":"` + unknownID + `","intent":"install"}`, []string{"deployments.view"}, http.StatusForbidden},
		{"create unknown version", "POST", "/api/v1/deployments", `{"name":"x","softwareVersionId":"` + unknownID + `","intent":"install"}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"create bad intent", "POST", "/api/v1/deployments", `{"name":"x","softwareVersionId":"` + unknownID + `","intent":"reinstall"}`, []string{"deployments.manage"}, http.StatusBadRequest},
		{"uninstall without high impact", "POST", "/api/v1/deployments", `{"name":"x","softwareVersionId":"` + unknownID + `","intent":"uninstall"}`, []string{"deployments.manage"}, http.StatusForbidden},
		{"ring with execute", "POST", "/api/v1/deployments/" + unknownID + "/rings", ring, []string{"deployments.execute"}, http.StatusForbidden},
		{"ring unknown deployment", "POST", "/api/v1/deployments/" + unknownID + "/rings", ring, []string{"deployments.manage"}, http.StatusNotFound},
		{"remove ring without version", "DELETE", "/api/v1/deployments/" + unknownID + "/rings/" + unknownID, "", []string{"deployments.manage"}, http.StatusBadRequest},
		{"validate with view", "POST", "/api/v1/deployments/" + unknownID + "/validate", `{}`, []string{"deployments.view"}, http.StatusForbidden},
		{"submit without high impact", "POST", "/api/v1/deployments/" + unknownID + "/submit", `{"approverUserId":"` + admin + `","expectedVersion":1}`, []string{"deployments.manage"}, http.StatusForbidden},
		{"schedule unknown", "POST", "/api/v1/deployments/" + unknownID + "/schedule", `{"expectedVersion":1}`, []string{"deployments.manage"}, http.StatusNotFound},
		{"cancel free text", "POST", "/api/v1/deployments/" + unknownID + "/cancel", `{"reason":"because I said so","expectedVersion":1}`, []string{"deployments.manage"}, http.StatusBadRequest},
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

func TestTargetSetHTTPRoundTrip(t *testing.T) {
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "http set " + hex.EncodeToString(b)
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.target_sets WHERE name = $1`, name)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	// Groups and Device ids in a definition need endpoints.view.
	if rec := do(serve(t, as(admin, "deployments.manage"), nil, false), "POST", "/api/v1/target-sets", `{"name":"`+name+`","definition":{"excludeDeviceIds":["`+unknownID+`"]}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("device ids without endpoints.view: %d %s", rec.Code, rec.Body.String())
	}
	h := serve(t, as(admin, "deployments.manage", "endpoints.view"), nil, false)
	rec := do(h, "POST", "/api/v1/target-sets", `{"name":"`+name+`","definition":{"filters":{"platform":["windows","windows"],
		"groups":[{"externalId":"grp-1","includeNested":true}]},"excludeDeviceIds":["`+strings.ToUpper(unknownID)+`"]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var set struct {
		ID         string `json:"id"`
		Reference  string `json:"reference"`
		AllDevices bool   `json:"allDevices"`
		Version    int    `json:"version"`
		Definition struct {
			Filters struct {
				Platform []string `json:"platform"`
			} `json:"filters"`
			IncludeDeviceIDs []string `json:"includeDeviceIds"`
			ExcludeDeviceIDs []string `json:"excludeDeviceIds"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(set.Reference, "TS-") || set.AllDevices || len(set.Definition.Filters.Platform) != 1 ||
		set.Definition.IncludeDeviceIDs == nil || set.Definition.ExcludeDeviceIDs[0] != unknownID {
		t.Fatalf("created set = %s", rec.Body.String())
	}
	if rec := do(h, "GET", "/api/v1/target-sets/"+set.ID+"/evaluate", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cap":5000`) {
		t.Fatalf("evaluate: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "GET", "/api/v1/target-sets/"+set.ID+"/explain?deviceId="+unknownID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("explain unknown device: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "PATCH", "/api/v1/target-sets/"+set.ID, `{"name":"`+name+`","definition":{},"expectedVersion":`+"99"+`}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale patch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/api/v1/target-sets/"+set.ID+"/archive", `{"expectedVersion":1}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"archivedAt":"`) {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}
}
