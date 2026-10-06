package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// Regression tests of the F9 G1 review fixes (docs/product/f9-software-lifecycle-design.md, "G1 review outcomes").

func (s *sw) packaged(name string) (application.SoftwareVersion, application.SoftwarePackage) {
	s.t.Helper()
	_, v := s.approvedProductVersion(name, hashA)
	pk, err := s.svc.PackageVersion(context.Background(), s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil {
		s.t.Fatal(err)
	}
	return v, pk
}

func (s *sw) sync() application.SoftwarePackageSyncResult {
	s.t.Helper()
	res, err := s.svc.SyncPackages(context.Background(), s.caller(), s.pkgr)
	if err != nil {
		s.t.Fatalf("sync: %v", err)
	}
	return res
}

func (s *sw) findingDetail(kind, packageID string) map[string]any {
	s.t.Helper()
	var raw []byte
	if err := s.pool.QueryRow(context.Background(), `SELECT detail::text FROM endpoints.findings WHERE kind = $1 AND software_package_id = $2 AND status = 'open'`,
		kind, packageID).Scan(&raw); err != nil {
		s.t.Fatalf("finding %s: %v", kind, err)
	}
	d := map[string]any{}
	_ = json.Unmarshal(raw, &d)
	return d
}

func (s *sw) events(typ string) int {
	return s.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = $1 AND correlation_id = $2`, typ, s.corr)
}

func (s *sw) audits(action string) int {
	return s.count(`SELECT count(*) FROM platform.audit_events WHERE action = $1 AND correlation_id = $2`, action, s.corr)
}

func TestSoftwarePackageRetryAfterReportedFailure(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	v, pk := s.packaged("Retry App")
	if pk.PackageAttempt != 1 {
		t.Fatalf("first attempt %d", pk.PackageAttempt)
	}
	first := *pk.ProviderPackageID
	s.fake.SetStatus(first, softwaremgmt.StatusFailed)
	s.sync()
	if got := s.pkg(pk.ID); got.Status != application.PackageFailed {
		t.Fatalf("failed not stored: %+v", got)
	}
	// A failed package is retried as a new attempt with a new operation key.
	again, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil || again.Status != application.PackagePackaged || again.PackageAttempt != 2 || s.fake.Calls("package") != 2 || s.fake.Packages() != 2 ||
		*again.ProviderPackageID == first {
		t.Fatalf("retry: %v %+v calls=%d", err, again, s.fake.Calls("package"))
	}
	if s.audits("endpoints.software_package.retry_requested") != 1 {
		t.Fatal("retry not audited")
	}
	// A packaged package is not packaged again.
	if same, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version); err != nil || same.PackageAttempt != 2 || s.fake.Calls("package") != 2 {
		t.Fatalf("repeat after retry: %v %+v", err, same)
	}
	// The publication works on the new attempt and uses its own operation key.
	pub, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, again.ID, &again.Version)
	if err != nil || pub.Status != application.PackagePublished || pub.PublishedAt == nil || pub.PublishAttempt != 1 {
		t.Fatalf("publish after retry: %v %+v", err, pub)
	}
}

func TestSoftwarePublishRetryReusesAttemptWhileUnanswered(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, pk := s.packaged("Publish Retry App")
	boom := errors.New("timeout")
	s.fake.FailOperation("publish", boom)
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); !errors.Is(err, boom) {
		t.Fatalf("publish failure: %v", err)
	}
	s.fake.FailOperation("publish", nil)
	pk = s.pkg(pk.ID)
	if pk.PublishAttempt != 1 || s.audits("endpoints.software_package.publish_failed") != 1 {
		t.Fatalf("after failure: %+v", pk)
	}
	pub, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version)
	if err != nil || pub.PublishAttempt != 1 || pub.PublishedAt == nil {
		t.Fatalf("retry with the same key: %v %+v", err, pub)
	}
	if s.audits("endpoints.software_package.publish_completed") != 1 {
		t.Fatal("publish result not audited")
	}
}

func TestSoftwarePackageOutOfOrderAndFreshnessOnlyReports(t *testing.T) {
	s := newSW(t)
	_, pk := s.packaged("Order App")
	id := *pk.ProviderPackageID
	obs := func() int {
		return s.count(`SELECT count(*) FROM endpoints.software_package_observations WHERE software_package_id = $1`, pk.ID)
	}
	before, n := s.pkg(pk.ID), obs()
	// An older report (failed) is not applied: only last_synced_at moves.
	s.fake.SetStatus(id, softwaremgmt.StatusFailed)
	s.fake.SetObservedAt(id, before.ObservedAt.Add(-10*time.Minute))
	if res := s.sync(); res.Stale != 1 || res.Changed != 0 {
		t.Fatalf("stale run: %+v", res)
	}
	got := s.pkg(pk.ID)
	if got.Status != application.PackagePackaged || got.Version != before.Version || !got.ObservedAt.Equal(*before.ObservedAt) || got.LastSyncedAt == nil || obs() != n {
		t.Fatalf("stale report applied: %+v", got)
	}
	// A newer report that changes nothing refreshes freshness without a version change.
	s.fake.SetStatus(id, softwaremgmt.StatusPackaged)
	if res := s.sync(); res.Changed != 0 {
		t.Fatalf("freshness-only run: %+v", res)
	}
	fresh := s.pkg(pk.ID)
	if fresh.Version != before.Version || !fresh.ObservedAt.After(*before.ObservedAt) || obs() != n {
		t.Fatalf("freshness bumped the version: %+v", fresh)
	}
}

func TestSoftwarePackagePublishedEventOnce(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, pk := s.packaged("Once App")
	pub, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version)
	if err != nil || pub.PublishedAt == nil {
		t.Fatalf("publish: %v %+v", err, pub)
	}
	// The provider flips the status back and forth: the event is still emitted only once.
	s.fake.SetStatus(*pk.ProviderPackageID, softwaremgmt.StatusPackaged)
	s.sync()
	s.fake.SetStatus(*pk.ProviderPackageID, softwaremgmt.StatusPublished)
	s.sync()
	if n := s.events(application.EventSoftwarePackagePublished); n != 1 {
		t.Fatalf("published events %d", n)
	}
}

func TestSoftwarePackageMissingHashKeepsFinding(t *testing.T) {
	s := newSW(t)
	_, pk := s.packaged("Missing Hash App")
	id := *pk.ProviderPackageID
	s.fake.ReportHash(id, hashB)
	s.sync()
	// A report without a valid hash is no information: stored hash and the open finding stay.
	for _, h := range []string{"", "not-a-hash"} {
		s.fake.ReportHash(id, h)
		if res := s.sync(); res.FindingsResolved != 0 {
			t.Fatalf("missing hash resolved the finding: %+v", res)
		}
		if got := s.pkg(pk.ID); got.InstallerSHA256 == nil || *got.InstallerSHA256 != hashB || !got.HashMismatch {
			t.Fatalf("missing hash overwrote state: %+v", got)
		}
	}
	s.fake.ReportHash(id, hashA)
	if res := s.sync(); res.FindingsResolved != 1 {
		t.Fatalf("approved hash did not resolve: %+v", res)
	}
}

func TestSoftwarePackageBindingDiffersClosesGate(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, pk := s.packaged("Binding App")
	id := *pk.ProviderPackageID
	s.fake.ReportBinding(id, func(r *softwaremgmt.PackageRecord) {
		r.Publisher = "Other Corp"
		r.InstallCommandSHA256 = hashB
	})
	if res := s.sync(); res.FindingsRaised != 1 {
		t.Fatalf("binding difference not raised: %+v", res)
	}
	d := s.findingDetail(application.FindingPackageHashMismatch, pk.ID)
	if d["reason"] != "binding_differs" || fmt.Sprint(d["differingFields"]) != "[publisher installCommandSha256]" {
		t.Fatalf("detail: %v", d)
	}
	pk = s.pkg(pk.ID)
	if !pk.HashMismatch {
		t.Fatal("mismatch flag not set")
	}
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); gateCode(err) != "hash_mismatch" || s.fake.Calls("publish") != 0 {
		t.Fatalf("publish with different binding: %v", err)
	}
	// The binding reported correctly again (and the approved hash) resolves it.
	s.fake.ReportBinding(id, func(r *softwaremgmt.PackageRecord) { r.Publisher, r.InstallCommandSHA256 = "Example Corp", "" })
	if res := s.sync(); res.FindingsResolved != 1 {
		t.Fatalf("not resolved: %+v", res)
	}
}

func TestSoftwarePublishPreflightAndProviderHashCheck(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, pk := s.packaged("Preflight App")
	// The provider's package changed after the last sync: the read right before publishing refuses it.
	s.fake.ReportHash(*pk.ProviderPackageID, hashB)
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); gateCode(err) != "hash_mismatch" || s.fake.Calls("publish") != 0 {
		t.Fatalf("preflight: %v %d", err, s.fake.Calls("publish"))
	}
	if !s.pkg(pk.ID).HashMismatch || s.audits("endpoints.software_package.publish_failed") != 1 {
		t.Fatal("preflight mismatch not recorded")
	}
}

func TestSoftwareRevokeDuringPublishIsNotAccepted(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	v, pk := s.packaged("Race App")
	s.fake.BeforePublish(func() {
		cur, err := s.svc.GetSoftwareVersion(ctx, s.appr, v.ID)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := s.svc.RevokeVersion(ctx, s.approverCaller(), s.appr, v.ID, "security_risk", &cur.Version.Version); err != nil {
			t.Error(err)
		}
		// The revocation flagged the in-flight publication already.
		if d := s.findingDetail(application.FindingPackagePublishedAfterRevoke, pk.ID); d["reason"] != application.PublishRejectInFlight {
			t.Errorf("in-flight flag: %v", d)
		}
	})
	got, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version)
	if err != nil || got.Status != application.PackagePublished || got.PublishedAt != nil || got.ManagementArtifactExternalID != nil ||
		!got.PublishedAfterRevoke || !got.VersionRevoked {
		t.Fatalf("publish after revoke accepted: %v %+v", err, got)
	}
	if d := s.findingDetail(application.FindingPackagePublishedAfterRevoke, pk.ID); d["reason"] != application.PublishRejectVersionNotApproved {
		t.Fatalf("finding detail: %v", d)
	}
	if s.events(application.EventSoftwarePackagePublished) != 0 || s.audits("endpoints.software_package.published_after_revoke") != 1 {
		t.Fatal("rejected publication emitted or not audited")
	}
}

func TestSoftwareBlockDuringPublishFlagsPackage(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	v, pk := s.packaged("Block Race App")
	s.fake.BeforePublish(func() {
		prod := s.productByID(v.ProductID)
		if _, err := s.svc.BlockProduct(ctx, s.approverCaller(), s.appr, prod.ID, "security_risk", &prod.Version); err != nil {
			t.Error(err)
		}
	})
	got, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version)
	if err != nil || got.PublishedAt != nil || !got.PublishedAfterRevoke || !got.ProductBlocked {
		t.Fatalf("publish after block: %v %+v", err, got)
	}
}

func TestSoftwareUnrequestedPublicationIsIgnored(t *testing.T) {
	s := newSW(t)
	_, pk := s.packaged("Unrequested App")
	s.fake.ReportBinding(*pk.ProviderPackageID, func(r *softwaremgmt.PackageRecord) {
		r.Status, r.ManagementArtifactExternalID = softwaremgmt.StatusPublished, "app-unrequested-"+s.corr
	})
	s.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{{ExternalID: "app-unrequested-" + s.corr, Kind: "application", Name: "X", Platform: "windows", AssignmentsKnown: true}}})
	s.sync()
	got := s.pkg(pk.ID)
	if got.ManagementArtifactExternalID != nil || got.ManagementArtifactID != nil || got.PublishedAt != nil || !got.PublishedAfterRevoke {
		t.Fatalf("unrequested publication linked: %+v", got)
	}
	if d := s.findingDetail(application.FindingPackagePublishedAfterRevoke, pk.ID); d["reason"] != application.PublishRejectWithoutRequest {
		t.Fatalf("detail: %v", d)
	}
	if s.events(application.EventSoftwarePackagePublished) != 0 {
		t.Fatal("event for an unrequested publication")
	}
}

func TestSoftwareSyncContinuesAfterPackageError(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, bad := s.packaged("Broken App")
	_, good := s.packaged("Healthy App")
	// A database failure on one package's row must not abort the run.
	fn := "endpoints.test_fail_" + s.corr[len("endpoints-"):]
	trigger := "test_fail_" + s.corr[len("endpoints-"):]
	if _, err := s.pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+trigger+` ON endpoints.software_packages`)
		_, _ = s.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`()`)
	})
	if _, err := s.pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON endpoints.software_packages FOR EACH ROW
		WHEN (OLD.id = '%s'::uuid) EXECUTE FUNCTION %s()`, trigger, bad.ID, fn)); err != nil {
		t.Fatal(err)
	}
	s.fake.SetStatus(*bad.ProviderPackageID, softwaremgmt.StatusFailed)
	s.fake.SetStatus(*good.ProviderPackageID, softwaremgmt.StatusFailed)
	res, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr)
	if err != nil || res.Errors != 1 || res.Changed != 1 {
		t.Fatalf("run: %v %+v", err, res)
	}
	if got := s.pkg(good.ID); got.Status != application.PackageFailed {
		t.Fatalf("healthy package not applied: %+v", got)
	}
}

func TestSoftwareSyncCooldownAndCatalogRateLimit(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	s.sync()
	s.svc.WithSoftwareSyncCooldown(30 * time.Second)
	if _, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr); !errors.Is(err, application.ErrSyncCooldown) {
		t.Fatalf("cooldown: %v", err)
	}
	s.svc.WithSoftwareSyncCooldown(0)

	s.svc.WithCatalogSearchLimit(3, time.Minute)
	t.Cleanup(func() {
		s.svc.WithCatalogSearchLimit(application.DefaultCatalogSearchLimit, application.DefaultCatalogSearchWindow)
	})
	for i := 0; i < 3; i++ {
		if _, err := s.svc.SearchCatalog(ctx, s.viewer, "fire"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.svc.SearchCatalog(ctx, s.viewer, "fire"); !errors.Is(err, application.ErrRateLimited) {
		t.Fatalf("rate limit: %v", err)
	}
	other := s.viewer
	other.UserID = s.approver
	if _, err := s.svc.SearchCatalog(ctx, other, "fire"); err != nil {
		t.Fatalf("limit is per user: %v", err)
	}
}

func TestSoftwareVersionApprovalTriggerAndArtifactUniqueness(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	p := s.product("Trigger App")
	v := s.register(ver(p.ID, hashA))
	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// registered -> approved skips pending; self-approval by the registrant; rejected -> approved.
	mustFail("registered to approved", `UPDATE endpoints.software_versions SET approval_status = 'approved', approval_requested_by = $2, approval_requested_at = now(),
		approval_decided_by = $3, approval_decided_at = now() WHERE id = $1`, v.ID, s.approver, s.newID())
	if _, err := s.pool.Exec(ctx, `UPDATE endpoints.software_versions SET approval_status = 'pending', approval_requested_by = $2, approval_requested_at = now() WHERE id = $1`, v.ID, s.approver); err != nil {
		t.Fatal(err)
	}
	mustFail("approved by registrant", `UPDATE endpoints.software_versions SET approval_status = 'approved', approval_decided_by = registered_by, approval_decided_at = now() WHERE id = $1`, v.ID)
	mustFail("approved by requester", `UPDATE endpoints.software_versions SET approval_status = 'approved', approval_decided_by = approval_requested_by, approval_decided_at = now() WHERE id = $1`, v.ID)
	if _, err := s.pool.Exec(ctx, `UPDATE endpoints.software_versions SET approval_status = 'rejected', approval_reason = 'policy', approval_decided_by = $2, approval_decided_at = now() WHERE id = $1`, v.ID, s.newID()); err != nil {
		t.Fatal(err)
	}
	mustFail("rejected to approved", `UPDATE endpoints.software_versions SET approval_status = 'approved', approval_reason = NULL WHERE id = $1`, v.ID)

	// One Management Artifact belongs to at most one package.
	_, a := s.packaged("Artifact A")
	_, b := s.packaged("Artifact B")
	set := `UPDATE endpoints.software_packages SET management_provider = 'intune', management_artifact_external_id = 'app-dup-' || $2 WHERE id = $1`
	if _, err := s.pool.Exec(ctx, set, a.ID, s.corr); err != nil {
		t.Fatal(err)
	}
	mustFail("duplicate artifact", set, b.ID, s.corr)
}
