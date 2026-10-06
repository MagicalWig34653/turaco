package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Software approvals (F9 G1, docs/product/f9-software-lifecycle-design.md). Audit actions:
// endpoints.software_product.<approved|deprecated|retired|blocked|unblocked>,
// endpoints.software_version.<registered|approval_requested|approved|rejected|revoked>. Audit carries ids,
// statuses, hashes and reason codes only; names, URLs, commands and rules are never copied into audit.

const (
	maxInstallerURL   = 2000
	maxInstallCommand = 2000
	maxDetectionRule  = 4000
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// BindingSHA256 is the identity of a version's binding: every value an approval is bound to. Changing any of
// them yields another binding and therefore another Software Version.
func BindingSHA256(productVersion, installerSHA256, installerURL string, publisher *string, commandSHA256, ruleSHA256 string) string {
	pub := ""
	if publisher != nil {
		pub = *publisher
	}
	return sha256Hex(strings.Join([]string{"turaco.software_version.v1", productVersion, installerSHA256, installerURL, pub, commandSHA256, ruleSHA256}, "\x00"))
}

func validHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// cleanHTTPSURL validates an absolute https URL with a host, without credentials or whitespace.
func cleanHTTPSURL(raw string, max int) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > max || !utf8.ValidString(raw) || strings.ContainsAny(raw, " \t\r\n\\\"<>`") || safetext.ContainsUnsafe(raw, false) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || !strings.HasPrefix(raw, "https://") {
		return "", false
	}
	return raw, true
}

// cleanSoftwareLine validates a single line of at most max characters; empty means none.
func cleanSoftwareLine(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", false
	}
	return s, true
}

func softwareActor(c Caller) (user, system *string) {
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		return &u, nil
	}
	name := c.Actor.System
	return nil, &name
}

func softwareTransition(c Caller, subjectID, from, to, op, reason string) SoftwareTransition {
	t := SoftwareTransition{SubjectID: subjectID, FromStatus: from, ToStatus: to, Operation: op, Reason: strPtr(reason), CorrelationID: c.CorrelationID}
	t.ActorUserID, t.ActorSystem = softwareActor(c)
	return t
}

func softwareAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, id string, before, after any, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: targetType, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta})
}

func productState(p SoftwareProduct) map[string]any {
	return map[string]any{"approvalStatus": p.ApprovalStatus, "approvalReason": p.ApprovalReason, "version": p.Version}
}

func versionState(v SoftwareVersion) map[string]any {
	return map[string]any{"approvalStatus": v.ApprovalStatus, "approvalReason": v.ApprovalReason, "version": v.Version}
}

func checkSoftwareReason(reason string, allowed []string) error {
	if !slices.Contains(allowed, reason) {
		return invalid("reason must be one of %s", strings.Join(allowed, ", "))
	}
	return nil
}

// ---- product approval status ----

// productOp describes one Software Approval Status operation.
type productOp struct {
	name, audit string
	from        []string
	to          string
	reasons     []string
}

var (
	opApproveProduct   = productOp{"approve", "approved", []string{ProductCandidate}, ProductApproved, nil}
	opDeprecateProduct = productOp{"deprecate", "deprecated", []string{ProductApproved}, ProductDeprecated, ProductDeprecateReasons}
	opRetireProduct    = productOp{"retire", "retired", []string{ProductDeprecated}, ProductRetired, ProductRetireReasons}
	opBlockProduct     = productOp{"block", "blocked", []string{ProductCandidate, ProductApproved, ProductDeprecated, ProductRetired}, ProductBlocked, ProductBlockReasons}
	opUnblockProduct   = productOp{"unblock", "unblocked", []string{ProductBlocked}, ProductCandidate, ProductUnblockReasons}
)

// ApproveProduct moves candidate -> approved. Requires software.approve.
func (s *Service) ApproveProduct(ctx context.Context, c Caller, p Principal, id string, expectedVersion *int) (SoftwareProduct, error) {
	return s.changeProduct(ctx, c, p, id, opApproveProduct, "", expectedVersion)
}

// DeprecateProduct moves approved -> deprecated with a reason. Requires software.approve.
func (s *Service) DeprecateProduct(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareProduct, error) {
	return s.changeProduct(ctx, c, p, id, opDeprecateProduct, reason, expectedVersion)
}

// RetireProduct moves deprecated -> retired with a reason. Requires software.approve.
func (s *Service) RetireProduct(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareProduct, error) {
	return s.changeProduct(ctx, c, p, id, opRetireProduct, reason, expectedVersion)
}

// BlockProduct blocks a product from any other status with a reason. Requires software.approve.
func (s *Service) BlockProduct(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareProduct, error) {
	return s.changeProduct(ctx, c, p, id, opBlockProduct, reason, expectedVersion)
}

// UnblockProduct returns a blocked product to candidate (re-evaluation) with a reason. Requires software.approve.
func (s *Service) UnblockProduct(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareProduct, error) {
	return s.changeProduct(ctx, c, p, id, opUnblockProduct, reason, expectedVersion)
}

func (s *Service) changeProduct(ctx context.Context, c Caller, p Principal, id string, op productOp, reason string, expectedVersion *int) (SoftwareProduct, error) {
	if err := c.validate(); err != nil {
		return SoftwareProduct{}, err
	}
	if !p.SoftwareApprove {
		return SoftwareProduct{}, ErrForbidden
	}
	if op.reasons == nil {
		if reason != "" {
			return SoftwareProduct{}, invalid("%s takes no reason", op.name)
		}
	} else if err := checkSoftwareReason(reason, op.reasons); err != nil {
		return SoftwareProduct{}, err
	}
	if expectedVersion == nil {
		return SoftwareProduct{}, invalid("expectedVersion is required")
	}
	if !validUUID(id) {
		return SoftwareProduct{}, ErrNotFound
	}
	var out SoftwareProduct
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockProductTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(op.from, cur.ApprovalStatus) {
			return &InvalidTransitionError{Operation: op.name, From: cur.ApprovalStatus}
		}
		var stored *string
		if op.to == ProductBlocked {
			stored = &reason
		}
		updated, err := s.store.UpdateProductApprovalTx(ctx, tx, cur.ID, op.to, stored)
		if err != nil {
			return err
		}
		if err := s.store.AppendProductTransitionTx(ctx, tx, softwareTransition(c, cur.ID, cur.ApprovalStatus, op.to, op.name, reason)); err != nil {
			return err
		}
		meta := map[string]any{"operation": op.name}
		if reason != "" {
			meta["reason"] = reason
		}
		out = updated
		return softwareAudit(ctx, tx, c, "endpoints.software_product."+op.audit, "software_product", cur.ID, productState(cur), productState(updated), meta)
	})
	return out, err
}

// ---- software versions ----

// RegisterVersion registers a Software Version binding (product, version, installer hash and https URL,
// publisher, install command and detection rule). Registering the same binding again returns the existing
// version (created false) and changes nothing; any changed value is a new version with its own approval.
// The product may not be blocked or retired. Requires software.package.
func (s *Service) RegisterVersion(ctx context.Context, c Caller, p Principal, in NewSoftwareVersion) (SoftwareVersion, bool, error) {
	if err := c.validate(); err != nil {
		return SoftwareVersion{}, false, err
	}
	if !p.SoftwarePackage {
		return SoftwareVersion{}, false, ErrForbidden
	}
	if c.Actor.UserID == "" {
		return SoftwareVersion{}, false, invalid("a version is registered by a person")
	}
	v, err := validateNewVersion(in)
	if err != nil {
		return SoftwareVersion{}, false, err
	}
	v.RegisteredBy = c.Actor.UserID
	var out SoftwareVersion
	var created bool
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		prod, err := s.store.LockProductTx(ctx, tx, v.ProductID)
		if err != nil {
			return err
		}
		if prod.ApprovalStatus == ProductBlocked || prod.ApprovalStatus == ProductRetired {
			return &GateError{Code: "product_blocked"}
		}
		if out, created, err = s.store.InsertVersionTx(ctx, tx, v); err != nil || !created {
			return err
		}
		return softwareAudit(ctx, tx, c, "endpoints.software_version.registered", "software_version", out.ID, nil, versionState(out),
			map[string]any{"productId": out.ProductID, "installerSha256": out.InstallerSHA256, "bindingSha256": out.BindingSHA256})
	})
	return out, created, err
}

func validateNewVersion(in NewSoftwareVersion) (SoftwareVersion, error) {
	if !validUUID(in.ProductID) {
		return SoftwareVersion{}, ErrNotFound
	}
	ver, ok := cleanSoftwareLine(in.ProductVersion, 100)
	if !ok || ver == "" {
		return SoftwareVersion{}, invalid("version must be 1-100 characters without control or invisible formatting characters")
	}
	hash := strings.ToLower(strings.TrimSpace(in.InstallerSHA256))
	if !validHex64(hash) {
		return SoftwareVersion{}, invalid("installerSha256 must be 64 hexadecimal characters")
	}
	u, ok := cleanHTTPSURL(in.InstallerURL, maxInstallerURL)
	if !ok {
		return SoftwareVersion{}, invalid("installerUrl must be an https URL of at most %d characters", maxInstallerURL)
	}
	pub, ok := cleanSoftwareLine(in.Publisher, 200)
	if !ok {
		return SoftwareVersion{}, invalid("publisher must be at most 200 characters without control or invisible formatting characters")
	}
	cmd := strings.TrimSpace(in.InstallCommand)
	if cmd == "" || utf8.RuneCountInString(cmd) > maxInstallCommand || !utf8.ValidString(cmd) || safetext.ContainsUnsafe(cmd, false) {
		return SoftwareVersion{}, invalid("installCommand must be one line of 1-%d characters without control or invisible formatting characters", maxInstallCommand)
	}
	rule := strings.TrimSpace(in.DetectionRule)
	if rule == "" || utf8.RuneCountInString(rule) > maxDetectionRule || !utf8.ValidString(rule) || safetext.ContainsUnsafe(rule, true) {
		return SoftwareVersion{}, invalid("detectionRule must be 1-%d characters without control or invisible formatting characters", maxDetectionRule)
	}
	v := SoftwareVersion{ProductID: strings.ToLower(in.ProductID), ProductVersion: ver, InstallerSHA256: hash, InstallerURL: u,
		Publisher: strPtr(pub), InstallCommand: cmd, InstallCommandSHA256: sha256Hex(cmd), DetectionRule: rule, DetectionRuleSHA256: sha256Hex(rule)}
	v.BindingSHA256 = BindingSHA256(v.ProductVersion, v.InstallerSHA256, v.InstallerURL, v.Publisher, v.InstallCommandSHA256, v.DetectionRuleSHA256)
	return v, nil
}

// lockVersion locks a version, checks the expected version and that the status is one of from.
func (s *Service) lockVersion(ctx context.Context, tx pgx.Tx, id string, expected int, op string, from ...string) (SoftwareVersion, error) {
	cur, err := s.store.LockVersionTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return SoftwareVersion{}, err
	}
	if expected != cur.Version {
		return SoftwareVersion{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.ApprovalStatus) {
		return SoftwareVersion{}, &InvalidTransitionError{Operation: op, From: cur.ApprovalStatus}
	}
	return cur, nil
}

func (s *Service) versionPreamble(c Caller, allowed bool, id string, expectedVersion *int) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	if c.Actor.UserID == "" {
		return invalid("software approval decisions are made by a person")
	}
	if expectedVersion == nil {
		return invalid("expectedVersion is required")
	}
	if !validUUID(id) {
		return ErrNotFound
	}
	return nil
}

// recordVersionDecision stores the new approval state, its append-only decision row and the audit entry.
func (s *Service) recordVersionDecision(ctx context.Context, tx pgx.Tx, c Caller, before, next SoftwareVersion, op, auditName, reason string) (SoftwareVersion, error) {
	updated, err := s.store.UpdateVersionApprovalTx(ctx, tx, next)
	if err != nil {
		return SoftwareVersion{}, err
	}
	t := softwareTransition(c, before.ID, before.ApprovalStatus, updated.ApprovalStatus, op, reason)
	t.InstallerSHA256, t.BindingSHA256 = before.InstallerSHA256, before.BindingSHA256
	if err := s.store.AppendVersionApprovalTx(ctx, tx, t); err != nil {
		return SoftwareVersion{}, err
	}
	meta := map[string]any{"operation": op, "productId": before.ProductID, "installerSha256": before.InstallerSHA256, "bindingSha256": before.BindingSHA256}
	if reason != "" {
		meta["reason"] = reason
	}
	return updated, softwareAudit(ctx, tx, c, "endpoints.software_version."+auditName, "software_version", before.ID, versionState(before), versionState(updated), meta)
}

// RequestVersionApproval moves registered -> pending and tells the holders of software.approve.
// Requires software.package.
func (s *Service) RequestVersionApproval(ctx context.Context, c Caller, p Principal, id string, expectedVersion *int) (SoftwareVersion, error) {
	if err := s.versionPreamble(c, p.SoftwarePackage, id, expectedVersion); err != nil {
		return SoftwareVersion{}, err
	}
	var out SoftwareVersion
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockVersion(ctx, tx, id, *expectedVersion, "request_approval", VersionRegistered)
		if err != nil {
			return err
		}
		prod, err := s.store.LockProductTx(ctx, tx, cur.ProductID)
		if err != nil {
			return err
		}
		if prod.ApprovalStatus == ProductBlocked || prod.ApprovalStatus == ProductRetired {
			return &GateError{Code: "product_blocked"}
		}
		next := cur
		now := s.now()
		next.ApprovalStatus, next.RequestedBy, next.RequestedAt = VersionPending, &c.Actor.UserID, &now
		if out, err = s.recordVersionDecision(ctx, tx, c, cur, next, "request_approval", "approval_requested", ""); err != nil {
			return err
		}
		return publish(ctx, tx, c, EventSoftwareVersionApprovalRequested, map[string]any{"versionId": out.ID, "productId": out.ProductID})
	})
	return out, err
}

// ApproveVersion moves pending -> approved, bound to the version's installer hash and binding. The person who
// registered the version or requested its approval cannot approve it (separation of duties), and the product
// may not be blocked or retired. Requires software.approve.
func (s *Service) ApproveVersion(ctx context.Context, c Caller, p Principal, id string, expectedVersion *int) (SoftwareVersion, error) {
	if err := s.versionPreamble(c, p.SoftwareApprove, id, expectedVersion); err != nil {
		return SoftwareVersion{}, err
	}
	var out SoftwareVersion
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockVersion(ctx, tx, id, *expectedVersion, "approve", VersionPending)
		if err != nil {
			return err
		}
		if cur.RegisteredBy == c.Actor.UserID || (cur.RequestedBy != nil && *cur.RequestedBy == c.Actor.UserID) {
			return ErrSeparationOfDuties
		}
		prod, err := s.store.LockProductTx(ctx, tx, cur.ProductID)
		if err != nil {
			return err
		}
		if prod.ApprovalStatus == ProductBlocked || prod.ApprovalStatus == ProductRetired {
			return &GateError{Code: "product_blocked"}
		}
		next := cur
		now := s.now()
		next.ApprovalStatus, next.DecidedBy, next.DecidedAt, next.ApprovalReason = VersionApproved, &c.Actor.UserID, &now, nil
		if out, err = s.recordVersionDecision(ctx, tx, c, cur, next, "approve", "approved", ""); err != nil {
			return err
		}
		return publish(ctx, tx, c, EventSoftwareVersionApproved, map[string]any{"versionId": out.ID, "productId": out.ProductID, "installerSha256": out.InstallerSHA256})
	})
	return out, err
}

// RejectVersion moves pending -> rejected with a reason. Requires software.approve.
func (s *Service) RejectVersion(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareVersion, error) {
	if err := s.versionPreamble(c, p.SoftwareApprove, id, expectedVersion); err != nil {
		return SoftwareVersion{}, err
	}
	if err := checkSoftwareReason(reason, VersionRejectReasons); err != nil {
		return SoftwareVersion{}, err
	}
	var out SoftwareVersion
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockVersion(ctx, tx, id, *expectedVersion, "reject", VersionPending)
		if err != nil {
			return err
		}
		next := cur
		now := s.now()
		decider := c.Actor.UserID
		next.ApprovalStatus, next.DecidedBy, next.DecidedAt, next.ApprovalReason = VersionRejected, &decider, &now, &reason
		out, err = s.recordVersionDecision(ctx, tx, c, cur, next, "reject", "rejected", reason)
		return err
	})
	return out, err
}

// RevokeVersion moves approved -> revoked with a reason. Requires software.approve.
func (s *Service) RevokeVersion(ctx context.Context, c Caller, p Principal, id, reason string, expectedVersion *int) (SoftwareVersion, error) {
	if err := s.versionPreamble(c, p.SoftwareApprove, id, expectedVersion); err != nil {
		return SoftwareVersion{}, err
	}
	if err := checkSoftwareReason(reason, VersionRevokeReasons); err != nil {
		return SoftwareVersion{}, err
	}
	var out SoftwareVersion
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockVersion(ctx, tx, id, *expectedVersion, "revoke", VersionApproved)
		if err != nil {
			return err
		}
		next := cur
		now := s.now()
		decider := c.Actor.UserID
		next.ApprovalStatus, next.DecidedBy, next.DecidedAt, next.ApprovalReason = VersionRevoked, &decider, &now, &reason
		if out, err = s.recordVersionDecision(ctx, tx, c, cur, next, "revoke", "revoked", reason); err != nil {
			return err
		}
		return publish(ctx, tx, c, EventSoftwareVersionRevoked, map[string]any{"versionId": out.ID, "productId": out.ProductID, "reason": reason})
	})
	return out, err
}

// ---- reads ----

// ListSoftwareProducts lists products with their approval status. Requires a software permission.
func (s *Service) ListSoftwareProducts(ctx context.Context, p Principal, f SoftwareProductFilter) (SoftwareProductResult, error) {
	if !p.canViewSoftware() {
		return SoftwareProductResult{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(ProductStatuses, f.Status) {
		return SoftwareProductResult{}, invalid("status must be one of %s", strings.Join(ProductStatuses, ", "))
	}
	f.Page = f.Page.Normalize()
	return s.store.ListSoftwareProducts(ctx, f)
}

// ListSoftwareVersions lists versions. Requires a software permission.
func (s *Service) ListSoftwareVersions(ctx context.Context, p Principal, f SoftwareVersionFilter) (SoftwareVersionResult, error) {
	if !p.canViewSoftware() {
		return SoftwareVersionResult{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(VersionStatuses, f.Status) {
		return SoftwareVersionResult{}, invalid("status must be one of %s", strings.Join(VersionStatuses, ", "))
	}
	if f.ProductID != "" && !validUUID(f.ProductID) {
		return SoftwareVersionResult{Items: []SoftwareVersion{}}, nil
	}
	f.Page = f.Page.Normalize()
	return s.store.ListSoftwareVersions(ctx, f)
}

// GetSoftwareVersion returns a version with its approval history and packages. Requires a software permission.
func (s *Service) GetSoftwareVersion(ctx context.Context, p Principal, id string) (SoftwareVersionDetail, error) {
	if !p.canViewSoftware() {
		return SoftwareVersionDetail{}, ErrForbidden
	}
	if !validUUID(id) {
		return SoftwareVersionDetail{}, ErrNotFound
	}
	v, err := s.store.GetSoftwareVersion(ctx, strings.ToLower(id))
	if err != nil {
		return SoftwareVersionDetail{}, err
	}
	approvals, err := s.store.VersionApprovals(ctx, v.ID)
	if err != nil {
		return SoftwareVersionDetail{}, err
	}
	pkgs, err := s.store.ListSoftwarePackages(ctx, SoftwarePackageFilter{VersionID: v.ID, Page: Page{Limit: MaxLimit}})
	if err != nil {
		return SoftwareVersionDetail{}, err
	}
	return SoftwareVersionDetail{Version: v, Approvals: approvals, Packages: pkgs.Items}, nil
}

// ListSoftwarePackages lists packages. Requires a software permission.
func (s *Service) ListSoftwarePackages(ctx context.Context, p Principal, f SoftwarePackageFilter) (SoftwarePackageResult, error) {
	if !p.canViewSoftware() {
		return SoftwarePackageResult{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(PackageStatuses, f.Status) {
		return SoftwarePackageResult{}, invalid("status must be one of %s", strings.Join(PackageStatuses, ", "))
	}
	if f.VersionID != "" && !validUUID(f.VersionID) {
		return SoftwarePackageResult{Items: []SoftwarePackage{}}, nil
	}
	f.Page = f.Page.Normalize()
	return s.store.ListSoftwarePackages(ctx, f)
}
