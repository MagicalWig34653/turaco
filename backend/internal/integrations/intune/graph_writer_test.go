package intune

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// tenant is a small stateful stand-in for the Graph objects the writer touches. Request and answer shapes follow
// the pages named in graph_writer.go (read 2026-10-10): create group 201 with id, add member $ref 204, remove member
// $ref 204, mobileAppAssignment list 200 / create 201 / delete 204, managedDevice and device lookups.
type tenant struct {
	groups      map[string]string          // id -> display name
	members     map[string]map[string]bool // group id -> device object ids
	assignments map[string][]map[string]any
	devices     map[string]string // managed id -> aad device id
	objects     map[string]string // aad device id -> device object id
	nextID      int
	writes      []string
	// foreign marks groups that carry the ring name but were not created by Turaco (no owner marker).
	foreign map[string]bool
}

func newTenant() *tenant {
	return &tenant{groups: map[string]string{}, members: map[string]map[string]bool{}, assignments: map[string][]map[string]any{"app-1": {}},
		devices: map[string]string{"dev-1": "11111111-1111-1111-1111-111111111111", "dev-2": "22222222-2222-2222-2222-222222222222"},
		objects: map[string]string{"11111111-1111-1111-1111-111111111111": "aaaaaaaa-0000-0000-0000-000000000001", "22222222-2222-2222-2222-222222222222": "aaaaaaaa-0000-0000-0000-000000000002"}}
}

func (tn *tenant) newID() string {
	tn.nextID++
	return fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", tn.nextID)
}

func (tn *tenant) install(t *testing.T, s *graphServer) {
	write := func(what string) { tn.writes = append(tn.writes, what) }
	s.mux.HandleFunc("/groups", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			filter := r.URL.Query().Get("$filter")
			var items []string
			for id, name := range tn.groups {
				if filter == "displayName eq '"+name+"'" {
					desc := ownerMarker(name)
					if tn.foreign[id] {
						desc = "someone else's group"
					}
					items = append(items, fmt.Sprintf(`{"id":%q,"displayName":%q,"description":%q,"mailNickname":%q,"securityEnabled":true,"mailEnabled":false,"groupTypes":[]}`, id, name, desc, name))
				}
			}
			json200(w, `{"value":[`+strings.Join(items, ",")+`]}`)
		case http.MethodPost:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["securityEnabled"] != true || b["mailEnabled"] != false {
				t.Errorf("group body %v", b)
			}
			id := tn.newID()
			tn.groups[id], tn.members[id] = b["displayName"].(string), map[string]bool{}
			write("create group")
			w.WriteHeader(http.StatusCreated)
			json200(w, fmt.Sprintf(`{"id":%q,"displayName":%q}`, id, b["displayName"]))
		}
	})
	s.mux.HandleFunc("/groups/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/groups/"), "/")
		gid := parts[0]
		switch {
		case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "members":
			var items []string
			for id := range tn.members[gid] {
				items = append(items, fmt.Sprintf(`{"@odata.type":"#microsoft.graph.device","id":%q}`, id))
			}
			sort.Strings(items)
			json200(w, `{"value":[`+strings.Join(items, ",")+`]}`)
		case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "$ref":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			id := b["@odata.id"][strings.LastIndex(b["@odata.id"], "/")+1:]
			if !strings.HasPrefix(b["@odata.id"], "https://graph.microsoft.com/v1.0/directoryObjects/") {
				t.Errorf("member reference %q", b["@odata.id"])
			}
			if tn.members[gid][id] {
				w.WriteHeader(http.StatusBadRequest) // already a member
				json200(w, `{"error":{"code":"Request_BadRequest","message":"One or more added object references already exist"}}`)
				return
			}
			tn.members[gid][id] = true
			write("add member")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && len(parts) == 4 && parts[3] == "$ref":
			delete(tn.members[gid], parts[2])
			write("remove member")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected group request %s %s", r.Method, r.URL.Path)
		}
	})
	s.mux.HandleFunc("/deviceManagement/managedDevices/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/deviceManagement/managedDevices/")
		aad, ok := tn.devices[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json200(w, `{"error":{"code":"ResourceNotFound"}}`)
			return
		}
		json200(w, fmt.Sprintf(`{"id":%q,"azureADDeviceId":%q}`, id, aad))
	})
	s.mux.HandleFunc("/devices", func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("$filter")
		for aad, obj := range tn.objects {
			if f == "deviceId eq '"+aad+"'" {
				json200(w, fmt.Sprintf(`{"value":[{"id":%q,"deviceId":%q}]}`, obj, aad))
				return
			}
		}
		json200(w, `{"value":[]}`)
	})
	s.mux.HandleFunc("/deviceAppManagement/mobileApps/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/deviceAppManagement/mobileApps/"), "/")
		app := parts[0]
		list, ok := tn.assignments[app]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json200(w, `{"error":{"code":"ResourceNotFound"}}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && len(parts) == 2:
			b, _ := json.Marshal(list)
			json200(w, `{"value":`+string(b)+`}`)
		case r.Method == http.MethodPost && len(parts) == 2:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			b["id"] = tn.newID()
			tn.assignments[app] = append(list, b)
			write("create assignment")
			w.WriteHeader(http.StatusCreated)
			out, _ := json.Marshal(b)
			json200(w, string(out))
		case r.Method == http.MethodDelete && len(parts) == 3:
			kept := list[:0:0]
			for _, a := range list {
				if a["id"] != parts[2] {
					kept = append(kept, a)
				}
			}
			tn.assignments[app] = kept
			write("delete assignment")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected app request %s %s", r.Method, r.URL.Path)
		}
	})
}

func ringOp(opID string, intent string, devices ...string) RingAssignmentOp {
	return RingAssignmentOp{OperationID: opID, ManagementArtifactExternalID: "app-1", RingKey: ringID, TargetGroupExternalID: RingGroupID(ringID), Intent: intent, DeviceExternalIDs: devices}
}

func groupTarget(id string) map[string]any {
	return map[string]any{"@odata.type": "#microsoft.graph.groupAssignmentTarget", "groupId": id}
}

func TestGraphWriterSetIsContentIdempotentAndTouchesOnlyItsOwnObjects(t *testing.T) {
	s := newGraphServer(t)
	tn := newTenant()
	tn.install(t, s)
	// A foreign assignment of the same app must survive everything the writer does.
	tn.assignments["app-1"] = []map[string]any{{"id": "foreign", "intent": "required", "target": map[string]any{"@odata.type": "#microsoft.graph.allDevicesAssignmentTarget"}}}
	w := NewGraphWriter(s.graph(t))
	ctx := context.Background()

	if err := w.SetRingAssignment(ctx, ringOp("op-1", "required", "dev-1", "dev-2")); err != nil {
		t.Fatal(err)
	}
	if len(tn.groups) != 1 || len(tn.assignments["app-1"]) != 2 {
		t.Fatalf("groups %v assignments %v", tn.groups, tn.assignments["app-1"])
	}
	var gid string
	for id, name := range tn.groups {
		gid = id
		if name != RingGroupID(ringID) {
			t.Fatalf("group name %q", name)
		}
	}
	if len(tn.members[gid]) != 2 {
		t.Fatalf("members %v", tn.members[gid])
	}
	first := len(tn.writes)

	// Replay with a new operation id: nothing is written again.
	if err := w.SetRingAssignment(ctx, ringOp("op-2", "required", "dev-2", "dev-1")); err != nil {
		t.Fatal(err)
	}
	if len(tn.writes) != first {
		t.Fatalf("replay wrote %v", tn.writes[first:])
	}

	// Smaller device set and a different intent replace, not append.
	if err := w.SetRingAssignment(ctx, ringOp("op-3", "uninstall", "dev-1")); err != nil {
		t.Fatal(err)
	}
	if len(tn.members[gid]) != 1 || !tn.members[gid]["aaaaaaaa-0000-0000-0000-000000000001"] {
		t.Fatalf("members %v", tn.members[gid])
	}
	own := 0
	for _, a := range tn.assignments["app-1"] {
		switch {
		case a["id"] == "foreign":
		case a["intent"] == "uninstall" && a["target"].(map[string]any)["groupId"] == gid:
			own++
		default:
			t.Errorf("unexpected assignment %v", a)
		}
	}
	if own != 1 || len(tn.assignments["app-1"]) != 2 {
		t.Fatalf("assignments %v", tn.assignments["app-1"])
	}
	if len(tn.groups) != 1 {
		t.Fatalf("a second group was created: %v", tn.groups)
	}

	// Clear removes the ring's assignment and members and is repeatable; the foreign assignment stays.
	if err := w.ClearRingAssignment(ctx, "op-4", "app-1", ringID); err != nil {
		t.Fatal(err)
	}
	if len(tn.members[gid]) != 0 || len(tn.assignments["app-1"]) != 1 || tn.assignments["app-1"][0]["id"] != "foreign" {
		t.Fatalf("after clear: members %v assignments %v", tn.members[gid], tn.assignments["app-1"])
	}
	n := len(tn.writes)
	if err := w.ClearRingAssignment(ctx, "op-5", "app-1", ringID); err != nil || len(tn.writes) != n {
		t.Fatalf("second clear: %v %v", err, tn.writes[n:])
	}
	if st := w.Status(); st.State != "verified" {
		t.Fatalf("status %+v", st)
	}
}

func TestGraphWriterClearWithoutGroupSucceeds(t *testing.T) {
	s := newGraphServer(t)
	newTenant().install(t, s)
	w := NewGraphWriter(s.graph(t))
	if err := w.ClearRingAssignment(context.Background(), "op", "app-1", ringID); err != nil {
		t.Fatal(err)
	}
}

func TestGraphWriterRejectsUnknownArtifactAndDeviceWithoutCreatingAnything(t *testing.T) {
	s := newGraphServer(t)
	tn := newTenant()
	tn.install(t, s)
	w := NewGraphWriter(s.graph(t))
	op := ringOp("op", "required", "dev-1")
	op.ManagementArtifactExternalID = "nope"
	if err := w.SetRingAssignment(context.Background(), op); !errors.Is(err, ErrArtifactUnknown) || !errors.Is(err, ErrPermanent) {
		t.Fatalf("unknown artifact: %v", err)
	}
	if len(tn.groups) != 0 {
		t.Fatal("a group was created for an unknown artifact")
	}
	err := w.SetRingAssignment(context.Background(), ringOp("op", "required", "dev-1", "ghost"))
	if !errors.Is(err, ErrPermanent) || IsTransient(err) {
		t.Fatalf("unknown device: %v", err)
	}
	if len(tn.assignments["app-1"]) != 0 {
		t.Fatal("an assignment was written although a device is unknown")
	}
}

func TestGraphWriterValidatesTheRingGroup(t *testing.T) {
	s := newGraphServer(t)
	newTenant().install(t, s)
	w := NewGraphWriter(s.graph(t))
	op := ringOp("op", "required", "dev-1")
	op.TargetGroupExternalID = "Finance-All-Users" // not a Turaco-owned ring group
	if err := w.SetRingAssignment(context.Background(), op); !errors.Is(err, ErrPermanent) {
		t.Fatalf("foreign group: %v", err)
	}
	op = ringOp("op", "required", "dev-1")
	op.RingKey = "x' or displayName ne '"
	op.TargetGroupExternalID = RingGroupID(op.RingKey)
	if err := w.SetRingAssignment(context.Background(), op); !errors.Is(err, ErrPermanent) {
		t.Fatalf("filter injection: %v", err)
	}
	for _, c := range s.calls() {
		if strings.Contains(c, "/groups") {
			t.Fatalf("a request was sent: %v", s.calls())
		}
	}
}

func TestGraphWriterErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		transient bool
	}{{"throttled", 429, true}, {"unavailable", 503, true}, {"forbidden", 403, false}, {"bad request", 400, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGraphServer(t)
			s.mux.HandleFunc("/deviceAppManagement/mobileApps/app-1/assignments", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(tc.status)
				json200(w, `{"error":{"code":"X","message":"tenant secret detail"}}`)
			})
			err := NewGraphWriter(s.graph(t)).SetRingAssignment(context.Background(), ringOp("op", "required"))
			if err == nil || IsTransient(err) != tc.transient || errors.Is(err, ErrPermanent) == tc.transient {
				t.Fatalf("err %v", err)
			}
			if strings.Contains(err.Error(), "tenant secret detail") {
				t.Fatalf("error leaks the Graph message: %v", err)
			}
		})
	}
}

func TestGraphWriterAddMemberAlreadyPresentIsSuccess(t *testing.T) {
	s := newGraphServer(t)
	tn := newTenant()
	tn.install(t, s)
	w := NewGraphWriter(s.graph(t))
	if err := w.SetRingAssignment(context.Background(), ringOp("op-1", "required", "dev-1")); err != nil {
		t.Fatal(err)
	}
	// Membership lost from the writer's view but present at the provider: the 400 of the add is resolved by reading.
	var gid string
	for id := range tn.groups {
		gid = id
	}
	if err := w.addMember(context.Background(), gid, "aaaaaaaa-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("already a member: %v", err)
	}
}

// A group that merely carries a ring's display name is not Turaco's: the writer refuses it and writes nothing.
func TestGraphWriterRefusesAForeignGroupWithARingName(t *testing.T) {
	tn := newTenant()
	tn.foreign = map[string]bool{}
	name := strings.ToLower(RingGroupID("ring-a"))
	id := tn.newID()
	tn.groups[id], tn.members[id] = name, map[string]bool{"cccccccc-0000-0000-0000-000000000001": true}
	tn.foreign[id] = true
	s := newGraphServer(t)
	tn.install(t, s)
	w := NewGraphWriter(s.graph(t))
	_, _, err := w.findGroup(context.Background(), name)
	if !errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("expected a permanent refusal, got %v", err)
	}
	if len(tn.writes) != 0 || !tn.members[id]["cccccccc-0000-0000-0000-000000000001"] {
		t.Fatalf("a foreign group must not be changed: writes %v", tn.writes)
	}
}
