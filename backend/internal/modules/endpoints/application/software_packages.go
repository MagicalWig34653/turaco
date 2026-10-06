package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Software Packages (F9 G1). Audit actions: endpoints.software_package.<requested|publish_requested> and
// endpoints.software_package_sync.<completed|failed>. Provider reports are observations: they are stored with
// source and freshness, appended to the package history only when they change something, and never mark a
// version approved or a deployment successful.

// SoftwareSyncActor is the system actor of the package synchronization.
const SoftwareSyncActor = "software-package-sync"

// SearchCatalog searches the Software Management Provider's catalog. The provider's answer is untrusted:
// entries with invalid values are left out. Requires a software permission.
func (s *Service) SearchCatalog(ctx context.Context, p Principal, query string) ([]CatalogEntry, error) {
	if !p.canViewSoftware() {
		return nil, ErrForbidden
	}
	q, ok := cleanSoftwareLine(query, 100)
	if !ok || utf8.RuneCountInString(q) < 2 {
		return nil, invalid("q must be 2-100 characters without control or invisible formatting characters")
	}
	raw, err := s.software.SearchCatalog(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []CatalogEntry{}
	for _, e := range raw {
		id, ok1 := cleanSoftwareLine(e.ProviderID, 200)
		name, ok2 := cleanSoftwareLine(e.Name, 200)
		pub, ok3 := cleanSoftwareLine(e.Publisher, 200)
		ver, ok4 := cleanSoftwareLine(e.LatestVersion, 100)
		if !ok1 || !ok2 || !ok3 || !ok4 || id == "" || name == "" {
			continue
		}
		entry := CatalogEntry{ProviderID: id, Name: name, Publisher: strPtr(pub), LatestVersion: strPtr(ver)}
		if u, ok := cleanHTTPSURL(e.SourceURL, maxInstallerURL); ok {
			entry.SourceURL = &u
		}
		out = append(out, entry)
		if len(out) == MaxCatalogResults {
			break
		}
	}
	return out, nil
}

// requireApprovedBinding checks that the version is approved and its product approved.
func (s *Service) requireApprovedBinding(ctx context.Context, tx pgx.Tx, v SoftwareVersion) error {
	if v.ApprovalStatus != VersionApproved {
		return &GateError{Code: "version_not_approved"}
	}
	prod, err := s.store.LockProductTx(ctx, tx, v.ProductID)
	if err != nil {
		return err
	}
	if prod.ApprovalStatus != ProductApproved {
		return &GateError{Code: "product_not_approved"}
	}
	return nil
}

// PackageVersion asks the Software Management Provider to package an approved version of an approved product.
// The package row is recorded as requested first; the provider call is idempotent per operation key (version
// id plus installer hash), so a retry after a failure or a repeated request never creates a second package.
// expectedVersion is the version's. Requires software.package.
func (s *Service) PackageVersion(ctx context.Context, c Caller, p Principal, versionID string, expectedVersion *int) (SoftwarePackage, error) {
	if err := c.validate(); err != nil {
		return SoftwarePackage{}, err
	}
	if !p.SoftwarePackage {
		return SoftwarePackage{}, ErrForbidden
	}
	if c.Actor.UserID == "" {
		return SoftwarePackage{}, invalid("a package is requested by a person")
	}
	if expectedVersion == nil {
		return SoftwarePackage{}, invalid("expectedVersion is required")
	}
	if !validUUID(versionID) {
		return SoftwarePackage{}, ErrNotFound
	}
	var pkg SoftwarePackage
	var ver SoftwareVersion
	var prodName string
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		v, err := s.store.LockVersionTx(ctx, tx, strings.ToLower(versionID))
		if err != nil {
			return err
		}
		if *expectedVersion != v.Version {
			return ErrVersionConflict
		}
		if err := s.requireApprovedBinding(ctx, tx, v); err != nil {
			return err
		}
		ver, prodName = v, v.ProductName
		var created bool
		if pkg, created, err = s.store.InsertPackageTx(ctx, tx, s.softwareKey, v.ID, c.Actor.UserID); err != nil || !created {
			return err
		}
		return softwareAudit(ctx, tx, c, "endpoints.software_package.requested", "software_package", pkg.ID, nil,
			map[string]any{"status": pkg.Status, "version": pkg.Version},
			map[string]any{"versionId": v.ID, "provider": s.softwareKey, "installerSha256": v.InstallerSHA256})
	})
	if err != nil {
		return SoftwarePackage{}, err
	}
	if pkg.Status != PackageRequested && pkg.Status != PackageFailed {
		return pkg, nil
	}
	pub := ""
	if ver.Publisher != nil {
		pub = *ver.Publisher
	}
	rec, err := s.software.Package(ctx, softwaremgmt.PackageRequest{ProductKey: ver.ProductID, ProductName: prodName, Publisher: pub,
		Version: ver.ProductVersion, InstallerURL: ver.InstallerURL, InstallerSHA256: ver.InstallerSHA256,
		InstallCommand: ver.InstallCommand, DetectionRule: ver.DetectionRule}, ver.ID+":"+ver.InstallerSHA256)
	if err != nil {
		return SoftwarePackage{}, fmt.Errorf("package version: %w", err)
	}
	return s.applyReport(ctx, c, pkg.ID, rec, sourceOperation)
}

// PublishPackage publishes a packaged package into the Management Provider. It requires an approved version of
// an approved product and that the installer hash the provider reported equals the approved hash; otherwise it
// is refused before the provider is called. A package already published is returned unchanged. The provider
// call is idempotent per operation key. Requires software.package.
func (s *Service) PublishPackage(ctx context.Context, c Caller, p Principal, packageID string, expectedVersion *int) (SoftwarePackage, error) {
	if err := c.validate(); err != nil {
		return SoftwarePackage{}, err
	}
	if !p.SoftwarePackage {
		return SoftwarePackage{}, ErrForbidden
	}
	if c.Actor.UserID == "" {
		return SoftwarePackage{}, invalid("a package is published by a person")
	}
	if expectedVersion == nil {
		return SoftwarePackage{}, invalid("expectedVersion is required")
	}
	if !validUUID(packageID) {
		return SoftwarePackage{}, ErrNotFound
	}
	var pkg SoftwarePackage
	var ver SoftwareVersion
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockPackageTx(ctx, tx, strings.ToLower(packageID))
		if err != nil {
			return err
		}
		if *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status == PackagePublished && cur.PublishRequestedBy != nil {
			pkg = cur
			return nil
		}
		if ver, err = s.store.LockVersionTx(ctx, tx, cur.VersionID); err != nil {
			return err
		}
		if err := s.requireApprovedBinding(ctx, tx, ver); err != nil {
			return err
		}
		if cur.Status != PackagePackaged && cur.Status != PackagePublished || cur.ProviderPackageID == nil {
			return &GateError{Code: "package_not_ready"}
		}
		if cur.InstallerSHA256 == nil || *cur.InstallerSHA256 != ver.InstallerSHA256 {
			return &GateError{Code: "hash_mismatch"}
		}
		next := cur
		now := s.now()
		target := s.viewProvider
		next.PublishRequestedBy, next.PublishRequestedAt, next.ManagementProvider = &c.Actor.UserID, &now, &target
		if pkg, err = s.store.UpdatePackageTx(ctx, tx, next); err != nil {
			return err
		}
		return softwareAudit(ctx, tx, c, "endpoints.software_package.publish_requested", "software_package", cur.ID,
			map[string]any{"status": cur.Status, "version": cur.Version}, map[string]any{"status": pkg.Status, "version": pkg.Version},
			map[string]any{"versionId": ver.ID, "managementProvider": target, "installerSha256": ver.InstallerSHA256})
	})
	if err != nil {
		return SoftwarePackage{}, err
	}
	if pkg.Status == PackagePublished && ver.ID == "" {
		return pkg, nil
	}
	rec, err := s.software.Publish(ctx, *pkg.ProviderPackageID, softwaremgmt.ProviderTarget{ManagementProvider: *pkg.ManagementProvider},
		pkg.ID+":publish:"+ver.InstallerSHA256)
	if err != nil {
		return SoftwarePackage{}, fmt.Errorf("publish package: %w", err)
	}
	return s.applyReport(ctx, c, pkg.ID, rec, sourceOperation)
}

const (
	sourceOperation = "operation"
	sourceSync      = "sync"
)

// applyReport stores a provider report about one package in its own transaction.
func (s *Service) applyReport(ctx context.Context, c Caller, packageID string, rec softwaremgmt.PackageRecord, source string) (SoftwarePackage, error) {
	var out SoftwarePackage
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, _, err = s.applyPackageRecordTx(ctx, tx, c, packageID, rec, source, &SoftwarePackageSyncResult{})
		return err
	})
	return out, err
}

// normalizedRecord is a validated provider report; invalid values become unknown (nil), an unknown status
// becomes failed.
type normalizedRecord struct {
	providerID, hash, artifact *string
	status                     string
	observedAt                 time.Time
}

func (s *Service) normalizeRecord(rec softwaremgmt.PackageRecord) normalizedRecord {
	n := normalizedRecord{status: rec.Status, observedAt: rec.ObservedAt.UTC().Truncate(time.Microsecond)}
	if !slices.Contains(softwaremgmt.Statuses, n.status) {
		n.status = PackageFailed
	}
	if id, ok := cleanSoftwareLine(rec.ProviderPackageID, 200); ok && id != "" {
		n.providerID = &id
	}
	if h := strings.ToLower(strings.TrimSpace(rec.InstallerSHA256)); validHex64(h) {
		n.hash = &h
	}
	if a, ok := cleanSoftwareLine(rec.ManagementArtifactExternalID, 200); ok && a != "" {
		n.artifact = &a
	}
	now := s.now().UTC()
	if n.observedAt.IsZero() || n.observedAt.After(now) || n.observedAt.Year() < 1990 {
		n.observedAt = now.Truncate(time.Microsecond)
	}
	return n
}

func eqPtr(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// applyPackageRecordTx applies one provider report: it updates the package with source and freshness, appends
// a history row only when the reported state changed, links the Management Artifact once the management sync
// has ingested it, raises or resolves package_hash_mismatch and publishes SoftwarePackagePublished when the
// package became published. It reports whether the state changed.
func (s *Service) applyPackageRecordTx(ctx context.Context, tx pgx.Tx, c Caller, packageID string, rec softwaremgmt.PackageRecord, source string, run *SoftwarePackageSyncResult) (SoftwarePackage, bool, error) {
	cur, err := s.store.LockPackageTx(ctx, tx, packageID)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	ver, err := s.store.LockVersionTx(ctx, tx, cur.VersionID)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	n := s.normalizeRecord(rec)
	if n.providerID == nil || (cur.ProviderPackageID != nil && *cur.ProviderPackageID != *n.providerID) {
		// A report without an id, or about another package, is not applied.
		return cur, false, fmt.Errorf("endpoints: provider report does not match package %s", cur.ID)
	}
	next := cur
	next.ProviderPackageID, next.Status, next.InstallerSHA256 = n.providerID, n.status, n.hash
	if n.artifact != nil {
		next.ManagementArtifactExternalID = n.artifact
	}
	if !eqPtr(next.ManagementArtifactExternalID, cur.ManagementArtifactExternalID) {
		next.ManagementArtifactID = nil
	}
	if next.ManagementArtifactExternalID != nil && next.ManagementArtifactID == nil {
		mp := s.viewProvider
		if next.ManagementProvider != nil {
			mp = *next.ManagementProvider
		}
		id, err := s.store.ArtifactIDByExternalTx(ctx, tx, mp, *next.ManagementArtifactExternalID)
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		if id != nil {
			next.ManagementArtifactID = id
			run.Linked++
		}
	}
	changed := next.Status != cur.Status || !eqPtr(next.InstallerSHA256, cur.InstallerSHA256) ||
		!eqPtr(next.ProviderPackageID, cur.ProviderPackageID) || !eqPtr(next.ManagementArtifactExternalID, cur.ManagementArtifactExternalID)
	linked := !eqPtr(next.ManagementArtifactID, cur.ManagementArtifactID)
	observed := n.observedAt
	next.ObservedAt, next.Source = &observed, source
	if source == sourceSync {
		now := s.now().UTC()
		next.LastSyncedAt = &now
	}
	updated, err := s.store.UpdatePackageTx(ctx, tx, next)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	if cur.ProviderPackageID == nil {
		if err := s.store.RecordProviderReferenceTx(ctx, tx, updated.Provider, updated.ID, *updated.ProviderPackageID); err != nil {
			return SoftwarePackage{}, false, err
		}
	}
	if changed {
		if err := s.store.AppendPackageObservationTx(ctx, tx, PackageObservation{PackageID: cur.ID, Status: updated.Status, ProviderPackageID: updated.ProviderPackageID,
			InstallerSHA256: updated.InstallerSHA256, ManagementArtifactExternalID: updated.ManagementArtifactExternalID, Source: source, ObservedAt: observed}); err != nil {
			return SoftwarePackage{}, false, err
		}
	}
	if updated.Status == PackagePublished && cur.Status != PackagePublished {
		if err := publish(ctx, tx, c, EventSoftwarePackagePublished, map[string]any{"packageId": updated.ID, "versionId": updated.VersionID, "provider": updated.Provider}); err != nil {
			return SoftwarePackage{}, false, err
		}
	}
	mismatch := updated.InstallerSHA256 != nil && *updated.InstallerSHA256 != ver.InstallerSHA256
	if mismatch {
		detail, err := json.Marshal(map[string]any{"versionId": ver.ID, "approvedSha256": ver.InstallerSHA256, "reportedSha256": *updated.InstallerSHA256})
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		id, raised, err := s.store.OpenPackageFindingTx(ctx, tx, FindingPackageHashMismatch, updated.ID, detail)
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		if raised {
			run.FindingsRaised++
			if err := publish(ctx, tx, c, "EndpointFindingRaised", map[string]any{"findingId": id, "softwarePackageId": updated.ID, "kind": FindingPackageHashMismatch}); err != nil {
				return SoftwarePackage{}, false, err
			}
		}
	} else {
		resolved, err := s.store.ResolvePackageFindingTx(ctx, tx, FindingPackageHashMismatch, updated.ID)
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		if resolved {
			run.FindingsResolved++
		}
	}
	updated.HashMismatch = mismatch
	return updated, changed || linked, nil
}

// SyncPackages reads the status of every known package from the Software Management Provider, stores what
// changed and links published packages to their Management Artifact once the management sync has ingested it.
// It needs SOFTWARE_PROVIDER_SYNC and software.package; one run per provider at a time.
func (s *Service) SyncPackages(ctx context.Context, c Caller, p Principal) (SoftwarePackageSyncResult, error) {
	if err := c.validate(); err != nil {
		return SoftwarePackageSyncResult{}, err
	}
	if !p.SoftwarePackage {
		return SoftwarePackageSyncResult{}, ErrForbidden
	}
	return s.syncPackages(ctx, c)
}

// HandleSoftwarePackageSync is the job handler of SoftwarePackageSyncJobType. With the synchronization off it
// does nothing.
func (s *Service) HandleSoftwarePackageSync(ctx context.Context, job jobs.Job) error {
	if !s.softwareSyncOn {
		return nil
	}
	_, err := s.syncPackages(ctx, Caller{Actor: audit.SystemActor(SoftwareSyncActor), CorrelationID: "job:" + job.ID})
	if errors.Is(err, ErrSyncRunning) {
		return nil
	}
	return err
}

func (s *Service) syncPackages(ctx context.Context, c Caller) (SoftwarePackageSyncResult, error) {
	run := SoftwarePackageSyncResult{}
	if !s.softwareSyncOn {
		return run, ErrSyncDisabled
	}
	unlock, ok, err := s.store.TryLockProvider(ctx, "software:"+s.softwareKey)
	if err != nil {
		return run, err
	}
	if !ok {
		return run, ErrSyncRunning
	}
	defer unlock()
	syncErr := s.syncPackagePages(ctx, c, &run)
	action := "endpoints.software_package_sync.completed"
	meta := map[string]any{"provider": s.softwareKey, "checked": run.Checked, "changed": run.Changed, "linked": run.Linked,
		"findingsRaised": run.FindingsRaised, "findingsResolved": run.FindingsResolved}
	if syncErr != nil {
		action = "endpoints.software_package_sync.failed"
		meta["errorCode"] = syncErrorCode(syncErr)
	}
	if err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "software_provider", TargetID: s.softwareKey, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: meta})
	}); err != nil {
		return run, errors.Join(syncErr, err)
	}
	return run, syncErr
}

func syncErrorCode(err error) string {
	if errors.Is(err, softwaremgmt.ErrNotConfigured) {
		return "provider_not_configured"
	}
	return "provider_error"
}

func (s *Service) syncPackagePages(ctx context.Context, c Caller, run *SoftwarePackageSyncResult) error {
	after := ""
	for {
		pkgs, err := s.store.SyncablePackages(ctx, s.softwareKey, after, softwareSyncBatch)
		if err != nil {
			return err
		}
		if len(pkgs) == 0 {
			return nil
		}
		byProvider := map[string]string{}
		ids := make([]string, 0, len(pkgs))
		for _, p := range pkgs {
			byProvider[*p.ProviderPackageID] = p.ID
			ids = append(ids, *p.ProviderPackageID)
		}
		recs, err := s.software.PackageStatus(ctx, ids)
		if err != nil {
			return fmt.Errorf("package status: %w", err)
		}
		for _, rec := range recs {
			id, ok := byProvider[strings.TrimSpace(rec.ProviderPackageID)]
			if !ok {
				continue
			}
			run.Checked++
			if err := s.store.InTx(ctx, func(tx pgx.Tx) error {
				_, changed, err := s.applyPackageRecordTx(ctx, tx, c, id, rec, sourceSync, run)
				if changed {
					run.Changed++
				}
				return err
			}); err != nil {
				return err
			}
		}
		if len(pkgs) < softwareSyncBatch {
			return nil
		}
		after = pkgs[len(pkgs)-1].ID
	}
}
