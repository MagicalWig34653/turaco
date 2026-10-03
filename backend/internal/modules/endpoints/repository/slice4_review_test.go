package repository_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// Tests for the F6 slice 4 review fixes.

const reviewWeek = 7 * 24 * time.Hour

// An observation the provider stopped reporting is retired; the Device is "absent" from then on, so the finding
// must wait a week from the retirement, not from the (older) assignment.
func TestIneffectiveAbsentClockRestartsWhenTheObservationIsRetired(t *testing.T) {
	s := newIneffectiveScenario(t)
	s.run(0, "applied")
	d1 := s.device("d1").ID
	// A week and a day later d1 is no longer reported (the complete snapshot omits it): retired just now.
	res := s.run(reviewWeek+24*time.Hour, "")
	if res.ObservationsRetired != 1 || res.IneffectiveFindingsRaised != 0 || s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatalf("raised on a freshly retired observation: %+v", res)
	}
	res = s.run(3*24*time.Hour, "")
	if res.IneffectiveFindingsRaised != 0 {
		t.Fatalf("raised after 3 days: %+v", res)
	}
	res = s.run(5*24*time.Hour, "")
	if res.IneffectiveFindingsRaised != 1 || !s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Fatalf("not raised a week after the retirement: %+v", res)
	}
}

// A retired observation that is reported again with the same state is a change: history gets a row, so the age of
// the state starts again (not_applicable must be shown for a week after it returned).
func TestReactivatedObservationWithTheSameStateAppendsHistory(t *testing.T) {
	s := newIneffectiveScenario(t)
	s.run(0, "not_applicable")
	d1 := s.device("d1").ID
	rows := func() int {
		return s.count(`SELECT count(*) FROM endpoints.management_observation_history WHERE device_id = $1::uuid`, d1)
	}
	if rows() != 1 {
		t.Fatalf("history rows = %d", rows())
	}
	s.run(reviewWeek+24*time.Hour, "") // retired
	res := s.run(time.Hour, "not_applicable")
	if rows() != 2 || res.ObservationsChanged < 1 {
		t.Errorf("rows = %d, res = %+v", rows(), res)
	}
	// not_applicable existed for more than a week before, but only for an hour since it returned.
	if res.IneffectiveFindingsRaised != 0 || s.openFindings(d1)[application.FindingAssignmentIneffective] {
		t.Errorf("raised on a state that just returned: %+v", res)
	}
	res = s.run(reviewWeek+time.Hour, "not_applicable")
	if res.IneffectiveFindingsRaised != 1 {
		t.Errorf("not raised a week after it returned: %+v", res)
	}
}

// The pass continues behind a rotating cursor, so Devices beyond the per-run cap are reached over successive runs.
func TestIneffectivePassRotatesThroughDevicesAcrossRuns(t *testing.T) {
	v := newViewEnv(t)
	v.svc.WithReconcileLimits(2, 0)
	devs := []application.SnapshotDevice{dev("rep", "REP", "SN0"), dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"), dev("d3", "PC-3", "SN3")}
	run := func(advance time.Duration) application.ManagementResult {
		v.clock = v.clock.Add(advance)
		v.ingest(devs...)
		return v.mingest(true, intune.ManagementSnapshot{
			Memberships:  []intune.DeviceGroupMembershipRecord{mship("d1", "g1"), mship("d2", "g1"), mship("d3", "g1")},
			Artifacts:    []intune.ArtifactRecord{art("a1", "Policy", grp("x1", "g1", "required"))},
			Observations: []intune.ObservationRecord{obs("rep", "a1", "applied", "")}, // the provider reports the artifact, but not for d1-d3
		})
	}
	open := func() int {
		return v.count(`SELECT count(*) FROM endpoints.findings f JOIN endpoints.devices d ON d.id = f.device_id
			WHERE d.provider = $1 AND f.kind = 'assignment_ineffective' AND f.status = 'open'`, v.provider)
	}
	run(0)
	first := run(reviewWeek + 24*time.Hour)
	afterFirst := open()
	if first.IneffectiveDevicesSkipped != 2 || afterFirst > 2 {
		t.Fatalf("first run: skipped %d, open findings %d", first.IneffectiveDevicesSkipped, afterFirst)
	}
	var cursor *string
	if err := v.pool.QueryRow(context.Background(), `SELECT ineffective_cursor::text FROM endpoints.provider_sync_state WHERE provider = $1`, v.provider).Scan(&cursor); err != nil || cursor == nil {
		t.Fatalf("cursor = %v, %v", cursor, err)
	}
	run(time.Minute)
	run(time.Minute)
	if got := open(); got != 3 {
		t.Errorf("open findings after the rotation = %d, want 3 (after first run %d)", got, afterFirst)
	}
}

// A failure of the derived-finding pass is audited on its own; the committed management run stays completed.
type failingCursorStore struct{ application.Store }

func (failingCursorStore) IneffectiveCursor(context.Context, string) (string, error) {
	return "", errors.New("boom")
}

func TestFindingReconcileFailureDoesNotFailTheRun(t *testing.T) {
	v := newViewEnv(t)
	v.svc = application.NewService(failingCursorStore{repository.New(v.pool)}, v.assets, nil, true, func() time.Time {
		v.clock = v.clock.Add(time.Second)
		return v.clock
	}).WithViews(v.dir, v.holders).WithProviderKey(v.provider)
	v.ingest(dev("d1", "PC-1", "SN1"))
	res := v.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}, Observations: []intune.ObservationRecord{obs("d1", "a1", "applied", "")}})
	if res.ObservationsCreated != 1 {
		t.Fatalf("res = %+v", res)
	}
	count := func(action string) int {
		return v.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2`, v.corr, action)
	}
	if count("endpoints.findings_reconcile.failed") != 1 || count("endpoints.management_sync.completed") != 1 || count("endpoints.management_sync.failed") != 0 {
		t.Errorf("audit: reconcile failed %d, completed %d, failed %d", count("endpoints.findings_reconcile.failed"), count("endpoints.management_sync.completed"), count("endpoints.management_sync.failed"))
	}
}

// Findings derived from management data need management access: the list filter, the findings list and the device detail.
func TestManagementFindingKindsNeedManagementAccess(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	d := e.device("d1")
	e.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{art("a1", "Policy")}, Observations: []intune.ObservationRecord{obs("d1", "a1", "failed", "E")}})
	ctx := context.Background()
	if !e.openFindingsAs(e.manage, d.ID)[application.FindingProviderReportedError] {
		t.Fatal("finding not raised")
	}
	if f := e.openFindingsAs(e.view, d.ID); f[application.FindingProviderReportedError] || !f[application.FindingNoAssetMatch] {
		t.Errorf("device detail without management access = %v", f)
	}
	for _, kind := range application.ManagementFindingKinds {
		if _, err := e.svc.ListFindings(ctx, e.view, application.FindingFilter{Kind: kind}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("list %s = %v", kind, err)
		}
		if _, err := e.svc.ListDevices(ctx, e.view, application.DeviceFilter{HasFinding: kind}); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("hasFinding %s = %v", kind, err)
		}
		if _, err := e.svc.ListDevices(ctx, e.manage, application.DeviceFilter{HasFinding: kind}); err != nil {
			t.Errorf("hasFinding %s with access = %v", kind, err)
		}
	}
	res, err := e.svc.ListFindings(ctx, e.view, application.FindingFilter{DeviceID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Items {
		if f.Kind == application.FindingProviderReportedError {
			t.Errorf("unfiltered list leaks %s", f.Kind)
		}
	}
	res, err = e.svc.ListFindings(ctx, e.manage, application.FindingFilter{DeviceID: d.ID, Kind: application.FindingProviderReportedError})
	if err != nil || len(res.Items) != 1 {
		t.Errorf("with access = %d, %v", len(res.Items), err)
	}
	if _, err := e.svc.ListDevices(ctx, e.view, application.DeviceFilter{HasFinding: application.FindingNoAssetMatch}); err != nil {
		t.Errorf("data-quality kind = %v", err)
	}
}

// The device history reads at most MaxHistoryArtifacts artifacts and says so.
func TestDeviceHistoryReportsScopeTruncation(t *testing.T) {
	v := newViewEnv(t)
	v.ingest(dev("d1", "PC-1", "SN1"))
	d1 := v.device("d1").ID
	var arts []intune.ArtifactRecord
	for i := 0; i < application.MaxHistoryArtifacts+1; i++ {
		arts = append(arts, art(fmt.Sprintf("a%d", i), fmt.Sprintf("A%d", i), allDevices(fmt.Sprintf("x%d", i))))
	}
	v.mingest(true, intune.ManagementSnapshot{Artifacts: arts})
	ctx := context.Background()
	res, err := v.svc.DeviceHistory(ctx, v.full, d1, application.Page{Limit: 100})
	if err != nil || !res.Truncated {
		t.Fatalf("truncated = %v, %v", res.Truncated, err)
	}
	seen := map[string]bool{}
	for _, it := range res.Items {
		if it.Artifact != nil {
			seen[it.Artifact.ID] = true
		}
	}
	if len(seen) > application.MaxHistoryArtifacts || len(res.Items) != 100 {
		t.Errorf("items %d, artifacts %d", len(res.Items), len(seen))
	}
	// A smaller scope is not truncated.
	v2 := newViewEnv(t)
	v2.ingest(dev("d1", "PC-1", "SN1"))
	v2.mingest(true, intune.ManagementSnapshot{Artifacts: arts[:3]})
	if res, err := v2.svc.DeviceHistory(ctx, v2.full, v2.device("d1").ID, application.Page{Limit: 100}); err != nil || res.Truncated || len(res.Items) != 3 {
		t.Errorf("small scope = %d truncated %v, %v", len(res.Items), res.Truncated, err)
	}
}

// The schema objects of migration 000042 that the review changed.
func TestSlice4MigrationObjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	idx := func(name string) string {
		var def string
		_ = e.pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'endpoints' AND indexname = $1`, name).Scan(&def)
		return def
	}
	if d := idx("management_assignments_chain_idx"); !strings.Contains(d, "(artifact_id, provider_assignment_id, valid_from, id)") {
		t.Errorf("chain index = %q", d)
	}
	if d := idx("management_assignments_history_kind_idx"); !strings.Contains(d, "all_devices") || strings.Contains(d, "<>") {
		t.Errorf("kind index = %q", d)
	}
	for _, gone := range []string{"devices_os_version_idx", "devices_last_checkin_idx"} {
		if idx(gone) != "" {
			t.Errorf("%s still exists", gone)
		}
	}
	var nullable string
	if err := e.pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema = 'endpoints' AND table_name = 'provider_sync_state' AND column_name = 'last_completed_at'`).Scan(&nullable); err != nil || nullable != "YES" {
		t.Errorf("last_completed_at nullable = %q, %v", nullable, err)
	}
	var validated bool
	if err := e.pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'findings_kind_check'`).Scan(&validated); err != nil || !validated {
		t.Errorf("findings_kind_check validated = %v, %v", validated, err)
	}
}

func TestGroupDiffRejectsMalformedOtherGroup(t *testing.T) {
	v := newViewEnv(t)
	g1 := v.orgGroup("g1")
	for _, other := range []string{"", "zz"} {
		_, err := v.svc.GroupDiff(context.Background(), v.full, g1, other, application.DiffFilter{})
		var inv *application.InvalidInputError
		if !errors.As(err, &inv) {
			t.Errorf("other group %q = %v", other, err)
		}
	}
}
