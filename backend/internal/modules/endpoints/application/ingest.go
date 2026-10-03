package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

var providerKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)

// SnapshotDevice is one device of a snapshot with its discovered software. SoftwareKnown is false
// when the software could not be read: the previous installations are then left untouched.
type SnapshotDevice struct {
	Record        intune.DeviceRecord
	Software      []intune.SoftwareRecord
	SoftwareKnown bool
}

// Snapshot is a view of a provider's devices. Only a Complete one may tombstone devices missing from
// it (and then only when it is not empty, which is treated as a provider failure, and the tombstone
// guard allows it); an incomplete snapshot (a partial import) only adds and updates.
type Snapshot struct {
	Provider string
	// Source is SourceSync (live provider) or SourceImport (file import).
	Source   string
	Complete bool
	Devices  []SnapshotDevice
}

// PlaceholderDeviceName replaces a device name that is empty or carries unsafe characters, so the
// device is still recorded (and counted as seen) instead of being rejected.
const PlaceholderDeviceName = "unnamed-device"

// placeholderSerials are values firmware reports when no serial number was programmed. They are
// never used to match Assets or to detect duplicates.
var placeholderSerials = []string{"0", "to be filled by o.e.m.", "default string", "none", "n/a", "system serial number"}

func placeholderSerial(serial string) bool {
	return slices.Contains(placeholderSerials, strings.ToLower(strings.TrimSpace(serial)))
}

// counters tallies what an ingestion run did.
type counters struct {
	DevicesCreated    int
	DevicesUpdated    int
	DevicesUnchanged  int
	DevicesTombstoned int
	TombstonesSkipped int
	DevicesRejected   int
	DevicesLinked     int
	SoftwareObserved  int
	SoftwareSkipped   int
	FindingsRaised    int
	FindingsResolved  int
}

func (a *counters) add(b counters) {
	a.DevicesCreated += b.DevicesCreated
	a.DevicesUpdated += b.DevicesUpdated
	a.DevicesUnchanged += b.DevicesUnchanged
	a.DevicesTombstoned += b.DevicesTombstoned
	a.TombstonesSkipped += b.TombstonesSkipped
	a.DevicesRejected += b.DevicesRejected
	a.DevicesLinked += b.DevicesLinked
	a.SoftwareObserved += b.SoftwareObserved
	a.SoftwareSkipped += b.SoftwareSkipped
	a.FindingsRaised += b.FindingsRaised
	a.FindingsResolved += b.FindingsResolved
}

// IngestResult reports what an ingestion run did.
type IngestResult struct {
	DevicesCreated    int
	DevicesUpdated    int
	DevicesUnchanged  int
	DevicesTombstoned int
	// TombstonesSkipped counts devices that were missing from the snapshot but not tombstoned because
	// the tombstone guard (more than half of the provider's live devices) refused it.
	TombstonesSkipped int
	// DevicesRejected counts records that were invalid (missing id, serial with unsafe content, duplicate id).
	DevicesRejected  int
	DevicesLinked    int
	SoftwareObserved int
	// SoftwareSkipped counts installations dropped as invalid or beyond the per-device limit.
	SoftwareSkipped  int
	FindingsRaised   int
	FindingsResolved int
	// SoftwareErrors counts devices whose software could not be read (Sync only).
	SoftwareErrors int
	// Management is the result of the management ingestion that Sync runs after the devices.
	Management ManagementResult
}

func resultOf(c counters) IngestResult {
	return IngestResult{
		DevicesCreated: c.DevicesCreated, DevicesUpdated: c.DevicesUpdated, DevicesUnchanged: c.DevicesUnchanged,
		DevicesTombstoned: c.DevicesTombstoned, TombstonesSkipped: c.TombstonesSkipped, DevicesRejected: c.DevicesRejected, DevicesLinked: c.DevicesLinked,
		SoftwareObserved: c.SoftwareObserved, SoftwareSkipped: c.SoftwareSkipped,
		FindingsRaised: c.FindingsRaised, FindingsResolved: c.FindingsResolved,
	}
}

func syncActorName(source string) string {
	if source == SourceImport {
		return "endpoint-import"
	}
	return "intune-sync"
}

// recordSync appends the audit entry of a run, successful or not, in a transaction of its own.
func (s *Service) recordSync(ctx context.Context, c Caller, action, provider, source string, complete bool, total counters, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			return fmt.Errorf("generate id: %w", err)
		}
		meta := map[string]any{
			"provider": provider, "source": source, "complete": complete, "created": total.DevicesCreated, "updated": total.DevicesUpdated,
			"unchanged": total.DevicesUnchanged, "tombstoned": total.DevicesTombstoned, "tombstonesSkipped": total.TombstonesSkipped,
			"rejected": total.DevicesRejected, "linked": total.DevicesLinked, "softwareObserved": total.SoftwareObserved,
			"findingsRaised": total.FindingsRaised, "findingsResolved": total.FindingsResolved,
		}
		if reason != "" {
			meta["reason"] = reason
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: action, TargetType: "endpoint_sync", TargetID: id, Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta,
		})
	})
}

// Sync reads the configured provider and ingests the snapshot. Requires endpoints.manage and the
// provider synchronization to be enabled (INTUNE_SYNC). Only one run per provider happens at a time;
// another one returns ErrSyncRunning.
func (s *Service) Sync(ctx context.Context, c Caller, p Principal) (IngestResult, error) {
	if err := c.validate(); err != nil {
		return IngestResult{}, err
	}
	if !p.Manage {
		return IngestResult{}, ErrForbidden
	}
	if !s.syncOn {
		return IngestResult{}, ErrSyncDisabled
	}
	unlock, ok, err := s.store.TryLockProvider(ctx, intune.ProviderKey)
	if err != nil {
		return IngestResult{}, err
	}
	if !ok {
		return IngestResult{}, ErrSyncRunning
	}
	defer unlock()
	// Checked under the lock so concurrent requests cannot both pass.
	if s.syncCooldown > 0 {
		last, err := s.store.LastSyncCompleted(ctx, intune.ProviderKey)
		if err != nil {
			return IngestResult{}, err
		}
		if last != nil && s.now().Sub(*last) < s.syncCooldown {
			return IngestResult{}, ErrSyncCooldown
		}
	}
	devices, err := s.provider.Devices(ctx)
	if err != nil {
		if !errors.Is(err, intune.ErrNotConfigured) {
			_ = s.recordSync(ctx, c, "endpoints.sync.failed", intune.ProviderKey, SourceSync, true, counters{}, "provider_read_failed")
		}
		return IngestResult{}, fmt.Errorf("read devices: %w", err)
	}
	snap := Snapshot{Provider: intune.ProviderKey, Source: SourceSync, Complete: true, Devices: make([]SnapshotDevice, 0, len(devices))}
	softwareErrors := 0
	for _, d := range devices {
		sd := SnapshotDevice{Record: d}
		if sw, err := s.provider.Software(ctx, d.ExternalID); err == nil {
			sd.Software, sd.SoftwareKnown = sw, true
		} else if ctx.Err() != nil {
			_ = s.recordSync(ctx, c, "endpoints.sync.failed", snap.Provider, snap.Source, true, counters{}, "canceled")
			return IngestResult{}, ctx.Err()
		} else {
			softwareErrors++
		}
		snap.Devices = append(snap.Devices, sd)
	}
	res, err := s.ingestLocked(ctx, c, snap)
	res.SoftwareErrors = softwareErrors
	if err != nil {
		return res, err
	}
	// The cooldown starts when the run ends, whatever happens to the management part.
	defer func() { _ = s.markSyncCompleted(ctx, snap.Provider) }()
	// The management data refers to the devices just ingested; the same lock is still held.
	mg, err := s.provider.Management(ctx)
	if err != nil {
		if ctx.Err() != nil {
			_ = s.recordManagementSync(ctx, c, auditManagementFailed, snap.Provider, snap.Source, true, ManagementResult{}, "canceled")
			return res, ctx.Err()
		}
		_ = s.recordManagementSync(ctx, c, auditManagementFailed, snap.Provider, snap.Source, true, ManagementResult{}, "provider_read_failed")
		res.Management.ManagementErrors++
		return res, nil
	}
	res.Management, err = s.ingestManagementLocked(ctx, c, ManagementSnapshot{Provider: snap.Provider, Source: snap.Source, Complete: true, ManagementSnapshot: mg})
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		// The device phase is committed: a failing management phase is reported like a failing read.
		// ingestManagementLocked already audited the failure.
		res.Management.ManagementErrors++
		return res, nil
	}
	return res, nil
}

func (s *Service) markSyncCompleted(ctx context.Context, provider string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.MarkSyncCompleted(ctx, provider, s.now().UTC())
}

// Ingest stores a snapshot idempotently under the provider's run lock (ErrSyncRunning when another
// run holds it): devices are upserted in batches (one short transaction each, bounded by device and
// installation count), a change history row is appended only when normalized values change, devices
// are linked to Assets by serial number, findings are raised and resolved, software is normalized
// through aliases, and - for a Complete, non-empty snapshot - devices missing from it are tombstoned
// unless that would tombstone more than half of the provider's live devices. Ingesting the same
// snapshot again changes nothing but freshness. Requires endpoints.manage.
func (s *Service) Ingest(ctx context.Context, c Caller, p Principal, snap Snapshot) (IngestResult, error) {
	if err := c.validate(); err != nil {
		return IngestResult{}, err
	}
	if !p.Manage {
		return IngestResult{}, ErrForbidden
	}
	if !providerKey.MatchString(snap.Provider) {
		return IngestResult{}, invalid("provider must match %s", providerKey.String())
	}
	if snap.Source != SourceSync && snap.Source != SourceImport {
		return IngestResult{}, invalid("source must be %s or %s", SourceSync, SourceImport)
	}
	unlock, ok, err := s.store.TryLockProvider(ctx, snap.Provider)
	if err != nil {
		return IngestResult{}, err
	}
	if !ok {
		return IngestResult{}, ErrSyncRunning
	}
	defer unlock()
	return s.ingestLocked(ctx, c, snap)
}

// batchItem is a normalized snapshot device with its Asset lookup result.
type batchItem struct {
	in    deviceInput
	match assetMatch
}

func (s *Service) ingestLocked(ctx context.Context, c Caller, snap Snapshot) (IngestResult, error) {
	if len(snap.Devices) > MaxSnapshotDevices {
		err := invalid("a snapshot may contain at most %d devices", MaxSnapshotDevices)
		_ = s.recordSync(ctx, c, "endpoints.sync.failed", snap.Provider, snap.Source, snap.Complete, counters{}, "snapshot_too_large")
		return IngestResult{}, err
	}
	var total counters
	fail := func(reason string, err error) (IngestResult, error) {
		_ = s.recordSync(ctx, c, "endpoints.sync.failed", snap.Provider, snap.Source, snap.Complete, total, reason)
		return resultOf(total), err
	}
	// Truncated to the database precision so the tombstone comparison below is exact.
	runAt := s.now().UTC().Truncate(time.Microsecond)
	seen := map[string]bool{}
	keep := []string{}
	for start := 0; start < len(snap.Devices); {
		var batch []batchItem
		installs := 0
		for start < len(snap.Devices) && len(batch) < BatchSize {
			in, extID, ok := normalizeDevice(snap, snap.Devices[start], runAt)
			if extID != "" && seen[extID] {
				ok = false
			}
			if !ok {
				total.DevicesRejected++
				if extID != "" && !seen[extID] {
					// Invalid, but the provider did report it: it must not be tombstoned.
					seen[extID] = true
					keep = append(keep, extID)
				}
				start++
				continue
			}
			if len(batch) > 0 && installs+len(in.Software) > MaxBatchInstallations {
				break
			}
			seen[extID] = true
			keep = append(keep, extID)
			installs += len(in.Software)
			batch = append(batch, batchItem{in: in, match: s.matchAsset(ctx, in.SerialNumber, in.Ownership)})
			start++
		}
		if len(batch) == 0 {
			continue
		}
		var got counters
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = counters{}
			for _, it := range batch {
				if err := s.ingestDevice(ctx, tx, c, &got, it.in, it.match); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fail("ingest_error", err)
		}
		total.add(got)
	}
	if snap.Complete && len(snap.Devices) > 0 && len(keep) > 0 {
		if err := s.tombstoneMissing(ctx, c, snap, runAt, keep, &total); err != nil {
			return fail("tombstone_error", err)
		}
	}
	if err := s.recordSync(ctx, c, "endpoints.sync.completed", snap.Provider, snap.Source, snap.Complete, total, ""); err != nil {
		return resultOf(total), err
	}
	return resultOf(total), nil
}

// tombstoneMissing tombstones the provider's live devices that the complete snapshot did not mention,
// unless that would remove more than half of them. Partners that shared a serial number with a
// tombstoned device are re-evaluated afterwards.
func (s *Service) tombstoneMissing(ctx context.Context, c Caller, snap Snapshot, runAt time.Time, keep []string, total *counters) error {
	var serials []string
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		serials = nil
		ids, live, err := s.store.TombstoneCandidatesTx(ctx, tx, snap.Provider, runAt, keep)
		if err != nil || len(ids) == 0 {
			return err
		}
		if live > MinGuardedDevices && len(ids)*2 > live {
			total.TombstonesSkipped += len(ids)
			return nil
		}
		tombstoned, unlinked, err := s.store.TombstoneDevicesTx(ctx, tx, ids, runAt)
		if err != nil {
			return err
		}
		after := map[string]Device{}
		for _, d := range tombstoned {
			after[d.ID] = d
		}
		for _, d := range tombstoned {
			total.DevicesTombstoned++
			if err := s.store.AppendHistoryTx(ctx, tx, historyOf(d)); err != nil {
				return err
			}
			for _, kind := range FindingKinds {
				if err := s.reconcileFinding(ctx, tx, c, total, kind, d.ID, false, nil); err != nil {
					return err
				}
			}
			if d.SerialNumber != nil && !placeholderSerial(*d.SerialNumber) {
				serials = append(serials, *d.SerialNumber)
			}
		}
		for _, before := range unlinked {
			b, a := before, after[before.ID]
			if err := s.auditUnlink(ctx, tx, c, &b, &a, "tombstoned"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(serials) == 0 {
		return err
	}
	return s.recheckPartners(ctx, c, total, serials)
}

// recheckPartners re-evaluates the live devices that shared a serial number with a tombstoned device:
// their duplicate finding may no longer apply and they may link now.
func (s *Service) recheckPartners(ctx context.Context, c Caller, total *counters, serials []string) error {
	var partners []Device
	seen := map[string]bool{}
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		partners = nil
		for _, serial := range serials {
			ds, err := s.store.LiveDevicesBySerialTx(ctx, tx, serial, "")
			if err != nil {
				return err
			}
			for _, d := range ds {
				if !seen[d.ID] {
					seen[d.ID] = true
					partners = append(partners, d)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for start := 0; start < len(partners); start += BatchSize {
		batch := partners[start:min(start+BatchSize, len(partners))]
		matches := make([]assetMatch, len(batch))
		for i, d := range batch {
			matches[i] = s.matchAsset(ctx, d.SerialNumber, d.Ownership)
		}
		var got counters
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = counters{}
			for i, p := range batch {
				d, err := s.store.LockDeviceTx(ctx, tx, p.ID)
				if err != nil {
					return err
				}
				if d.DeletedObservedAt != nil {
					continue
				}
				if err := s.reconcileLink(ctx, tx, c, &got, &d, matches[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		total.add(got)
	}
	return nil
}

// deviceInput is a validated and normalized snapshot device.
type deviceInput struct {
	NewDevice
	Software      []intune.SoftwareRecord
	SoftwareKnown bool
	skipped       int
}

func cleanRequired(s string, limit int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", false
	}
	return s, true
}

// cleanOptional keeps a usable value: unsafe text is dropped, long text is shortened.
func cleanOptional(s string, limit int) *string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return nil
	}
	if r := []rune(s); len(r) > limit {
		s = strings.TrimSpace(string(r[:limit]))
	}
	if s == "" {
		return nil
	}
	return &s
}

func oneOf(value string, set []string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if slices.Contains(set, value) {
		return value
	}
	return fallback
}

// normalizeDevice validates one record. extID is the record's external id when that is valid, even
// when the record is rejected for another reason, so the device still counts as seen.
func normalizeDevice(snap Snapshot, sd SnapshotDevice, at time.Time) (in deviceInput, extID string, ok bool) {
	r := sd.Record
	id, ok := cleanRequired(r.ExternalID, 200)
	if !ok {
		return deviceInput{}, "", false
	}
	name, ok := cleanRequired(r.Name, 256)
	if !ok {
		name = PlaceholderDeviceName
	}
	var serial *string
	if strings.TrimSpace(r.SerialNumber) != "" {
		v, ok := cleanRequired(r.SerialNumber, 100)
		if !ok {
			return deviceInput{}, id, false
		}
		serial = &v
	}
	var checkin *time.Time
	if r.LastCheckinAt != nil {
		t := r.LastCheckinAt.UTC().Truncate(time.Microsecond)
		checkin = &t
	}
	in = deviceInput{
		NewDevice: NewDevice{
			Provider: snap.Provider, ExternalID: id, Name: name, SerialNumber: serial,
			OSPlatform: oneOf(r.OSPlatform, OSPlatforms, "other"), OSVersion: cleanOptional(r.OSVersion, 100),
			Manufacturer: cleanOptional(r.Manufacturer, 100), Model: cleanOptional(r.Model, 100),
			Ownership: oneOf(r.Ownership, Ownerships, "unknown"), ComplianceState: oneOf(r.ComplianceState, ComplianceStates, "unknown"),
			LastCheckinAt: checkin, Source: snap.Source, ObservedAt: at, SyncedAt: at,
		},
		SoftwareKnown: sd.SoftwareKnown,
	}
	if sd.SoftwareKnown {
		seen := map[string]bool{}
		for _, sw := range sd.Software {
			name, ok := cleanRequired(sw.Name, 300)
			if !ok {
				in.skipped++
				continue
			}
			version := ""
			if v := cleanOptional(sw.Version, 100); v != nil {
				version = *v
			}
			key := strings.ToLower(name) + "\x00" + version
			if seen[key] || len(in.Software) >= MaxSoftwarePerDevice {
				if !seen[key] {
					in.skipped++
				}
				continue
			}
			seen[key] = true
			in.Software = append(in.Software, intune.SoftwareRecord{Name: name, Version: version, Publisher: derefOr(cleanOptional(sw.Publisher, 200), "")})
		}
	}
	return in, id, true
}

func derefOr(s *string, d string) string {
	if s == nil {
		return d
	}
	return *s
}

// assetMatch is the outcome of looking up an Asset by a device's serial number before the transaction.
type assetMatch struct {
	asset *AssetInfo
	err   error // nil, ErrAssetNotFound or ErrAssetAmbiguous
	fail  error // a lookup failure that aborts the run
}

// matchAsset looks the Asset up unless the device cannot be matched automatically: personal
// devices, and missing or placeholder serial numbers.
func (s *Service) matchAsset(ctx context.Context, serial *string, ownership string) assetMatch {
	if serial == nil || placeholderSerial(*serial) || ownership == "personal" {
		return assetMatch{err: ErrAssetNotFound}
	}
	a, err := s.assets.FindBySerial(ctx, *serial)
	switch {
	case err == nil:
		return assetMatch{asset: &a}
	case errors.Is(err, ErrAssetNotFound), errors.Is(err, ErrAssetAmbiguous):
		return assetMatch{err: err}
	default:
		return assetMatch{fail: err}
	}
}

func sameStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameSerial(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.EqualFold(*a, *b)
}

func (s *Service) ingestDevice(ctx context.Context, tx pgx.Tx, c Caller, run *counters, in deviceInput, match assetMatch) error {
	if match.fail != nil {
		return fmt.Errorf("look up asset: %w", match.fail)
	}
	d, err := s.upsertDevice(ctx, tx, c, run, in)
	if err != nil {
		return err
	}
	if err := s.reconcileLink(ctx, tx, c, run, &d, match); err != nil {
		return err
	}
	run.SoftwareSkipped += in.skipped
	if !in.SoftwareKnown {
		return nil
	}
	return s.ingestSoftware(ctx, tx, c, run, d.ID, in)
}

// upsertDevice creates the device or updates it when normalized values changed or it reappeared; the
// history gets a row only in those cases.
func (s *Service) upsertDevice(ctx context.Context, tx pgx.Tx, c Caller, run *counters, in deviceInput) (Device, error) {
	d, err := s.store.LockDeviceByExternalTx(ctx, tx, in.Provider, in.ExternalID)
	if err != nil {
		return Device{}, err
	}
	if d == nil {
		created, ok, err := s.store.InsertDeviceTx(ctx, tx, in.NewDevice)
		if err != nil {
			return Device{}, err
		}
		if ok {
			run.DevicesCreated++
			return created, s.store.AppendHistoryTx(ctx, tx, historyOf(created))
		}
		// A concurrent writer created it first.
		if d, err = s.store.LockDeviceByExternalTx(ctx, tx, in.Provider, in.ExternalID); err != nil || d == nil {
			return Device{}, fmt.Errorf("lock device after concurrent create: %w", err)
		}
	}
	revived := d.DeletedObservedAt != nil
	changed := revived || d.Name != in.Name || !sameStr(d.SerialNumber, in.SerialNumber) || d.OSPlatform != in.OSPlatform ||
		!sameStr(d.OSVersion, in.OSVersion) || !sameStr(d.Manufacturer, in.Manufacturer) || !sameStr(d.Model, in.Model) ||
		d.Ownership != in.Ownership || d.ComplianceState != in.ComplianceState
	if !changed {
		run.DevicesUnchanged++
		if err := s.store.TouchDeviceTx(ctx, tx, d.ID, in.ObservedAt, in.SyncedAt, in.LastCheckinAt, in.Source); err != nil {
			return Device{}, err
		}
		d.ObservedAt, d.LastSyncedAt, d.LastCheckinAt, d.Source = in.ObservedAt, in.SyncedAt, in.LastCheckinAt, in.Source
		return *d, nil
	}
	before := *d
	serialChanged := !sameSerial(d.SerialNumber, in.SerialNumber)
	d.Name, d.SerialNumber, d.OSPlatform, d.OSVersion, d.Manufacturer, d.Model = in.Name, in.SerialNumber, in.OSPlatform, in.OSVersion, in.Manufacturer, in.Model
	d.Ownership, d.ComplianceState, d.LastCheckinAt, d.Source = in.Ownership, in.ComplianceState, in.LastCheckinAt, in.Source
	d.ObservedAt, d.LastSyncedAt, d.DeletedObservedAt = in.ObservedAt, in.SyncedAt, nil
	dropMethod := ""
	switch {
	case serialChanged && d.AssetLinkSource != nil && *d.AssetLinkSource == LinkSerial:
		// A link made by serial number is only as good as the serial number: drop it, then match again.
		dropMethod = "serial_changed"
	case revived && d.AssetID != nil:
		// A manual link survives a tombstone only while the Asset is still free.
		others, err := s.store.OtherLiveDevicesByAssetTx(ctx, tx, *d.AssetID, d.ID)
		if err != nil {
			return Device{}, err
		}
		if len(others) > 0 {
			dropMethod = "revived_asset_taken"
		}
	}
	if dropMethod != "" {
		d.AssetID, d.AssetLinkSource = nil, nil
	}
	updated, err := s.store.UpdateDeviceTx(ctx, tx, *d)
	if err != nil {
		return Device{}, err
	}
	run.DevicesUpdated++
	if err := s.store.AppendHistoryTx(ctx, tx, historyOf(updated)); err != nil {
		return Device{}, err
	}
	if dropMethod != "" {
		if err := s.auditUnlink(ctx, tx, c, &before, &updated, dropMethod); err != nil {
			return Device{}, err
		}
	}
	return updated, nil
}

func historyOf(d Device) History {
	return History{
		DeviceID: d.ID, Name: d.Name, SerialNumber: d.SerialNumber, OSPlatform: d.OSPlatform, OSVersion: d.OSVersion,
		Manufacturer: d.Manufacturer, Model: d.Model, Ownership: d.Ownership, ComplianceState: d.ComplianceState,
		Source: d.Source, ObservedAt: d.ObservedAt,
	}
}

// systemChange records a link change that the synchronization derived (not a person's decision): the
// actor is the sync system, the triggering user is metadata.
func systemChange(c Caller, d *Device, meta map[string]any) (audit.Actor, map[string]any) {
	meta["triggeredBy"] = c.Actor.UserID
	if c.Actor.UserID == "" {
		meta["triggeredBy"] = nil
	}
	return audit.SystemActor(syncActorName(d.Source)), meta
}

// auditUnlink audits and publishes a link that the system removed (serial change, duplicate serial,
// tombstone, revival conflict). before holds the link, after the device without it.
func (s *Service) auditUnlink(ctx context.Context, tx pgx.Tx, c Caller, before, after *Device, method string) error {
	actor, meta := systemChange(c, after, map[string]any{"method": method})
	if err := s.recordLinkAs(ctx, tx, actor, c, "endpoints.device.unlinked", before, after, meta); err != nil {
		return err
	}
	return publish(ctx, tx, c, "DeviceUnlinked", map[string]any{"deviceId": after.ID, "assetId": before.AssetID, "method": method})
}

// unlinkSerial removes a serial-source link and audits it.
func (s *Service) unlinkSerial(ctx context.Context, tx pgx.Tx, c Caller, d *Device, method string) error {
	before := *d
	d.AssetID, d.AssetLinkSource = nil, nil
	updated, err := s.store.UpdateDeviceTx(ctx, tx, *d)
	if err != nil {
		return err
	}
	*d = updated
	return s.auditUnlink(ctx, tx, c, &before, &updated, method)
}

func hasLink(d *Device, source string) bool {
	return d.AssetID != nil && d.AssetLinkSource != nil && *d.AssetLinkSource == source
}

// reconcileLink links an unlinked device to the Asset with its serial number and keeps the
// no_asset_match, serial_conflict and duplicate_device findings in step. A manual unlink or a manual
// link stops it. Personal devices, placeholder serial numbers and Assets that left use are never
// matched, and a serial number shared by several live devices links none of them.
func (s *Service) reconcileLink(ctx context.Context, tx pgx.Tx, c Caller, run *counters, d *Device, match assetMatch) error {
	if match.fail != nil {
		return fmt.Errorf("look up asset: %w", match.fail)
	}
	if d.AutoLinkBlocked || hasLink(d, LinkManual) || d.Ownership == "personal" {
		return s.resolveLinkFindings(ctx, tx, c, run, d.ID)
	}
	var noMatch, conflict, duplicate bool
	var detail map[string]any
	var link *AssetInfo
	switch {
	case d.SerialNumber == nil:
		noMatch, detail = true, map[string]any{"reason": "no_serial_number"}
	case placeholderSerial(*d.SerialNumber):
		noMatch, detail = true, map[string]any{"reason": "placeholder_serial_number"}
	default:
		same, err := s.store.LiveDevicesBySerialTx(ctx, tx, *d.SerialNumber, d.ID)
		if err != nil {
			return err
		}
		if len(same) > 0 {
			duplicate, detail = true, map[string]any{"reason": "same_serial_number", "otherDevices": len(same)}
			if hasLink(d, LinkSerial) {
				if err := s.unlinkSerial(ctx, tx, c, d, "serial_duplicate"); err != nil {
					return err
				}
			}
			if err := s.flagPartners(ctx, tx, c, run, same, len(same)); err != nil {
				return err
			}
			break
		}
		if d.AssetID != nil {
			// A serial link that still holds.
			return s.resolveLinkFindings(ctx, tx, c, run, d.ID)
		}
		switch {
		case errors.Is(match.err, ErrAssetNotFound):
			noMatch, detail = true, map[string]any{"reason": "no_asset_with_serial_number"}
		case errors.Is(match.err, ErrAssetAmbiguous):
			conflict, detail = true, map[string]any{"reason": "serial_number_shared_by_assets"}
		case match.asset != nil && match.asset.Terminal():
			noMatch, detail = true, map[string]any{"reason": "asset_not_in_use"}
		case match.asset != nil:
			others, err := s.store.OtherLiveDevicesByAssetTx(ctx, tx, match.asset.ID, d.ID)
			if err != nil {
				return err
			}
			if len(others) > 0 {
				duplicate, detail = true, map[string]any{"reason": "asset_linked_to_other_device", "otherDevices": len(others)}
			} else {
				link = match.asset
			}
		}
	}
	if link != nil {
		linked, ok, err := s.autoLink(ctx, tx, c, d, link)
		if err != nil {
			return err
		}
		if ok {
			run.DevicesLinked++
			*d = linked
			return s.resolveLinkFindings(ctx, tx, c, run, d.ID)
		}
		// The Asset was taken by another device in the meantime.
		duplicate, detail = true, map[string]any{"reason": "asset_linked_to_other_device"}
	}
	for _, f := range []struct {
		kind string
		want bool
	}{{FindingNoAssetMatch, noMatch}, {FindingSerialConflict, conflict}, {FindingDuplicateDevice, duplicate}} {
		if err := s.reconcileFinding(ctx, tx, c, run, f.kind, d.ID, f.want, detail); err != nil {
			return err
		}
	}
	return nil
}

// autoLink links by serial number inside a savepoint, so losing the race for the Asset (unique
// violation) is a finding and not a failed run.
func (s *Service) autoLink(ctx context.Context, tx pgx.Tx, c Caller, d *Device, asset *AssetInfo) (Device, bool, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Device{}, false, fmt.Errorf("savepoint: %w", err)
	}
	cand := *d
	cand.AssetID, cand.AssetLinkSource = &asset.ID, strPtr(LinkSerial)
	updated, err := s.store.UpdateDeviceTx(ctx, sp, cand)
	if errors.Is(err, ErrConflict) {
		return Device{}, false, sp.Rollback(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		return Device{}, false, err
	}
	if err := sp.Commit(ctx); err != nil {
		return Device{}, false, fmt.Errorf("release savepoint: %w", err)
	}
	actor, meta := systemChange(c, d, map[string]any{"method": LinkSerial})
	if err := s.recordLinkAs(ctx, tx, actor, c, "endpoints.device.linked", d, &updated, meta); err != nil {
		return Device{}, false, err
	}
	if err := publish(ctx, tx, c, "DeviceLinked", map[string]any{"deviceId": updated.ID, "assetId": asset.ID, "method": LinkSerial}); err != nil {
		return Device{}, false, err
	}
	return updated, true, nil
}

// flagPartners handles the other live devices that share a serial number: their serial-source links
// are dropped and they get the duplicate_device finding too. Blocked, manually linked and personal
// devices are left alone.
func (s *Service) flagPartners(ctx context.Context, tx pgx.Tx, c Caller, run *counters, partners []Device, others int) error {
	for i := range partners {
		o := &partners[i]
		if o.AutoLinkBlocked || hasLink(o, LinkManual) || o.Ownership == "personal" {
			continue
		}
		if hasLink(o, LinkSerial) {
			if err := s.unlinkSerial(ctx, tx, c, o, "serial_duplicate"); err != nil {
				return err
			}
		}
		if err := s.reconcileFinding(ctx, tx, c, run, FindingDuplicateDevice, o.ID, true, map[string]any{"reason": "same_serial_number", "otherDevices": others}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ingestSoftware(ctx context.Context, tx pgx.Tx, c Caller, run *counters, deviceID string, in deviceInput) error {
	items := make([]InstallationInput, 0, len(in.Software))
	keys := make([]string, 0, len(in.Software))
	for _, sw := range in.Software {
		keys = append(keys, NormalizeSoftwareName(sw.Name))
	}
	aliases, err := s.store.ResolveAliasesTx(ctx, tx, keys)
	if err != nil {
		return err
	}
	for i, sw := range in.Software {
		it := InstallationInput{Name: sw.Name, Version: sw.Version, Key: keys[i]}
		if id, ok := aliases[keys[i]]; ok {
			it.ProductID = &id
		}
		if sw.Publisher != "" {
			p := sw.Publisher
			it.Publisher = &p
		}
		items = append(items, it)
	}
	if err := s.store.UpsertInstallationsTx(ctx, tx, deviceID, items, in.ObservedAt, in.SyncedAt); err != nil {
		return err
	}
	run.SoftwareObserved += len(in.Software)
	if err := s.store.TombstoneInstallationsTx(ctx, tx, deviceID, in.SyncedAt, in.SyncedAt); err != nil {
		return err
	}
	unmatched, err := s.store.CountUnmatchedTx(ctx, tx, deviceID)
	if err != nil {
		return err
	}
	return s.reconcileFinding(ctx, tx, c, run, FindingUnmatchedSoftware, deviceID, unmatched > 0, map[string]any{"count": unmatched})
}
