package repository_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// Tests for the F6 slice 2 management model and its ingestion.

func (e *env) mingest(complete bool, m intune.ManagementSnapshot) application.ManagementResult {
	e.t.Helper()
	res, err := e.mingestErr(complete, m)
	if err != nil {
		e.t.Fatalf("ingest management: %v", err)
	}
	return res
}

func (e *env) mingestErr(complete bool, m intune.ManagementSnapshot) (application.ManagementResult, error) {
	return e.svc.IngestManagement(context.Background(), e.caller(), e.manage, application.ManagementSnapshot{
		Provider: e.provider, Source: application.SourceSync, Complete: complete, ManagementSnapshot: m})
}

func art(id, name string, as ...intune.AssignmentRecord) intune.ArtifactRecord {
	return intune.ArtifactRecord{ExternalID: id, Kind: "configuration_profile", Name: name, Platform: "windows", AssignmentsKnown: true, Assignments: as}
}

func grp(id, group, intent string) intune.AssignmentRecord {
	return intune.AssignmentRecord{ProviderAssignmentID: id, TargetKind: "group", TargetGroupExternalID: group, Mode: "include", Intent: intent}
}

func obs(dev, artifact, state, raw string) intune.ObservationRecord {
	return intune.ObservationRecord{ExternalDeviceID: dev, ArtifactExternalID: artifact, State: state, RawStatus: raw}
}

func (e *env) artifactID(ext string) string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM endpoints.management_artifacts WHERE provider = $1 AND external_id = $2`, e.provider, ext).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) assignmentRows(artifactExt string) (current, closed int) {
	e.t.Helper()
	id := e.artifactID(artifactExt)
	return e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid AND valid_until IS NULL`, id),
		e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid AND valid_until IS NOT NULL`, id)
}

func (e *env) assignmentEvents() int {
	return e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ManagementAssignmentChanged'`, e.corr)
}

func (e *env) obsHistory(deviceID string) int {
	return e.count(`SELECT count(*) FROM endpoints.management_observation_history WHERE device_id = $1::uuid`, deviceID)
}

func (e *env) artifact(ext string) application.Artifact {
	e.t.Helper()
	a, err := repository.New(e.pool).GetArtifact(context.Background(), e.artifactID(ext))
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func TestManagementIngestIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	snap := intune.ManagementSnapshot{
		Filters:   []intune.FilterRecord{{ExternalID: "f1", Name: "Corporate", Platform: "windows", Rule: `(device.ownership -eq "Corporate")`}},
		Artifacts: []intune.ArtifactRecord{art("a1", "BitLocker", grp("x1", "g1", "none"), intune.AssignmentRecord{ProviderAssignmentID: "x2", TargetKind: "all_devices", Mode: "include", FilterExternalID: "f1", FilterMode: "include"})},
		Observations: []intune.ObservationRecord{
			obs("d1", "a1", "applied", "Succeeded"), obs("d2", "a1", "pending", "In progress"),
		},
		Memberships: []intune.DeviceGroupMembershipRecord{{ExternalDeviceID: "d1", GroupExternalID: "g1"}, {ExternalDeviceID: "d2", GroupExternalID: "g1"}},
	}
	first := e.mingest(true, snap)
	if first.FiltersCreated != 1 || first.ArtifactsCreated != 1 || first.AssignmentsOpened != 2 || first.ObservationsCreated != 2 || first.MembershipsOpened != 2 || first.AssignmentEvents != 1 {
		t.Fatalf("first = %+v", first)
	}
	if got := e.auditCount("endpoints.management_sync.completed", ""); got != 1 {
		t.Errorf("completed audit entries = %d", got)
	}
	a := e.artifact("a1")
	events := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
	rows := e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid`, a.ID)

	again := e.mingest(true, snap)
	if again.FiltersUnchanged != 1 || again.ArtifactsUnchanged != 1 || again.AssignmentsUnchanged != 2 || again.AssignmentsOpened != 0 || again.AssignmentsClosed != 0 ||
		again.ObservationsUnchanged != 2 || again.ObservationsChanged != 0 || again.MembershipsUnchanged != 2 || again.MembershipsOpened != 0 || again.AssignmentEvents != 0 {
		t.Fatalf("second = %+v", again)
	}
	if got := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1`, e.corr); got != events {
		t.Errorf("a repeated snapshot published %d new events", got-events)
	}
	after := e.artifact("a1")
	if after.Version != a.Version || !after.LastSyncedAt.After(a.LastSyncedAt) {
		t.Errorf("artifact version %d->%d, synced %v->%v: a repeat must refresh freshness only", a.Version, after.Version, a.LastSyncedAt, after.LastSyncedAt)
	}
	if got := e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid`, a.ID); got != rows {
		t.Errorf("assignment rows %d -> %d", rows, got)
	}
	d1 := e.device("d1")
	if e.obsHistory(d1.ID) != 1 {
		t.Errorf("observation history = %d, want 1", e.obsHistory(d1.ID))
	}
}

func TestAssignmentChangeClosesTheOldRowAndOpensANewOne(t *testing.T) {
	e := newEnv(t)
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"), grp("x2", "g2", "required"))}})
	if cur, closed := e.assignmentRows("a1"); cur != 2 || closed != 0 {
		t.Fatalf("rows = %d current, %d closed", cur, closed)
	}
	events := e.assignmentEvents()

	// x1 changes its intent, x2 vanishes, x3 is new: one event for the artifact.
	res := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "available"), grp("x3", "g3", "required"))}})
	if res.AssignmentsClosed != 2 || res.AssignmentsOpened != 2 || res.AssignmentEvents != 1 {
		t.Fatalf("change = %+v", res)
	}
	if cur, closed := e.assignmentRows("a1"); cur != 2 || closed != 2 {
		t.Errorf("rows = %d current, %d closed, want 2 and 2", cur, closed)
	}
	if got := e.assignmentEvents(); got != events+1 {
		t.Errorf("events %d -> %d, want exactly one more", events, got)
	}
	id := e.artifactID("a1")
	if e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid AND provider_assignment_id = 'x1' AND intent = 'required' AND valid_until IS NOT NULL`, id) != 1 ||
		e.count(`SELECT count(*) FROM endpoints.management_assignments WHERE artifact_id = $1::uuid AND provider_assignment_id = 'x1' AND intent = 'available' AND valid_until IS NULL`, id) != 1 {
		t.Error("the old x1 row must be closed and a new current one opened")
	}
	// The same snapshot again writes nothing.
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "available"), grp("x3", "g3", "required"))}})
	if cur, closed := e.assignmentRows("a1"); cur != 2 || closed != 2 || e.assignmentEvents() != events+1 {
		t.Errorf("unchanged re-read changed rows (%d/%d) or events", cur, closed)
	}
	// The detail read shows current rows first and flags closed ones.
	d, err := e.svc.GetArtifact(context.Background(), e.view, id)
	if !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("endpoints.view alone must not read artifacts: %v", err)
	}
	d, err = e.svc.GetArtifact(context.Background(), application.Principal{UserID: e.user, ManagementView: true}, id)
	if err != nil || len(d.Assignments) != 4 || !d.Assignments[0].Current() || d.Assignments[3].Current() {
		t.Fatalf("detail = %+v, %v", d.Assignments, err)
	}
}

func TestAssignmentsOfAnArtifactAreKeptWhenUnknownOrInvalid(t *testing.T) {
	e := newEnv(t)
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"))}})
	unknown := art("a1", "Policy")
	unknown.AssignmentsKnown = false
	badFilter := art("a1", "Policy", intune.AssignmentRecord{ProviderAssignmentID: "x1", TargetKind: "all_users", Mode: "include", FilterExternalID: "missing", FilterMode: "include"})
	badTarget := art("a1", "Policy", intune.AssignmentRecord{ProviderAssignmentID: "x1", TargetKind: "mystery", Mode: "include"})
	for name, a := range map[string]intune.ArtifactRecord{"unknown": unknown, "filter reference": badFilter, "target kind": badTarget} {
		res := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{a}})
		if res.AssignmentsClosed != 0 || res.AssignmentsOpened != 0 {
			t.Errorf("%s: previous assignments must stay, got %+v", name, res)
		}
		if name != "unknown" && res.AssignmentsRejected == 0 {
			t.Errorf("%s: rejection not counted", name)
		}
	}
	if cur, closed := e.assignmentRows("a1"); cur != 1 || closed != 0 {
		t.Errorf("rows = %d/%d", cur, closed)
	}
	// An explicit empty list is a real statement: the assignment is closed.
	res := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}})
	if res.AssignmentsClosed != 1 {
		t.Errorf("empty known list = %+v", res)
	}
}

func TestArtifactTombstonesAndGuards(t *testing.T) {
	e := newEnv(t)
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "One", grp("x1", "g1", "required")), art("a2", "Two", grp("x2", "g1", "required"))}})
	// An incomplete snapshot and an empty one tombstone nothing.
	e.mingest(false, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "One", grp("x1", "g1", "required"))}})
	e.mingest(true, intune.ManagementSnapshot{})
	if e.artifact("a2").DeletedObservedAt != nil {
		t.Fatal("an incomplete or empty snapshot tombstoned an artifact")
	}
	// A rejected artifact (unknown kind) the provider did report is not tombstoned.
	bad := art("a2", "Two")
	bad.Kind = "mystery"
	res := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "One", grp("x1", "g1", "required")), bad}})
	if res.ArtifactsRejected != 1 || res.ArtifactsTombstoned != 0 {
		t.Fatalf("rejected = %+v", res)
	}
	// A complete snapshot tombstones the missing one, closes its assignments and publishes one event.
	events := e.assignmentEvents()
	res = e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "One", grp("x1", "g1", "required"))}})
	if res.ArtifactsTombstoned != 1 || res.AssignmentsClosed != 1 || res.AssignmentEvents != 1 {
		t.Fatalf("tombstone = %+v", res)
	}
	if e.artifact("a2").DeletedObservedAt == nil || e.assignmentEvents() != events+1 {
		t.Error("a2 must be tombstoned with one event")
	}
	if cur, closed := e.assignmentRows("a2"); cur != 0 || closed != 1 {
		t.Errorf("a2 rows = %d/%d", cur, closed)
	}
	// The live list hides it, includeDeleted shows it.
	p := application.Principal{UserID: e.user, ManagementView: true}
	list, err := e.svc.ListArtifacts(context.Background(), p, application.ArtifactFilter{Query: "tw"})
	if err != nil || len(list.Items) != 0 {
		t.Errorf("live list = %v %v", list.Items, err)
	}
	list, err = e.svc.ListArtifacts(context.Background(), p, application.ArtifactFilter{Query: "tw", IncludeDeleted: true})
	if err != nil || len(list.Items) != 1 {
		t.Errorf("list with deleted = %v %v", list.Items, err)
	}
	// It comes back with its assignment.
	back := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "One", grp("x1", "g1", "required")), art("a2", "Two", grp("x2", "g1", "required"))}})
	if back.ArtifactsUpdated != 1 || back.AssignmentsOpened != 1 || e.artifact("a2").DeletedObservedAt != nil {
		t.Errorf("revival = %+v", back)
	}

	// Guard: more than half of more than 10 live artifacts missing means no tombstones.
	var many []intune.ArtifactRecord
	for i := 0; i < 14; i++ {
		many = append(many, art(fmt.Sprintf("m%02d", i), fmt.Sprintf("Many %02d", i)))
	}
	e.mingest(true, intune.ManagementSnapshot{Artifacts: many})
	res = e.mingest(true, intune.ManagementSnapshot{Artifacts: many[:3]})
	if res.ArtifactsTombstoned != 0 || res.ManagementTombstonesSkipped == 0 {
		t.Errorf("guard = %+v", res)
	}
}

func TestFiltersAreBoundedAndTombstoned(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x", application.MaxFilterRuleLength+1)
	res := e.mingest(true, intune.ManagementSnapshot{Filters: []intune.FilterRecord{
		{ExternalID: "f-ok", Name: "Ok", Platform: "windows", Rule: `(device.model -eq "X")`},
		{ExternalID: "f-long", Name: "Long", Rule: long},
		{ExternalID: "f-ctrl", Name: "Ctrl", Rule: "a\x00b"},
		{ExternalID: "f-hidden", Name: "Hid‮den", Rule: "r"},
		{ExternalID: "", Name: "No id"},
	}})
	if res.FiltersCreated != 2 || res.FiltersRejected != 3 {
		t.Fatalf("filters = %+v", res)
	}
	if got := e.count(`SELECT count(*) FROM endpoints.management_filters WHERE provider = $1 AND name = $2`, e.provider, application.PlaceholderArtifactName); got != 1 {
		t.Errorf("a filter with an invisible character in its name must get the placeholder, got %d", got)
	}
	if e.count(`SELECT count(*) FROM endpoints.management_filters WHERE provider = $1 AND external_id = 'f-long'`, e.provider) != 0 {
		t.Error("an overlong rule must reject the filter, not shorten it")
	}
	// The rejected-but-reported filter does not tombstone, a vanished one does.
	res = e.mingest(true, intune.ManagementSnapshot{Filters: []intune.FilterRecord{{ExternalID: "f-long", Name: "Long", Rule: long}}})
	if res.FiltersTombstoned != 2 {
		t.Errorf("tombstoned = %+v", res)
	}
	list, err := e.svc.ListManagementFilters(context.Background(), e.manage, application.FilterListFilter{IncludeDeleted: true, Query: "ok"})
	if err != nil || len(list.Items) != 1 || list.Items[0].DeletedObservedAt == nil {
		t.Errorf("filters = %v %v", list.Items, err)
	}
}

func TestArtifactNamesWithInvisibleCharactersGetAPlaceholder(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("n", 300)
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{
		art("a-bidi", "Evil‮txt"), art("a-zw", "Zero​width"), art("a-long", long), art("a-blank", "   "),
	}})
	for _, ext := range []string{"a-bidi", "a-zw", "a-long", "a-blank"} {
		if got := e.artifact(ext).Name; got != application.PlaceholderArtifactName {
			t.Errorf("%s name = %q", ext, got)
		}
	}
}

func TestObservationHistoryOnlyOnChange(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	d := e.device("d1")
	snap := func(state, raw string) intune.ManagementSnapshot {
		return intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}, Observations: []intune.ObservationRecord{obs("d1", "a1", state, raw)}}
	}
	e.mingest(true, snap("pending", "Waiting"))
	e.mingest(true, snap("pending", "Waiting"))
	if e.obsHistory(d.ID) != 1 {
		t.Fatalf("history after repeat = %d", e.obsHistory(d.ID))
	}
	res := e.mingest(true, snap("applied", "Waiting"))
	if res.ObservationsChanged != 1 || e.obsHistory(d.ID) != 2 {
		t.Fatalf("state change = %+v, history %d", res, e.obsHistory(d.ID))
	}
	e.mingest(true, snap("applied", "Succeeded"))
	if e.obsHistory(d.ID) != 3 {
		t.Errorf("raw status change must be history too, got %d", e.obsHistory(d.ID))
	}
	// An unknown state is stored as unknown, a long raw status is shortened.
	res = e.mingest(true, snap("weird", strings.Repeat("r", 500)))
	if res.ObservationsChanged != 1 {
		t.Fatalf("res = %+v", res)
	}
	o, err := e.svc.ListDeviceObservations(context.Background(), e.manage, d.ID, application.Page{})
	if err != nil || len(o.Items) != 1 || o.Items[0].NormalizedState != "unknown" || len(o.Items[0].RawStatus) != 200 {
		t.Fatalf("observations = %+v %v", o.Items, err)
	}
	if err := e.pool.QueryRow(context.Background(), `UPDATE endpoints.management_observation_history SET raw_status = 'x' WHERE device_id = $1::uuid RETURNING 1`, d.ID).Scan(new(int)); err == nil {
		t.Error("the observation history must be immutable")
	}
	if _, err := e.pool.Exec(context.Background(), `TRUNCATE endpoints.management_observation_history`); err == nil {
		t.Error("the observation history must not be truncatable")
	}
}

func TestObservationsForUnknownOrRemovedObjectsAreSkipped(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	res := e.mingest(true, intune.ManagementSnapshot{
		Artifacts: []intune.ArtifactRecord{art("a1", "Policy")},
		Observations: []intune.ObservationRecord{
			obs("d1", "a1", "applied", ""), obs("d1", "a1", "failed", ""), // duplicate pair
			obs("ghost", "a1", "applied", ""), obs("d1", "ghost", "applied", ""), obs("", "a1", "applied", ""),
		},
	})
	if res.ObservationsCreated != 1 || res.ObservationsSkipped != 4 {
		t.Errorf("res = %+v", res)
	}
}

func TestProviderReportedErrorFindingIsRaisedAndResolved(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	d := e.device("d1")
	snap := func(state string) intune.ManagementSnapshot {
		return intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}, Observations: []intune.ObservationRecord{obs("d1", "a1", state, "Error 0x87D1")}}
	}
	res := e.mingest(true, snap("failed"))
	if res.ProviderFindingsRaised != 1 || !e.openFindings(d.ID)[application.FindingProviderReportedError] {
		t.Fatalf("raised = %+v findings %v", res, e.openFindings(d.ID))
	}
	// Distinct from the data-quality kinds, and the detail carries counts, not provider text.
	var detail string
	if err := e.pool.QueryRow(context.Background(), `SELECT detail::text FROM endpoints.findings WHERE device_id = $1::uuid AND kind = 'provider_reported_error' AND status = 'open'`, d.ID).Scan(&detail); err != nil || strings.Contains(detail, "0x87D1") {
		t.Errorf("detail = %s, %v", detail, err)
	}
	// Repeating raises nothing; a conflict changes the detail without a new finding.
	if again := e.mingest(true, snap("failed")); again.ProviderFindingsRaised != 0 {
		t.Errorf("repeat raised %+v", again)
	}
	e.mingest(true, snap("conflict"))
	if !e.openFindings(d.ID)[application.FindingProviderReportedError] {
		t.Error("a conflict keeps the finding open")
	}
	res = e.mingest(true, snap("applied"))
	if res.ProviderFindingsResolved != 1 || e.openFindings(d.ID)[application.FindingProviderReportedError] {
		t.Errorf("resolved = %+v findings %v", res, e.openFindings(d.ID))
	}
	// A failure on an artifact that vanishes no longer counts.
	e.mingest(true, snap("failed"))
	if !e.openFindings(d.ID)[application.FindingProviderReportedError] {
		t.Fatal("finding not raised again")
	}
	other := art("a2", "Other")
	res = e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{other}})
	if res.ArtifactsTombstoned != 1 || res.ProviderFindingsResolved != 1 || e.openFindings(d.ID)[application.FindingProviderReportedError] {
		t.Errorf("tombstoned artifact = %+v findings %v", res, e.openFindings(d.ID))
	}
	// The finding list filters by the new kind.
	if _, err := e.svc.ListFindings(context.Background(), e.view, application.FindingFilter{Kind: application.FindingProviderReportedError}); err != nil {
		t.Errorf("list by kind: %v", err)
	}
}

func TestMembershipsAreIntervals(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	m := func(d, g string) intune.DeviceGroupMembershipRecord {
		return intune.DeviceGroupMembershipRecord{ExternalDeviceID: d, GroupExternalID: g}
	}
	e.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{m("d1", "g1"), m("d2", "g1"), m("d1", "g1"), m("ghost", "g1")}})
	cur := func() int {
		return e.count(`SELECT count(*) FROM endpoints.device_group_memberships ms JOIN endpoints.devices d ON d.id = ms.device_id WHERE d.provider = $1 AND ms.observed_until IS NULL`, e.provider)
	}
	if cur() != 2 {
		t.Fatalf("current = %d", cur())
	}
	res := e.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{m("d1", "g1"), m("d1", "g2")}})
	if res.MembershipsOpened != 1 || res.MembershipsUnchanged != 1 || res.MembershipsClosed != 1 || cur() != 2 {
		t.Fatalf("res = %+v current %d", res, cur())
	}
	// An incomplete snapshot closes nothing; coming back opens a new interval.
	e.mingest(false, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{m("d2", "g9")}})
	if e.count(`SELECT count(*) FROM endpoints.device_group_memberships ms JOIN endpoints.devices d ON d.id = ms.device_id WHERE d.provider = $1 AND ms.group_external_id = 'g2' AND ms.observed_until IS NULL`, e.provider) != 1 {
		t.Error("an incomplete snapshot must not close memberships")
	}
	e.mingest(true, intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{m("d1", "g1"), m("d2", "g1")}})
	if e.count(`SELECT count(*) FROM endpoints.device_group_memberships ms JOIN endpoints.devices d ON d.id = ms.device_id WHERE d.provider = $1 AND ms.group_external_id = 'g1'`, e.provider) != 3 {
		t.Error("d2 rejoining g1 must open a new interval row")
	}
}

func TestApplicationArtifactLinksToASoftwareProductByAlias(t *testing.T) {
	e := newEnv(t)
	name := "Zip Tool " + e.provider
	if _, _, err := e.svc.RegisterSoftwareProduct(context.Background(), e.caller(), e.manage, name, "Acme", nil); err != nil {
		t.Fatal(err)
	}
	app := art("app1", strings.ToUpper(name))
	app.Kind = "application"
	profile := art("prof1", name) // not an application: never linked
	res := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{app, profile}})
	if res.ArtifactsLinked != 1 || e.artifact("app1").SoftwareProductID == nil || e.artifact("prof1").SoftwareProductID != nil {
		t.Errorf("res = %+v", res)
	}
	if again := e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{app, profile}}); again.ArtifactsLinked != 0 || again.ArtifactsUpdated != 0 {
		t.Errorf("repeat = %+v", again)
	}
}

func TestManagementIngestionSharesTheProviderLock(t *testing.T) {
	e := newEnv(t)
	repo := repository.New(e.pool)
	unlock, ok, err := repo.TryLockProvider(context.Background(), e.provider)
	if err != nil || !ok {
		t.Fatalf("lock = %v %v", ok, err)
	}
	_, err = e.mingestErr(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}})
	if !errors.Is(err, application.ErrSyncRunning) {
		t.Errorf("management ingestion during a device run = %v", err)
	}
	// And the other way round: a device run during a management run.
	unlock()
	unlock, ok, err = repo.TryLockProvider(context.Background(), e.provider)
	if err != nil || !ok {
		t.Fatal("relock failed")
	}
	_, err = e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Complete: true, Devices: e.many(1, "a")})
	if !errors.Is(err, application.ErrSyncRunning) {
		t.Errorf("device run during a management run = %v", err)
	}
	unlock()
}

func TestSyncRunsManagementAfterDevicesUnderOneLock(t *testing.T) {
	e := newEnv(t)
	repo := repository.New(e.pool)
	fake := intune.NewFake()
	fake.SetDevices(intune.DeviceRecord{ExternalID: "d1", Name: "PC-1", SerialNumber: "SN1", OSPlatform: "windows"})
	fake.SetManagement(intune.ManagementSnapshot{
		Artifacts:    []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"))},
		Observations: []intune.ObservationRecord{obs("d1", "a1", "applied", "ok")},
		Memberships:  []intune.DeviceGroupMembershipRecord{{ExternalDeviceID: "d1", GroupExternalID: "g1"}},
	})
	// Sync is bound to the intune provider key; hold that lock to prove a sync cannot interleave.
	svc := application.NewService(repo, e.assets, fake, true, nil)
	unlock, ok, err := repo.TryLockProvider(context.Background(), intune.ProviderKey)
	if err != nil || !ok {
		t.Skip("the intune provider lock is held elsewhere")
	}
	if _, err := svc.Sync(context.Background(), e.caller(), e.manage); !errors.Is(err, application.ErrSyncRunning) {
		t.Errorf("sync with the lock held = %v", err)
	}
	unlock()
	// A failing management read leaves the device result intact and is audited.
	fake.FailManagementWith(errors.New("boom"))
	defer func() {
		ctx := context.Background()
		_, _ = e.pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = 'intune' AND external_id = 'd1' AND name = 'PC-1'`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM endpoints.management_assignments WHERE artifact_id IN (SELECT id FROM endpoints.management_artifacts WHERE provider = 'intune' AND external_id = 'a1')`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM endpoints.management_artifacts WHERE provider = 'intune' AND external_id = 'a1'`)
	}()
	res, err := svc.Sync(context.Background(), e.caller(), e.manage)
	if err != nil || res.Management.ManagementErrors != 1 || res.DevicesCreated+res.DevicesUnchanged+res.DevicesUpdated != 1 {
		t.Fatalf("sync with failing management = %+v %v", res, err)
	}
	if e.auditCount("endpoints.management_sync.failed", "") != 1 {
		t.Error("a failed management read must be audited")
	}
	fake.FailManagementWith(nil)
	res, err = svc.Sync(context.Background(), e.caller(), e.manage)
	if err != nil || res.Management.ArtifactsCreated+res.Management.ArtifactsUnchanged != 1 || res.Management.ObservationsCreated+res.Management.ObservationsUnchanged != 1 {
		t.Fatalf("sync = %+v %v", res, err)
	}
}

func TestManagementReadsRequirePermissionsAndHideUnknownIDs(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}, Observations: []intune.ObservationRecord{obs("d1", "a1", "applied", "")}})
	ctx := context.Background()
	none := application.Principal{UserID: e.user}
	devicesOnly := application.Principal{UserID: e.user, View: true}
	mgmtOnly := application.Principal{UserID: e.user, ManagementView: true}
	both := application.Principal{UserID: e.user, View: true, ManagementView: true}
	aid, did := e.artifactID("a1"), e.device("d1").ID

	for name, p := range map[string]application.Principal{"none": none, "endpoints.view only": devicesOnly} {
		if _, err := e.svc.ListArtifacts(ctx, p, application.ArtifactFilter{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: list artifacts = %v", name, err)
		}
		if _, err := e.svc.GetArtifact(ctx, p, aid); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: get artifact = %v", name, err)
		}
		if _, err := e.svc.ListManagementFilters(ctx, p, application.FilterListFilter{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: list filters = %v", name, err)
		}
	}
	// Observations reveal the device: management access alone is not enough, nor is device access alone.
	for name, p := range map[string]application.Principal{"none": none, "devices only": devicesOnly, "management only": mgmtOnly} {
		if _, err := e.svc.ListDeviceObservations(ctx, p, did, application.Page{}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("%s: observations = %v", name, err)
		}
	}
	if o, err := e.svc.ListDeviceObservations(ctx, both, did, application.Page{}); err != nil || len(o.Items) != 1 {
		t.Errorf("both: observations = %v %v", o.Items, err)
	}
	if _, err := e.svc.ListDeviceObservations(ctx, e.manage, did, application.Page{}); err != nil {
		t.Errorf("manage reads observations: %v", err)
	}
	if _, err := e.svc.ListDeviceObservations(ctx, both, e.newID(), application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown device = %v", err)
	}
	if _, err := e.svc.GetArtifact(ctx, mgmtOnly, e.newID()); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown artifact = %v", err)
	}
	if _, err := e.svc.GetArtifact(ctx, mgmtOnly, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("bad artifact id = %v", err)
	}
	if _, err := e.svc.ListArtifacts(ctx, mgmtOnly, application.ArtifactFilter{Kind: "bogus"}); err == nil {
		t.Error("an unknown kind must be rejected")
	}
	if _, err := e.svc.ListArtifacts(ctx, mgmtOnly, application.ArtifactFilter{Page: application.Page{Cursor: "x"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor = %v", err)
	}
	// Writing needs endpoints.manage.
	if _, err := e.svc.IngestManagement(ctx, e.caller(), mgmtOnly, application.ManagementSnapshot{Provider: e.provider, Source: application.SourceSync}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("ingest without manage = %v", err)
	}
	d, err := e.svc.GetArtifact(ctx, mgmtOnly, aid)
	if err != nil || d.ObservationCounts["applied"] != 1 {
		t.Errorf("counts = %v %v", d.ObservationCounts, err)
	}
}

func TestManagementKeysetPagination(t *testing.T) {
	e := newEnv(t)
	var arts []intune.ArtifactRecord
	for i := 0; i < 5; i++ {
		arts = append(arts, art(fmt.Sprintf("p%d", i), fmt.Sprintf("Page %s %d", e.provider, i)))
	}
	e.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	p := application.Principal{UserID: e.user, ManagementView: true}
	var seen int
	cursor := ""
	for i := 0; i < 5; i++ {
		res, err := e.svc.ListArtifacts(context.Background(), p, application.ArtifactFilter{Query: "page " + e.provider, Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(res.Items)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if seen != 5 {
		t.Errorf("paged through %d artifacts, want 5", seen)
	}
}

func TestManagementRunsAreAuditedWithCountsOnly(t *testing.T) {
	e := newEnv(t)
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Secret Policy Name")}})
	var meta string
	if err := e.pool.QueryRow(context.Background(), `SELECT metadata::text FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.management_sync.completed'`, e.corr).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta, "Secret") || !strings.Contains(meta, `"artifactsCreated"`) {
		t.Errorf("audit metadata = %s", meta)
	}
	// An oversized snapshot is refused and the failure audited.
	big := make([]intune.ArtifactRecord, application.MaxSnapshotArtifacts+1)
	_, err := e.mingestErr(true, intune.ManagementSnapshot{Artifacts: big})
	var inv *application.InvalidInputError
	if !errors.As(err, &inv) || e.auditCount("endpoints.management_sync.failed", "") != 1 {
		t.Errorf("oversized = %v", err)
	}
	// An invalid provider is refused up front.
	if _, err := e.svc.IngestManagement(context.Background(), e.caller(), e.manage, application.ManagementSnapshot{Provider: "Bad Provider", Source: application.SourceSync}); !errors.As(err, &inv) {
		t.Errorf("bad provider = %v", err)
	}
}

func TestManagementSchemaConstraintsAndIndexes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}})
	id := e.artifactID("a1")
	for name, sql := range map[string]string{
		"group target without group id": `INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, mode, source, observed_at, last_synced_at, valid_from) VALUES ($1::uuid, 'z', 'group', 'include', 'sync', now(), now(), now())`,
		"all_devices with group id":     `INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, target_group_external_id, mode, source, observed_at, last_synced_at, valid_from) VALUES ($1::uuid, 'z', 'all_devices', 'g', 'include', 'sync', now(), now(), now())`,
		"filter mode without filter":    `INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, mode, filter_mode, source, observed_at, last_synced_at, valid_from) VALUES ($1::uuid, 'z', 'all_users', 'include', 'include', 'sync', now(), now(), now())`,
		"closed before opened":          `INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, mode, source, observed_at, last_synced_at, valid_from, valid_until) VALUES ($1::uuid, 'z', 'all_users', 'include', 'sync', now(), now(), now(), now() - interval '1 day')`,
		"bad intent":                    `INSERT INTO endpoints.management_assignments (artifact_id, provider_assignment_id, target_kind, mode, intent, source, observed_at, last_synced_at, valid_from) VALUES ($1::uuid, 'z', 'all_users', 'include', 'bogus', 'sync', now(), now(), now())`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id); err == nil {
			t.Errorf("%s: the constraint did not hold", name)
		}
	}
	for _, idx := range []string{"management_assignments_group_idx", "management_assignments_artifact_idx", "management_assignments_current_unique", "device_group_memberships_group_idx", "management_observations_device_idx"} {
		if e.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'endpoints' AND indexname = $1`, idx) != 1 {
			t.Errorf("missing index %s", idx)
		}
	}
}
