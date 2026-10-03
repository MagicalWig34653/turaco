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

// Snapshot is a complete view of a provider's devices. Because it is complete, devices missing from
// it are tombstoned (unless it is empty, which is treated as a provider failure and changes nothing).
type Snapshot struct {
	Provider string
	// Source is SourceSync (live provider) or SourceImport (file import).
	Source  string
	Devices []SnapshotDevice
}

// counters tallies what an ingestion run did.
type counters struct {
	DevicesCreated    int
	DevicesUpdated    int
	DevicesUnchanged  int
	DevicesTombstoned int
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
	// DevicesRejected counts records that were invalid (missing id, name or serial with unsafe content).
	DevicesRejected  int
	DevicesLinked    int
	SoftwareObserved int
	// SoftwareSkipped counts installations dropped as invalid or beyond the per-device limit.
	SoftwareSkipped  int
	FindingsRaised   int
	FindingsResolved int
	// SoftwareErrors counts devices whose software could not be read (Sync only).
	SoftwareErrors int
}

func resultOf(c counters) IngestResult {
	return IngestResult{
		DevicesCreated: c.DevicesCreated, DevicesUpdated: c.DevicesUpdated, DevicesUnchanged: c.DevicesUnchanged,
		DevicesTombstoned: c.DevicesTombstoned, DevicesRejected: c.DevicesRejected, DevicesLinked: c.DevicesLinked,
		SoftwareObserved: c.SoftwareObserved, SoftwareSkipped: c.SoftwareSkipped,
		FindingsRaised: c.FindingsRaised, FindingsResolved: c.FindingsResolved,
	}
}

// Sync reads the configured provider and ingests the snapshot. Requires endpoints.manage and the
// provider synchronization to be enabled (INTUNE_SYNC).
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
	devices, err := s.provider.Devices(ctx)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read devices: %w", err)
	}
	snap := Snapshot{Provider: intune.ProviderKey, Source: SourceSync, Devices: make([]SnapshotDevice, 0, len(devices))}
	softwareErrors := 0
	for _, d := range devices {
		sd := SnapshotDevice{Record: d}
		if sw, err := s.provider.Software(ctx, d.ExternalID); err == nil {
			sd.Software, sd.SoftwareKnown = sw, true
		} else if ctx.Err() != nil {
			return IngestResult{}, ctx.Err()
		} else {
			softwareErrors++
		}
		snap.Devices = append(snap.Devices, sd)
	}
	res, err := s.Ingest(ctx, c, p, snap)
	res.SoftwareErrors = softwareErrors
	return res, err
}

// Ingest stores a snapshot idempotently: devices are upserted in batches of BatchSize (one short
// transaction each), a change history row is appended only when normalized values change, devices
// are linked to Assets by serial number, findings are raised and resolved, software is normalized
// through aliases, and devices missing from a non-empty snapshot are tombstoned. Ingesting the same
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
	// Truncated to the database precision so the tombstone comparison below is exact.
	runAt := s.now().UTC().Truncate(time.Microsecond)
	var total counters
	seen := map[string]bool{}
	for start := 0; start < len(snap.Devices); start += BatchSize {
		end := min(start+BatchSize, len(snap.Devices))
		batch := snap.Devices[start:end]
		matches := s.matchAssets(ctx, batch)
		var got counters
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			got = counters{}
			for i, sd := range batch {
				in, ok := normalizeDevice(snap, sd, runAt)
				if !ok || seen[in.ExternalID] {
					got.DevicesRejected++
					continue
				}
				seen[in.ExternalID] = true
				if err := s.ingestDevice(ctx, tx, c, &got, in, matches[i]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return IngestResult{}, err
		}
		total.add(got)
	}
	if len(seen) > 0 {
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			ids, err := s.store.TombstoneMissingTx(ctx, tx, snap.Provider, runAt, runAt)
			if err != nil {
				return err
			}
			total.DevicesTombstoned += len(ids)
			for _, id := range ids {
				for _, kind := range FindingKinds {
					if err := s.reconcileFinding(ctx, tx, c, &total, kind, id, false, nil); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return IngestResult{}, err
		}
	}
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			return fmt.Errorf("generate id: %w", err)
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "endpoints.sync.completed", TargetType: "endpoint_sync", TargetID: id, Actor: c.Actor, CorrelationID: c.CorrelationID,
			Metadata: map[string]any{
				"provider": snap.Provider, "source": snap.Source, "created": total.DevicesCreated, "updated": total.DevicesUpdated,
				"unchanged": total.DevicesUnchanged, "tombstoned": total.DevicesTombstoned, "rejected": total.DevicesRejected,
				"linked": total.DevicesLinked, "findingsRaised": total.FindingsRaised, "findingsResolved": total.FindingsResolved,
			},
		})
	})
	if err != nil {
		return IngestResult{}, err
	}
	return resultOf(total), nil
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

func normalizeDevice(snap Snapshot, sd SnapshotDevice, at time.Time) (deviceInput, bool) {
	r := sd.Record
	id, ok := cleanRequired(r.ExternalID, 200)
	if !ok {
		return deviceInput{}, false
	}
	name, ok := cleanRequired(r.Name, 256)
	if !ok {
		return deviceInput{}, false
	}
	var serial *string
	if strings.TrimSpace(r.SerialNumber) != "" {
		v, ok := cleanRequired(r.SerialNumber, 100)
		if !ok {
			return deviceInput{}, false
		}
		serial = &v
	}
	var checkin *time.Time
	if r.LastCheckinAt != nil {
		t := r.LastCheckinAt.UTC().Truncate(time.Microsecond)
		checkin = &t
	}
	in := deviceInput{
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
	return in, true
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
	err   error // nil, ErrAssetNotFound or ErrAssetAmbiguous (lookup failures abort via lookupErr)
	fail  error
}

func (s *Service) matchAssets(ctx context.Context, batch []SnapshotDevice) []assetMatch {
	out := make([]assetMatch, len(batch))
	for i, sd := range batch {
		serial := strings.TrimSpace(sd.Record.SerialNumber)
		if serial == "" {
			out[i].err = ErrAssetNotFound
			continue
		}
		a, err := s.assets.FindBySerial(ctx, serial)
		switch {
		case err == nil:
			out[i].asset = &a
		case errors.Is(err, ErrAssetNotFound), errors.Is(err, ErrAssetAmbiguous):
			out[i].err = err
		default:
			out[i].fail = err
		}
	}
	return out
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
	d, err := s.upsertDevice(ctx, tx, run, in)
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

// upsertDevice creates the device or updates it when normalized values changed; the history gets a
// row only in those cases.
func (s *Service) upsertDevice(ctx context.Context, tx pgx.Tx, run *counters, in deviceInput) (Device, error) {
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
		// A concurrent run created it first.
		if d, err = s.store.LockDeviceByExternalTx(ctx, tx, in.Provider, in.ExternalID); err != nil || d == nil {
			return Device{}, fmt.Errorf("lock device after concurrent create: %w", err)
		}
	}
	changed := d.Name != in.Name || !sameStr(d.SerialNumber, in.SerialNumber) || d.OSPlatform != in.OSPlatform ||
		!sameStr(d.OSVersion, in.OSVersion) || !sameStr(d.Manufacturer, in.Manufacturer) || !sameStr(d.Model, in.Model) ||
		d.Ownership != in.Ownership || d.ComplianceState != in.ComplianceState
	if !changed {
		run.DevicesUnchanged++
		if err := s.store.TouchDeviceTx(ctx, tx, d.ID, in.ObservedAt, in.SyncedAt, in.LastCheckinAt); err != nil {
			return Device{}, err
		}
		d.ObservedAt, d.LastSyncedAt, d.LastCheckinAt, d.DeletedObservedAt = in.ObservedAt, in.SyncedAt, in.LastCheckinAt, nil
		return *d, nil
	}
	serialChanged := !sameSerial(d.SerialNumber, in.SerialNumber)
	d.Name, d.SerialNumber, d.OSPlatform, d.OSVersion, d.Manufacturer, d.Model = in.Name, in.SerialNumber, in.OSPlatform, in.OSVersion, in.Manufacturer, in.Model
	d.Ownership, d.ComplianceState, d.LastCheckinAt, d.Source = in.Ownership, in.ComplianceState, in.LastCheckinAt, in.Source
	d.ObservedAt, d.LastSyncedAt, d.DeletedObservedAt = in.ObservedAt, in.SyncedAt, nil
	// A link made by serial number is only as good as the serial number: drop it, then match again.
	if serialChanged && d.AssetLinkSource != nil && *d.AssetLinkSource == LinkSerial {
		d.AssetID, d.AssetLinkSource = nil, nil
	}
	updated, err := s.store.UpdateDeviceTx(ctx, tx, *d)
	if err != nil {
		return Device{}, err
	}
	run.DevicesUpdated++
	return updated, s.store.AppendHistoryTx(ctx, tx, historyOf(updated))
}

func historyOf(d Device) History {
	return History{
		DeviceID: d.ID, Name: d.Name, SerialNumber: d.SerialNumber, OSPlatform: d.OSPlatform, OSVersion: d.OSVersion,
		Manufacturer: d.Manufacturer, Model: d.Model, Ownership: d.Ownership, ComplianceState: d.ComplianceState,
		Source: d.Source, ObservedAt: d.ObservedAt,
	}
}

// reconcileLink links an unlinked device to the Asset with its serial number and keeps the
// no_asset_match, serial_conflict and duplicate_device findings in step. A manual unlink stops it.
func (s *Service) reconcileLink(ctx context.Context, tx pgx.Tx, c Caller, run *counters, d *Device, match assetMatch) error {
	if d.AutoLinkBlocked || d.AssetID != nil {
		return s.resolveLinkFindings(ctx, tx, c, run, d.ID)
	}
	var noMatch, conflict, duplicate bool
	var detail map[string]any
	var link *AssetInfo
	switch {
	case d.SerialNumber == nil:
		noMatch, detail = true, map[string]any{"reason": "no_serial_number"}
	default:
		same, err := s.store.OtherLiveDevicesBySerialTx(ctx, tx, *d.SerialNumber, d.ID)
		if err != nil {
			return err
		}
		if len(same) > 0 {
			duplicate, detail = true, map[string]any{"reason": "same_serial_number", "otherDevices": len(same)}
			break
		}
		switch {
		case errors.Is(match.err, ErrAssetNotFound):
			noMatch, detail = true, map[string]any{"reason": "no_asset_with_serial_number"}
		case errors.Is(match.err, ErrAssetAmbiguous):
			conflict, detail = true, map[string]any{"reason": "serial_number_shared_by_assets"}
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
		before := *d
		d.AssetID, d.AssetLinkSource = &link.ID, strPtr(LinkSerial)
		updated, err := s.store.UpdateDeviceTx(ctx, tx, *d)
		if err != nil {
			return err
		}
		*d = updated
		run.DevicesLinked++
		if err := s.recordLink(ctx, tx, c, "endpoints.device.linked", &before, &updated, map[string]any{"method": LinkSerial}); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, "DeviceLinked", map[string]any{"deviceId": updated.ID, "assetId": link.ID, "method": LinkSerial}); err != nil {
			return err
		}
		return s.resolveLinkFindings(ctx, tx, c, run, d.ID)
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

func (s *Service) ingestSoftware(ctx context.Context, tx pgx.Tx, c Caller, run *counters, deviceID string, in deviceInput) error {
	keys := make([]string, 0, len(in.Software))
	for _, sw := range in.Software {
		keys = append(keys, NormalizeSoftwareName(sw.Name))
	}
	aliases, err := s.store.ResolveAliasesTx(ctx, tx, keys)
	if err != nil {
		return err
	}
	for i, sw := range in.Software {
		var product *string
		if id, ok := aliases[keys[i]]; ok {
			product = &id
		}
		var publisher *string
		if sw.Publisher != "" {
			publisher = &sw.Publisher
		}
		if err := s.store.UpsertInstallationTx(ctx, tx, deviceID, product, sw.Name, sw.Version, publisher, in.ObservedAt, in.SyncedAt); err != nil {
			return err
		}
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
