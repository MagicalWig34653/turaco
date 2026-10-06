package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Tests for Target Sets and Deployment planning (F9 G2).

type fakeApprovals struct {
	mu        sync.Mutex
	newID     func() string
	requested map[string][]string // approval id -> excluded users
	subjects  map[string]string   // approval id -> deployment id
	cancelled []string
	viewers   map[string]bool // deployment id + user id
}

func (f *fakeApprovals) RequestInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, subjectID, _ string, a application.Approver, excluded []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.UserID != nil && slices.Contains(excluded, *a.UserID) {
		return "", application.ErrNoEligibleApprover
	}
	id := f.newID()
	f.requested[id], f.subjects[id] = excluded, subjectID
	return id, nil
}

func (f *fakeApprovals) CancelBySubjectInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, subjectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, subjectID)
	return nil
}

func (f *fakeApprovals) ForSubject(context.Context, string) ([]application.DeploymentApprovalInfo, error) {
	return nil, nil
}

func (f *fakeApprovals) CanView(_ context.Context, subjectID, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewers[subjectID+userID], nil
}

type fakeChanges map[string]application.ChangeWindow

func (f fakeChanges) Lookup(_ context.Context, ids []string) (map[string]application.ChangeWindow, error) {
	out := map[string]application.ChangeWindow{}
	for _, id := range ids {
		if c, ok := f[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

type fakeLocations map[string]string // asset id -> location id

func (f fakeLocations) Locations(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if l, ok := f[id]; ok {
			out[id] = l
		}
	}
	return out, nil
}

type depEnv struct {
	*sw
	dir       *fakeDir
	approvals *fakeApprovals
	changes   fakeChanges
	locations fakeLocations
	planner   application.Principal
	reader    application.Principal
	other     string
}

func newDepEnv(t *testing.T) *depEnv {
	t.Helper()
	s := newSW(t)
	d := &depEnv{sw: s, dir: &fakeDir{groups: map[string]application.DirectoryGroup{}, parents: map[string][]string{}, members: map[string][]string{},
		users: map[string]string{}, noIdentity: map[string]bool{}}, changes: fakeChanges{}, locations: fakeLocations{}, other: s.newID()}
	d.approvals = &fakeApprovals{newID: s.newID, requested: map[string][]string{}, subjects: map[string]string{}, viewers: map[string]bool{}}
	s.svc.WithViews(d.dir, fakeHolders{held: map[string]string{}}).WithDeployments(d.approvals, d.changes, d.locations)
	d.planner = application.Principal{UserID: s.user, DeploymentsManage: true, DeploymentsHighImpact: true, View: true}
	d.reader = application.Principal{UserID: s.user, DeploymentsView: true}
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_transitions WHERE deployment_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, s.user)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployment_rings WHERE deployment_id IN (SELECT id FROM endpoints.deployments WHERE created_by = $1)`, s.user)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.deployments WHERE created_by = $1`, s.user)
		_, _ = conn.Exec(ctx, `DELETE FROM endpoints.target_sets WHERE created_by = $1`, s.user)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
	return d
}

func (d *depEnv) suffix() string { return strings.TrimPrefix(d.corr, "endpoints-") }

func (d *depEnv) set(name string, def application.TargetDefinition) application.TargetSet {
	d.t.Helper()
	t, err := d.svc.CreateTargetSet(context.Background(), d.caller(), d.planner, application.TargetSetInput{Name: name + " " + d.suffix(), Definition: def})
	if err != nil {
		d.t.Fatalf("create target set: %v", err)
	}
	return t
}

func (d *depEnv) evaluate(p application.Principal, id string) application.TargetEvaluation {
	d.t.Helper()
	ev, err := d.svc.EvaluateTargetSet(context.Background(), p, id)
	if err != nil {
		d.t.Fatalf("evaluate: %v", err)
	}
	return ev
}

func device(ext, name, platform, osv, ownership, compliance, maker, model, serial string) application.SnapshotDevice {
	return application.SnapshotDevice{Record: intune.DeviceRecord{ExternalID: ext, Name: name, SerialNumber: serial, OSPlatform: platform, OSVersion: osv,
		Manufacturer: maker, Model: model, Ownership: ownership, ComplianceState: compliance}}
}

// fleet ingests four Devices: d1 (windows 11, Dell, compliant, at location A), d2 (windows 10, HP, noncompliant), d3 (macOS,
// personal) and d4 (windows 11, Dell, compliant, member of the child group).
func (d *depEnv) fleet(locA string) map[string]application.Device {
	d.t.Helper()
	assetA := d.newID()
	d.assets.bySerial["sn-"+d.suffix()+"-1"] = assetA
	d.locations[assetA] = locA
	d.ingest(
		device("d1", "PC-1", "windows", "10.0.22631", "corporate", "compliant", "Dell Inc.", "Latitude 7440", "sn-"+d.suffix()+"-1"),
		device("d2", "PC-2", "windows", "10.0.19045", "corporate", "noncompliant", "HP", "EliteBook", ""),
		device("d3", "MAC-3", "macos", "14.5", "personal", "compliant", "Apple", "MacBook Air", ""),
		device("d4", "PC-4", "windows", "10.0.22631", "corporate", "compliant", "Dell Inc.", "Latitude 7440", ""),
	)
	d.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{mship("d1", "parent"), mship("d4", "child")}})
	out := map[string]application.Device{}
	for _, ext := range []string{"d1", "d2", "d3", "d4"} {
		out[ext] = d.device(ext)
	}
	return out
}

func TestTargetSetEvaluationFiltersAndExplain(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	locA := d.newID()
	devs := d.fleet(locA)
	parent, child := d.newID(), d.newID()
	d.dir.groups[parent] = application.DirectoryGroup{ID: parent, ExternalID: "parent", Name: "Parent"}
	d.dir.groups[child] = application.DirectoryGroup{ID: child, ExternalID: "child", Name: "Child"}
	d.dir.parents[child] = []string{parent}
	if devs["d1"].AssetID == nil {
		t.Fatal("d1 should be linked to its asset")
	}

	cases := []struct {
		name string
		def  application.TargetDefinition
		want []string
	}{
		{"all devices", application.TargetDefinition{}, []string{"d1", "d2", "d3", "d4"}},
		{"platform", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}}, []string{"d3"}},
		{"os prefix", application.TargetDefinition{Filters: application.TargetFilters{OSVersionPrefix: "10.0.22"}}, []string{"d1", "d4"}},
		{"ownership", application.TargetDefinition{Filters: application.TargetFilters{Ownership: []string{"personal"}}}, []string{"d3"}},
		{"compliance", application.TargetDefinition{Filters: application.TargetFilters{Compliance: []string{"noncompliant"}}}, []string{"d2"}},
		{"manufacturer", application.TargetDefinition{Filters: application.TargetFilters{Manufacturer: []string{"dell inc."}}}, []string{"d1", "d4"}},
		{"model", application.TargetDefinition{Filters: application.TargetFilters{Model: []string{"EliteBook"}}}, []string{"d2"}},
		{"group direct", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "parent"}}}}, []string{"d1"}},
		{"group nested", application.TargetDefinition{Filters: application.TargetFilters{Groups: []application.TargetGroup{{ExternalID: "parent", IncludeNested: true}}}}, []string{"d1", "d4"}},
		{"location", application.TargetDefinition{Filters: application.TargetFilters{AssetLocationIDs: []string{locA}}}, []string{"d1"}},
		{"combined", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}, Compliance: []string{"compliant"}}}, []string{"d1", "d4"}},
		{"include", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}, IncludeDeviceIDs: []string{devs["d2"].ID}}, []string{"d2", "d3"}},
		{"include only", application.TargetDefinition{IncludeDeviceIDs: []string{devs["d4"].ID}}, []string{"d4"}},
		{"exclude", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}}, ExcludeDeviceIDs: []string{devs["d1"].ID}}, []string{"d2", "d4"}},
	}
	for _, tc := range cases {
		set := d.set(tc.name, tc.def)
		ev := d.evaluate(d.planner, set.ID)
		var want []string
		for _, ext := range tc.want {
			want = append(want, devs[ext].ID)
		}
		var got []string
		for _, e := range ev.Examples {
			got = append(got, e.DeviceID)
		}
		slices.Sort(want)
		slices.Sort(got)
		if ev.Matched != len(tc.want) || !slices.Equal(got, want) || ev.Truncated || ev.Incomplete {
			t.Errorf("%s: matched %d %v, want %v (%+v)", tc.name, ev.Matched, got, want, ev)
		}
		if set.AllDevices != (tc.name == "all devices") {
			t.Errorf("%s: allDevices = %v", tc.name, set.AllDevices)
		}
	}

	// Explain names every clause and why the Device is out.
	set := d.set("explain", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}, Compliance: []string{"compliant"},
		Groups: []application.TargetGroup{{ExternalID: "parent", IncludeNested: true}}}})
	ex, err := d.svc.ExplainTargetSet(ctx, d.planner, set.ID, devs["d2"].ID)
	if err != nil || ex.Matched || ex.DeviceName != "PC-2" {
		t.Fatalf("explain d2: %v %+v", err, ex)
	}
	results := map[string]string{}
	for _, c := range ex.Clauses {
		results[c.Clause] = c.Result + "/" + c.Reason
	}
	if results["platform"] != "matched/" || results["compliance"] != "failed/value_differs" || results["group"] != "failed/not_member" {
		t.Fatalf("clauses = %v", results)
	}
	if ex, err = d.svc.ExplainTargetSet(ctx, d.reader, set.ID, devs["d4"].ID); err != nil || !ex.Matched || !ex.Redacted || ex.DeviceName != "" {
		t.Fatalf("explain d4 without endpoints.view: %v %+v", err, ex)
	}
	if _, err := d.svc.ExplainTargetSet(ctx, d.planner, set.ID, d.newID()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}
	// Truncation of the nested lookup makes the result incomplete, not silently complete.
	d.dir.cutNesting = true
	if ev := d.evaluate(d.planner, set.ID); !ev.Incomplete {
		t.Fatalf("cut nesting not reported: %+v", ev)
	}
}

func TestTargetSetCapsRedactionPermissionsAndLifecycle(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	all := d.set("everything", application.TargetDefinition{})
	ev := d.evaluate(d.reader, all.ID)
	if ev.Matched != 4 || len(ev.Examples) != 4 || !ev.Examples[0].Redacted || ev.Examples[0].Name != "" || ev.ByPlatform["windows"] != 3 || ev.ByCompliance["noncompliant"] != 1 {
		t.Fatalf("evaluation without endpoints.view: %+v", ev)
	}
	d.svc.WithTargetCap(2)
	if ev := d.evaluate(d.planner, all.ID); ev.Matched != 2 || !ev.Truncated || ev.Examples[0].Name == "" {
		t.Fatalf("capped evaluation: %+v", ev)
	}

	// Permissions: no deployments permission reads nothing; deployments.view cannot change.
	none := application.Principal{UserID: d.user, View: true, Manage: true}
	if _, err := d.svc.ListTargetSets(ctx, none, application.TargetSetFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("list without permission: %v", err)
	}
	if _, err := d.svc.EvaluateTargetSet(ctx, none, all.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("evaluate without permission: %v", err)
	}
	if _, err := d.svc.CreateTargetSet(ctx, d.caller(), d.reader, application.TargetSetInput{Name: "x"}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("create with view: %v", err)
	}
	// Names are unique case-insensitively; the definition is validated.
	if _, err := d.svc.CreateTargetSet(ctx, d.caller(), d.planner, application.TargetSetInput{Name: strings.ToUpper("everything " + d.suffix())}); !errors.Is(err, application.ErrTargetSetNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := d.svc.CreateTargetSet(ctx, d.caller(), d.planner, application.TargetSetInput{Name: "bad " + d.suffix(),
		Definition: application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"amiga"}}}}); err == nil {
		t.Fatal("invalid platform accepted")
	}
	// Update needs the current version; archive retires the set.
	stale := all.Version
	upd, err := d.svc.UpdateTargetSet(ctx, d.caller(), d.planner, all.ID, &stale, application.TargetSetInput{Name: all.Name,
		Definition: application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}}})
	if err != nil || upd.AllDevices || upd.Version != all.Version+1 {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if _, err := d.svc.UpdateTargetSet(ctx, d.caller(), d.planner, all.ID, &stale, application.TargetSetInput{Name: all.Name}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	arch, err := d.svc.ArchiveTargetSet(ctx, d.caller(), d.planner, upd.ID, &upd.Version)
	if err != nil || arch.ArchivedAt == nil {
		t.Fatalf("archive: %v %+v", err, arch)
	}
	if _, err := d.svc.ArchiveTargetSet(ctx, d.caller(), d.planner, upd.ID, &arch.Version); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("archive twice: %v", err)
	}
	listed, err := d.svc.ListTargetSets(ctx, d.reader, application.TargetSetFilter{Page: application.Page{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range listed.Items {
		if s.ID == arch.ID {
			t.Fatal("archived set listed by default")
		}
	}
	if n := d.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action LIKE 'endpoints.target_set.%'`, all.ID); n != 3 {
		t.Fatalf("target set audits = %d", n)
	}
	if n := d.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'TargetSetChanged' AND correlation_id = $1`, d.corr); n != 3 {
		t.Fatalf("TargetSetChanged events = %d", n)
	}
	// Audit carries no names or filter values.
	var after string
	if err := d.pool.QueryRow(ctx, `SELECT after_data::text || metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'endpoints.target_set.updated'`, all.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "macos") || strings.Contains(after, "everything") {
		t.Fatalf("audit leaks values: %s", after)
	}
}

// plan creates a draft for an approved version with one pilot ring on set (no window).
func (d *depEnv) plan(versionID string, set application.TargetSet) (application.Deployment, application.DeploymentRing) {
	d.t.Helper()
	ctx := context.Background()
	dep, err := d.svc.CreateDeployment(ctx, d.caller(), d.planner, application.DeploymentInput{Name: "Rollout", SoftwareVersionID: versionID, Intent: application.IntentInstall})
	if err != nil {
		d.t.Fatalf("create deployment: %v", err)
	}
	dep, ring, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "Pilot", TargetSetID: set.ID,
		SuccessThresholdPercent: 90, NoWindowRequired: true})
	if err != nil {
		d.t.Fatalf("add pilot: %v", err)
	}
	return dep, ring
}

func issueCodes(issues []application.PlanIssue, blockingOnly bool) []string {
	var out []string
	for _, i := range issues {
		if !blockingOnly || i.Blocking {
			out = append(out, i.Code)
		}
	}
	slices.Sort(out)
	return out
}

func TestDeploymentPlanningLifecycleAndRingRules(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	_, v := d.approvedProductVersion("Plan App", hashA)
	pilotSet := d.set("pilot", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}})
	broadSet := d.set("broad", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}}})

	// An unapproved version is refused; uninstall needs deployments.high_impact.
	p2 := d.product("Unapproved App")
	unapproved := d.register(ver(p2.ID, hashB))
	if _, err := d.svc.CreateDeployment(ctx, d.caller(), d.planner, application.DeploymentInput{Name: "x", SoftwareVersionID: unapproved.ID, Intent: "install"}); gateCode(err) != application.IssueVersionNotApproved {
		t.Fatalf("unapproved version: %v", err)
	}
	manager := application.Principal{UserID: d.user, DeploymentsManage: true}
	if _, err := d.svc.CreateDeployment(ctx, d.caller(), manager, application.DeploymentInput{Name: "x", SoftwareVersionID: v.ID, Intent: "uninstall"}); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("uninstall without high impact: %v", err)
	}
	if _, err := d.svc.CreateDeployment(ctx, d.caller(), d.reader, application.DeploymentInput{Name: "x", SoftwareVersionID: v.ID, Intent: "install"}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("create with view: %v", err)
	}

	dep, pilot := d.plan(v.ID, pilotSet)
	if dep.Status != application.DeploymentDraft || dep.HighImpact || pilot.Position != 1 || pilot.MaxTargets != application.MaxTargetDevices {
		t.Fatalf("draft: %+v %+v", dep, pilot)
	}
	// Ring rules: only the pilot may run without a window; a ring needs a usable Change window.
	if _, _, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "Broad", TargetSetID: broadSet.ID,
		SuccessThresholdPercent: 95, NoWindowRequired: true}); err == nil {
		t.Fatal("no-window ring at position 2 accepted")
	}
	for _, bad := range []application.RingInput{
		{Name: "Broad", TargetSetID: broadSet.ID, SuccessThresholdPercent: 0},
		{Name: "Broad", TargetSetID: broadSet.ID, SuccessThresholdPercent: 101},
		{Name: "Broad", TargetSetID: broadSet.ID, SuccessThresholdPercent: 90, SoakMinutes: application.MaxSoakMinutes + 1},
		{Name: "Broad", TargetSetID: broadSet.ID, SuccessThresholdPercent: 90, MaxTargets: ptr(application.MaxTargetDevices + 1)},
		{Name: "", TargetSetID: broadSet.ID, SuccessThresholdPercent: 90},
	} {
		if _, _, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, bad); err == nil {
			t.Errorf("ring %+v accepted", bad)
		}
	}
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-time.Hour)
	draftChange, endedChange, okChange := d.newID(), d.newID(), d.newID()
	d.changes[draftChange] = application.ChangeWindow{ID: draftChange, Status: "draft", WindowStart: &future, WindowEnd: &future}
	d.changes[endedChange] = application.ChangeWindow{ID: endedChange, Status: "scheduled", WindowStart: &past, WindowEnd: &past}
	for _, id := range []string{draftChange, endedChange, d.newID()} {
		if _, _, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "Broad", TargetSetID: broadSet.ID,
			SuccessThresholdPercent: 95, ChangeID: &id}); gateCode(err) != application.IssueChangeWindowInvalid {
			t.Errorf("change %s: %v", id, err)
		}
	}
	start := time.Now().Add(24 * time.Hour)
	d.changes[okChange] = application.ChangeWindow{ID: okChange, Reference: "CHG-1", Status: "approved", WindowStart: &start, WindowEnd: &future}
	dep, broad, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "Broad", TargetSetID: broadSet.ID,
		SuccessThresholdPercent: 95, SoakMinutes: 60, ChangeID: &okChange, MaxTargets: ptr(2)})
	if err != nil || broad.Position != 2 {
		t.Fatalf("add broad: %v %+v", err, broad)
	}
	// The pilot keeps position 1 while it runs without a window.
	if _, err := d.svc.ReorderRings(ctx, d.caller(), d.planner, dep.ID, &dep.Version, []string{broad.ID, pilot.ID}); err == nil {
		t.Fatal("reorder moved the no-window pilot")
	}
	if _, err := d.svc.ReorderRings(ctx, d.caller(), d.planner, dep.ID, &dep.Version, []string{pilot.ID}); err == nil {
		t.Fatal("reorder with a missing ring accepted")
	}

	// Validation: the broad ring reaches 3 windows Devices but is capped at 2; the package is not published yet.
	val, err := d.svc.ValidateDeployment(ctx, d.planner, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := issueCodes(val.Issues, true); !slices.Equal(got, []string{application.IssueTooManyTargets}) || val.Valid || len(val.Rings) != 2 || val.Rings[1].Matched != 3 {
		t.Fatalf("validation = %+v", val)
	}
	if !slices.Contains(issueCodes(val.Issues, false), application.IssuePackageNotPublished) {
		t.Fatalf("missing package warning: %+v", val.Issues)
	}
	var plan *application.PlanInvalidError
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); !errors.As(err, &plan) {
		t.Fatalf("schedule invalid plan: %v", err)
	}
	dep, broad, err = d.svc.UpdateRing(ctx, d.caller(), d.planner, dep.ID, broad.ID, &dep.Version, application.RingInput{Name: "Broad", TargetSetID: broadSet.ID,
		SuccessThresholdPercent: 95, SoakMinutes: 60, ChangeID: &okChange})
	if err != nil || broad.MaxTargets != application.MaxTargetDevices {
		t.Fatalf("update ring: %v %+v", err, broad)
	}
	// Not high impact: scheduled straight from draft, bound to its plan hash.
	stale := dep.Version - 1
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale schedule: %v", err)
	}
	dep, err = d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version)
	if err != nil || dep.Status != application.DeploymentScheduled || dep.PlanSHA256 == nil || dep.ScheduledBy == nil {
		t.Fatalf("schedule: %v %+v", err, dep)
	}
	// A scheduled plan is frozen: rings cannot change and its Target Sets cannot change or be archived.
	if _, _, err := d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.RingInput{Name: "x", TargetSetID: broadSet.ID, SuccessThresholdPercent: 1}); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("ring on scheduled plan: %v", err)
	}
	if _, err := d.svc.UpdateTargetSet(ctx, d.caller(), d.planner, broadSet.ID, &broadSet.Version, application.TargetSetInput{Name: broadSet.Name}); !errors.Is(err, application.ErrTargetSetInUse) {
		t.Fatalf("update set in use: %v", err)
	}
	if _, err := d.svc.ArchiveTargetSet(ctx, d.caller(), d.planner, broadSet.ID, &broadSet.Version); !errors.Is(err, application.ErrTargetSetInUse) {
		t.Fatalf("archive set in use: %v", err)
	}
	if _, err := d.svc.CancelDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, "because"); err == nil {
		t.Fatal("free-text cancel reason accepted")
	}
	dep, err = d.svc.CancelDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, "no_longer_needed")
	if err != nil || dep.Status != application.DeploymentCancelled || dep.StatusReason == nil {
		t.Fatalf("cancel: %v %+v", err, dep)
	}
	detail, err := d.svc.GetDeployment(ctx, d.reader, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, tr := range detail.Transitions {
		ops = append(ops, tr.Operation)
	}
	if !slices.Equal(ops, []string{"created", "scheduled", "cancelled"}) || len(detail.Rings) != 2 || detail.Changes[okChange].Reference != "CHG-1" {
		t.Fatalf("detail = %v %+v", ops, detail)
	}
	for _, typ := range []string{application.EventDeploymentScheduled, application.EventDeploymentCancelled} {
		if n := d.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = $1 AND correlation_id = $2`, typ, d.corr); n != 1 {
			t.Errorf("%s events = %d", typ, n)
		}
	}
	if n := d.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action LIKE 'endpoints.deployment.%'`, dep.ID); n < 6 {
		t.Errorf("deployment audits = %d", n)
	}
}

func ptr(n int) *int { return &n }

func (d *depEnv) decide(dep application.Deployment, decision string) error {
	d.t.Helper()
	payload, _ := json.Marshal(map[string]string{"approvalId": *dep.ApprovalID, "subjectType": "deployment", "subjectId": dep.ID, "decision": decision})
	return d.repo().InTx(context.Background(), func(tx pgx.Tx) error {
		return d.svc.OnApprovalDecided(context.Background(), tx, events.OutboxEvent{EventType: "ApprovalDecided", CorrelationID: d.corr, Payload: payload})
	})
}

func (d *depEnv) get(id string) application.Deployment {
	d.t.Helper()
	got, err := d.svc.GetDeployment(context.Background(), d.reader, id)
	if err != nil {
		d.t.Fatal(err)
	}
	return got.Deployment
}

func TestDeploymentHighImpactNeedsPermissionAndApproval(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	_, v := d.approvedProductVersion("Fleet App", hashA)
	all := d.set("all", application.TargetDefinition{})
	manager := application.Principal{UserID: d.user, DeploymentsManage: true}

	dep, err := d.svc.CreateDeployment(ctx, d.caller(), manager, application.DeploymentInput{Name: "Everyone", SoftwareVersionID: v.ID, Intent: "install"})
	if err != nil {
		t.Fatal(err)
	}
	// An all-Devices ring needs deployments.high_impact.
	ring := application.RingInput{Name: "Pilot", TargetSetID: all.ID, SuccessThresholdPercent: 90, NoWindowRequired: true}
	if _, _, err := d.svc.AddRing(ctx, d.caller(), manager, dep.ID, &dep.Version, ring); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("all devices without high impact: %v", err)
	}
	dep, _, err = d.svc.AddRing(ctx, d.caller(), d.planner, dep.ID, &dep.Version, ring)
	if err != nil || !dep.HighImpact {
		t.Fatalf("all devices ring: %v %+v", err, dep)
	}
	// Scheduling needs the plan Approval first.
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); gateCode(err) != application.IssueApprovalRequired {
		t.Fatalf("schedule without approval: %v", err)
	}
	approver := d.approver
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), manager, dep.ID, &dep.Version, application.Approver{UserID: &approver}); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("submit without high impact: %v", err)
	}
	// Separation of duties: the planner (owner, creator, editor) is excluded.
	self := d.user
	if _, err := d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &self}); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("self approval: %v", err)
	}
	dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &approver})
	if err != nil || dep.Status != application.DeploymentPendingApproval || dep.ApprovalID == nil || dep.PlanSHA256 == nil {
		t.Fatalf("submit: %v %+v", err, dep)
	}
	if ex := d.approvals.requested[*dep.ApprovalID]; !slices.Contains(ex, d.user) {
		t.Fatalf("excluded = %v", ex)
	}
	// A decision about another approval is a permanent error; other subjects are ignored.
	wrong := dep
	other := d.newID()
	wrong.ApprovalID = &other
	if err := d.decide(wrong, "approve"); !events.IsPermanent(err) {
		t.Fatalf("foreign approval: %v", err)
	}
	// Rejected: back to draft with approval_rejected; the plan may be submitted again.
	if err := d.decide(dep, "reject"); err != nil {
		t.Fatal(err)
	}
	dep = d.get(dep.ID)
	if dep.Status != application.DeploymentDraft || dep.StatusReason == nil || *dep.StatusReason != application.ReasonApprovalRejected {
		t.Fatalf("after rejection: %+v", dep)
	}
	if err := d.decide(dep, "approve"); err != nil {
		t.Fatalf("stale decision must be ignored: %v", err)
	}
	if got := d.get(dep.ID); got.Status != application.DeploymentDraft {
		t.Fatalf("stale decision changed the plan: %+v", got)
	}
	dep, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.Approver{UserID: &approver})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.decide(dep, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := d.decide(dep, "approve"); err != nil {
		t.Fatalf("duplicate decision: %v", err)
	}
	dep = d.get(dep.ID)
	if dep.Status != application.DeploymentApproved || dep.ApprovedAt == nil {
		t.Fatalf("after approval: %+v", dep)
	}
	// Approved plans cannot be edited; scheduling needs high impact and keeps the approved hash.
	if _, err := d.svc.UpdateDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version, application.DeploymentInput{Name: "x", SoftwareVersionID: v.ID, Intent: "install"}); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("edit approved plan: %v", err)
	}
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), manager, dep.ID, &dep.Version); !errors.Is(err, application.ErrHighImpactForbidden) {
		t.Fatalf("schedule high impact without permission: %v", err)
	}
	hash := *dep.PlanSHA256
	dep, err = d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version)
	if err != nil || dep.Status != application.DeploymentScheduled || *dep.PlanSHA256 != hash {
		t.Fatalf("schedule approved plan: %v %+v", err, dep)
	}

	// Cancelling a pending plan cancels its Approval.
	dep2, _ := d.plan(v.ID, all)
	dep2, err = d.svc.SubmitDeployment(ctx, d.caller(), d.planner, dep2.ID, &dep2.Version, application.Approver{UserID: &approver})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.svc.CancelDeployment(ctx, d.caller(), d.planner, dep2.ID, &dep2.Version, "plan_error"); err != nil || !slices.Contains(d.approvals.cancelled, dep2.ID) {
		t.Fatalf("cancel pending: %v %v", err, d.approvals.cancelled)
	}
}

func TestDeploymentGatesFollowVersionAndPackageState(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	v, pk := d.packaged("Gate App")
	mac := d.set("mac", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"macos"}}})
	dep, _ := d.plan(v.ID, mac)
	if val, err := d.svc.ValidateDeployment(ctx, d.planner, dep.ID); err != nil || !val.Valid {
		t.Fatalf("valid plan: %v %+v", err, val)
	}
	// A provider-reported hash that differs from the approved hash closes the gate.
	d.fake.ReportHash(*pk.ProviderPackageID, hashB)
	d.sync()
	val, err := d.svc.ValidateDeployment(ctx, d.planner, dep.ID)
	if err != nil || !slices.Contains(issueCodes(val.Issues, true), application.IssuePackageGateClosed) {
		t.Fatalf("hash mismatch: %v %+v", err, val)
	}
	// A revoked version blocks too.
	cur, err := d.svc.GetSoftwareVersion(ctx, d.appr, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.svc.RevokeVersion(ctx, d.approverCaller(), d.appr, v.ID, "defect", &cur.Version.Version); err != nil {
		t.Fatal(err)
	}
	var plan *application.PlanInvalidError
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version); !errors.As(err, &plan) ||
		!slices.Contains(issueCodes(plan.Issues, true), application.IssueVersionNotApproved) {
		t.Fatalf("schedule revoked version: %v", err)
	}
	detail, err := d.svc.GetDeployment(ctx, d.reader, dep.ID)
	if err != nil || detail.Validation.Valid || detail.Validation.Evaluated {
		t.Fatalf("detail validation: %v %+v", err, detail.Validation)
	}
}

func TestDeploymentOverlapWarningAndScopedReads(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	d.fleet(d.newID())
	_, v := d.approvedProductVersion("Overlap App", hashA)
	win := d.set("win", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"windows"}}})
	first, _ := d.plan(v.ID, win)
	if _, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, first.ID, &first.Version); err != nil {
		t.Fatal(err)
	}
	second, _ := d.plan(v.ID, win)
	val, err := d.svc.ValidateDeployment(ctx, d.planner, second.ID)
	if err != nil || !val.Valid {
		t.Fatalf("overlap must not block: %v %+v", err, val)
	}
	found := false
	for _, i := range val.Issues {
		if i.Code == application.IssueOverlap && i.DeploymentID != nil && *i.DeploymentID == first.ID && *i.Count == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("overlap warning missing: %+v", val.Issues)
	}

	// Scoped reads: another User without deployments.view sees neither the plans nor their detail, unless they approve one.
	stranger := application.Principal{UserID: d.other}
	res, err := d.svc.ListDeployments(ctx, stranger, application.DeploymentFilter{})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("stranger list: %v %d", err, len(res.Items))
	}
	if _, err := d.svc.GetDeployment(ctx, stranger, first.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger detail: %v", err)
	}
	d.approvals.viewers[first.ID+d.other] = true
	if _, err := d.svc.GetDeployment(ctx, stranger, first.ID); err != nil {
		t.Fatalf("approver detail: %v", err)
	}
	own := application.Principal{UserID: d.user}
	mine, err := d.svc.ListDeployments(ctx, own, application.DeploymentFilter{VersionID: v.ID})
	if err != nil || len(mine.Items) != 2 {
		t.Fatalf("own list: %v %d", err, len(mine.Items))
	}
	if res, err := d.svc.ListDeployments(ctx, d.reader, application.DeploymentFilter{Status: "scheduled", VersionID: v.ID}); err != nil || len(res.Items) != 1 {
		t.Fatalf("filtered list: %v %+v", err, res)
	}
	if _, err := d.svc.ListDeployments(ctx, d.reader, application.DeploymentFilter{Status: "running"}); err == nil {
		t.Fatal("unknown status accepted")
	}
	if _, err := d.svc.ValidateDeployment(ctx, d.reader, first.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("validate with view: %v", err)
	}
}

func TestDeploymentTablesEnforceInvariants(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	_, v := d.approvedProductVersion("Invariant App", hashA)
	set := d.set("inv", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}})
	dep, ring := d.plan(v.ID, set)
	dep, err := d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &dep.Version)
	if err != nil {
		t.Fatal(err)
	}
	refused := []string{
		`UPDATE endpoints.deployments SET status = 'draft', scheduled_at = NULL, scheduled_by = NULL WHERE id = $1`,
		`UPDATE endpoints.deployments SET intent = 'uninstall' WHERE id = $1`,
		`UPDATE endpoints.deployments SET status = 'cancelled' WHERE id = $1`,
		`DELETE FROM endpoints.deployments WHERE id = $1`,
		`UPDATE endpoints.deployment_transitions SET reason = 'other' WHERE deployment_id = $1`,
		`DELETE FROM endpoints.deployment_transitions WHERE deployment_id = $1`,
		`UPDATE endpoints.deployment_rings SET soak_minutes = 5 WHERE deployment_id = $1`,
		`DELETE FROM endpoints.deployment_rings WHERE deployment_id = $1`,
	}
	for _, sql := range refused {
		if _, err := d.pool.Exec(ctx, sql, dep.ID); err == nil {
			t.Errorf("accepted: %s", sql)
		}
	}
	if _, err := d.pool.Exec(ctx, `TRUNCATE endpoints.deployment_transitions`); err == nil {
		t.Error("truncate accepted")
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM endpoints.target_sets WHERE id = $1`, set.ID); err == nil {
		t.Error("target set deleted")
	}
	// Per-status fields and ring rules hold for a draft too.
	draft, _ := d.plan(v.ID, set)
	for _, sql := range []string{
		`UPDATE endpoints.deployments SET status = 'pending_approval' WHERE id = $1`,
		`UPDATE endpoints.deployments SET status = 'approved' WHERE id = $1`,
		`UPDATE endpoints.deployment_rings SET position = 2 WHERE deployment_id = $1`,
		`UPDATE endpoints.deployment_rings SET change_id = uuidv7() WHERE deployment_id = $1`,
		`UPDATE endpoints.deployment_rings SET success_threshold_percent = 0 WHERE deployment_id = $1`,
	} {
		if _, err := d.pool.Exec(ctx, sql, draft.ID); err == nil {
			t.Errorf("accepted: %s", sql)
		}
	}
	_ = ring
}

func TestDeploymentConcurrentScheduleAndCancel(t *testing.T) {
	d := newDepEnv(t)
	ctx := context.Background()
	_, v := d.approvedProductVersion("Race App", hashA)
	set := d.set("race", application.TargetDefinition{Filters: application.TargetFilters{Platform: []string{"linux"}}})
	for round := 0; round < 3; round++ {
		dep, _ := d.plan(v.ID, set)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			ver := dep.Version
			_, errs[0] = d.svc.ScheduleDeployment(ctx, d.caller(), d.planner, dep.ID, &ver)
		}()
		go func() {
			defer wg.Done()
			ver := dep.Version
			_, errs[1] = d.svc.CancelDeployment(ctx, d.caller(), d.planner, dep.ID, &ver, "superseded")
		}()
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if !errors.Is(err, application.ErrVersionConflict) {
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
		if ok != 1 {
			t.Fatalf("round %d: %d operations succeeded (%v)", round, ok, errs)
		}
		got := d.get(dep.ID)
		if n := d.count(`SELECT count(*) FROM endpoints.deployment_transitions WHERE deployment_id = $1`, dep.ID); n != 2 || got.Version != dep.Version+1 {
			t.Fatalf("round %d: transitions %d, version %d", round, n, got.Version)
		}
	}
}
