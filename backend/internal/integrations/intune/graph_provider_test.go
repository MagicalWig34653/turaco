package intune

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

const ringID = "0190c0de-0000-7000-8000-000000000001"

func newProviderServer(t *testing.T) *graphServer {
	s := newGraphServer(t)
	// managedDevices: https://learn.microsoft.com/en-us/graph/api/intune-devices-manageddevice-list?view=graph-rest-1.0
	// (read 2026-10-10). Two pages; unknown fields and a duplicate are tolerated.
	s.mux.HandleFunc("/deviceManagement/managedDevices", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json200(w, `{"value":[{"id":"dev-2","deviceName":"MAC-2","operatingSystem":"macOS","managedDeviceOwnerType":"personal","complianceState":"inGracePeriod","azureADDeviceId":"22222222-2222-2222-2222-222222222222"},{"id":"dev-1","deviceName":"dup"}]}`)
			return
		}
		json200(w, `{"@odata.context":"x","value":[{"@odata.type":"#microsoft.graph.managedDevice","id":"dev-1","deviceName":"PC-1","operatingSystem":"Windows","osVersion":"10.0.22631","serialNumber":"SN1","manufacturer":"Dell","model":"Latitude","managedDeviceOwnerType":"company","complianceState":"compliant","lastSyncDateTime":"2026-10-09T08:15:00Z","azureADDeviceId":"11111111-1111-1111-1111-111111111111","futureField":{"a":1}},{"id":""}],"@odata.nextLink":"`+s.URL+`/deviceManagement/managedDevices?page=2"}`)
	})
	return s
}

func TestGraphProviderDevices(t *testing.T) {
	s := newProviderServer(t)
	p := NewGraphProvider(s.graph(t), false)
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("devices %+v", devs)
	}
	d := devs[0]
	if d.ExternalID != "dev-1" || d.Name != "PC-1" || d.OSPlatform != "windows" || d.Ownership != "corporate" || d.ComplianceState != "compliant" ||
		d.SerialNumber != "SN1" || d.LastCheckinAt == nil || d.LastCheckinAt.Format("2006-01-02T15:04:05Z") != "2026-10-09T08:15:00Z" {
		t.Fatalf("device %+v", d)
	}
	if devs[1].OSPlatform != "macos" || devs[1].Ownership != "personal" || devs[1].ComplianceState != "in_grace_period" || devs[1].LastCheckinAt != nil {
		t.Fatalf("device 2 %+v", devs[1])
	}
	if st := p.Status(); st.State != providerstatus.Verified {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphProviderSoftware(t *testing.T) {
	s := newGraphServer(t)
	// detectedApps: https://learn.microsoft.com/en-us/graph/api/resources/intune-devices-detectedapp?view=graph-rest-1.0 (read 2026-10-10).
	s.mux.HandleFunc("/deviceManagement/managedDevices/dev-1/detectedApps", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"id":"a1","displayName":"7-Zip","version":"24.08","publisher":"Igor Pavlov","sizeInByte":1,"deviceCount":4,"platform":"windows"},{"id":"a2","displayName":" "}]}`)
	})
	s.mux.HandleFunc("/deviceManagement/managedDevices/gone/detectedApps", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json200(w, `{"error":{"code":"ResourceNotFound"}}`)
	})
	p := NewGraphProvider(s.graph(t), false)
	sw, err := p.Software(context.Background(), "dev-1")
	if err != nil || len(sw) != 1 || sw[0].Name != "7-Zip" || sw[0].Version != "24.08" || sw[0].Publisher != "Igor Pavlov" {
		t.Fatalf("software %+v err %v", sw, err)
	}
	if sw, err := p.Software(context.Background(), "gone"); err != nil || len(sw) != 0 {
		t.Fatalf("vanished device: %+v %v", sw, err)
	}
	// An id that is not a plain identifier never reaches a URL.
	if sw, err := p.Software(context.Background(), "../../users"); err != nil || sw != nil {
		t.Fatalf("hostile id: %+v %v", sw, err)
	}
}

func TestGraphProviderManagement(t *testing.T) {
	s := newProviderServer(t)
	// mobileApps with expanded assignments: mobileApp list and mobileAppAssignment pages (read 2026-10-10).
	s.mux.HandleFunc("/deviceAppManagement/mobileApps", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$expand") != "assignments" {
			t.Errorf("assignments are not expanded: %s", r.URL.RawQuery)
		}
		json200(w, `{"value":[
		 {"@odata.type":"#microsoft.graph.win32LobApp","id":"app-1","displayName":"7-Zip","lastModifiedDateTime":"2026-10-01T10:00:00Z",
		  "assignments":[
		   {"@odata.type":"#microsoft.graph.mobileAppAssignment","id":"as-1","intent":"required","target":{"@odata.type":"#microsoft.graph.groupAssignmentTarget","groupId":"grp-1","deviceAndAppManagementAssignmentFilterId":"flt-1","deviceAndAppManagementAssignmentFilterType":"include"}},
		   {"id":"as-2","intent":"availableWithoutEnrollment","target":{"@odata.type":"#microsoft.graph.exclusionGroupAssignmentTarget","groupId":"grp-2"}},
		   {"id":"as-3","intent":"uninstall","target":{"@odata.type":"#microsoft.graph.allDevicesAssignmentTarget"}},
		   {"id":"as-4","intent":"available","target":{"@odata.type":"#microsoft.graph.allLicensedUsersAssignmentTarget"}},
		   {"id":"as-5","intent":"required","target":{"@odata.type":"#microsoft.graph.groupAssignmentTarget","groupId":"`+"0190c0de-0000-7000-8000-00000000aaaa"+`"}}]},
		 {"@odata.type":"#microsoft.graph.macOSLobApp","id":"app-2","displayName":"Mac tool","assignments":[{"id":"as-9","intent":"required","target":{"@odata.type":"#microsoft.graph.futureTarget"}}]},
		 {"@odata.type":"#microsoft.graph.iosVppApp","id":"app-3","displayName":"No assignments key"}]}`)
	})
	s.mux.HandleFunc("/deviceManagement/deviceConfigurations", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"@odata.type":"#microsoft.graph.windows10GeneralConfiguration","id":"cfg-1","displayName":"Baseline","version":3,"lastModifiedDateTime":"2026-09-01T00:00:00Z","assignments":[{"id":"ca-1","target":{"@odata.type":"#microsoft.graph.groupAssignmentTarget","groupId":"grp-1"}}]}]}`)
	})
	s.mux.HandleFunc("/deviceManagement/deviceCompliancePolicies", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"@odata.type":"#microsoft.graph.windows10CompliancePolicy","id":"cmp-1","displayName":"Compliance","version":1,"assignments":[]}]}`)
	})
	s.mux.HandleFunc("/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ConsistencyLevel") != "eventual" || !strings.Contains(r.URL.Query().Get("$filter"), "startswith(displayName,'turaco-ring-')") {
			t.Errorf("ring group query: %s %v", r.URL.RawQuery, r.Header)
		}
		json200(w, `{"value":[{"id":"0190c0de-0000-7000-8000-00000000aaaa","displayName":"turaco-ring-`+ringID+`"},{"id":"zzz","displayName":"turaco-ring-not-a-uuid"}]}`)
	})
	members := func(devs string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { json200(w, `{"value":[`+devs+`]}`) }
	}
	// Device members cast: https://learn.microsoft.com/en-us/graph/api/group-list-members?view=graph-rest-1.0 (read 2026-10-10).
	s.mux.HandleFunc("/groups/grp-1/transitiveMembers/microsoft.graph.device", members(`{"@odata.type":"#microsoft.graph.device","id":"obj-1","deviceId":"11111111-1111-1111-1111-111111111111"},{"id":"obj-x","deviceId":"99999999-9999-9999-9999-999999999999"}`))
	s.mux.HandleFunc("/groups/grp-2/transitiveMembers/microsoft.graph.device", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json200(w, `{"error":{"code":"Request_ResourceNotFound"}}`)
	})
	s.mux.HandleFunc("/groups/0190c0de-0000-7000-8000-00000000aaaa/transitiveMembers/microsoft.graph.device", members(`{"id":"obj-2","deviceId":"22222222-2222-2222-2222-222222222222"}`))

	g := s.graph(t)
	p := NewGraphProvider(g, false)
	snap, err := p.Management(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ArtifactRecord{}
	for _, a := range snap.Artifacts {
		byID[a.ExternalID] = a
	}
	if len(snap.Artifacts) != 5 || len(snap.Filters) != 0 || len(snap.Observations) != 0 {
		t.Fatalf("artifacts %d filters %d observations %d", len(snap.Artifacts), len(snap.Filters), len(snap.Observations))
	}
	app := byID["app-1"]
	if app.Kind != "application" || app.Name != "7-Zip" || app.Platform != "windows" || !app.AssignmentsKnown || len(app.Assignments) != 5 {
		t.Fatalf("app-1 %+v", app)
	}
	want := []AssignmentRecord{
		{ProviderAssignmentID: "as-1", TargetKind: "group", TargetGroupExternalID: "grp-1", Mode: "include", Intent: "required", FilterExternalID: "flt-1", FilterMode: "include"},
		{ProviderAssignmentID: "as-2", TargetKind: "group", TargetGroupExternalID: "grp-2", Mode: "exclude", Intent: "available", FilterMode: "none"},
		{ProviderAssignmentID: "as-3", TargetKind: "all_devices", Mode: "include", Intent: "uninstall", FilterMode: "none"},
		{ProviderAssignmentID: "as-4", TargetKind: "all_users", Mode: "include", Intent: "available", FilterMode: "none"},
		// The ring group is reported under Turaco's deterministic id, not the Graph object id.
		{ProviderAssignmentID: "as-5", TargetKind: "group", TargetGroupExternalID: RingGroupID(ringID), Mode: "include", Intent: "required", FilterMode: "none"},
	}
	for i, w := range want {
		if app.Assignments[i] != w {
			t.Errorf("assignment %d: got %+v want %+v", i, app.Assignments[i], w)
		}
	}
	if a := byID["app-2"]; a.Platform != "macos" || a.AssignmentsKnown || len(a.Assignments) != 0 {
		t.Errorf("unrepresentable target must leave assignments unknown: %+v", a)
	}
	if a := byID["app-3"]; a.Platform != "ios" || a.AssignmentsKnown {
		t.Errorf("missing assignments key: %+v", a)
	}
	if a := byID["cfg-1"]; a.Kind != "configuration_profile" || a.Revision != "v3 2026-09-01T00:00:00Z" || len(a.Assignments) != 1 || a.Assignments[0].Intent != "none" {
		t.Errorf("cfg-1 %+v", a)
	}
	if a := byID["cmp-1"]; a.Kind != "compliance_policy" || !a.AssignmentsKnown || len(a.Assignments) != 0 {
		t.Errorf("cmp-1 %+v", a)
	}
	got := map[string]bool{}
	for _, m := range snap.Memberships {
		got[m.GroupExternalID+"|"+m.ExternalDeviceID] = true
	}
	if len(snap.Memberships) != 2 || !got["grp-1|dev-1"] || !got[RingGroupID(ringID)+"|dev-2"] {
		t.Errorf("memberships %+v", snap.Memberships)
	}
	if st := g.Status(); st.State != providerstatus.Verified {
		t.Errorf("status %+v", st)
	}
}

func TestGraphProviderBetaFiltersAndObservations(t *testing.T) {
	s := newProviderServer(t)
	s.mux.HandleFunc("/deviceAppManagement/mobileApps", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"@odata.type":"#microsoft.graph.win32LobApp","id":"app-1","displayName":"7-Zip","assignments":[]}]}`)
	})
	s.mux.HandleFunc("/deviceManagement/deviceConfigurations", func(w http.ResponseWriter, r *http.Request) { json200(w, `{"value":[]}`) })
	s.mux.HandleFunc("/deviceManagement/deviceCompliancePolicies", func(w http.ResponseWriter, r *http.Request) { json200(w, `{"value":[]}`) })
	// Settings catalog (beta): deviceManagementConfigurationPolicy (read 2026-10-10).
	s.mux.HandleFunc("/deviceManagement/configurationPolicies", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"id":"pol-1","name":"Edge settings","platforms":"windows10","lastModifiedDateTime":"2026-08-01T00:00:00Z","assignments":[{"id":"pa-1","target":{"@odata.type":"#microsoft.graph.allDevicesAssignmentTarget"}}]}]}`)
	})
	// Assignment filters (beta): deviceAndAppManagementAssignmentFilter (read 2026-10-10).
	s.mux.HandleFunc("/deviceManagement/assignmentFilters", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"id":"flt-1","displayName":"Win 11","platform":"windows10AndLater","rule":"(device.osVersion -startsWith \"10.0.22\")","lastModifiedDateTime":"2026-07-01T00:00:00Z","roleScopeTags":["0"]}]}`)
	})
	// App install statuses (beta): mobileAppInstallStatus (read 2026-10-10).
	s.mux.HandleFunc("/deviceAppManagement/mobileApps/app-1/deviceStatuses", func(w http.ResponseWriter, r *http.Request) {
		json200(w, `{"value":[{"id":"st-1","deviceName":"PC-1","deviceId":"dev-1","lastSyncDateTime":"2026-10-09T10:00:00Z","installState":"failed","installStateDetail":"dependencyFailedToInstall","errorCode":9},{"id":"st-2","deviceId":"dev-2","installState":"installed"}]}`)
	})
	s.mux.HandleFunc("/groups", func(w http.ResponseWriter, r *http.Request) { json200(w, `{"value":[]}`) })

	p := NewGraphProvider(s.graph(t), true)
	snap, err := p.Management(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Filters) != 1 || snap.Filters[0] != (FilterRecord{ExternalID: "flt-1", Name: "Win 11", Platform: "windows", Rule: `(device.osVersion -startsWith "10.0.22")`, Revision: "2026-07-01T00:00:00Z"}) {
		t.Errorf("filters %+v", snap.Filters)
	}
	var pol ArtifactRecord
	for _, a := range snap.Artifacts {
		if a.ExternalID == "pol-1" {
			pol = a
		}
	}
	if pol.Kind != "configuration_profile" || pol.Platform != "windows" || len(pol.Assignments) != 1 || pol.Assignments[0].TargetKind != "all_devices" {
		t.Errorf("settings catalog policy %+v", pol)
	}
	if len(snap.Observations) != 2 || snap.Observations[0].State != "failed" || snap.Observations[0].RawStatus != "failed" ||
		snap.Observations[0].ArtifactExternalID != "app-1" || snap.Observations[0].ExternalDeviceID != "dev-1" || snap.Observations[1].State != "applied" {
		t.Errorf("observations %+v", snap.Observations)
	}
}

func TestGraphProviderManagementFailsAsAWholeAndNeverLeaksTheBody(t *testing.T) {
	s := newGraphServer(t)
	s.mux.HandleFunc("/deviceAppManagement/mobileApps", func(w http.ResponseWriter, r *http.Request) { json200(w, `{"value":[]}`) })
	s.mux.HandleFunc("/deviceManagement/deviceConfigurations", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json200(w, `{"error":{"code":"Forbidden","message":"Application is not authorized for tenant contoso.onmicrosoft.com"}}`)
	})
	p := NewGraphProvider(s.graph(t), false)
	_, err := p.Management(context.Background())
	var ge *microsoft.GraphError
	if !errors.As(err, &ge) || ge.Status != 403 || strings.Contains(err.Error(), "contoso") {
		t.Fatalf("err %v", err)
	}
	if st := p.Status(); st.State != providerstatus.Failing || st.LastErrorCode != "http_403" {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphProviderUnverifiedUntilAnObservedCall(t *testing.T) {
	s := newGraphServer(t)
	p := NewGraphProvider(s.graph(t), false)
	if st := p.Status(); st.State != providerstatus.Unverified {
		t.Fatalf("status %+v", st)
	}
}

func TestMapAssignmentRejectsAGroupTargetWithoutGroup(t *testing.T) {
	if _, ok := mapAssignment(graphAssignment{ID: "x", Target: graphTarget{ODataType: "#microsoft.graph.groupAssignmentTarget"}}, true); ok {
		t.Fatal("accepted")
	}
}
