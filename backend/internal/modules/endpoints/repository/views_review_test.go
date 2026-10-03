package repository_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// Tests for the F6 slice 3 review fixes: results must never look certain when inputs are missing or were cut.

// holderOf links the Device (already ingested) to a fresh Asset held by the User and returns the Device id.
func (v *viewEnv) holderOf(deviceExt, user string) string {
	v.t.Helper()
	id := v.device(deviceExt).ID
	asset := v.newID()
	v.assets.existing[asset] = true
	if _, err := v.link(v.manage, id, asset, "serial_confirmed"); err != nil {
		v.t.Fatal(err)
	}
	v.holders.held[asset] = user
	return id
}

func (v *viewEnv) newUser(name string) string {
	id := v.newID()
	v.dir.users[id] = name
	return id
}

// groupTargetScenario: Device d1 (member of "gx", so its memberships are known) held by a User with directory data,
// and an artifact targeting a group the Device and its User are not in.
func groupTargetScenario(t *testing.T) (v *viewEnv, deviceID, userID string) {
	v = newViewEnv(t)
	v.orgGroup("gx")
	v.orgGroup("g1")
	userID = v.newUser("Alice")
	v.ingest(dev("d1", "PC-1", "SN1"))
	deviceID = v.holderOf("d1", userID)
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "gx")},
		Artifacts:   []intune.ArtifactRecord{art("a1", "P", grp("x1", "g1", "required"))},
	})
	return v, deviceID, userID
}

func TestKnownInputsGiveNotApplicableButCutInputsGiveUnknown(t *testing.T) {
	v, d, user := groupTargetScenario(t)
	// The artifact does not reach the Device (neither it nor its User is in g1), so the device view omits it.
	if res, err := v.svc.DeviceManagement(context.Background(), v.full, d, application.DeviceManagementFilter{}); err != nil || len(res.Items) != 0 || res.Truncated {
		t.Fatalf("baseline device view = %+v %v", res, err)
	}
	if p, err := v.svc.AssignmentPath(context.Background(), v.full, d, v.artifactID("a1")); err != nil ||
		p.Expected.Result != application.ExpectedNotApplicable || p.Expected.Confidence != "high" {
		t.Fatalf("complete inputs: %+v %v", p.Expected, err)
	}

	for name, cut := range map[string]func(){
		"user memberships cut": func() { v.dir.cutMemberships = true },
		"nesting cut":          func() { v.dir.cutNesting = true },
	} {
		v.dir.cutMemberships, v.dir.cutNesting = false, false
		cut()
		res, err := v.svc.DeviceManagement(context.Background(), v.full, d, application.DeviceManagementFilter{})
		if err != nil || len(res.Items) != 1 {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		e := res.Items[0].Expected
		if e.Result != application.ExpectedUnknown || e.Confidence != "low" || !contains(e.Reasons, "inputs_truncated") || !res.Truncated {
			t.Errorf("%s: expected %+v truncated=%v", name, e, res.Truncated)
		}
		path, err := v.svc.AssignmentPath(context.Background(), v.full, d, v.artifactID("a1"))
		if err != nil || path.Expected.Result != application.ExpectedUnknown {
			t.Errorf("%s: path = %+v %v", name, path.Expected, err)
		}
		um, err := v.svc.UserManagement(context.Background(), v.full, user, application.Page{})
		if err != nil || !um.Truncated || len(um.Items) != 1 || um.Items[0].UserResult != application.ExpectedUnknown {
			t.Errorf("%s: user view = %+v %v", name, um, err)
		}
	}
	// A cut member lookup marks the Directory Group view as truncated.
	v.dir.cutMemberships, v.dir.cutNesting, v.dir.cutMembers = false, false, true
	var g1 string
	for id, g := range v.dir.groups {
		if g.ExternalID == "g1" {
			g1 = id
		}
	}
	gm, err := v.svc.GroupManagement(context.Background(), v.full, g1, application.Page{})
	if err != nil || !gm.CandidatesTruncated {
		t.Errorf("group view with cut members = %+v %v", gm, err)
	}
	// Every directory lookup asked for the provider's own key.
	if len(v.dir.keys) != 1 || !v.dir.keys[v.provider] {
		t.Errorf("provider keys used = %v, want only %q", v.dir.keys, v.provider)
	}
}

func TestMembershipsWithoutSyncedDataAreUnknown(t *testing.T) {
	ctx := context.Background()
	v, d, user := groupTargetScenario(t)

	// The User has no synced directory identity: their memberships are unknown, not empty.
	v.dir.noIdentity[user] = true
	e := v.status(v.full, d, application.DeviceManagementFilter{})["a1"].Expected
	if e.Result != application.ExpectedUnknown || !contains(e.Reasons, "memberships_unknown") {
		t.Errorf("user without identity: %+v", e)
	}
	um, err := v.svc.UserManagement(ctx, v.full, user, application.Page{})
	if err != nil || len(um.Items) != 1 || um.Items[0].UserResult != application.ExpectedUnknown {
		t.Errorf("user view for a User without identity = %+v %v", um, err)
	}
	delete(v.dir.noIdentity, user)

	// A Device that never had a synced membership: its memberships are unknown.
	v.ingest(dev("d2", "PC-2", "SN2"))
	d2 := v.holderOf("d2", user)
	e = v.status(v.full, d2, application.DeviceManagementFilter{})["a1"].Expected
	if e.Result != application.ExpectedUnknown || !contains(e.Reasons, "memberships_unknown") {
		t.Errorf("device without membership rows: %+v", e)
	}
	// Once a membership existed (even when it was closed later) the Device's memberships are known.
	v.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{mship("d2", "gx"), mship("d1", "gx")}})
	v.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "gx")}})
	if n := v.count(`SELECT count(*) FROM endpoints.device_group_memberships WHERE device_id = $1::uuid AND observed_until IS NOT NULL`, d2); n != 1 {
		t.Fatalf("closed memberships of d2 = %d", n)
	}
	p, err := v.svc.AssignmentPath(ctx, v.full, d2, v.artifactID("a1"))
	if err != nil || p.Expected.Result != application.ExpectedNotApplicable {
		t.Errorf("device with closed membership history: %+v %v", p.Expected, err)
	}
}

func TestMixedOriginExclusionIsUnknown(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("devgroup")
	blocked := v.orgGroup("blocked")
	user := v.newUser("Alice")
	v.dir.members[blocked] = []string{user}
	v.ingest(dev("d1", "PC-1", "SN1"))
	d := v.holderOf("d1", user)
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "devgroup")},
		Artifacts:   []intune.ArtifactRecord{art("a1", "P", grp("x1", "devgroup", "required"), excl("x2", "blocked"))},
	})
	e := v.status(v.full, d, application.DeviceManagementFilter{})["a1"].Expected
	if e.Result != application.ExpectedUnknown || !contains(e.Reasons, "mixed_origin_exclusion") {
		t.Errorf("device include with user group exclude: %+v", e)
	}
}

func TestAssignedNeedsAnIncludeAndFilteredOutDevicesAreNotFlagged(t *testing.T) {
	v := newViewEnv(t)
	v.orgGroup("g1")
	v.ingest(dev("d1", "PC-1", "SN1"))
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "g1")},
		Filters:     []intune.FilterRecord{{ExternalID: "mac", Name: "Macs", Platform: "windows", Rule: `(device.manufacturer -eq "Apple")`}},
		Artifacts: []intune.ArtifactRecord{
			art("a-excl", "OnlyExclude", excl("x1", "g1")),
			art("a-filtered", "Filtered", intune.AssignmentRecord{ProviderAssignmentID: "x2", TargetKind: "group", TargetGroupExternalID: "g1", Mode: "include",
				Intent: "required", FilterExternalID: "mac", FilterMode: "include"}),
		},
	})
	d := v.device("d1").ID
	got := v.status(v.full, d, application.DeviceManagementFilter{})
	if x := got["a-excl"]; x.Assigned || x.Mismatch != "" {
		t.Errorf("exclude-only artifact: assigned=%v mismatch=%q", x.Assigned, x.Mismatch)
	}
	if f := got["a-filtered"]; f.Expected.Result != application.ExpectedNotApplicable || f.Mismatch != "" {
		t.Errorf("filtered-out include: expected=%+v mismatch=%q (must not be assigned_not_observed)", f.Expected, f.Mismatch)
	}
}

// countingHolders records whether the Asset holder was asked.
type countingHolders struct {
	fakeHolders
	calls *int
}

func (c countingHolders) UserHolders(ctx context.Context, assets []string) (map[string]string, error) {
	*c.calls++
	return c.fakeHolders.UserHolders(ctx, assets)
}

func TestWithoutAssetsViewTheHolderIsNotLoadedAndNoUserOriginLeaks(t *testing.T) {
	ctx := context.Background()
	v := newViewEnv(t)
	ug := v.orgGroup("ug")
	v.orgGroup("gx")
	user := v.newUser("Alice")
	v.dir.members[ug] = []string{user}
	v.ingest(dev("d1", "PC-1", "SN1"))
	d := v.holderOf("d1", user)
	v.mingest(true, intune.ManagementSnapshot{
		Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "gx")},
		Artifacts:   []intune.ArtifactRecord{art("a1", "ForUsers", grp("x1", "ug", "required"))},
	})
	calls := 0
	v.svc.WithViews(v.dir, countingHolders{v.holders, &calls}).WithProviderKey(v.provider)

	// With assets.view the User is known and the User's group applies.
	if e := v.status(v.full, d, application.DeviceManagementFilter{})["a1"].Expected; e.Result != application.ExpectedApplicable {
		t.Fatalf("with assets.view: %+v", e)
	}
	if calls == 0 {
		t.Fatal("holder should be loaded with assets.view")
	}
	calls = 0
	noAssets := v.full
	noAssets.AssetsView = false
	e := v.status(noAssets, d, application.DeviceManagementFilter{})["a1"].Expected
	if e.Result != application.ExpectedUnknown || !contains(e.Reasons, "user_unknown") {
		t.Errorf("without assets.view: %+v", e)
	}
	path, err := v.svc.AssignmentPath(ctx, noAssets, d, v.artifactID("a1"))
	if err != nil {
		t.Fatal(err)
	}
	if path.User != nil {
		t.Errorf("User leaked: %+v", path.User)
	}
	for _, s := range path.Path {
		if s.Origin == "user" {
			t.Errorf("user origin leaked in step %+v", s)
		}
	}
	tg, err := v.svc.ArtifactTargets(ctx, noAssets, v.artifactID("a1"))
	if err != nil || tg.Evaluation.Expected[application.ExpectedApplicable] != 0 {
		t.Errorf("targets without assets.view = %+v %v", tg.Evaluation, err)
	}
	if calls != 0 {
		t.Errorf("holders were loaded %d times without assets.view", calls)
	}
}

func TestUserViewDevicesTruncationCountsOnlyEndpointDevices(t *testing.T) {
	v := newViewEnv(t)
	g1 := v.orgGroup("g1")
	user := v.newUser("Alice")
	v.dir.members[g1] = []string{user}
	var devs []application.SnapshotDevice
	for i := 0; i < application.MaxUserViewDevices+1; i++ {
		devs = append(devs, dev(fmt.Sprintf("d%02d", i), fmt.Sprintf("PC-%02d", i), fmt.Sprintf("SN-%s-%02d", v.provider, i)))
	}
	v.ingest(devs...)
	v.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "P", grp("x1", "g1", "required"))}})
	for i := 0; i < 5; i++ {
		v.holderOf(fmt.Sprintf("d%02d", i), user)
	}
	// Assets that are not endpoint Devices (monitors, phones) must not count against the Device limit.
	for i := 0; i < 30; i++ {
		v.holders.held[v.newID()] = user
	}
	um, err := v.svc.UserManagement(context.Background(), v.full, user, application.Page{})
	if err != nil || um.DevicesTruncated || len(um.Items) != 1 || len(um.Items[0].Devices) != 5 {
		t.Fatalf("5 devices among many assets = truncated %v, %d devices, %v", um.DevicesTruncated, len(um.Items[0].Devices), err)
	}
	for i := 5; i < application.MaxUserViewDevices+1; i++ {
		v.holderOf(fmt.Sprintf("d%02d", i), user)
	}
	um, err = v.svc.UserManagement(context.Background(), v.full, user, application.Page{})
	if err != nil || !um.DevicesTruncated || len(um.Items[0].Devices) != application.MaxUserViewDevices {
		t.Fatalf("too many devices = truncated %v, %d devices, %v", um.DevicesTruncated, len(um.Items[0].Devices), err)
	}
}

func TestDeviceViewCursorOnlyWhenAnotherMatchExists(t *testing.T) {
	ctx := context.Background()
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"))
	arts := []intune.ArtifactRecord{art("a0", "A0", allDevices("x0")), art("a1", "A1", allDevices("x1")), art("a2", "A2", allDevices("x2"))}
	arts[1].Kind = "application" // not a configuration_profile
	v.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	d := v.device("d1").ID

	res, err := v.svc.DeviceManagement(ctx, v.full, d, application.DeviceManagementFilter{Page: application.Page{Limit: 3}})
	if err != nil || len(res.Items) != 3 || res.NextCursor != "" {
		t.Fatalf("exactly one full page: %d items, cursor %q, %v", len(res.Items), res.NextCursor, err)
	}
	res, err = v.svc.DeviceManagement(ctx, v.full, d, application.DeviceManagementFilter{Page: application.Page{Limit: 2}})
	if err != nil || len(res.Items) != 2 || res.NextCursor == "" {
		t.Fatalf("first of two pages: %d items, cursor %q, %v", len(res.Items), res.NextCursor, err)
	}
	next, err := v.svc.DeviceManagement(ctx, v.full, d, application.DeviceManagementFilter{Page: application.Page{Limit: 2, Cursor: res.NextCursor}})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatalf("second page: %d items, cursor %q, %v", len(next.Items), next.NextCursor, err)
	}
	// The kind filter is applied inside the index-driven branches: a limit smaller than the unfiltered
	// candidates still returns every matching artifact.
	only, err := v.svc.DeviceManagement(ctx, v.full, d, application.DeviceManagementFilter{Kind: "configuration_profile", Page: application.Page{Limit: 2}})
	if err != nil || len(only.Items) != 2 || only.NextCursor != "" {
		t.Fatalf("kind filter: %d items, cursor %q, %v", len(only.Items), only.NextCursor, err)
	}
}

func TestReadSnapshotIsConsistent(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "Before", "SN1"))
	id := e.device("d1").ID
	repo := repository.New(e.pool)
	ctx := context.Background()
	err := repo.ReadSnapshot(ctx, func(ctx context.Context) error {
		first, err := repo.GetDevice(ctx, id)
		if err != nil {
			return err
		}
		if _, err := e.pool.Exec(context.Background(), `UPDATE endpoints.devices SET name = 'After' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		second, err := repo.GetDevice(ctx, id)
		if err != nil {
			return err
		}
		if first.Name != "Before" || second.Name != "Before" {
			t.Errorf("snapshot saw %q then %q; both reads must see the same state", first.Name, second.Name)
		}
		// The snapshot is read only.
		if _, err := repo.GetDevice(ctx, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetDevice(ctx, id); err != nil || got.Name != "After" {
		t.Errorf("outside the snapshot = %q, %v", got.Name, err)
	}
}
