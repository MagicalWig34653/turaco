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

// Software Packages (F9 G1). Audit actions: endpoints.software_package.<requested|retry_requested|
// publish_requested|publish_completed|publish_failed|published_after_revoke> and
// endpoints.software_package_sync.<completed|failed>. Provider reports are observations: they are stored with
// source and freshness, appended to the package history only when they change something, and never mark a
// version approved or a deployment successful.
//
// Lock order everywhere: package -> version -> product. Rows that are mutated are locked FOR NO KEY UPDATE, the
// approval gate rows are read FOR SHARE.

// SoftwareSyncActor is the system actor of the package synchronization.
const SoftwareSyncActor = "software-package-sync"

// catalogLimiter is a fixed-window per-user limit of catalog searches (per API process).
type catalogLimiter struct {
	limit  int
	window time.Duration
	hits   map[string]catalogWindow
}

type catalogWindow struct {
	start time.Time
	n     int
}

// allow counts one search of user at now and reports whether it is within the limit.
func (l *catalogLimiter) allow(user string, now time.Time) bool {
	if l.limit <= 0 {
		return true
	}
	if l.hits == nil || len(l.hits) > 10000 {
		l.hits = map[string]catalogWindow{}
	}
	w := l.hits[user]
	if now.Sub(w.start) >= l.window || now.Before(w.start) {
		w = catalogWindow{start: now}
	}
	if w.n >= l.limit {
		return false
	}
	w.n++
	l.hits[user] = w
	return true
}

// WithCatalogSearchLimit sets how many catalog searches one user may run per window (0 disables the limit).
func (s *Service) WithCatalogSearchLimit(limit int, window time.Duration) *Service {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	s.catalog = catalogLimiter{limit: limit, window: window}
	return s
}

// WithSoftwareSyncCooldown sets the minimum time between the end of one package synchronization and the start
// of a manual one (0 disables it; default DefaultSyncCooldown).
func (s *Service) WithSoftwareSyncCooldown(d time.Duration) *Service {
	s.softwareSyncCooldown = d
	return s
}

// SearchCatalog searches the Software Management Provider's catalog. The provider's answer is untrusted:
// entries with invalid values are left out. Requires a software permission; each user may search
// DefaultCatalogSearchLimit times per DefaultCatalogSearchWindow (ErrRateLimited).
func (s *Service) SearchCatalog(ctx context.Context, p Principal, query string) ([]CatalogEntry, error) {
	if !p.canViewSoftware() {
		return nil, ErrForbidden
	}
	q, ok := cleanSoftwareLine(query, 100)
	if !ok || utf8.RuneCountInString(q) < 2 {
		return nil, invalid("q must be 2-100 characters without control or invisible formatting characters")
	}
	s.catalogMu.Lock()
	allowed := s.catalog.allow(p.UserID, s.now())
	s.catalogMu.Unlock()
	if !allowed {
		return nil, ErrRateLimited
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

// requireApprovedBinding checks that the version is approved and its product approved; the product is read FOR
// SHARE so it cannot change until the transaction ends.
func (s *Service) requireApprovedBinding(ctx context.Context, tx pgx.Tx, v SoftwareVersion) error {
	if v.ApprovalStatus != VersionApproved {
		return &GateError{Code: "version_not_approved"}
	}
	prod, err := s.store.ShareProductTx(ctx, tx, v.ProductID)
	if err != nil {
		return err
	}
	if prod.ApprovalStatus != ProductApproved {
		return &GateError{Code: "product_not_approved"}
	}
	return nil
}

// packageOpKey is the provider operation key of one packaging attempt.
func packageOpKey(v SoftwareVersion, attempt int) string {
	return fmt.Sprintf("%s:%s:attempt%d", v.ID, v.InstallerSHA256, attempt)
}

// publishOpKey is the provider operation key of one publication attempt.
func publishOpKey(p SoftwarePackage, v SoftwareVersion) string {
	return fmt.Sprintf("%s:publish:%s:attempt%d", p.ID, v.InstallerSHA256, p.PublishAttempt)
}

// PackageVersion asks the Software Management Provider to package an approved version of an approved product.
// The package row is recorded as requested first; the provider call is idempotent per operation key (version
// id, installer hash and attempt), so a retry after a transport failure or a repeated request never creates a
// second package. A package the provider reported failed is retried as a new attempt. expectedVersion is the
// version's. Requires software.package.
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
	call := false
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		vid := strings.ToLower(versionID)
		cur, found, err := s.store.LockPackageOfVersionTx(ctx, tx, s.softwareKey, vid)
		if err != nil {
			return err
		}
		v, err := s.store.ShareVersionTx(ctx, tx, vid)
		if err != nil {
			return err
		}
		if *expectedVersion != v.Version {
			return ErrVersionConflict
		}
		if err := s.requireApprovedBinding(ctx, tx, v); err != nil {
			return err
		}
		ver = v
		if !found {
			var created bool
			if cur, created, err = s.store.InsertPackageTx(ctx, tx, s.softwareKey, v.ID, c.Actor.UserID); err != nil {
				return err
			}
			if created {
				pkg, call = cur, true
				return softwareAudit(ctx, tx, c, "endpoints.software_package.requested", "software_package", pkg.ID, nil,
					map[string]any{"status": pkg.Status, "version": pkg.Version},
					map[string]any{"versionId": v.ID, "provider": s.softwareKey, "installerSha256": v.InstallerSHA256, "attempt": pkg.PackageAttempt})
			}
		}
		switch cur.Status {
		case PackageRequested:
			// The provider never answered this attempt: retry it with the same operation key.
			pkg, call = cur, true
			return nil
		case PackageFailed:
			next := cur
			next.Status, next.PackageAttempt, next.Source = PackageRequested, cur.PackageAttempt+1, sourceOperation
			next.ProviderPackageID, next.InstallerSHA256, next.ManagementArtifactExternalID, next.ManagementArtifactID = nil, nil, nil, nil
			next.PublishRequestedBy, next.PublishRequestedAt, next.ManagementProvider = nil, nil, nil
			if pkg, err = s.store.UpdatePackageTx(ctx, tx, next); err != nil {
				return err
			}
			call = true
			return softwareAudit(ctx, tx, c, "endpoints.software_package.retry_requested", "software_package", cur.ID,
				map[string]any{"status": cur.Status, "version": cur.Version}, map[string]any{"status": pkg.Status, "version": pkg.Version},
				map[string]any{"versionId": v.ID, "provider": s.softwareKey, "installerSha256": v.InstallerSHA256, "attempt": pkg.PackageAttempt})
		default:
			pkg = cur
			return nil
		}
	})
	if err != nil {
		return SoftwarePackage{}, err
	}
	if !call {
		return pkg, nil
	}
	pub := ""
	if ver.Publisher != nil {
		pub = *ver.Publisher
	}
	rec, err := s.software.Package(ctx, softwaremgmt.PackageRequest{ProductKey: ver.ProductID, ProductName: ver.ProductName, Publisher: pub,
		Version: ver.ProductVersion, InstallerURL: ver.InstallerURL, InstallerSHA256: ver.InstallerSHA256,
		InstallCommand: ver.InstallCommand, DetectionRule: ver.DetectionRule}, packageOpKey(ver, pkg.PackageAttempt))
	if err != nil {
		return SoftwarePackage{}, fmt.Errorf("package version: %w", err)
	}
	return s.applyReport(ctx, c, pkg.ID, rec, sourceOperation, nil)
}

// PublishPackage publishes a packaged package into the Management Provider. It requires an approved version of
// an approved product, a provider-reported installer hash equal to the approved hash and no open
// package_hash_mismatch; otherwise it is refused before the provider is called. Right before the publication
// the provider's current state is read again and compared, and the publication itself carries the approved
// hash, which the provider must check. When the approval was withdrawn while the publication was in flight,
// the result is not accepted (package_published_after_revoke). A package already accepted as published is
// returned unchanged. The provider call is idempotent per operation key (package, hash and publish attempt).
// Requires software.package.
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
	done := false
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockPackageTx(ctx, tx, strings.ToLower(packageID))
		if err != nil {
			return err
		}
		if *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status == PackagePublished && cur.PublishedAt != nil {
			pkg, done = cur, true
			return nil
		}
		if ver, err = s.store.ShareVersionTx(ctx, tx, cur.VersionID); err != nil {
			return err
		}
		if err := s.requireApprovedBinding(ctx, tx, ver); err != nil {
			return err
		}
		if cur.Status != PackagePackaged && cur.Status != PackagePublished || cur.ProviderPackageID == nil {
			return &GateError{Code: "package_not_ready"}
		}
		if cur.InstallerSHA256 == nil || *cur.InstallerSHA256 != ver.InstallerSHA256 || cur.HashMismatch {
			return &GateError{Code: "hash_mismatch"}
		}
		next := cur
		if !cur.publishInFlight() {
			// A new attempt; a repeated request while the previous one is unanswered reuses its key.
			next.PublishAttempt = cur.PublishAttempt + 1
		}
		now := s.now()
		target := s.viewProvider
		next.PublishRequestedBy, next.PublishRequestedAt, next.ManagementProvider = &c.Actor.UserID, &now, &target
		if pkg, err = s.store.UpdatePackageTx(ctx, tx, next); err != nil {
			return err
		}
		return softwareAudit(ctx, tx, c, "endpoints.software_package.publish_requested", "software_package", cur.ID,
			map[string]any{"status": cur.Status, "version": cur.Version}, map[string]any{"status": pkg.Status, "version": pkg.Version},
			map[string]any{"versionId": ver.ID, "managementProvider": target, "installerSha256": ver.InstallerSHA256, "attempt": pkg.PublishAttempt})
	})
	if err != nil || done {
		return pkg, err
	}

	// Read the provider's current state right before publishing: a package changed since the last report is
	// not published. A matching report is not stored (it is no new information and the publication stays in
	// flight until the provider answers).
	recs, err := s.software.PackageStatus(ctx, []string{*pkg.ProviderPackageID})
	if err != nil {
		return SoftwarePackage{}, s.publishFailed(ctx, c, pkg, ver, "provider_error", fmt.Errorf("publish package: status: %w", err))
	}
	var pre *softwaremgmt.PackageRecord
	for i := range recs {
		if strings.TrimSpace(recs[i].ProviderPackageID) == *pkg.ProviderPackageID {
			pre = &recs[i]
		}
	}
	if pre == nil {
		return SoftwarePackage{}, s.publishFailed(ctx, c, pkg, ver, "package_not_found", &GateError{Code: "package_not_ready"})
	}
	if n := s.normalizeRecord(*pre); n.hash == nil || *n.hash != ver.InstallerSHA256 || len(bindingDiffs(n, ver)) > 0 ||
		(n.status != PackagePackaged && n.status != PackagePublished) {
		if _, err := s.applyReport(ctx, c, pkg.ID, *pre, sourceOperation, nil); err != nil {
			return SoftwarePackage{}, err
		}
		code := "hash_mismatch"
		if n.status != PackagePackaged && n.status != PackagePublished {
			code = "package_not_ready"
		}
		return SoftwarePackage{}, s.publishFailed(ctx, c, pkg, ver, code, &GateError{Code: code})
	}

	rec, err := s.software.Publish(ctx, softwaremgmt.PublishRequest{ProviderPackageID: *pkg.ProviderPackageID,
		Target: softwaremgmt.ProviderTarget{ManagementProvider: *pkg.ManagementProvider}, ExpectedInstallerSHA256: ver.InstallerSHA256},
		publishOpKey(pkg, ver))
	if errors.Is(err, softwaremgmt.ErrHashDiffers) {
		return SoftwarePackage{}, s.publishFailed(ctx, c, pkg, ver, "hash_mismatch", &GateError{Code: "hash_mismatch"})
	}
	if err != nil {
		return SoftwarePackage{}, s.publishFailed(ctx, c, pkg, ver, "provider_error", fmt.Errorf("publish package: %w", err))
	}
	return s.applyReport(ctx, c, pkg.ID, rec, sourceOperation, func(tx pgx.Tx, updated SoftwarePackage) error {
		return softwareAudit(ctx, tx, c, "endpoints.software_package.publish_completed", "software_package", updated.ID, nil,
			map[string]any{"status": updated.Status, "version": updated.Version},
			map[string]any{"versionId": ver.ID, "attempt": pkg.PublishAttempt, "accepted": updated.PublishedAt != nil})
	})
}

// publishFailed audits a publication that did not happen and returns cause.
func (s *Service) publishFailed(ctx context.Context, c Caller, pkg SoftwarePackage, ver SoftwareVersion, code string, cause error) error {
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		return softwareAudit(ctx, tx, c, "endpoints.software_package.publish_failed", "software_package", pkg.ID, nil, nil,
			map[string]any{"versionId": ver.ID, "attempt": pkg.PublishAttempt, "errorCode": code})
	})
	return errors.Join(cause, err)
}

const (
	sourceOperation = "operation"
	sourceSync      = "sync"
)

// applyReport stores a provider report about one package in its own transaction; after, when set, runs in the
// same transaction.
func (s *Service) applyReport(ctx context.Context, c Caller, packageID string, rec softwaremgmt.PackageRecord, source string,
	after func(pgx.Tx, SoftwarePackage) error) (SoftwarePackage, error) {
	var out SoftwarePackage
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, _, err = s.applyPackageRecordTx(ctx, tx, c, packageID, rec, source, &SoftwarePackageSyncResult{}); err != nil {
			return err
		}
		if after != nil {
			return after(tx, out)
		}
		return nil
	})
	return out, err
}

// normalizedRecord is a validated provider report; invalid values become unknown (nil), an unknown status
// becomes failed.
type normalizedRecord struct {
	providerID, hash, artifact                *string
	productKey, version, publisher, cmd, rule *string
	status                                    string
	observedAt                                time.Time
}

func (s *Service) normalizeRecord(rec softwaremgmt.PackageRecord) normalizedRecord {
	n := normalizedRecord{status: rec.Status, observedAt: rec.ObservedAt.UTC().Truncate(time.Microsecond)}
	if !slices.Contains(softwaremgmt.Statuses, n.status) {
		n.status = PackageFailed
	}
	line := func(v string, max int) *string {
		if l, ok := cleanSoftwareLine(v, max); ok && l != "" {
			return &l
		}
		return nil
	}
	hex := func(v string) *string {
		if h := strings.ToLower(strings.TrimSpace(v)); validHex64(h) {
			return &h
		}
		return nil
	}
	n.providerID, n.artifact = line(rec.ProviderPackageID, 200), line(rec.ManagementArtifactExternalID, 200)
	n.hash, n.cmd, n.rule = hex(rec.InstallerSHA256), hex(rec.InstallCommandSHA256), hex(rec.DetectionRuleSHA256)
	n.productKey, n.version, n.publisher = line(rec.ProductKey, 200), line(rec.Version, 100), line(rec.Publisher, 200)
	now := s.now().UTC()
	if n.observedAt.IsZero() || n.observedAt.After(now) || n.observedAt.Year() < 1990 {
		n.observedAt = now.Truncate(time.Microsecond)
	}
	return n
}

// bindingDiffs lists the bound values a report states differently from the version (unreported values are not
// compared).
func bindingDiffs(n normalizedRecord, v SoftwareVersion) []string {
	var out []string
	if n.productKey != nil && !strings.EqualFold(*n.productKey, v.ProductID) {
		out = append(out, "productKey")
	}
	if n.version != nil && *n.version != v.ProductVersion {
		out = append(out, "version")
	}
	if n.publisher != nil && (v.Publisher == nil || *n.publisher != *v.Publisher) {
		out = append(out, "publisher")
	}
	if n.cmd != nil && *n.cmd != v.InstallCommandSHA256 {
		out = append(out, "installCommandSha256")
	}
	if n.rule != nil && *n.rule != v.DetectionRuleSHA256 {
		out = append(out, "detectionRuleSha256")
	}
	return out
}

func eqPtr(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// applyPackageRecordTx applies one provider report:
//   - a report older than the stored observation only refreshes last_synced_at (no history, no version change);
//   - a missing or invalid hash is no information: the stored hash and an open package_hash_mismatch stay;
//   - a reported hash or bound value that differs from the approved binding raises package_hash_mismatch, which
//     is resolved only when the reported hash equals the approved hash and nothing else differs;
//   - a publication (status published or a Management Artifact id) is accepted only for a Turaco publish request
//     of an approved version of an approved product without hash mismatch; otherwise the link is ignored and
//     package_published_after_revoke is raised and audited. SoftwarePackagePublished is emitted once per
//     package, when published_at is set;
//   - the package version changes only when something other than freshness changed.
//
// It reports whether the state changed.
func (s *Service) applyPackageRecordTx(ctx context.Context, tx pgx.Tx, c Caller, packageID string, rec softwaremgmt.PackageRecord, source string, run *SoftwarePackageSyncResult) (SoftwarePackage, bool, error) {
	cur, err := s.store.LockPackageTx(ctx, tx, packageID)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	ver, err := s.store.ShareVersionTx(ctx, tx, cur.VersionID)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	prod, err := s.store.ShareProductTx(ctx, tx, ver.ProductID)
	if err != nil {
		return SoftwarePackage{}, false, err
	}
	n := s.normalizeRecord(rec)
	if n.providerID == nil || (cur.ProviderPackageID != nil && *cur.ProviderPackageID != *n.providerID) {
		// A report without an id, or about another package, is not applied.
		return cur, false, fmt.Errorf("endpoints: provider report does not match package %s", cur.ID)
	}
	var syncedAt *time.Time
	if source == sourceSync {
		now := s.now().UTC()
		syncedAt = &now
	}
	if cur.ObservedAt != nil && n.observedAt.Before(*cur.ObservedAt) {
		run.Stale++
		if syncedAt == nil {
			return cur, false, nil
		}
		out, err := s.store.TouchPackageTx(ctx, tx, cur.ID, nil, cur.Source, syncedAt)
		return out, false, err
	}

	next := cur
	next.ProviderPackageID, next.Status = n.providerID, n.status
	if n.hash != nil {
		next.InstallerSHA256 = n.hash
	}
	diffs := bindingDiffs(n, ver)
	hashDiffers := n.hash != nil && *n.hash != ver.InstallerSHA256
	mismatch := hashDiffers || len(diffs) > 0 || (n.hash == nil && cur.HashMismatch)

	// Publication acceptance.
	reject := ""
	switch {
	case cur.PublishRequestedBy == nil:
		reject = PublishRejectWithoutRequest
	case ver.ApprovalStatus != VersionApproved:
		reject = PublishRejectVersionNotApproved
	case prod.ApprovalStatus != ProductApproved:
		reject = PublishRejectProductNotApproved
	}
	accepted := reject == "" && !mismatch && next.InstallerSHA256 != nil && *next.InstallerSHA256 == ver.InstallerSHA256
	if n.artifact != nil && !eqPtr(n.artifact, cur.ManagementArtifactExternalID) && accepted {
		owner, err := s.store.PackageIDByArtifactTx(ctx, tx, *cur.ManagementProvider, *n.artifact)
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		if owner != "" && owner != cur.ID {
			accepted, reject = false, PublishRejectArtifactTaken
		}
	}
	publicationReported := n.status == PackagePublished || n.artifact != nil
	if n.artifact != nil && accepted {
		next.ManagementArtifactExternalID = n.artifact
	}
	if !eqPtr(next.ManagementArtifactExternalID, cur.ManagementArtifactExternalID) {
		next.ManagementArtifactID = nil
	}
	publishedNow := false
	if n.status == PackagePublished && accepted && cur.PublishedAt == nil {
		at := s.now().UTC()
		next.PublishedAt, publishedNow = &at, true
	}
	if next.ManagementArtifactExternalID != nil && next.ManagementArtifactID == nil && next.ManagementProvider != nil {
		id, err := s.store.ArtifactIDByExternalTx(ctx, tx, *next.ManagementProvider, *next.ManagementArtifactExternalID)
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
	var updated SoftwarePackage
	if changed || linked || publishedNow {
		next.ObservedAt, next.Source = &observed, source
		if syncedAt != nil {
			next.LastSyncedAt = syncedAt
		}
		updated, err = s.store.UpdatePackageTx(ctx, tx, next)
	} else {
		updated, err = s.store.TouchPackageTx(ctx, tx, cur.ID, &observed, source, syncedAt)
	}
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
	if publishedNow {
		if err := publish(ctx, tx, c, EventSoftwarePackagePublished, map[string]any{"packageId": updated.ID, "versionId": updated.VersionID, "provider": updated.Provider}); err != nil {
			return SoftwarePackage{}, false, err
		}
	}

	switch {
	case hashDiffers || len(diffs) > 0:
		reason := "hash_differs"
		if !hashDiffers {
			reason = "binding_differs"
		}
		detail := map[string]any{"versionId": ver.ID, "approvedSha256": ver.InstallerSHA256, "reason": reason}
		if updated.InstallerSHA256 != nil {
			detail["reportedSha256"] = *updated.InstallerSHA256
		}
		if len(diffs) > 0 {
			detail["differingFields"] = diffs
		}
		if err := s.raisePackageFindingTx(ctx, tx, c, run, FindingPackageHashMismatch, updated.ID, detail); err != nil {
			return SoftwarePackage{}, false, err
		}
	case n.hash != nil:
		resolved, err := s.store.ResolvePackageFindingTx(ctx, tx, FindingPackageHashMismatch, updated.ID)
		if err != nil {
			return SoftwarePackage{}, false, err
		}
		if resolved {
			run.FindingsResolved++
		}
	}
	// A rejected publication is a finding only while Turaco has not accepted the package as published: a package
	// published before its approval was withdrawn is shown through VersionRevoked/ProductBlocked instead.
	if publicationReported && reject != "" && cur.PublishedAt == nil {
		detail := map[string]any{"versionId": ver.ID, "reason": reject, "reportedStatus": n.status}
		if err := s.raisePackageFindingTx(ctx, tx, c, run, FindingPackagePublishedAfterRevoke, updated.ID, detail); err != nil {
			return SoftwarePackage{}, false, err
		}
		if err := softwareAudit(ctx, tx, c, "endpoints.software_package.published_after_revoke", "software_package", updated.ID, nil,
			map[string]any{"status": updated.Status, "version": updated.Version},
			map[string]any{"versionId": ver.ID, "reason": reject, "versionStatus": ver.ApprovalStatus, "productStatus": prod.ApprovalStatus}); err != nil {
			return SoftwarePackage{}, false, err
		}
		updated.PublishedAfterRevoke = true
	}
	updated.HashMismatch = mismatch
	return updated, changed || linked || publishedNow, nil
}

// raisePackageFindingTx raises (or refreshes the detail of) a package finding and publishes EndpointFindingRaised
// when it was newly raised.
func (s *Service) raisePackageFindingTx(ctx context.Context, tx pgx.Tx, c Caller, run *SoftwarePackageSyncResult, kind, packageID string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	id, raised, err := s.store.OpenPackageFindingTx(ctx, tx, kind, packageID, raw)
	if err != nil || !raised {
		return err
	}
	if run != nil {
		run.FindingsRaised++
	}
	return publish(ctx, tx, c, "EndpointFindingRaised", map[string]any{"findingId": id, "softwarePackageId": packageID, "kind": kind})
}

// flagPublishingTx flags the packages of a version (or of a product's versions) whose publication is in flight
// when the approval is withdrawn: the provider may still publish them, and Turaco will not accept that.
func (s *Service) flagPublishingTx(ctx context.Context, tx pgx.Tx, c Caller, versionID, productID, operation string) (int, error) {
	ids, err := s.store.PublishingPackagesTx(ctx, tx, versionID, productID)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		detail := map[string]any{"reason": PublishRejectInFlight, "operation": operation}
		if versionID != "" {
			detail["versionId"] = versionID
		} else {
			detail["productId"] = productID
		}
		if err := s.raisePackageFindingTx(ctx, tx, c, nil, FindingPackagePublishedAfterRevoke, id, detail); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// SyncPackages reads the status of every known package from the Software Management Provider, stores what
// changed and links published packages to their Management Artifact once the management sync has ingested it.
// It needs SOFTWARE_PROVIDER_SYNC and software.package; one run per provider at a time, and not again within
// the cooldown after the last run ended (ErrSyncCooldown).
func (s *Service) SyncPackages(ctx context.Context, c Caller, p Principal) (SoftwarePackageSyncResult, error) {
	if err := c.validate(); err != nil {
		return SoftwarePackageSyncResult{}, err
	}
	if !p.SoftwarePackage {
		return SoftwarePackageSyncResult{}, ErrForbidden
	}
	return s.syncPackages(ctx, c, true)
}

// HandleSoftwarePackageSync is the job handler of SoftwarePackageSyncJobType. With the synchronization off it
// does nothing.
func (s *Service) HandleSoftwarePackageSync(ctx context.Context, job jobs.Job) error {
	if !s.softwareSyncOn {
		return nil
	}
	_, err := s.syncPackages(ctx, Caller{Actor: audit.SystemActor(SoftwareSyncActor), CorrelationID: "job:" + job.ID}, false)
	if errors.Is(err, ErrSyncRunning) {
		return nil
	}
	return err
}

func (s *Service) syncPackages(ctx context.Context, c Caller, manual bool) (SoftwarePackageSyncResult, error) {
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
	// Checked under the lock so concurrent requests cannot both pass.
	if manual && s.softwareSyncCooldown > 0 {
		last, err := s.store.LastSyncCompleted(ctx, softwareSyncStateKey)
		if err != nil {
			return run, err
		}
		if last != nil && s.now().Sub(*last) < s.softwareSyncCooldown {
			return run, ErrSyncCooldown
		}
	}
	syncErr := s.syncPackagePages(ctx, c, &run)
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	markErr := s.store.MarkSyncCompleted(markCtx, softwareSyncStateKey, s.now().UTC())
	action := "endpoints.software_package_sync.completed"
	meta := map[string]any{"provider": s.softwareKey, "checked": run.Checked, "changed": run.Changed, "linked": run.Linked,
		"findingsRaised": run.FindingsRaised, "findingsResolved": run.FindingsResolved, "stale": run.Stale, "errors": run.Errors}
	if syncErr != nil {
		action = "endpoints.software_package_sync.failed"
		meta["errorCode"] = syncErrorCode(syncErr)
	}
	if err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "software_provider", TargetID: s.softwareKey, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: meta})
	}); err != nil {
		return run, errors.Join(syncErr, err, markErr)
	}
	return run, errors.Join(syncErr, markErr)
}

func syncErrorCode(err error) string {
	if errors.Is(err, softwaremgmt.ErrNotConfigured) {
		return "provider_not_configured"
	}
	return "provider_error"
}

// syncPackagePages reads the packages page by page. A provider failure ends the run; a report that cannot be
// applied to its package is counted in Errors and the run continues with the next package.
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
				if changed && err == nil {
					run.Changed++
				}
				return err
			}); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				run.Errors++
			}
		}
		if len(pkgs) < softwareSyncBatch {
			return nil
		}
		after = pkgs[len(pkgs)-1].ID
	}
}
