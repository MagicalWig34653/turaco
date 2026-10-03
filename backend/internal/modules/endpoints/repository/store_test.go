package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fakeAssets struct {
	bySerial  map[string]string // lower serial -> asset id
	ambiguous map[string]bool
	existing  map[string]bool
	status    map[string]string // asset id -> status (default "available")
}

func (f *fakeAssets) statusOf(id string) string {
	if st, ok := f.status[id]; ok {
		return st
	}
	return "available"
}

func (f *fakeAssets) FindBySerial(_ context.Context, serial string) (application.AssetInfo, error) {
	k := strings.ToLower(serial)
	if f.ambiguous[k] {
		return application.AssetInfo{}, application.ErrAssetAmbiguous
	}
	if id, ok := f.bySerial[k]; ok {
		return application.AssetInfo{ID: id, Status: f.statusOf(id)}, nil
	}
	return application.AssetInfo{}, application.ErrAssetNotFound
}

func (f *fakeAssets) ByID(_ context.Context, id string) (application.AssetInfo, bool, error) {
	return application.AssetInfo{ID: id, Status: f.statusOf(id)}, f.existing[id], nil
}

type env struct {
	t        *testing.T
	pool     *pgxpool.Pool
	svc      *application.Service
	assets   *fakeAssets
	provider string
	corr     string
	clock    time.Time
	manage   application.Principal
	view     application.Principal
	user     string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	e := &env{t: t, pool: pool, provider: "t" + suffix, corr: "endpoints-" + suffix, clock: time.Now().UTC().Add(-time.Hour),
		assets: &fakeAssets{bySerial: map[string]string{}, ambiguous: map[string]bool{}, existing: map[string]bool{}, status: map[string]string{}}}
	if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&e.user); err != nil {
		t.Fatal(err)
	}
	e.manage = application.Principal{UserID: e.user, Manage: true, AssetsView: true}
	e.view = application.Principal{UserID: e.user, View: true}
	e.svc = application.NewService(repository.New(pool), e.assets, nil, true, func() time.Time {
		e.clock = e.clock.Add(time.Second)
		return e.clock
	})
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		// Observations and memberships restrict the deletion of their device or artifact.
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_observations WHERE device_id IN (SELECT id FROM endpoints.devices WHERE provider = $1)`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.device_group_memberships WHERE provider = $1`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.devices WHERE provider = $1`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_assignments WHERE artifact_id IN (SELECT id FROM endpoints.management_artifacts WHERE provider = $1)`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_artifacts WHERE provider = $1`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.management_filters WHERE provider = $1`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.provider_sync_state WHERE provider = $1`, e.provider)
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.software_aliases WHERE alias LIKE $1`, "%"+suffix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM endpoints.software_products WHERE name LIKE $1`, "%"+suffix+"%")
	})
	return e
}

func (e *env) caller() application.Caller {
	return application.Caller{Actor: audit.UserActor(e.user), CorrelationID: e.corr}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) newID() string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) ingest(devs ...application.SnapshotDevice) application.IngestResult {
	e.t.Helper()
	res, err := e.svc.Ingest(context.Background(), e.caller(), e.manage, application.Snapshot{Provider: e.provider, Source: application.SourceSync, Complete: true, Devices: devs})
	if err != nil {
		e.t.Fatalf("ingest: %v", err)
	}
	return res
}

func dev(id, name, serial string) application.SnapshotDevice {
	return application.SnapshotDevice{Record: intune.DeviceRecord{
		ExternalID: id, Name: name, SerialNumber: serial, OSPlatform: "windows", OSVersion: "11", Manufacturer: "Acme", Model: "X1",
		Ownership: "corporate", ComplianceState: "compliant",
	}}
}

// link and unlink send the device's current version, as the API requires.
func (e *env) link(p application.Principal, deviceID, assetID, reason string) (application.Device, error) {
	e.t.Helper()
	d, err := repository.New(e.pool).GetDevice(context.Background(), deviceID)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.svc.ManualLink(context.Background(), e.caller(), p, deviceID, assetID, reason, &d.Version)
}

func (e *env) unlink(p application.Principal, deviceID, reason string) (application.Device, error) {
	e.t.Helper()
	d, err := repository.New(e.pool).GetDevice(context.Background(), deviceID)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.svc.ManualUnlink(context.Background(), e.caller(), p, deviceID, reason, &d.Version)
}

func (e *env) device(externalID string) application.Device {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id::text FROM endpoints.devices WHERE provider = $1 AND external_id = $2`, e.provider, externalID).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	d, err := repository.New(e.pool).GetDevice(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *env) openFindings(deviceID string) map[string]bool {
	e.t.Helper()
	return e.openFindingsAs(e.manage, deviceID)
}

func (e *env) openFindingsAs(p application.Principal, deviceID string) map[string]bool {
	e.t.Helper()
	d, err := e.svc.GetDevice(context.Background(), p, deviceID)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range d.Findings {
		out[f.Kind] = true
	}
	return out
}

func (e *env) history(deviceID string) int {
	return e.count(`SELECT count(*) FROM endpoints.device_observation_history WHERE device_id = $1::uuid`, deviceID)
}

func TestIngestIsIdempotentAndHistoryOnlyOnChange(t *testing.T) {
	e := newEnv(t)
	first := e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	if first.DevicesCreated != 2 || first.DevicesUpdated != 0 {
		t.Fatalf("first = %+v", first)
	}
	d1 := e.device("d1")
	if e.history(d1.ID) != 1 {
		t.Fatalf("history after create = %d", e.history(d1.ID))
	}
	events := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)

	again := e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	if again.DevicesCreated != 0 || again.DevicesUpdated != 0 || again.DevicesUnchanged != 2 || again.FindingsRaised != 0 {
		t.Fatalf("second = %+v", again)
	}
	if got := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1`, e.corr); got != events {
		t.Errorf("a repeated snapshot published %d new events", got-events)
	}
	after := e.device("d1")
	if after.Version != d1.Version || !after.LastSyncedAt.After(d1.LastSyncedAt) {
		t.Errorf("version %d->%d, synced %v->%v: a repeat must refresh freshness only", d1.Version, after.Version, d1.LastSyncedAt, after.LastSyncedAt)
	}
	if e.history(d1.ID) != 1 {
		t.Errorf("unchanged snapshot added history: %d", e.history(d1.ID))
	}

	// A new check-in time alone is not a meaningful change.
	checkin := dev("d1", "PC-1", "SN1")
	now := time.Now()
	checkin.Record.LastCheckinAt = &now
	if r := e.ingest(checkin, dev("d2", "PC-2", "SN2")); r.DevicesUpdated != 0 {
		t.Errorf("check-in only = %+v", r)
	}
	if e.history(d1.ID) != 1 {
		t.Errorf("check-in added history")
	}

	changed := dev("d1", "PC-1", "SN1")
	changed.Record.ComplianceState = "noncompliant"
	if r := e.ingest(changed, dev("d2", "PC-2", "SN2")); r.DevicesUpdated != 1 || r.DevicesUnchanged != 1 {
		t.Fatalf("changed = %+v", r)
	}
	if e.history(d1.ID) != 2 || e.device("d1").Version != d1.Version+1 {
		t.Errorf("history %d version %d", e.history(d1.ID), e.device("d1").Version)
	}
	if e.device("d1").ComplianceState != "noncompliant" || e.device("d1").Source != "sync" {
		t.Errorf("device = %+v", e.device("d1"))
	}
}

func TestHistoryIsAppendOnly(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"))
	d := e.device("d1")
	if _, err := e.pool.Exec(context.Background(), `UPDATE endpoints.device_observation_history SET name = 'x' WHERE device_id = $1::uuid`, d.ID); err == nil {
		t.Error("history update succeeded")
	}
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM endpoints.device_observation_history WHERE device_id = $1::uuid`, d.ID); err == nil {
		t.Error("history delete succeeded")
	}
}

func TestInvalidRecordsAreRejectedNotFatal(t *testing.T) {
	e := newEnv(t)
	bad := dev("", "no id", "SN")
	unsafeName := dev("d9", "name‮with bidi", "SN9")
	res := e.ingest(bad, unsafeName, dev("d1", "PC-1", "SN1"), dev("d1", "PC-1 again", "SN1"))
	if res.DevicesCreated != 2 || res.DevicesRejected != 2 {
		t.Fatalf("res = %+v", res)
	}
	if d := e.device("d9"); d.Name != application.PlaceholderDeviceName {
		t.Errorf("unsafe name was kept: %q", d.Name)
	}
	odd := dev("d2", "PC-2", "SN2")
	odd.Record.OSPlatform, odd.Record.Ownership, odd.Record.ComplianceState = "beos", "x", "y"
	e.ingest(odd)
	if d := e.device("d2"); d.OSPlatform != "other" || d.Ownership != "unknown" || d.ComplianceState != "unknown" {
		t.Errorf("unknown values not normalized: %+v", d)
	}
}

func TestMissingDevicesAreTombstonedAndComeBack(t *testing.T) {
	e := newEnv(t)
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	if res := e.ingest(dev("d1", "PC-1", "SN1")); res.DevicesTombstoned != 1 {
		t.Fatalf("res = %+v", res)
	}
	if e.device("d2").DeletedObservedAt == nil {
		t.Fatal("d2 was not tombstoned")
	}
	if e.device("d1").DeletedObservedAt != nil {
		t.Fatal("d1 was tombstoned")
	}
	live, err := e.svc.ListDevices(context.Background(), e.view, application.DeviceFilter{Query: "pc-", Page: application.Page{Limit: 200}})
	if err != nil || len(liveFor(live.Items, e.provider)) != 1 {
		t.Fatalf("live devices = %v %v", live.Items, err)
	}
	all, _ := e.svc.ListDevices(context.Background(), e.view, application.DeviceFilter{Query: "pc-", IncludeDeleted: true, Page: application.Page{Limit: 200}})
	if len(liveFor(all.Items, e.provider)) != 2 {
		t.Errorf("including deleted = %d", len(liveFor(all.Items, e.provider)))
	}

	// An empty snapshot is treated as a provider failure: nothing is tombstoned.
	if res := e.ingest(); res.DevicesTombstoned != 0 {
		t.Errorf("empty snapshot tombstoned %d", res.DevicesTombstoned)
	}
	if e.device("d1").DeletedObservedAt != nil {
		t.Error("empty snapshot tombstoned d1")
	}

	if res := e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2")); res.DevicesTombstoned != 0 {
		t.Fatalf("res = %+v", res)
	}
	if e.device("d2").DeletedObservedAt != nil {
		t.Error("tombstone not cleared when the device reappeared")
	}
}

func liveFor(items []application.Device, provider string) []application.Device {
	var out []application.Device
	for _, d := range items {
		if d.Provider == provider {
			out = append(out, d)
		}
	}
	return out
}

func TestLinksAssetBySerialNumberAndRaisesNoMatchFinding(t *testing.T) {
	e := newEnv(t)
	asset := e.newID()
	e.assets.bySerial["sn-linked"] = asset

	res := e.ingest(dev("d1", "PC-1", "SN-Linked"), dev("d2", "PC-2", "SN-Orphan"), dev("d3", "PC-3", ""))
	if res.DevicesLinked != 1 || res.FindingsRaised != 2 {
		t.Fatalf("res = %+v", res)
	}
	d1 := e.device("d1")
	if d1.AssetID == nil || *d1.AssetID != asset || *d1.AssetLinkSource != "serial" {
		t.Fatalf("d1 = %+v", d1)
	}
	if got := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.device.linked' AND target_id = $2`, e.corr, d1.ID); got != 1 {
		t.Errorf("link audit rows = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'DeviceLinked'`, e.corr); got != 1 {
		t.Errorf("DeviceLinked events = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'EndpointFindingRaised'`, e.corr); got != 2 {
		t.Errorf("finding events = %d", got)
	}
	if f := e.openFindings(e.device("d2").ID); !f["no_asset_match"] || len(f) != 1 {
		t.Errorf("d2 findings = %v", f)
	}
	if f := e.openFindings(e.device("d3").ID); !f["no_asset_match"] {
		t.Errorf("d3 findings = %v", f)
	}
	if f := e.openFindings(d1.ID); len(f) != 0 {
		t.Errorf("d1 findings = %v", f)
	}

	// Repeating raises nothing new; when the asset appears the finding resolves and the device links.
	if res := e.ingest(dev("d1", "PC-1", "SN-Linked"), dev("d2", "PC-2", "SN-Orphan"), dev("d3", "PC-3", "")); res.FindingsRaised != 0 || res.DevicesLinked != 0 {
		t.Fatalf("repeat = %+v", res)
	}
	late := e.newID()
	e.assets.bySerial["sn-orphan"] = late
	res = e.ingest(dev("d1", "PC-1", "SN-Linked"), dev("d2", "PC-2", "SN-Orphan"), dev("d3", "PC-3", ""))
	if res.DevicesLinked != 1 || res.FindingsResolved != 1 {
		t.Fatalf("late = %+v", res)
	}
	if f := e.openFindings(e.device("d2").ID); len(f) != 0 {
		t.Errorf("d2 findings = %v", f)
	}
}

func TestSerialConflictAndDuplicateDevices(t *testing.T) {
	e := newEnv(t)
	e.assets.ambiguous["sn-shared"] = true
	e.assets.bySerial["sn-dup"] = e.newID()

	e.ingest(dev("d1", "PC-1", "SN-Shared"), dev("d2", "PC-2", "SN-Dup"), dev("d3", "PC-3", "sn-dup"))
	if f := e.openFindings(e.device("d1").ID); !f["serial_conflict"] || len(f) != 1 {
		t.Errorf("d1 findings = %v", f)
	}
	if d := e.device("d1"); d.AssetID != nil {
		t.Errorf("ambiguous serial linked: %+v", d)
	}
	// Two devices share a serial number: neither links (no arbitrary winner), both are flagged.
	for _, id := range []string{"d2", "d3"} {
		if f := e.openFindings(e.device(id).ID); !f["duplicate_device"] || len(f) != 1 {
			t.Errorf("%s findings = %v", id, f)
		}
		if e.device(id).AssetID != nil {
			t.Errorf("%s was linked despite the shared serial number", id)
		}
	}
	// The duplicate disappears: the partner links and loses its finding, the tombstoned device keeps none.
	res := e.ingest(dev("d1", "PC-1", "SN-Shared"), dev("d2", "PC-2", "SN-Dup"))
	if res.DevicesTombstoned != 1 {
		t.Fatalf("res = %+v", res)
	}
	if f := e.openFindings(e.device("d3").ID); len(f) != 0 {
		t.Errorf("tombstoned device keeps findings: %v", f)
	}
	if f := e.openFindings(e.device("d2").ID); len(f) != 0 {
		t.Errorf("partner keeps duplicate finding: %v", f)
	}
	if e.device("d2").AssetID == nil {
		t.Error("partner should link once the duplicate is gone")
	}
}

func (e *env) registerSoftware(name string, aliases ...string) {
	e.t.Helper()
	if _, _, err := e.svc.RegisterSoftwareProduct(context.Background(), e.caller(), e.manage, name, "", aliases); err != nil {
		e.t.Fatalf("register %s: %v", name, err)
	}
}

func TestSoftwareIsNormalizedThroughAliases(t *testing.T) {
	e := newEnv(t)
	suffix := strings.TrimPrefix(e.corr, "endpoints-")
	zip := "SevenZip " + suffix
	e.registerSoftware(zip, "7-Zip  23.01 (x64) "+suffix)

	d := dev("d1", "PC-1", "SN1")
	d.SoftwareKnown = true
	d.Software = []intune.SoftwareRecord{
		{Name: "7-zip 23.01   (X64) " + suffix, Version: "23.01"},
		{Name: "sevenzip " + suffix, Version: "24.00"},
		{Name: "Mystery Tool " + suffix, Version: "1.0"},
		{Name: "mystery tool " + suffix, Version: "1.0"}, // duplicate by case
	}
	res := e.ingest(d)
	if res.SoftwareObserved != 3 {
		t.Fatalf("res = %+v", res)
	}
	detail, err := e.svc.GetDevice(context.Background(), e.view, e.device("d1").ID)
	if err != nil {
		t.Fatal(err)
	}
	matched, unmatched := 0, 0
	for _, s := range detail.Software {
		if s.SoftwareProduct != nil {
			matched++
			if s.ProductName == nil || *s.ProductName != zip {
				t.Errorf("product name = %v", s.ProductName)
			}
		} else {
			unmatched++
			if !strings.HasPrefix(s.RawName, "Mystery") {
				t.Errorf("unexpected unmatched %q", s.RawName)
			}
		}
	}
	if matched != 2 || unmatched != 1 {
		t.Fatalf("matched %d unmatched %d", matched, unmatched)
	}
	if f := e.openFindings(detail.Device.ID); !f["unmatched_software"] {
		t.Fatalf("findings = %v", f)
	}

	// Registering the missing product relinks existing installations and resolves the finding at once.
	e.registerSoftware("Mystery Tool " + suffix)
	if f := e.openFindings(detail.Device.ID); f["unmatched_software"] {
		t.Errorf("findings after register = %v", f)
	}
	if res := e.ingest(d); res.FindingsResolved != 0 || res.FindingsRaised != 0 {
		t.Fatalf("after register = %+v", res)
	}

	// An installation that disappears is tombstoned; repeating the same data adds no rows.
	d.Software = d.Software[:1]
	e.ingest(d)
	again, _ := e.svc.GetDevice(context.Background(), e.view, detail.Device.ID)
	if len(again.Software) != 1 {
		t.Errorf("live software = %d", len(again.Software))
	}
	e.ingest(d)
	if n := e.count(`SELECT count(*) FROM endpoints.software_installations WHERE device_id = $1::uuid`, detail.Device.ID); n != 3 {
		t.Errorf("installation rows = %d", n)
	}

	// Software that could not be read leaves the previous state alone.
	unknown := dev("d1", "PC-1", "SN1")
	e.ingest(unknown)
	still, _ := e.svc.GetDevice(context.Background(), e.view, detail.Device.ID)
	if len(still.Software) != 1 {
		t.Errorf("unknown software cleared installations: %d", len(still.Software))
	}
}

func TestRegisterSoftwareProductRejectsDuplicatesAndNeedsManage(t *testing.T) {
	e := newEnv(t)
	name := "Dup " + strings.TrimPrefix(e.corr, "endpoints-")
	e.registerSoftware(name)
	if _, _, err := e.svc.RegisterSoftwareProduct(context.Background(), e.caller(), e.manage, name, "", nil); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate = %v", err)
	}
	if _, _, err := e.svc.RegisterSoftwareProduct(context.Background(), e.caller(), e.view, name+"2", "", nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only = %v", err)
	}
}

func TestManualLinkAndUnlink(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	asset, other := e.newID(), e.newID()
	e.assets.existing[asset], e.assets.existing[other] = true, true
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	d1, d2 := e.device("d1"), e.device("d2")
	if f := e.openFindings(d1.ID); !f["no_asset_match"] {
		t.Fatalf("findings = %v", f)
	}

	if _, err := e.link(e.view, d1.ID, asset, "correction"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only link = %v", err)
	}
	if _, err := e.link(e.manage, d1.ID, asset, "because I said so"); err == nil {
		t.Error("free-text reason accepted")
	}
	if _, err := e.link(e.manage, d1.ID, e.newID(), "correction"); !errors.Is(err, application.ErrAssetInvalid) {
		t.Errorf("unknown asset = %v", err)
	}
	stale := d1.Version + 5
	if _, err := e.svc.ManualLink(ctx, e.caller(), e.manage, d1.ID, asset, "correction", &stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version = %v", err)
	}
	linked, err := e.svc.ManualLink(ctx, e.caller(), e.manage, d1.ID, asset, "correction", &d1.Version)
	if err != nil || linked.AssetID == nil || *linked.AssetID != asset || *linked.AssetLinkSource != "manual" || linked.Version != d1.Version+1 {
		t.Fatalf("link = %+v %v", linked, err)
	}
	if f := e.openFindings(d1.ID); len(f) != 0 {
		t.Errorf("findings after link = %v", f)
	}
	// Linking the same asset again is a no-op; another asset needs an unlink first; the asset cannot serve two devices.
	if again, err := e.link(e.manage, d1.ID, asset, "correction"); err != nil || again.Version != linked.Version {
		t.Errorf("repeat = %+v %v", again, err)
	}
	if _, err := e.link(e.manage, d1.ID, other, "correction"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("relink = %v", err)
	}
	if _, err := e.link(e.manage, d2.ID, asset, "correction"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("asset on two devices = %v", err)
	}
	if got := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.device.linked' AND metadata->>'reason' = 'correction'`, e.corr); got != 1 {
		t.Errorf("link audit rows = %d", got)
	}

	// Unlinking is explicit, audited, and survives the next sync even though the serial would match.
	e.assets.bySerial["sn1"] = asset
	if _, err := e.unlink(e.view, d1.ID, "wrong_asset"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only unlink = %v", err)
	}
	unlinked, err := e.unlink(e.manage, d1.ID, "wrong_asset")
	if err != nil || unlinked.AssetID != nil || !unlinked.AutoLinkBlocked {
		t.Fatalf("unlink = %+v %v", unlinked, err)
	}
	if _, err := e.unlink(e.manage, d1.ID, "wrong_asset"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("unlink twice = %v", err)
	}
	if got := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'endpoints.device.unlinked' AND target_id = $2`, e.corr, d1.ID); got != 1 {
		t.Errorf("unlink audit rows = %d", got)
	}
	res := e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN2"))
	if res.DevicesLinked != 0 || e.device("d1").AssetID != nil {
		t.Errorf("sync relinked a manually unlinked device: %+v", res)
	}
	if f := e.openFindings(d1.ID); len(f) != 0 {
		t.Errorf("blocked device findings = %v", f)
	}
	// A manual link lifts the block.
	if relinked, err := e.link(e.manage, d1.ID, asset, "serial_confirmed"); err != nil || relinked.AutoLinkBlocked {
		t.Errorf("relink = %+v %v", relinked, err)
	}
}

func TestChangedSerialDropsSerialLinkButKeepsManualLink(t *testing.T) {
	e := newEnv(t)
	a1, a2 := e.newID(), e.newID()
	e.assets.bySerial["sn1"], e.assets.bySerial["sn2"] = a1, a2
	e.assets.existing[a1] = true
	e.ingest(dev("d1", "PC-1", "SN1"), dev("d2", "PC-2", "SN-X"))
	if id := e.device("d1").AssetID; id == nil || *id != a1 {
		t.Fatalf("d1 = %+v", e.device("d1"))
	}
	e.ingest(dev("d1", "PC-1", "SN2"), dev("d2", "PC-2", "SN-X"))
	if id := e.device("d1").AssetID; id == nil || *id != a2 {
		t.Errorf("serial change should rematch: %+v", e.device("d1"))
	}

	if _, err := e.link(e.manage, e.device("d2").ID, a1, "correction"); err != nil {
		t.Fatal(err)
	}
	e.ingest(dev("d1", "PC-1", "SN2"), dev("d2", "PC-2", "SN-Y"))
	if id := e.device("d2").AssetID; id == nil || *id != a1 {
		t.Errorf("manual link must survive a serial change: %+v", e.device("d2"))
	}
}

func TestPermissionsRequired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	none := application.Principal{UserID: e.user}
	if _, err := e.svc.Ingest(ctx, e.caller(), e.view, application.Snapshot{Provider: e.provider, Source: application.SourceImport}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("ingest as viewer = %v", err)
	}
	if _, err := e.svc.Sync(ctx, e.caller(), e.view); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("sync as viewer = %v", err)
	}
	if _, err := e.svc.ListDevices(ctx, none, application.DeviceFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("list = %v", err)
	}
	if _, err := e.svc.GetDevice(ctx, none, e.newID()); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("get = %v", err)
	}
	if _, err := e.svc.ListFindings(ctx, none, application.FindingFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("findings = %v", err)
	}
	if _, err := e.svc.GetDevice(ctx, e.view, e.newID()); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown device = %v", err)
	}
	if _, err := e.svc.GetDevice(ctx, e.view, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("bad id = %v", err)
	}
}

func TestSyncReadsTheProvider(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fake := intune.NewFake()
	fake.SetDevices(intune.DeviceRecord{ExternalID: "x1", Name: "PC-X", SerialNumber: "SNX", OSPlatform: "macos"})
	fake.SetSoftware("x1", intune.SoftwareRecord{Name: "Some App " + e.provider, Version: "1"})

	off := application.NewService(repository.New(e.pool), e.assets, fake, false, nil).WithSyncCooldown(0)
	if _, err := off.Sync(ctx, e.caller(), e.manage); !errors.Is(err, application.ErrSyncDisabled) {
		t.Fatalf("disabled sync = %v", err)
	}
	on := application.NewService(repository.New(e.pool), e.assets, fake, true, nil).WithSyncCooldown(0)
	res, err := on.Sync(ctx, e.caller(), e.manage)
	if err != nil || res.DevicesCreated != 1 || res.SoftwareObserved != 1 {
		t.Fatalf("sync = %+v %v", res, err)
	}
	// The sync stores devices under the Intune provider key; remove them again.
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM endpoints.devices WHERE provider = $1 AND external_id = 'x1'`, intune.ProviderKey)
	})
	fake.FailWith(errors.New("graph unavailable"))
	if _, err := on.Sync(ctx, e.caller(), e.manage); err == nil {
		t.Error("provider failure was not reported")
	}
	if got := e.count(`SELECT count(*) FROM endpoints.devices WHERE provider = $1 AND external_id = 'x1' AND deleted_observed_at IS NOT NULL`, intune.ProviderKey); got != 0 {
		t.Error("provider failure tombstoned devices")
	}
}

func TestListDevicesFiltersAndPaginates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.assets.bySerial["sn-1"] = e.newID()
	mac := dev("d3", "Mac-3", "SN-3")
	mac.Record.OSPlatform, mac.Record.ComplianceState = "macos", "noncompliant"
	e.ingest(dev("d1", "Zeta-1", "SN-1"), dev("d2", "Zeta-2", "SN-2"), mac)

	list := func(f application.DeviceFilter) []application.Device {
		f.Page.Limit = 200
		res, err := e.svc.ListDevices(ctx, e.view, f)
		if err != nil {
			t.Fatal(err)
		}
		return liveFor(res.Items, e.provider)
	}
	yes, no := true, false
	if n := len(list(application.DeviceFilter{Platform: "macos"})); n != 1 {
		t.Errorf("platform = %d", n)
	}
	if n := len(list(application.DeviceFilter{Compliance: "noncompliant"})); n != 1 {
		t.Errorf("compliance = %d", n)
	}
	if n := len(list(application.DeviceFilter{Query: "zeta"})); n != 2 {
		t.Errorf("query = %d", n)
	}
	if n := len(list(application.DeviceFilter{Query: "SN-2"})); n != 1 {
		t.Errorf("serial query = %d", n)
	}
	if n := len(list(application.DeviceFilter{Query: "zeta", Linked: &yes})); n != 1 {
		t.Errorf("linked = %d", n)
	}
	if n := len(list(application.DeviceFilter{Query: "zeta", Linked: &no})); n != 1 {
		t.Errorf("unlinked = %d", n)
	}

	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 100; i++ {
		res, err := e.svc.ListDevices(ctx, e.view, application.DeviceFilter{Query: "zeta", Page: application.Page{Limit: 1, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Items {
			seen[d.ID] = true
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 2 {
		t.Errorf("paged devices = %d", len(seen))
	}
	if _, err := e.svc.ListDevices(ctx, e.view, application.DeviceFilter{Page: application.Page{Cursor: "x"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("bad cursor = %v", err)
	}
	fs, err := e.svc.ListFindings(ctx, e.view, application.FindingFilter{Kind: "no_asset_match", Page: application.Page{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, f := range fs.Items {
		if f.DeviceID == e.device("d2").ID || f.DeviceID == e.device("d3").ID {
			mine++
		}
	}
	if mine != 2 {
		t.Errorf("findings listed = %d", mine)
	}
}
