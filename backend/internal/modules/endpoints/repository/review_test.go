package repository_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
)

// Tests for the F6 slice 1 review findings (docs/product/f6-endpoint-intelligence-design.md).

func (e *env) auditCount(action string, extra string, args ...any) int {
	e.t.Helper()
	q := `SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2` + extra
	return e.count(q, append([]any{e.corr, action}, args...)...)
}

func (e *env) many(n int, prefix string) []application.SnapshotDevice {
	out := make([]application.SnapshotDevice, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, dev(fmt.Sprintf("%s%02d", prefix, i), fmt.Sprintf("PC-%s%02d", prefix, i), fmt.Sprintf("SN-%s-%s%02d", e.provider, prefix, i)))
	}
	return out
}

func (e *env) ingestIncomplete(devs ...application.SnapshotDevice) application.IngestResult {
	e.t.Helper()
	res, err := e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Devices: devs})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// A. one run per provider at a time; freshness never moves backwards.
func TestSecondRunForTheSameProviderIsRefused(t *testing.T) {
	e := newEnv(t)
	repo := repository.New(e.pool)
	unlock, ok, err := repo.TryLockProvider(context.Background(), e.provider)
	if err != nil || !ok {
		t.Fatalf("lock = %v %v", ok, err)
	}
	_, err = e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Complete: true, Devices: e.many(1, "a")})
	if !errors.Is(err, application.ErrSyncRunning) || !errors.Is(err, application.ErrConflict) {
		t.Fatalf("concurrent run = %v", err)
	}
	// Another provider is not blocked.
	unlockOther, ok2, err := repo.TryLockProvider(context.Background(), e.provider+"x")
	if err != nil || !ok2 {
		t.Fatalf("other provider lock = %v %v", ok2, err)
	}
	unlockOther()
	unlock()
	if _, err := e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Complete: true, Devices: e.many(1, "a")}); err != nil {
		t.Fatalf("run after unlock = %v", err)
	}
}

func TestFreshnessNeverMovesBackwards(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	first := e.device("d1").LastSyncedAt
	old := application.NewService(repository.New(e.pool), e.assets, nil, true, func() time.Time { return first.Add(-time.Hour) })
	changed := dev("d1", "PC-1", "SN1")
	changed.Record.ComplianceState = "noncompliant"
	if _, err := old.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Devices: []application.SnapshotDevice{changed}}); err != nil {
		t.Fatal(err)
	}
	if got := e.device("d1").LastSyncedAt; got.Before(first) {
		t.Errorf("last_synced_at moved back: %v < %v", got, first)
	}
}

// B. partial snapshots.
func TestRejectedRecordWithValidIDIsNotTombstoned(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	bad := dev("d2", "PC-2", "SN2‮")
	res := e.ingest(dev("d1", "PC-1", "SN1"), bad)
	if res.DevicesRejected != 1 || res.DevicesTombstoned != 0 || e.device("d2").DeletedObservedAt != nil {
		t.Fatalf("res = %+v d2 = %+v", res, e.device("d2"))
	}
}

func TestIncompleteSnapshotNeverTombstones(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	res := e.ingestIncomplete(dev("d1", "PC-1", "SN1"))
	if res.DevicesTombstoned != 0 || e.device("d2").DeletedObservedAt != nil {
		t.Fatalf("incomplete snapshot tombstoned: %+v", res)
	}
}

func TestTombstoneGuardRefusesMassTombstoneButAppliesUpdates(t *testing.T) {
	e := newEnv(t)
	devs := e.many(12, "g")
	e.ingest(devs...)
	changed := devs[0]
	changed.Record.ComplianceState = "noncompliant"
	res := e.ingest(changed, devs[1])
	if res.DevicesTombstoned != 0 || res.TombstonesSkipped != 10 || res.DevicesUpdated != 1 {
		t.Fatalf("guarded run = %+v", res)
	}
	if got := e.count(`SELECT count(*) FROM endpoints.devices WHERE provider = $1 AND deleted_observed_at IS NOT NULL`, e.provider); got != 0 {
		t.Errorf("%d devices tombstoned despite the guard", got)
	}
	if e.auditCount("endpoints.sync.completed", ` AND (metadata->>'tombstonesSkipped')::int = 10`) != 1 {
		t.Error("tombstonesSkipped missing from the audit entry")
	}
	// Dropping a minority is fine.
	res = e.ingest(devs[:8]...)
	if res.DevicesTombstoned != 4 || res.TombstonesSkipped != 0 {
		t.Fatalf("minority run = %+v", res)
	}
}

// C. one live device per Asset; tombstone and revival.
func TestAssetBelongsToOneLiveDevice(t *testing.T) {
	e := newEnv(t)
	asset := e.newID()
	e.assets.bySerial["sn1"] = asset
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	d1, d2 := e.device("d1"), e.device("d2")
	if d1.AssetID == nil {
		t.Fatal("d1 should be linked")
	}
	// The database refuses a second live device on the Asset.
	err := repository.New(e.pool).InTx(context.Background(), func(tx pgx.Tx) error {
		d2.AssetID, d2.AssetLinkSource = &asset, strp("manual")
		_, err := repository.New(e.pool).UpdateDeviceTx(context.Background(), tx, d2)
		return err
	})
	if !errors.Is(err, application.ErrConflict) {
		t.Fatalf("second live device on the asset = %v", err)
	}
}

func strp(s string) *string { return &s }

func TestTombstoneClearsSerialLinkKeepsManualAndRevalidatesOnRevival(t *testing.T) {
	e := newEnv(t)
	serialAsset, manualAsset := e.newID(), e.newID()
	e.assets.bySerial["sn1"] = serialAsset
	e.assets.existing[manualAsset] = true
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"), dev("d3", "PC-3", "SN3"))
	if _, err := e.link(e.manage, e.device("d2").ID, manualAsset, "correction"); err != nil {
		t.Fatal(err)
	}
	if e.device("d1").AssetID == nil {
		t.Fatal("d1 should be linked by serial")
	}
	// Only d3 stays: d1 and d2 are tombstoned.
	res := e.ingest(dev("d3", "PC-3", "SN3"))
	if res.DevicesTombstoned != 2 {
		t.Fatalf("res = %+v", res)
	}
	if d := e.device("d1"); d.AssetID != nil || d.DeletedObservedAt == nil {
		t.Errorf("serial link must go with the tombstone: %+v", d)
	}
	if d := e.device("d2"); d.AssetID == nil || *d.AssetLinkSource != "manual" {
		t.Errorf("manual link must survive the tombstone: %+v", d)
	}
	if e.auditCount("endpoints.device.unlinked", ` AND target_id = $3 AND metadata->>'method' = 'tombstoned'`, e.device("d1").ID) != 1 {
		t.Error("tombstone unlink not audited")
	}
	if e.count(`SELECT count(*) FROM endpoints.device_observation_history WHERE device_id = $1::uuid`, e.device("d1").ID) != 2 {
		t.Error("tombstone left no history row")
	}
	// While d2 is gone another device takes its Asset; when d2 reappears the link is dropped.
	if _, err := e.link(e.manage, e.device("d3").ID, manualAsset, "correction"); err != nil {
		t.Fatalf("tombstoned device blocks the asset: %v", err)
	}
	e.ingest(dev("d2", "PC-2", "SN2"), dev("d3", "PC-3", "SN3"))
	if d := e.device("d2"); d.AssetID != nil || d.DeletedObservedAt != nil {
		t.Errorf("revived device kept a taken asset: %+v", d)
	}
	if e.auditCount("endpoints.device.unlinked", ` AND target_id = $3 AND metadata->>'method' = 'revived_asset_taken'`, e.device("d2").ID) != 1 {
		t.Error("revival conflict not audited")
	}
}

// D. installations.
func TestInstallationsAreBulkUpsertedBatchedAndTombstonedWithTheDevice(t *testing.T) {
	e := newEnv(t)
	var devs []application.SnapshotDevice
	for i := 0; i < 3; i++ {
		d := dev(fmt.Sprintf("s%d", i), fmt.Sprintf("PC-%d", i), fmt.Sprintf("SN-S%d", i))
		d.SoftwareKnown = true
		for j := 0; j < 1500; j++ {
			d.Software = append(d.Software, intune.SoftwareRecord{Name: fmt.Sprintf("Tool   %d Z%s", j, e.provider), Version: "1"})
		}
		devs = append(devs, d)
	}
	res := e.ingest(devs...)
	if res.SoftwareObserved != 4500 {
		t.Fatalf("res = %+v", res)
	}
	var key string
	if err := e.pool.QueryRow(context.Background(), `SELECT normalized_name FROM endpoints.software_installations WHERE device_id = $1::uuid AND raw_name LIKE 'Tool   7 Z%'`, e.device("s0").ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != strings.ToLower("Tool 7 Z"+e.provider) {
		t.Errorf("normalized_name = %q", key)
	}
	// s2 vanishes: its installations go with it.
	e.ingest(devs[:2]...)
	if e.device("s2").DeletedObservedAt == nil {
		t.Fatal("s2 not tombstoned")
	}
	if got := e.count(`SELECT count(*) FROM endpoints.software_installations WHERE device_id = $1::uuid AND deleted_observed_at IS NULL`, e.device("s2").ID); got != 0 {
		t.Errorf("live installations of a tombstoned device = %d", got)
	}
}

// E. a reappearing device is a change; a touch records the source.
func TestReappearingDeviceIsAChange(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	e.ingest(dev("d1", "PC-1", "SN1"))
	gone := e.device("d2")
	res := e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	back := e.device("d2")
	if res.DevicesUpdated != 1 || back.DeletedObservedAt != nil || back.Version != gone.Version+1 {
		t.Fatalf("res = %+v back = %+v", res, back)
	}
	if e.history(back.ID) != 3 { // created, tombstoned, revived
		t.Errorf("history rows = %d", e.history(back.ID))
	}

	imp, err := e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceImport, Devices: []application.SnapshotDevice{dev("d1", "PC-1", "SN1")}})
	if err != nil || imp.DevicesUnchanged != 1 {
		t.Fatalf("import = %+v %v", imp, err)
	}
	if got := e.device("d1").Source; got != "import" {
		t.Errorf("touch kept source %q", got)
	}
}

// F. a serial change that drops a serial link is audited and published.
func TestSerialChangeUnlinkIsAuditedAndPublished(t *testing.T) {
	e := newEnv(t)
	e.assets.bySerial["sn1"] = e.newID()
	e.ingest(dev("d1", "PC-1", "SN1"))
	e.ingest(dev("d1", "PC-1", "SN-NEW"))
	id := e.device("d1").ID
	if e.auditCount("endpoints.device.unlinked", ` AND target_id = $3 AND metadata->>'method' = 'serial_changed'`, id) != 1 {
		t.Error("serial_changed unlink not audited")
	}
	if got := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'DeviceUnlinked' AND payload->>'deviceId' = $2`, e.corr, id); got != 1 {
		t.Errorf("DeviceUnlinked events = %d", got)
	}
}

// G. indexes and the truncate guard.
func TestHistoryCannotBeTruncatedAndIndexesExist(t *testing.T) {
	e := newEnv(t)
	if _, err := e.pool.Exec(context.Background(), `TRUNCATE endpoints.device_observation_history`); err == nil {
		t.Error("history truncate succeeded")
	}
	for _, idx := range []string{"findings_status_id_idx", "devices_serial_prefix_idx", "devices_serial_idx", "devices_asset_live_unique"} {
		if e.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = 'endpoints' AND indexname = $1`, idx) != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
}

// H. finding detail follows the data.
func TestFindingDetailIsRefreshed(t *testing.T) {
	e := newEnv(t)
	d := dev("d1", "PC-1", "SN1")
	d.SoftwareKnown = true
	d.Software = []intune.SoftwareRecord{{Name: "Alpha " + e.provider, Version: "1"}}
	e.ingest(d)
	d.Software = append(d.Software, intune.SoftwareRecord{Name: "Beta " + e.provider, Version: "1"})
	res := e.ingest(d)
	if res.FindingsRaised != 0 {
		t.Fatalf("res = %+v", res)
	}
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT (detail->>'count')::int FROM endpoints.findings WHERE device_id = $1::uuid AND kind = 'unmatched_software' AND status = 'open'`, e.device("d1").ID).Scan(&count); err != nil || count != 2 {
		t.Errorf("detail count = %d %v", count, err)
	}
}

// J. automatic linking is conservative.
func TestAutoLinkSkipsPersonalPlaceholderAndRetiredAssets(t *testing.T) {
	e := newEnv(t)
	byod, retired, normal := e.newID(), e.newID(), e.newID()
	e.assets.bySerial["sn-byod"], e.assets.bySerial["sn-old"], e.assets.bySerial["default string"], e.assets.bySerial["sn-ok"] = byod, retired, e.newID(), normal
	e.assets.status[retired] = "disposed"
	personal := dev("p1", "Phone", "SN-BYOD")
	personal.Record.Ownership = "personal"
	res := e.ingest(personal, dev("r1", "Old", "SN-OLD"), dev("h1", "PC-H1", "Default String"), dev("h2", "PC-H2", "Default String"), dev("n1", "PC-N", "SN-OK"))
	if res.DevicesLinked != 1 {
		t.Fatalf("res = %+v", res)
	}
	for _, id := range []string{"p1", "r1", "h1", "h2"} {
		if e.device(id).AssetID != nil {
			t.Errorf("%s was linked", id)
		}
	}
	if e.device("n1").AssetID == nil {
		t.Error("n1 should link")
	}
	// A disposed asset reads as no match, shared placeholder serials are not duplicates, personal devices raise nothing.
	if f := e.openFindings(e.device("r1").ID); !f["no_asset_match"] {
		t.Errorf("r1 findings = %v", f)
	}
	if f := e.openFindings(e.device("h1").ID); f["duplicate_device"] || !f["no_asset_match"] {
		t.Errorf("h1 findings = %v", f)
	}
	if f := e.openFindings(e.device("p1").ID); len(f) != 0 {
		t.Errorf("p1 findings = %v", f)
	}
}

func TestSharedSerialDropsExistingSerialLinks(t *testing.T) {
	e := newEnv(t)
	e.assets.bySerial["sn1"] = e.newID()
	e.ingest(dev("d1", "PC-1", "SN1"))
	if e.device("d1").AssetID == nil {
		t.Fatal("d1 should link first")
	}
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "sn1"))
	for _, id := range []string{"d1", "d2"} {
		if e.device(id).AssetID != nil || !e.openFindings(e.device(id).ID)["duplicate_device"] {
			t.Errorf("%s = %+v %v", id, e.device(id), e.openFindings(e.device(id).ID))
		}
	}
	if e.auditCount("endpoints.device.unlinked", ` AND target_id = $3 AND metadata->>'method' = 'serial_duplicate'`, e.device("d1").ID) != 1 {
		t.Error("duplicate unlink not audited")
	}
}

func TestManualLinkNeedsAssetsViewVersionAndAnActiveAsset(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	live, gone := e.newID(), e.newID()
	e.assets.existing[live], e.assets.existing[gone] = true, true
	e.assets.status[gone] = "lost"
	e.ingest(dev("d1", "PC-1", "SN1"))
	d := e.device("d1")
	noAssets := application.Principal{UserID: e.user, Manage: true}
	if _, err := e.svc.ManualLink(ctx, e.caller(), noAssets, d.ID, live, "correction", &d.Version); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("link without assets.view = %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := e.svc.ManualLink(ctx, e.caller(), e.manage, d.ID, live, "correction", nil); !errors.As(err, &inv) {
		t.Errorf("link without version = %v", err)
	}
	if _, err := e.svc.ManualUnlink(ctx, e.caller(), e.manage, d.ID, "correction", nil); !errors.As(err, &inv) {
		t.Errorf("unlink without version = %v", err)
	}
	if _, err := e.link(e.manage, d.ID, gone, "correction"); !errors.Is(err, application.ErrAssetInvalid) {
		t.Errorf("link to a lost asset = %v", err)
	}
}

// K. every run is audited; serial links are system changes triggered by a user.
func TestRunsAreAuditedAndSerialLinksUseTheSystemActor(t *testing.T) {
	e := newEnv(t)
	e.assets.bySerial["sn1"] = e.newID()
	e.ingest(dev("d1", "PC-1", "SN1"))
	var actor, triggered string
	if err := e.pool.QueryRow(context.Background(), `SELECT metadata->>'actor', metadata->>'triggeredBy' FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.device.linked'`, e.corr).Scan(&actor, &triggered); err != nil {
		t.Fatal(err)
	}
	if actor != "intune-sync" || triggered != e.user {
		t.Errorf("actor = %q triggeredBy = %q", actor, triggered)
	}
	if got := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.device.linked' AND actor_id IS NOT NULL`, e.corr); got != 0 {
		t.Errorf("serial link audited as a person: %d", got)
	}
	e.ingest() // an empty snapshot is still a recorded run
	if e.auditCount("endpoints.sync.completed", "") != 2 {
		t.Errorf("completed runs audited = %d", e.auditCount("endpoints.sync.completed", ""))
	}
}

// L. snapshot cap, recorded as a failed run.
func TestOversizedSnapshotIsRefusedAndAudited(t *testing.T) {
	e := newEnv(t)
	devs := make([]application.SnapshotDevice, application.MaxSnapshotDevices+1)
	_, err := e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Complete: true, Devices: devs})
	var inv *application.InvalidInputError
	if !errors.As(err, &inv) {
		t.Fatalf("oversized = %v", err)
	}
	if e.auditCount("endpoints.sync.failed", ` AND metadata->>'reason' = 'snapshot_too_large'`) != 1 {
		t.Error("failed run not audited")
	}
}
