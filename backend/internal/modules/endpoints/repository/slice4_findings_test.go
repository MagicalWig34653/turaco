package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Tests for the F6 slice 4 Turaco-derived finding and the device list filters.

// ineffectiveScenario: d1 and d2 are members of g1; artifact a1 targets g1; the provider reports a1 for d2 only.
type ineffectiveScenario struct {
	*viewEnv
	devs []application.SnapshotDevice
}

func newIneffectiveScenario(t *testing.T) *ineffectiveScenario {
	v := newViewEnv(t)
	return &ineffectiveScenario{viewEnv: v, devs: []application.SnapshotDevice{dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2")}}
}

// run advances the test clock, re-reads both devices (so inputs are fresh) and ingests the management snapshot.
func (s *ineffectiveScenario) run(advance time.Duration, d1State string, assignments ...intune.AssignmentRecord) application.ManagementResult {
	s.t.Helper()
	s.clock = s.clock.Add(advance)
	s.ingest(s.devs...)
	if len(assignments) == 0 {
		assignments = []intune.AssignmentRecord{grp("x1", "g1", "required")}
	}
	snap := intune.ManagementSnapshot{
		Memberships:  []intune.DeviceGroupMembershipRecord{mship("d1", "g1"), mship("d2", "g1")},
		Artifacts:    []intune.ArtifactRecord{art("a1", "Policy", assignments...), art("unreported", "Unreported", grp("u1", "g1", "required"))},
		Observations: []intune.ObservationRecord{obs("d2", "a1", "applied", "")},
	}
	if d1State != "" {
		snap.Observations = append(snap.Observations, obs("d1", "a1", d1State, ""))
	}
	return s.mingest(true, snap)
}

func (s *ineffectiveScenario) findingEvents() int {
	return s.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'EndpointFindingRaised'`, s.corr)
}

func TestAssignmentIneffectiveFindingIsRaisedResolvedAndIdempotent(t *testing.T) {
	s := newIneffectiveScenario(t)
	const week = 7 * 24 * time.Hour
	// Too young: the evidence is not older than a week.
	res := s.run(0, "")
	if res.IneffectiveFindingsRaised != 0 {
		t.Fatalf("raised too early: %+v", res)
	}
	d1, d2 := s.device("d1").ID, s.device("d2").ID

	// A week and a day later d1 is assigned and expected but the provider shows nothing for it. d2 is applied; the
	// artifact nobody reports (no observation on any device) says nothing about d1 either.
	res = s.run(week+24*time.Hour, "")
	if res.IneffectiveFindingsRaised != 1 || !s.openFindings(d1)[application.FindingAssignmentIneffective] || s.openFindings(d2)[application.FindingAssignmentIneffective] {
		t.Fatalf("not raised: %+v / %v / %v", res, s.openFindings(d1), s.openFindings(d2))
	}
	events := s.findingEvents()
	var detail string
	if err := s.pool.QueryRow(context.Background(), `SELECT detail::text FROM endpoints.findings WHERE device_id = $1::uuid AND kind = 'assignment_ineffective' AND status = 'open'`, d1).Scan(&detail); err != nil ||
		!strings.Contains(detail, `"absent": 1`) || strings.Contains(detail, "Policy") {
		t.Errorf("detail = %s, %v", detail, err)
	}
	// Idempotent: no second finding, no second event.
	res = s.run(time.Hour, "")
	if res.IneffectiveFindingsRaised != 0 || s.findingEvents() != events || s.count(`SELECT count(*) FROM endpoints.findings WHERE device_id = $1::uuid AND kind = 'assignment_ineffective'`, d1) != 1 {
		t.Errorf("repeat raised %+v events %d -> %d", res, events, s.findingEvents())
	}
	// Distinct from the provider's own statement: no provider_reported_error was raised.
	if s.openFindings(d1)[application.FindingProviderReportedError] {
		t.Error("ineffective must not be reported as provider error")
	}

	// The provider now applies it: resolved.
	res = s.run(time.Hour, "applied")
	if res.IneffectiveFindingsResolved != 1 || s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatalf("not resolved: %+v / %v", res, s.openFindings(d1))
	}

	// not_applicable counts only after it has been shown for a week.
	res = s.run(time.Hour, "not_applicable")
	if res.IneffectiveFindingsRaised != 0 {
		t.Errorf("not_applicable raised immediately: %+v", res)
	}
	res = s.run(week+time.Hour, "not_applicable")
	if res.IneffectiveFindingsRaised != 1 || !s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatalf("not_applicable not raised after a week: %+v", res)
	}
	// A provider error is its own kind and takes the Device out of ineffective.
	res = s.run(time.Hour, "failed")
	f := s.openFindings(d1)
	if f[application.FindingAssignmentIneffective] || !f[application.FindingProviderReportedError] || res.IneffectiveFindingsResolved != 1 {
		t.Errorf("failed = %+v / %v", res, f)
	}

	// Removing the assignment resolves it too.
	s.run(time.Hour, "not_applicable")
	s.run(week+time.Hour, "not_applicable")
	if !s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatal("finding not raised again")
	}
	res = s.run(time.Hour, "not_applicable", grp("x9", "g9", "required"))
	if s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Errorf("assignment gone but finding open: %+v", res)
	}
	// And a vanished device.
	s.run(time.Hour, "not_applicable")
	s.run(week+time.Hour, "not_applicable")
	if !s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatal("finding not raised a third time")
	}
	s.clock = s.clock.Add(time.Hour)
	// A vanished device: the device ingestion resolves its findings, the next management run keeps it that way.
	s.ingest(s.devs[1])
	if _, err := s.svc.IngestManagement(context.Background(), s.caller(), s.manage, application.ManagementSnapshot{Provider: s.provider, Source: application.SourceSync, Complete: true,
		ManagementSnapshot: intune.ManagementSnapshot{Memberships: []intune.DeviceGroupMembershipRecord{mship("d2", "g1")}, Artifacts: []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"))}}}); err != nil {
		t.Fatal(err)
	}
	if s.count(`SELECT count(*) FROM endpoints.findings WHERE device_id = $1::uuid AND kind = 'assignment_ineffective' AND status = 'open'`, d1) != 0 {
		t.Error("finding of a tombstoned device is still open")
	}
	// The kind is listable and validated.
	if _, err := s.svc.ListFindings(context.Background(), s.manage, application.FindingFilter{Kind: application.FindingAssignmentIneffective, Status: application.FindingResolved}); err != nil {
		t.Errorf("list by kind: %v", err)
	}
}

func TestAssignmentIneffectiveNeedsHighOrMediumConfidence(t *testing.T) {
	s := newIneffectiveScenario(t)
	const week = 7 * 24 * time.Hour
	s.run(0, "")
	// Stale inputs lower the confidence to low: the Device is not re-read, so nothing is raised.
	s.clock = s.clock.Add(week + 24*time.Hour)
	res := s.mingest(true, intune.ManagementSnapshot{
		Memberships:  []intune.DeviceGroupMembershipRecord{mship("d1", "g1"), mship("d2", "g1")},
		Artifacts:    []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"))},
		Observations: []intune.ObservationRecord{obs("d2", "a1", "applied", "")},
	})
	if res.IneffectiveFindingsRaised != 0 || s.openFindings(s.device("d1").ID)[application.FindingAssignmentIneffective] {
		t.Errorf("raised on stale device data: %+v", res)
	}
}

func TestIneffectiveReconcileOnlyRunsForTheViewProvider(t *testing.T) {
	s := newIneffectiveScenario(t)
	s.WithProviderKey("someone-else")
	const week = 7 * 24 * time.Hour
	s.run(0, "")
	res := s.run(week+24*time.Hour, "")
	if res.IneffectiveFindingsRaised != 0 {
		t.Errorf("raised for a provider the views do not evaluate: %+v", res)
	}
}

func (v *viewEnv) WithProviderKey(key string) { v.svc.WithProviderKey(key) }

func TestDeviceListFilters(t *testing.T) {
	e := newEnv(t)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-time.Hour)
	mk := func(id, name, serial, os string, checkin *time.Time) application.SnapshotDevice {
		d := dev(id, name, serial)
		d.Record.OSVersion = os
		d.Record.LastCheckinAt = checkin
		return d
	}
	e.ingest(mk("d1", "PC-1", "SN1", "10.0.19045", &old), mk("d2", "PC-2", "SN2", "10.0.22631", &recent), mk("d3", "PC-3", "SN3", "14.5", nil))
	e.mingest(true, intune.ManagementSnapshot{
		Artifacts: []intune.ArtifactRecord{art("a1", "P1"), art("a2", "P2")},
		Observations: []intune.ObservationRecord{
			obs("d1", "a1", "failed", ""), obs("d2", "a1", "pending", ""), obs("d2", "a2", "applied", ""), obs("d3", "a2", "conflict", ""),
		},
	})
	ctx := context.Background()
	p := application.Principal{UserID: e.user, View: true, ManagementView: true}
	names := func(f application.DeviceFilter) string {
		t.Helper()
		res, err := e.svc.ListDevices(ctx, p, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		var out []string
		for _, d := range res.Items {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		f    application.DeviceFilter
		want string
	}{
		{application.DeviceFilter{ManagementState: "failed"}, "PC-1"},
		{application.DeviceFilter{ManagementState: "pending"}, "PC-2"},
		{application.DeviceFilter{ManagementState: "conflict"}, "PC-3"},
		{application.DeviceFilter{OSVersionPrefix: "10.0."}, "PC-1,PC-2"},
		{application.DeviceFilter{OSVersionPrefix: "10.0.2"}, "PC-2"},
		{application.DeviceFilter{OSVersionPrefix: "10%"}, ""},
		{application.DeviceFilter{LastCheckinOlderThanDays: 7}, "PC-1"},
		{application.DeviceFilter{LastCheckinOlderThanDays: 60}, ""},
		{application.DeviceFilter{HasFinding: application.FindingProviderReportedError}, "PC-1,PC-3"},
		{application.DeviceFilter{HasFinding: application.FindingProviderReportedError, OSVersionPrefix: "14"}, "PC-3"},
		{application.DeviceFilter{HasFinding: application.FindingAssignmentIneffective}, ""},
	} {
		if got := names(c.f); got != c.want {
			t.Errorf("%+v = %q, want %q", c.f, got, c.want)
		}
	}
	// Retired observations and tombstoned artifacts no longer count for managementState.
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a2", "P2")}, Observations: []intune.ObservationRecord{obs("d2", "a2", "applied", "")}})
	if got := names(application.DeviceFilter{ManagementState: "failed"}); got != "" {
		t.Errorf("tombstoned artifact still counts: %q", got)
	}
	// Validation and permissions: the observed state is management data.
	for _, f := range []application.DeviceFilter{{ManagementState: "applied"}, {HasFinding: "x"}, {LastCheckinOlderThanDays: -1}, {LastCheckinOlderThanDays: application.MaxCheckinDays + 1}} {
		if _, err := e.svc.ListDevices(ctx, p, f); err == nil {
			t.Errorf("%+v accepted", f)
		}
	}
	viewOnly := application.Principal{UserID: e.user, View: true}
	if _, err := e.svc.ListDevices(ctx, viewOnly, application.DeviceFilter{ManagementState: "failed"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("managementState without management access = %v", err)
	}
	if _, err := e.svc.ListDevices(ctx, viewOnly, application.DeviceFilter{OSVersionPrefix: "10"}); err != nil {
		t.Errorf("osVersion filter should work with endpoints.view: %v", err)
	}
}
