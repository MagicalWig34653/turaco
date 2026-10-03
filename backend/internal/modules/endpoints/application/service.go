package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Endpoints operations. Audit actions: endpoints.device.linked,
// endpoints.device.unlinked, endpoints.software_product.registered, endpoints.sync.completed and
// endpoints.sync.failed. Links the synchronization derives are audited with the sync system as actor
// and the triggering user in metadata.
// Audit entries carry ids, enumerated codes and counts only; provider-reported text such as device
// names and software names is never copied into audit.
type Service struct {
	store    Store
	assets   Assets
	provider intune.Provider
	syncOn   bool
	now      func() time.Time
	// syncCooldown is the minimum time between two manual synchronizations of the provider.
	syncCooldown time.Duration
}

// NewService creates the service. provider may be nil (synchronization then reports not configured);
// syncEnabled switches Sync on. now may be nil.
func NewService(store Store, assets Assets, provider intune.Provider, syncEnabled bool, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if provider == nil {
		provider = intune.NotConfigured{}
	}
	return &Service{store: store, assets: assets, provider: provider, syncOn: syncEnabled, now: now, syncCooldown: DefaultSyncCooldown}
}

// WithSyncCooldown sets the minimum time between the end of one Sync and the start of the next (0 disables it).
func (s *Service) WithSyncCooldown(d time.Duration) *Service {
	s.syncCooldown = d
	return s
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func linkState(d *Device) any {
	if d == nil {
		return nil
	}
	return map[string]any{"assetId": d.AssetID, "linkSource": d.AssetLinkSource, "autoLinkBlocked": d.AutoLinkBlocked, "version": d.Version}
}

func (s *Service) recordLink(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Device, meta map[string]any) error {
	return s.recordLinkAs(ctx, tx, c.Actor, c, action, before, after, meta)
}

// recordLinkAs audits a link change made by actor (a person, or the sync system for derived changes).
func (s *Service) recordLinkAs(ctx context.Context, tx pgx.Tx, actor audit.Actor, c Caller, action string, before, after *Device, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "device", TargetID: after.ID, Actor: actor,
		CorrelationID: c.CorrelationID, Before: linkState(before), After: linkState(after), Metadata: meta,
	})
}

// raiseFinding raises or resolves the finding of one kind so it matches want.
func (s *Service) reconcileFinding(ctx context.Context, tx pgx.Tx, c Caller, run *counters, kind, deviceID string, want bool, detail map[string]any) error {
	if !want {
		resolved, err := s.store.ResolveFindingTx(ctx, tx, kind, deviceID)
		if resolved {
			run.FindingsResolved++
		}
		return err
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal finding detail: %w", err)
	}
	id, raised, err := s.store.OpenFindingTx(ctx, tx, kind, deviceID, raw)
	if err != nil || !raised {
		return err
	}
	run.FindingsRaised++
	return publish(ctx, tx, c, "EndpointFindingRaised", map[string]any{"findingId": id, "deviceId": deviceID, "kind": kind})
}

// resolveLinkFindings resolves the findings that only make sense for an unlinked device.
func (s *Service) resolveLinkFindings(ctx context.Context, tx pgx.Tx, c Caller, run *counters, deviceID string) error {
	for _, kind := range []string{FindingNoAssetMatch, FindingSerialConflict, FindingDuplicateDevice} {
		if err := s.reconcileFinding(ctx, tx, c, run, kind, deviceID, false, nil); err != nil {
			return err
		}
	}
	return nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}

// ---- manual link and unlink ----

// ManualLink links a Device to an Asset by hand and stops automatic matching from undoing it later.
// reason is one of ReasonCodes. Linking the same Asset again is a no-op; a Device already linked to
// another Asset must be unlinked first. Requires endpoints.manage.
func (s *Service) ManualLink(ctx context.Context, c Caller, p Principal, deviceID, assetID, reason string, expectedVersion *int) (Device, error) {
	if err := c.validate(); err != nil {
		return Device{}, err
	}
	if !p.Manage {
		return Device{}, ErrForbidden
	}
	// Binding an Asset reveals that it exists and what state it is in, so assets.view is required too.
	if !p.AssetsView {
		return Device{}, ErrForbidden
	}
	if !slices.Contains(ReasonCodes, reason) {
		return Device{}, invalid("reason must be one of %s", strings.Join(ReasonCodes, ", "))
	}
	if expectedVersion == nil {
		return Device{}, invalid("expectedVersion is required")
	}
	if !validUUID(deviceID) {
		return Device{}, ErrNotFound
	}
	if !validUUID(assetID) {
		return Device{}, ErrAssetInvalid
	}
	asset, exists, err := s.assets.ByID(ctx, assetID)
	if err != nil {
		return Device{}, fmt.Errorf("check asset: %w", err)
	}
	if !exists || asset.Terminal() {
		return Device{}, ErrAssetInvalid
	}
	var out Device
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		d, err := s.store.LockDeviceTx(ctx, tx, deviceID)
		if err != nil {
			return err
		}
		if *expectedVersion != d.Version {
			return ErrVersionConflict
		}
		if d.DeletedObservedAt != nil {
			return ErrConflict
		}
		if d.AssetID != nil {
			if *d.AssetID == assetID && d.AssetLinkSource != nil && *d.AssetLinkSource == LinkManual {
				out = d
				return nil
			}
			return ErrConflict
		}
		others, err := s.store.OtherLiveDevicesByAssetTx(ctx, tx, assetID, d.ID)
		if err != nil {
			return err
		}
		if len(others) > 0 {
			return ErrConflict
		}
		before := d
		d.AssetID, d.AssetLinkSource, d.AutoLinkBlocked = &assetID, strPtr(LinkManual), false
		updated, err := s.store.UpdateDeviceTx(ctx, tx, d)
		if err != nil {
			return err
		}
		if err := s.recordLink(ctx, tx, c, "endpoints.device.linked", &before, &updated, map[string]any{"method": LinkManual, "reason": reason}); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, "DeviceLinked", map[string]any{"deviceId": updated.ID, "assetId": assetID, "method": LinkManual}); err != nil {
			return err
		}
		if err := s.resolveLinkFindings(ctx, tx, c, &counters{}, updated.ID); err != nil {
			return err
		}
		out = updated
		return nil
	})
	return out, err
}

// ManualUnlink removes the link between a Device and its Asset and stops automatic matching from
// restoring it. reason is one of ReasonCodes. Requires endpoints.manage.
func (s *Service) ManualUnlink(ctx context.Context, c Caller, p Principal, deviceID, reason string, expectedVersion *int) (Device, error) {
	if err := c.validate(); err != nil {
		return Device{}, err
	}
	if !p.Manage {
		return Device{}, ErrForbidden
	}
	if !slices.Contains(ReasonCodes, reason) {
		return Device{}, invalid("reason must be one of %s", strings.Join(ReasonCodes, ", "))
	}
	if expectedVersion == nil {
		return Device{}, invalid("expectedVersion is required")
	}
	if !validUUID(deviceID) {
		return Device{}, ErrNotFound
	}
	var out Device
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		d, err := s.store.LockDeviceTx(ctx, tx, deviceID)
		if err != nil {
			return err
		}
		if *expectedVersion != d.Version {
			return ErrVersionConflict
		}
		if d.AssetID == nil {
			return ErrConflict
		}
		before := d
		d.AssetID, d.AssetLinkSource, d.AutoLinkBlocked = nil, nil, true
		updated, err := s.store.UpdateDeviceTx(ctx, tx, d)
		if err != nil {
			return err
		}
		out = updated
		if err := s.recordLink(ctx, tx, c, "endpoints.device.unlinked", &before, &updated, map[string]any{"method": LinkManual, "reason": reason}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "DeviceUnlinked", map[string]any{"deviceId": updated.ID, "assetId": before.AssetID, "method": LinkManual})
	})
	return out, err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- software products ----

// NormalizeSoftwareName is the deterministic alias key of a reported software name: lower case with
// single spaces.
func NormalizeSoftwareName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// RegisterSoftwareProduct defines a normalized software product with extra aliases (its own name is an
// alias too) and matches already observed, unmatched installations against it. Requires endpoints.manage.
func (s *Service) RegisterSoftwareProduct(ctx context.Context, c Caller, p Principal, name, publisher string, aliases []string) (ProductInfo, int, error) {
	if err := c.validate(); err != nil {
		return ProductInfo{}, 0, err
	}
	if !p.Manage {
		return ProductInfo{}, 0, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 200 || !utf8.ValidString(name) || safetext.ContainsUnsafe(name, false) {
		return ProductInfo{}, 0, invalid("name must be 1-200 characters without control or invisible formatting characters")
	}
	var pub *string
	if publisher = strings.TrimSpace(publisher); publisher != "" {
		if utf8.RuneCountInString(publisher) > 200 || !utf8.ValidString(publisher) || safetext.ContainsUnsafe(publisher, false) {
			return ProductInfo{}, 0, invalid("publisher must be at most 200 characters without control or invisible formatting characters")
		}
		pub = &publisher
	}
	if len(aliases) > 50 {
		return ProductInfo{}, 0, invalid("at most 50 aliases are allowed")
	}
	keys := []string{NormalizeSoftwareName(name)}
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" || utf8.RuneCountInString(a) > 300 || !utf8.ValidString(a) || safetext.ContainsUnsafe(a, false) {
			return ProductInfo{}, 0, invalid("an alias must be 1-300 characters without control or invisible formatting characters")
		}
		if k := NormalizeSoftwareName(a); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	var out ProductInfo
	var relinked int
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		prod, err := s.store.InsertProductTx(ctx, tx, name, pub, keys)
		if err != nil {
			return err
		}
		var devices []string
		if relinked, devices, err = s.store.RelinkInstallationsTx(ctx, tx); err != nil {
			return err
		}
		// Installations that matched may have cleared the devices' unmatched_software finding.
		run := &counters{}
		for _, id := range devices {
			unmatched, err := s.store.CountUnmatchedTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := s.reconcileFinding(ctx, tx, c, run, FindingUnmatchedSoftware, id, unmatched > 0, map[string]any{"count": unmatched}); err != nil {
				return err
			}
		}
		out = prod
		return audit.Record(ctx, tx, audit.Change{
			Action: "endpoints.software_product.registered", TargetType: "software_product", TargetID: prod.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"aliasCount": len(keys), "relinkedInstallations": relinked, "affectedDevices": len(devices)},
		})
	})
	return out, relinked, err
}

// ---- reads ----

// ListDevices lists devices. Requires endpoints.view.
func (s *Service) ListDevices(ctx context.Context, p Principal, f DeviceFilter) (DeviceResult, error) {
	if !p.canView() {
		return DeviceResult{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListDevices(ctx, f)
}

// GetDevice returns a device with its live software and open findings. Requires endpoints.view.
func (s *Service) GetDevice(ctx context.Context, p Principal, id string) (DeviceDetail, error) {
	if !p.canView() {
		return DeviceDetail{}, ErrForbidden
	}
	if !validUUID(id) {
		return DeviceDetail{}, ErrNotFound
	}
	d, err := s.store.GetDevice(ctx, id)
	if err != nil {
		return DeviceDetail{}, err
	}
	sw, err := s.store.Installations(ctx, id)
	if err != nil {
		return DeviceDetail{}, err
	}
	fs, err := s.store.OpenFindings(ctx, id)
	if err != nil {
		return DeviceDetail{}, err
	}
	return DeviceDetail{Device: d, Software: sw, Findings: fs}, nil
}

// ListFindings lists findings (open by default). Requires endpoints.view.
func (s *Service) ListFindings(ctx context.Context, p Principal, f FindingFilter) (FindingResult, error) {
	if !p.canView() {
		return FindingResult{}, ErrForbidden
	}
	if f.Status == "" {
		f.Status = FindingOpen
	}
	if f.Status != FindingOpen && f.Status != FindingResolved {
		return FindingResult{}, invalid("status must be open or resolved")
	}
	if f.Kind != "" && !slices.Contains(FindingKinds, f.Kind) {
		return FindingResult{}, invalid("kind must be one of %s", strings.Join(FindingKinds, ", "))
	}
	if f.DeviceID != "" && !validUUID(f.DeviceID) {
		return FindingResult{Items: []Finding{}}, nil
	}
	f.Page = f.Page.Normalize()
	return s.store.ListFindings(ctx, f)
}
