package repository_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Tests for the F6 slice 3 management views.

type fakeDir struct {
	groups  map[string]application.DirectoryGroup // org id -> group
	parents map[string][]string                   // child org id -> parent org ids
	members map[string][]string                   // group org id -> user ids
	users   map[string]string                     // user id -> name
	// noIdentity lists Users without synced directory data; cutMemberships / cutNesting / cutMembers make the
	// corresponding lookups report truncation.
	noIdentity     map[string]bool
	cutMemberships bool
	cutNesting     bool
	cutMembers     bool
	keys           map[string]bool // provider keys the views asked for
}

func (f *fakeDir) key(k string) {
	if f.keys == nil {
		f.keys = map[string]bool{}
	}
	f.keys[k] = true
}

func (f *fakeDir) UsersWithIdentity(_ context.Context, key string, users []string) (map[string]bool, error) {
	f.key(key)
	out := map[string]bool{}
	for _, u := range users {
		out[u] = !f.noIdentity[u]
	}
	return out, nil
}

func (f *fakeDir) GroupsByExternalIDs(_ context.Context, key string, exts []string) ([]application.DirectoryGroup, bool, error) {
	f.key(key)
	var out []application.DirectoryGroup
	for _, g := range f.groups {
		for _, x := range exts {
			if g.ExternalID == x {
				out = append(out, g)
			}
		}
	}
	return out, false, nil
}

func (f *fakeDir) GroupsByIDs(_ context.Context, key string, ids []string) ([]application.DirectoryGroup, bool, error) {
	f.key(key)
	var out []application.DirectoryGroup
	for _, id := range ids {
		if g, ok := f.groups[id]; ok {
			out = append(out, g)
		}
	}
	return out, false, nil
}

func (f *fakeDir) NestingUp(_ context.Context, key string, ids []string) ([]application.NestingEdge, bool, error) {
	f.key(key)
	var out []application.NestingEdge
	seen := map[string]bool{}
	queue := append([]string(nil), ids...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		for _, p := range f.parents[c] {
			out = append(out, application.NestingEdge{ChildID: c, ParentID: p})
			queue = append(queue, p)
		}
	}
	return out, f.cutNesting, nil
}

func (f *fakeDir) NestingDown(_ context.Context, key string, ids []string) ([]application.NestingEdge, bool, error) {
	f.key(key)
	var out []application.NestingEdge
	seen := map[string]bool{}
	queue := append([]string(nil), ids...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		for child, ps := range f.parents {
			for _, p := range ps {
				if p == c {
					out = append(out, application.NestingEdge{ChildID: child, ParentID: c})
					queue = append(queue, child)
				}
			}
		}
	}
	return out, f.cutNesting, nil
}

func (f *fakeDir) UserMemberships(_ context.Context, key string, users []string) ([]application.UserMembership, bool, error) {
	f.key(key)
	var out []application.UserMembership
	for g, us := range f.members {
		for _, u := range us {
			for _, want := range users {
				if u == want {
					out = append(out, application.UserMembership{UserID: u, GroupID: g, ObservedAt: time.Now()})
				}
			}
		}
	}
	return out, f.cutMemberships, nil
}

func (f *fakeDir) GroupMembers(_ context.Context, key string, groups []string, limit int) ([]application.UserMembership, bool, error) {
	f.key(key)
	var out []application.UserMembership
	for _, g := range groups {
		for _, u := range f.members[g] {
			out = append(out, application.UserMembership{UserID: u, GroupID: g, ObservedAt: time.Now()})
		}
	}
	return out, f.cutMembers, nil
}

func (f *fakeDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := f.users[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

type fakeHolders struct{ held map[string]string } // asset id -> user id

func (f fakeHolders) UserHolders(_ context.Context, assets []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range assets {
		if u, ok := f.held[a]; ok {
			out[a] = u
		}
	}
	return out, nil
}

func (f fakeHolders) AssetsHeldByUsers(_ context.Context, users []string, _ int) (map[string][]string, error) {
	out := map[string][]string{}
	for a, u := range f.held {
		for _, want := range users {
			if u == want {
				out[u] = append(out[u], a)
			}
		}
	}
	return out, nil
}

type viewEnv struct {
	*env
	dir     *fakeDir
	holders fakeHolders
	full    application.Principal
}

func newViewEnv(t *testing.T) *viewEnv {
	e := newEnv(t)
	v := &viewEnv{env: e, dir: &fakeDir{groups: map[string]application.DirectoryGroup{}, parents: map[string][]string{}, members: map[string][]string{}, users: map[string]string{}, noIdentity: map[string]bool{}},
		holders: fakeHolders{held: map[string]string{}}}
	e.svc.WithViews(v.dir, v.holders).WithProviderKey(e.provider)
	v.full = application.Principal{UserID: e.user, View: true, ManagementView: true, DirectoryView: true, AssetsView: true}
	return v
}

// orgGroup registers a Directory Group whose provider external id is ext.
func (v *viewEnv) orgGroup(ext string) string {
	id := v.newID()
	v.dir.groups[id] = application.DirectoryGroup{ID: id, ExternalID: ext, Name: "Name-" + ext, ObservedAt: time.Now()}
	return id
}

func mship(d, g string) intune.DeviceGroupMembershipRecord {
	return intune.DeviceGroupMembershipRecord{ExternalDeviceID: d, GroupExternalID: g}
}

func excl(id, group string) intune.AssignmentRecord {
	return intune.AssignmentRecord{ProviderAssignmentID: id, TargetKind: "group", TargetGroupExternalID: group, Mode: "exclude", Intent: "required"}
}

func allDevices(id string) intune.AssignmentRecord {
	return intune.AssignmentRecord{ProviderAssignmentID: id, TargetKind: "all_devices", Mode: "include", Intent: "required"}
}

func (v *viewEnv) status(p application.Principal, deviceID string, f application.DeviceManagementFilter) map[string]application.DeviceArtifactStatus {
	v.t.Helper()
	res, err := v.svc.DeviceManagement(context.Background(), p, deviceID, f)
	if err != nil {
		v.t.Fatalf("device management: %v", err)
	}
	out := map[string]application.DeviceArtifactStatus{}
	for _, it := range res.Items {
		out[it.Artifact.ExternalID] = it
	}
	return out
}

func TestDeviceViewKeepsAssignedExpectedAndObservedApart(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("g1")
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "g1")},
		Artifacts: []intune.ArtifactRecord{
			art("a-failed", "Failed", grp("x1", "g1", "required")),        // assigned + expected, observed failed
			art("a-unobs", "Unobserved", grp("x2", "g1", "required")),     // assigned + expected, never observed
			art("a-excl", "Excluded", allDevices("x3"), excl("x4", "g1")), // exclude beats include
			art("a-other", "Other", grp("x5", "gOther", "required")),      // not reaching the device
		},
		Observations: []intune.ObservationRecord{obs("d1", "a-failed", "failed", "error 5")},
	})
	d := v.device("d1").ID
	got := v.status(v.full, d, application.DeviceManagementFilter{})
	f := got["a-failed"]
	if !f.Assigned || f.Expected.Result != application.ExpectedApplicable || f.Observed == nil || f.Observed.State != "failed" || f.Mismatch != application.MismatchExpectedNotApplied {
		t.Errorf("failed = %+v", f)
	}
	if u := got["a-unobs"]; !u.Assigned || u.Expected.Result != application.ExpectedApplicable || u.Observed != nil || u.Mismatch != application.MismatchAssignedNotObserved {
		t.Errorf("unobserved = %+v", u)
	}
	if x := got["a-excl"]; x.Expected.Result != application.ExpectedExcluded || x.Observed != nil {
		t.Errorf("excluded = %+v", x)
	}
	// The Device's User is unknown, so a group assignment that is not Device-reachable might still address the
	// User: it is unknown and not assigned to this Device, never not_applicable.
	if o := got["a-other"]; o.Assigned || o.Expected.Result != application.ExpectedUnknown {
		t.Errorf("other = %+v", o)
	}
	// Filters.
	if m := v.status(v.full, d, application.DeviceManagementFilter{Mismatch: application.MismatchAssignedNotObserved}); len(m) != 1 || m["a-unobs"].Artifact.ID == "" {
		t.Errorf("mismatch filter = %v", m)
	}
	if m := v.status(v.full, d, application.DeviceManagementFilter{State: "failed"}); len(m) != 1 || m["a-failed"].Artifact.ID == "" {
		t.Errorf("state filter = %v", m)
	}
	if m := v.status(v.full, d, application.DeviceManagementFilter{State: application.ObservedNone}); len(m) != 3 {
		t.Errorf("state none = %v", m)
	}
	if m := v.status(v.full, d, application.DeviceManagementFilter{Kind: "application"}); len(m) != 0 {
		t.Errorf("kind filter = %v", m)
	}
	// Redaction: without organization.directory.view neither group name nor external id is returned.
	noDir := v.full
	noDir.DirectoryView = false
	for _, as := range v.status(noDir, d, application.DeviceManagementFilter{})["a-failed"].Assignments {
		if as.Group == nil || !as.Group.Redacted || as.Group.ExternalID != nil || as.Group.Name != nil {
			t.Errorf("group not redacted: %+v", as.Group)
		}
	}
	for _, as := range f.Assignments {
		if as.Group == nil || as.Group.Redacted || as.Group.ExternalID == nil || as.Group.Name == nil {
			t.Errorf("group should be visible: %+v", as.Group)
		}
	}
}

func TestDeviceViewKeysetAndInvalidFilters(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"))
	var arts []intune.ArtifactRecord
	for i := 0; i < 5; i++ {
		arts = append(arts, art(fmt.Sprintf("a%d", i), fmt.Sprintf("A%d", i), allDevices(fmt.Sprintf("x%d", i))))
	}
	v.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	d := v.device("d1").ID
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		res, err := v.svc.DeviceManagement(context.Background(), v.full, d, application.DeviceManagementFilter{Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if seen[it.Artifact.ID] {
				t.Fatalf("duplicate %s", it.Artifact.ID)
			}
			seen[it.Artifact.ID] = true
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("seen = %d", len(seen))
	}
	for _, f := range []application.DeviceManagementFilter{{Kind: "x"}, {State: "x"}, {Expected: "x"}, {Mismatch: "x"}} {
		if _, err := v.svc.DeviceManagement(context.Background(), v.full, d, f); err == nil {
			t.Errorf("filter %+v accepted", f)
		}
	}
	if _, err := v.svc.DeviceManagement(context.Background(), v.full, d, application.DeviceManagementFilter{Page: application.Page{Cursor: "zz"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor = %v", err)
	}
}

func TestViewsRequirePermissionsAndHideUnknownIDs(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("g1")
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "g1")}, Artifacts: []intune.ArtifactRecord{art("a1", "P", grp("x1", "g1", "required"))}})
	ctx := context.Background()
	d, a := v.device("d1").ID, v.artifactID("a1")
	var gid string
	for id := range v.dir.groups {
		gid = id
	}
	user := v.newID()
	v.dir.users[user] = "Alice"
	none := application.Principal{UserID: v.user}
	mgmtOnly := application.Principal{UserID: v.user, ManagementView: true}
	devOnly := application.Principal{UserID: v.user, View: true}
	noDirectory := application.Principal{UserID: v.user, View: true, ManagementView: true}
	for name, p := range map[string]application.Principal{"none": none, "management only": mgmtOnly, "devices only": devOnly} {
		if _, err := v.svc.DeviceManagement(ctx, p, d, application.DeviceManagementFilter{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: device view = %v", name, err)
		}
		if _, err := v.svc.AssignmentPath(ctx, p, d, a); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: path = %v", name, err)
		}
		if _, err := v.svc.GroupManagement(ctx, p, gid, application.Page{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: group view = %v", name, err)
		}
		if _, err := v.svc.UserManagement(ctx, p, user, application.Page{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: user view = %v", name, err)
		}
	}
	// Group and User views reveal directory data.
	if _, err := v.svc.GroupManagement(ctx, noDirectory, gid, application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("group view without directory = %v", err)
	}
	if _, err := v.svc.UserManagement(ctx, noDirectory, user, application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("user view without directory = %v", err)
	}
	// Artifact targets: management access suffices, but the Device evaluation is hidden without device access.
	if _, err := v.svc.ArtifactTargets(ctx, none, a); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("targets none = %v", err)
	}
	tg, err := v.svc.ArtifactTargets(ctx, mgmtOnly, a)
	if err != nil || tg.Evaluation.Shown || len(tg.Evaluation.Examples) != 0 {
		t.Errorf("targets without device access = %+v %v", tg.Evaluation, err)
	}
	tg, err = v.svc.ArtifactTargets(ctx, noDirectory, a)
	if err != nil || !tg.Evaluation.Shown || tg.Evaluation.Expected[application.ExpectedApplicable] != 1 || len(tg.Evaluation.Examples) != 1 {
		t.Errorf("targets = %+v %v", tg.Evaluation, err)
	}
	for _, as := range tg.Assignments {
		if as.Group == nil || !as.Group.Redacted {
			t.Errorf("targets leak group: %+v", as.Group)
		}
	}
	// Unknown or malformed ids look the same.
	for _, id := range []string{"nope", v.newID()} {
		if _, err := v.svc.DeviceManagement(ctx, v.full, id, application.DeviceManagementFilter{}); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("device %s = %v", id, err)
		}
		if _, err := v.svc.AssignmentPath(ctx, v.full, d, id); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("path artifact %s = %v", id, err)
		}
		if _, err := v.svc.GroupManagement(ctx, v.full, id, application.Page{}); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("group %s = %v", id, err)
		}
		if _, err := v.svc.UserManagement(ctx, v.full, id, application.Page{}); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("user %s = %v", id, err)
		}
		if _, err := v.svc.ArtifactTargets(ctx, v.full, id); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("targets %s = %v", id, err)
		}
	}
}

func TestNestedGroupsAndAssignmentPath(t *testing.T) {
	v := newViewEnv(t)
	child, parent := v.orgGroup("child"), v.orgGroup("parent")
	v.dir.parents[child] = []string{parent}
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "child")},
		Artifacts:   []intune.ArtifactRecord{art("a1", "P", grp("x1", "parent", "required"))},
	})
	d, a := v.device("d1").ID, v.artifactID("a1")
	st := v.status(v.full, d, application.DeviceManagementFilter{})["a1"]
	if st.Expected.Result != application.ExpectedApplicable {
		t.Fatalf("nested expected = %+v", st.Expected)
	}
	path, err := v.svc.AssignmentPath(context.Background(), v.full, d, a)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, s := range path.Path {
		kinds[s.Kind] = true
	}
	for _, k := range []string{"membership", "nested_in", "assignment", "result"} {
		if !kinds[k] {
			t.Errorf("path lacks %s step: %+v", k, path.Path)
		}
	}
	if path.Expected.Result != application.ExpectedApplicable || path.Observed != nil {
		t.Errorf("path = %+v observed %v", path.Expected, path.Observed)
	}
	// The same path for a caller without directory access names no group.
	noDir := v.full
	noDir.DirectoryView = false
	p2, err := v.svc.AssignmentPath(context.Background(), noDir, d, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p2.Path {
		if s.Group != nil && (!s.Group.Redacted || s.Group.ExternalID != nil || s.Group.Name != nil) {
			t.Errorf("path leaks group: %+v", s.Group)
		}
	}
	// The Directory Group view finds the artifact assigned to the parent from the child group.
	g, err := v.svc.GroupManagement(context.Background(), v.full, child, application.Page{})
	if err != nil || len(g.Items) != 1 || !g.Items[0].Evaluation.Shown || g.Items[0].Evaluation.Expected[application.ExpectedApplicable] != 1 {
		t.Fatalf("group view = %+v %v", g, err)
	}
	if !g.Items[0].Assignments[0].Nested {
		t.Error("assignment through the parent group must be marked nested")
	}
}

func TestStaleMembershipLowersConfidenceAndUnknownUserStaysUnknown(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("g1")
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "g1")},
		Artifacts: []intune.ArtifactRecord{
			art("a1", "Device", grp("x1", "g1", "required")),
			art("a2", "Users", intune.AssignmentRecord{ProviderAssignmentID: "x2", TargetKind: "all_users", Mode: "include", Intent: "required"}),
		},
	})
	d := v.device("d1").ID
	if _, err := v.pool.Exec(context.Background(), `UPDATE endpoints.device_group_memberships SET last_synced_at = now() - interval '5 days' WHERE device_id = $1::uuid`, d); err != nil {
		t.Fatal(err)
	}
	got := v.status(v.full, d, application.DeviceManagementFilter{})
	a1 := got["a1"].Expected
	if a1.Result != application.ExpectedApplicable || a1.Confidence != "low" || !contains(a1.Reasons, "inputs_stale") {
		t.Errorf("stale = %+v", a1)
	}
	// The Device has no known User, so a User assignment is unknown, never not_applicable.
	a2 := got["a2"].Expected
	if a2.Result != application.ExpectedUnknown {
		t.Errorf("unknown user = %+v", a2)
	}
}

func contains(in []string, s string) bool {
	for _, x := range in {
		if x == s {
			return true
		}
	}
	return false
}

func TestUserViewSeparatesUserTargetingFromDeviceResults(t *testing.T) {
	v := newViewEnv(t)
	ug := v.orgGroup("ug")
	user := v.newID()
	v.dir.users[user] = "Alice"
	v.dir.members[ug] = []string{user}
	v.ingest(dev("d1", "PC-1", "SN1"))
	asset := v.newID()
	v.assets.existing[asset] = true
	if _, err := v.link(v.manage, v.device("d1").ID, asset, "serial_confirmed"); err != nil {
		t.Fatal(err)
	}
	v.holders.held[asset] = user
	v.mingest(true, intune.ManagementSnapshot{
		Artifacts:    []intune.ArtifactRecord{art("a1", "ForAlice", grp("x1", "ug", "required"))},
		Observations: []intune.ObservationRecord{obs("d1", "a1", "applied", "")},
	})
	res, err := v.svc.UserManagement(context.Background(), v.full, user, application.Page{})
	if err != nil || len(res.Items) != 1 || !res.DevicesShown {
		t.Fatalf("user view = %+v %v", res, err)
	}
	it := res.Items[0]
	if len(it.Targeting) != 1 || len(it.Devices) != 1 || it.Devices[0].Expected.Result != application.ExpectedApplicable ||
		it.Devices[0].Observed == nil || it.Devices[0].Observed.State != "applied" {
		t.Errorf("item = %+v", it)
	}
	// Without assets.view the holder-derived Devices are not shown.
	p := v.full
	p.AssetsView = false
	res, err = v.svc.UserManagement(context.Background(), p, user, application.Page{})
	if err != nil || res.DevicesShown {
		t.Fatalf("no assets.view: %+v %v", res, err)
	}
	for _, it := range res.Items {
		if len(it.Devices) != 0 {
			t.Errorf("devices leaked: %+v", it.Devices)
		}
	}
}

func TestGroupViewIsBoundedAndReportsTruncation(t *testing.T) {
	v := newViewEnv(t)
	gid := v.orgGroup("g1")
	devs := make([]application.SnapshotDevice, 0, application.MaxEvalDevices+1)
	var ms []intune.DeviceGroupMembershipRecord
	for i := 0; i < application.MaxEvalDevices+1; i++ {
		id := fmt.Sprintf("d%04d", i)
		devs = append(devs, dev(id, "PC-"+id, fmt.Sprintf("SN-%s-%04d", v.provider, i)))
		ms = append(ms, mship(id, "g1"))
	}
	v.ingest(devs...)
	v.mingest(true, intune.ManagementSnapshot{Memberships: ms, Artifacts: []intune.ArtifactRecord{art("a1", "P", grp("x1", "g1", "required"))}})
	g, err := v.svc.GroupManagement(context.Background(), v.full, gid, application.Page{})
	if err != nil || len(g.Items) != 1 {
		t.Fatalf("group = %+v %v", g, err)
	}
	ev := g.Items[0].Evaluation
	if !ev.Truncated || ev.Evaluated != application.MaxEvalDevices || !g.CandidatesTruncated || len(ev.Examples) > application.MaxViewExamples {
		t.Errorf("evaluation = evaluated %d truncated %v examples %d", ev.Evaluated, ev.Truncated, len(ev.Examples))
	}
	tg, err := v.svc.ArtifactTargets(context.Background(), v.full, v.artifactID("a1"))
	if err != nil || !tg.Evaluation.Truncated {
		t.Errorf("targets truncated = %+v %v", tg.Evaluation, err)
	}
}
