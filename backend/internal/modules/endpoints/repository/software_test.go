package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// sw is the software part of the endpoints test environment: a packager (the env user), a separate approver and
// a Fake Software Management Provider whose package ids are unique to this test.
type sw struct {
	*env
	fake     *softwaremgmt.Fake
	approver string
	pkgr     application.Principal
	appr     application.Principal
	viewer   application.Principal
}

func newSW(t *testing.T) *sw {
	t.Helper()
	e := newEnv(t)
	s := &sw{env: e, fake: softwaremgmt.NewFake(), approver: e.newID()}
	s.fake.SetIDPrefix(strings.TrimPrefix(e.corr, "endpoints-") + "-")
	e.svc.WithSoftware(s.fake, true).WithProviderKey(e.provider)
	s.pkgr = application.Principal{UserID: e.user, SoftwarePackage: true}
	s.appr = application.Principal{UserID: s.approver, SoftwareApprove: true}
	s.viewer = application.Principal{UserID: e.user, SoftwareView: true}
	return s
}

func (s *sw) approverCaller() application.Caller {
	return application.Caller{Actor: audit.UserActor(s.approver), CorrelationID: s.corr}
}

func (s *sw) product(name string) application.SoftwareProduct {
	s.t.Helper()
	suffix := strings.TrimPrefix(s.corr, "endpoints-")
	info, _, err := s.svc.RegisterSoftwareProduct(context.Background(), s.caller(), s.manage, name+" "+suffix, "", nil)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.productByID(info.ID)
}

func (s *sw) productByID(id string) application.SoftwareProduct {
	s.t.Helper()
	res, err := s.svc.ListSoftwareProducts(context.Background(), s.viewer, application.SoftwareProductFilter{Page: application.Page{Limit: 200}})
	if err != nil {
		s.t.Fatal(err)
	}
	for cursor := res.NextCursor; ; {
		for _, p := range res.Items {
			if p.ID == id {
				return p
			}
		}
		if cursor == "" {
			break
		}
		res, err = s.svc.ListSoftwareProducts(context.Background(), s.viewer, application.SoftwareProductFilter{Page: application.Page{Limit: 200, Cursor: cursor}})
		if err != nil {
			s.t.Fatal(err)
		}
		cursor = res.NextCursor
	}
	s.t.Fatalf("product %s not listed", id)
	return application.SoftwareProduct{}
}

func ver(productID, hash string) application.NewSoftwareVersion {
	return application.NewSoftwareVersion{ProductID: productID, ProductVersion: "1.2.3", InstallerSHA256: hash,
		InstallerURL: "https://downloads.example.com/app-1.2.3.msi", Publisher: "Example Corp",
		InstallCommand: "msiexec /i app-1.2.3.msi /qn", DetectionRule: "msi product code {0000-1111}\nversion >= 1.2.3"}
}

func (s *sw) register(in application.NewSoftwareVersion) application.SoftwareVersion {
	s.t.Helper()
	v, _, err := s.svc.RegisterVersion(context.Background(), s.caller(), s.pkgr, in)
	if err != nil {
		s.t.Fatalf("register version: %v", err)
	}
	return v
}

// approvedProductVersion returns an approved product and an approved version of it.
func (s *sw) approvedProductVersion(name, hash string) (application.SoftwareProduct, application.SoftwareVersion) {
	s.t.Helper()
	ctx := context.Background()
	p := s.product(name)
	p, err := s.svc.ApproveProduct(ctx, s.approverCaller(), s.appr, p.ID, &p.Version)
	if err != nil {
		s.t.Fatal(err)
	}
	v := s.register(ver(p.ID, hash))
	if v, err = s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, v.ID, &v.Version); err != nil {
		s.t.Fatal(err)
	}
	if v, err = s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version); err != nil {
		s.t.Fatal(err)
	}
	return p, v
}

func (s *sw) pkg(id string) application.SoftwarePackage {
	s.t.Helper()
	var out application.SoftwarePackage
	err := s.repo().InTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		out, err = s.repo().LockPackageTx(context.Background(), tx, id)
		return err
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *sw) repo() *repository.Repository { return repository.New(s.pool) }

func gateCode(err error) string {
	var g *application.GateError
	if errors.As(err, &g) {
		return g.Code
	}
	return ""
}

func TestSoftwareVersionApprovalLifecycle(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	p := s.product("Lifecycle App")
	v, created, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, ver(p.ID, strings.ToUpper(hashA)))
	if err != nil || !created || v.ApprovalStatus != application.VersionRegistered || v.InstallerSHA256 != hashA {
		t.Fatalf("register: %v %v %+v", err, created, v)
	}
	if v.InstallCommandSHA256 == "" || v.BindingSHA256 == "" {
		t.Fatalf("hashes: %+v", v)
	}
	again, created, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, ver(p.ID, hashA))
	if err != nil || created || again.ID != v.ID {
		t.Fatalf("idempotent register: %v %v %s", err, created, again.ID)
	}
	if n := s.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.software_version.registered' AND target_id = $1`, v.ID); n != 1 {
		t.Fatalf("register audits %d", n)
	}

	// Approve before a request, a missing version and a view-only caller are refused.
	if _, err := s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("approve registered: %v", err)
	}
	if _, err := s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, v.ID, nil); err == nil {
		t.Fatal("missing expectedVersion accepted")
	}
	if _, err := s.svc.RequestVersionApproval(ctx, s.caller(), s.viewer, v.ID, &v.Version); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("viewer request: %v", err)
	}
	v, err = s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil || v.ApprovalStatus != application.VersionPending {
		t.Fatalf("request: %v %+v", err, v)
	}
	// Separation of duties: the registrant holding software.approve still cannot approve.
	both := application.Principal{UserID: s.user, SoftwareApprove: true, SoftwarePackage: true}
	if _, err := s.svc.ApproveVersion(ctx, s.caller(), both, v.ID, &v.Version); !errors.Is(err, application.ErrSeparationOfDuties) {
		t.Fatalf("self approval: %v", err)
	}
	if _, err := s.svc.ApproveVersion(ctx, s.approverCaller(), s.pkgr, v.ID, &v.Version); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("packager approval: %v", err)
	}
	stale := v.Version - 1
	if _, err := s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &stale); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale approve: %v", err)
	}
	v, err = s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version)
	if err != nil || v.ApprovalStatus != application.VersionApproved || v.DecidedBy == nil || *v.DecidedBy != s.approver {
		t.Fatalf("approve: %v %+v", err, v)
	}
	if n := s.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'SoftwareVersionApproved' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("approved events %d", n)
	}

	// A changed installer URL is a new version; the approved one stays untouched.
	changed := ver(p.ID, hashA)
	changed.InstallerURL = "https://mirror.example.com/app-1.2.3.msi"
	nv := s.register(changed)
	if nv.ID == v.ID || nv.ApprovalStatus != application.VersionRegistered {
		t.Fatalf("changed binding: %+v", nv)
	}
	changed = ver(p.ID, hashB)
	if hv := s.register(changed); hv.ID == v.ID || hv.ID == nv.ID {
		t.Fatal("changed hash must be a new version")
	}
	still, err := s.svc.GetSoftwareVersion(ctx, s.viewer, v.ID)
	if err != nil || still.Version.ApprovalStatus != application.VersionApproved || still.Version.InstallerURL != ver(p.ID, hashA).InstallerURL {
		t.Fatalf("approved version changed: %v %+v", err, still.Version)
	}

	// Revoke needs a valid reason; the decision history is complete and bound to the hash.
	if _, err := s.svc.RevokeVersion(ctx, s.approverCaller(), s.appr, v.ID, "because", &v.Version); err == nil {
		t.Fatal("free-text reason accepted")
	}
	v, err = s.svc.RevokeVersion(ctx, s.approverCaller(), s.appr, v.ID, "security_risk", &v.Version)
	if err != nil || v.ApprovalStatus != application.VersionRevoked || v.ApprovalReason == nil {
		t.Fatalf("revoke: %v %+v", err, v)
	}
	detail, err := s.svc.GetSoftwareVersion(ctx, s.viewer, v.ID)
	if err != nil || len(detail.Approvals) != 3 {
		t.Fatalf("approvals: %v %d", err, len(detail.Approvals))
	}
	for _, a := range detail.Approvals {
		if a.InstallerSHA256 != hashA || a.BindingSHA256 != v.BindingSHA256 {
			t.Fatalf("decision not bound: %+v", a)
		}
	}
	if n := s.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'SoftwareVersionRevoked' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("revoked events %d", n)
	}

	// Reject path.
	nv, err = s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, nv.ID, &nv.Version)
	if err != nil {
		t.Fatal(err)
	}
	if nv, err = s.svc.RejectVersion(ctx, s.approverCaller(), s.appr, nv.ID, "untrusted_source", &nv.Version); err != nil || nv.ApprovalStatus != application.VersionRejected {
		t.Fatalf("reject: %v %+v", err, nv)
	}
	if _, err := s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, nv.ID, &nv.Version); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("approve rejected: %v", err)
	}
	// Audit carries no free text.
	if n := s.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND (metadata::text LIKE '%msiexec%' OR metadata::text LIKE '%example.com%')`, s.corr); n != 0 {
		t.Fatalf("free text in audit: %d", n)
	}
	// Reads need a software permission; unknown ids are not found.
	if _, err := s.svc.GetSoftwareVersion(ctx, s.view, v.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("endpoints.view read: %v", err)
	}
	if _, err := s.svc.GetSoftwareVersion(ctx, s.viewer, s.newID()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestSoftwareProductApprovalStatus(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	p := s.product("Status App")
	if p.ApprovalStatus != application.ProductCandidate {
		t.Fatalf("initial: %+v", p)
	}
	if _, err := s.svc.DeprecateProduct(ctx, s.approverCaller(), s.appr, p.ID, "superseded", &p.Version); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("deprecate candidate: %v", err)
	}
	if _, err := s.svc.ApproveProduct(ctx, s.caller(), s.pkgr, p.ID, &p.Version); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("packager approves product: %v", err)
	}
	steps := []struct {
		name   string
		do     func(v *int) (application.SoftwareProduct, error)
		status string
	}{
		{"approve", func(v *int) (application.SoftwareProduct, error) {
			return s.svc.ApproveProduct(ctx, s.approverCaller(), s.appr, p.ID, v)
		}, application.ProductApproved},
		{"deprecate", func(v *int) (application.SoftwareProduct, error) {
			return s.svc.DeprecateProduct(ctx, s.approverCaller(), s.appr, p.ID, "vendor_end_of_support", v)
		}, application.ProductDeprecated},
		{"retire", func(v *int) (application.SoftwareProduct, error) {
			return s.svc.RetireProduct(ctx, s.approverCaller(), s.appr, p.ID, "no_longer_used", v)
		}, application.ProductRetired},
		{"block", func(v *int) (application.SoftwareProduct, error) {
			return s.svc.BlockProduct(ctx, s.approverCaller(), s.appr, p.ID, "security_risk", v)
		}, application.ProductBlocked},
	}
	for _, st := range steps {
		next, err := st.do(&p.Version)
		if err != nil || next.ApprovalStatus != st.status || next.Version != p.Version+1 {
			t.Fatalf("%s: %v %+v", st.name, err, next)
		}
		p = next
	}
	if p.ApprovalReason == nil || *p.ApprovalReason != "security_risk" {
		t.Fatalf("block reason: %+v", p)
	}
	if _, _, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, ver(p.ID, hashA)); gateCode(err) != "product_blocked" {
		t.Fatalf("register on blocked: %v", err)
	}
	if _, err := s.svc.BlockProduct(ctx, s.approverCaller(), s.appr, p.ID, "policy", &p.Version); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Fatalf("block blocked: %v", err)
	}
	if _, err := s.svc.UnblockProduct(ctx, s.approverCaller(), s.appr, p.ID, "", &p.Version); err == nil {
		t.Fatal("unblock without reason accepted")
	}
	p, err := s.svc.UnblockProduct(ctx, s.approverCaller(), s.appr, p.ID, "re_evaluation", &p.Version)
	if err != nil || p.ApprovalStatus != application.ProductCandidate || p.ApprovalReason != nil {
		t.Fatalf("unblock: %v %+v", err, p)
	}
	if n := s.count(`SELECT count(*) FROM endpoints.software_product_transitions WHERE software_product_id = $1`, p.ID); n != 5 {
		t.Fatalf("transitions %d", n)
	}
	if n := s.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action LIKE 'endpoints.software_product.%' AND action <> 'endpoints.software_product.registered'`, p.ID); n != 5 {
		t.Fatalf("audits %d", n)
	}
	if got, err := s.svc.ListSoftwareProducts(ctx, s.viewer, application.SoftwareProductFilter{Status: "nope"}); err == nil {
		t.Fatalf("invalid status filter: %+v", got)
	}
	if _, err := s.svc.ListSoftwareProducts(ctx, s.view, application.SoftwareProductFilter{}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("list without software permission: %v", err)
	}
}

func TestSoftwareTablesEnforceInvariants(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, v := s.approvedProductVersion("Invariant App", hashA)
	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	mustFail("binding update", `UPDATE endpoints.software_versions SET installer_sha256 = $2 WHERE id = $1`, v.ID, hashB)
	mustFail("url update", `UPDATE endpoints.software_versions SET installer_url = 'https://evil.example.com/x.msi' WHERE id = $1`, v.ID)
	mustFail("version delete", `DELETE FROM endpoints.software_versions WHERE id = $1`, v.ID)
	mustFail("approval update", `UPDATE endpoints.software_version_approvals SET reason = 'other' WHERE software_version_id = $1`, v.ID)
	mustFail("approval delete", `DELETE FROM endpoints.software_version_approvals WHERE software_version_id = $1`, v.ID)
	mustFail("approval truncate", `TRUNCATE endpoints.software_version_approvals`)
	mustFail("product transitions truncate", `TRUNCATE endpoints.software_product_transitions`)
	mustFail("observations truncate", `TRUNCATE endpoints.software_package_observations`)
	mustFail("approved without decider", `UPDATE endpoints.software_versions SET approval_decided_by = NULL, approval_decided_at = NULL WHERE id = $1`, v.ID)
	mustFail("rejected without reason", `UPDATE endpoints.software_versions SET approval_status = 'rejected' WHERE id = $1`, v.ID)
	mustFail("wrong command hash", `INSERT INTO endpoints.software_versions (software_product_id, product_version, installer_sha256, installer_url,
		install_command, install_command_sha256, detection_rule, detection_rule_sha256, binding_sha256, registered_by)
		VALUES ($1, '9', $2, 'https://x.example.com/a', 'cmd', $2, 'rule', encode(sha256('rule'::bytea), 'hex'), $2, $3)`, v.ProductID, hashA, s.user)
	mustFail("http url", `INSERT INTO endpoints.software_versions (software_product_id, product_version, installer_sha256, installer_url,
		install_command, install_command_sha256, detection_rule, detection_rule_sha256, binding_sha256, registered_by)
		VALUES ($1, '9', $2, 'http://x.example.com/a', 'cmd', encode(sha256('cmd'::bytea), 'hex'), 'rule', encode(sha256('rule'::bytea), 'hex'), $2, $3)`, v.ProductID, hashA, s.user)
	mustFail("blocked without reason", `UPDATE endpoints.software_products SET approval_status = 'blocked' WHERE id = $1`, v.ProductID)
	mustFail("package finding without package", `INSERT INTO endpoints.findings (kind) VALUES ('package_hash_mismatch')`)
	// Invalid input never reaches the database.
	bad := ver(v.ProductID, "xyz")
	if _, _, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, bad); err == nil {
		t.Fatal("short hash accepted")
	}
	bad = ver(v.ProductID, hashA)
	bad.InstallerURL = "https://user:pw@example.com/a.msi"
	if _, _, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, bad); err == nil {
		t.Fatal("credential URL accepted")
	}
	bad = ver(v.ProductID, hashA)
	bad.InstallCommand = "a\nb"
	if _, _, err := s.svc.RegisterVersion(ctx, s.caller(), s.pkgr, bad); err == nil {
		t.Fatal("multi-line command accepted")
	}
}

func TestSoftwarePackagePublishGatesAndHashBinding(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	p := s.product("Gate App")
	v := s.register(ver(p.ID, hashA))
	if _, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version); gateCode(err) != "version_not_approved" {
		t.Fatalf("package unapproved: %v", err)
	}
	v, _ = s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	v, err := s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version); gateCode(err) != "product_not_approved" {
		t.Fatalf("package with candidate product: %v", err)
	}
	if _, err := s.svc.ApproveProduct(ctx, s.approverCaller(), s.appr, p.ID, &p.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.PackageVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("approver packages: %v", err)
	}
	pk, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil || pk.Status != application.PackagePackaged || pk.ProviderPackageID == nil || pk.InstallerSHA256 == nil || *pk.InstallerSHA256 != hashA {
		t.Fatalf("package: %v %+v", err, pk)
	}
	again, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil || again.ID != pk.ID || s.fake.Packages() != 1 || s.fake.Calls("package") != 1 {
		t.Fatalf("idempotent package: %v %s %d %d", err, again.ID, s.fake.Packages(), s.fake.Calls("package"))
	}
	obsCount := func() int {
		return s.count(`SELECT count(*) FROM endpoints.software_package_observations WHERE software_package_id = $1`, pk.ID)
	}
	if n := obsCount(); n != 1 {
		t.Fatalf("observations after package %d", n)
	}

	// The provider reports another installer: finding raised, publish refused before the provider is called.
	s.fake.ReportHash(*pk.ProviderPackageID, hashB)
	res, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr)
	if err != nil || res.FindingsRaised != 1 || res.Changed != 1 {
		t.Fatalf("sync mismatch: %v %+v", err, res)
	}
	pk = s.pkg(pk.ID)
	if !pk.HashMismatch {
		t.Fatalf("mismatch not flagged: %+v", pk)
	}
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); gateCode(err) != "hash_mismatch" || s.fake.Calls("publish") != 0 {
		t.Fatalf("publish mismatch: %v %d", err, s.fake.Calls("publish"))
	}
	var detail []byte
	if err := s.pool.QueryRow(ctx, `SELECT detail::text FROM endpoints.findings WHERE software_package_id = $1 AND status = 'open'`, pk.ID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var d map[string]string
	_ = json.Unmarshal(detail, &d)
	if d["approvedSha256"] != hashA || d["reportedSha256"] != hashB {
		t.Fatalf("finding detail: %s", detail)
	}
	// The device findings list never shows package findings.
	if res, err := s.svc.ListFindings(ctx, s.manage, application.FindingFilter{}); err != nil {
		t.Fatal(err)
	} else {
		for _, f := range res.Items {
			if f.Kind == application.FindingPackageHashMismatch {
				t.Fatal("package finding in device list")
			}
		}
	}

	// The provider reports the approved hash again: finding resolved; an unchanged re-read adds no history.
	s.fake.ReportHash(*pk.ProviderPackageID, hashA)
	if res, err = s.svc.SyncPackages(ctx, s.caller(), s.pkgr); err != nil || res.FindingsResolved != 1 {
		t.Fatalf("sync resolve: %v %+v", err, res)
	}
	before := obsCount()
	if res, err = s.svc.SyncPackages(ctx, s.caller(), s.pkgr); err != nil || res.Changed != 0 || obsCount() != before {
		t.Fatalf("unchanged sync wrote history: %v %+v %d->%d", err, res, before, obsCount())
	}

	// Revoked approval blocks publishing.
	pk = s.pkg(pk.ID)
	rv, err := s.svc.RevokeVersion(ctx, s.approverCaller(), s.appr, v.ID, "defect", &v.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); gateCode(err) != "version_not_approved" {
		t.Fatalf("publish revoked: %v", err)
	}
	_ = rv
}

func TestSoftwarePackagePublishAndArtifactLink(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, v := s.approvedProductVersion("Publish App", hashA)
	pk, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, nil); err == nil {
		t.Fatal("missing expectedVersion accepted")
	}
	pub, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version)
	if err != nil || pub.Status != application.PackagePublished || pub.ManagementArtifactExternalID == nil || pub.ManagementArtifactID != nil {
		t.Fatalf("publish: %v %+v", err, pub)
	}
	if n := s.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'SoftwarePackagePublished' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("published events %d", n)
	}
	again, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pub.Version)
	if err != nil || again.ID != pub.ID || s.fake.Calls("publish") != 1 {
		t.Fatalf("idempotent publish: %v %d", err, s.fake.Calls("publish"))
	}
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("double click: %v", err)
	}

	// The management sync ingests the published app; the next package sync links it.
	ext := *pub.ManagementArtifactExternalID
	s.mingest(true, intune.ManagementSnapshot{Artifacts: []intune.ArtifactRecord{{ExternalID: ext, Kind: "application", Name: "Publish App", Platform: "windows", AssignmentsKnown: true}}})
	res, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr)
	if err != nil || res.Linked != 1 {
		t.Fatalf("sync link: %v %+v", err, res)
	}
	if got := s.pkg(pk.ID); got.ManagementArtifactID == nil || *got.ManagementArtifactID != s.artifactID(ext) {
		t.Fatalf("artifact link: %+v", got)
	}
	if n := s.count(`SELECT count(*) FROM platform.outbox_events WHERE event_type = 'SoftwarePackagePublished' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("re-sync republished %d", n)
	}
	if n := s.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.software_package_sync.completed' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("sync audits %d", n)
	}
}

func TestSoftwareProviderFailuresAndSyncSwitch(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	_, v := s.approvedProductVersion("Failure App", hashA)
	boom := errors.New("provider down")
	s.fake.FailOperation("package", boom)
	if _, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version); !errors.Is(err, boom) {
		t.Fatalf("package failure: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM endpoints.software_packages WHERE software_version_id = $1 AND status = 'requested'`, v.ID); n != 1 {
		t.Fatalf("requested row %d", n)
	}
	s.fake.FailOperation("package", nil)
	pk, err := s.svc.PackageVersion(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil || pk.Status != application.PackagePackaged || s.fake.Packages() != 1 {
		t.Fatalf("retry: %v %+v %d", err, pk, s.fake.Packages())
	}
	s.fake.FailOperation("status", boom)
	if _, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr); !errors.Is(err, boom) {
		t.Fatalf("status failure: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM platform.audit_events WHERE action = 'endpoints.software_package_sync.failed' AND correlation_id = $1`, s.corr); n != 1 {
		t.Fatalf("failed sync audits %d", n)
	}
	s.fake.FailOperation("status", nil)
	if _, err := s.svc.SyncPackages(ctx, s.caller(), s.viewer); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("viewer sync: %v", err)
	}
	// A failed publish keeps the package publishable for a retry.
	s.fake.FailOperation("publish", boom)
	if _, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); !errors.Is(err, boom) {
		t.Fatalf("publish failure: %v", err)
	}
	s.fake.FailOperation("publish", nil)
	pk = s.pkg(pk.ID)
	if pub, err := s.svc.PublishPackage(ctx, s.caller(), s.pkgr, pk.ID, &pk.Version); err != nil || pub.Status != application.PackagePublished {
		t.Fatalf("publish retry: %v %+v", err, pub)
	}

	// The switch off: the endpoint refuses, the job does nothing; the placeholder reports not configured.
	s.svc.WithSoftware(nil, false)
	if _, err := s.svc.SyncPackages(ctx, s.caller(), s.pkgr); !errors.Is(err, application.ErrSyncDisabled) {
		t.Fatalf("disabled sync: %v", err)
	}
	if err := s.svc.HandleSoftwarePackageSync(ctx, jobs.Job{ID: s.newID()}); err != nil {
		t.Fatalf("disabled job: %v", err)
	}
	placeholder := application.NewService(s.repo(), s.assets, nil, false, nil)
	if _, err := placeholder.SearchCatalog(ctx, s.viewer, "firefox"); !errors.Is(err, softwaremgmt.ErrNotConfigured) {
		t.Fatalf("placeholder search: %v", err)
	}
	if _, err := s.svc.SearchCatalog(ctx, s.viewer, "x"); err == nil {
		t.Fatal("one-character query accepted")
	}
	s.fake.SetCatalog(softwaremgmt.CatalogEntry{ProviderID: "Mozilla.Firefox", Name: "Firefox", SourceURL: "javascript:alert(1)"},
		softwaremgmt.CatalogEntry{ProviderID: "Bad.Entry", Name: "Fire‮bad"})
	got, err := s.svc.SearchCatalog(ctx, s.viewer, "fire")
	if err != nil || len(got) != 1 || got[0].SourceURL != nil {
		t.Fatalf("catalog sanitizing: %v %+v", err, got)
	}
}

type capturedNotes struct{ intents []notifications.Intent }

func (c *capturedNotes) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	c.intents = append(c.intents, in)
	return true, nil
}

type listHolders []string

func (l listHolders) ActiveUsers(_ context.Context, after string, limit int) ([]string, error) {
	out := []string{}
	for _, id := range l {
		if id > after && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

type allActive struct{}

func (allActive) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

type permsByUser map[string][]string

func (p permsByUser) Permissions(_ context.Context, id string) (map[string]struct{}, error) {
	m := map[string]struct{}{}
	for _, perm := range p[id] {
		m[perm] = struct{}{}
	}
	return m, nil
}

func TestSoftwareApprovalRequestNotifiesApprovers(t *testing.T) {
	s := newSW(t)
	ctx := context.Background()
	p := s.product("Notify App")
	v := s.register(ver(p.ID, hashA))
	v, err := s.svc.RequestVersionApproval(ctx, s.caller(), s.pkgr, v.ID, &v.Version)
	if err != nil {
		t.Fatal(err)
	}
	other := s.newID()
	holders := listHolders{s.user, s.approver, other}
	perms := permsByUser{s.user: {application.PermSoftwareApprove}, s.approver: {application.PermSoftwareApprove}, other: {application.PermSoftwareView}}
	notes := &capturedNotes{}
	n := application.NewSoftwareNotifications(s.repo(), allActive{}, holders, perms, notes).WithChunk(1)
	actor := s.user
	ev := events.OutboxEvent{ID: s.newID(), EventType: application.EventSoftwareVersionApprovalRequested, ActorID: &actor, CorrelationID: s.corr,
		Payload: json.RawMessage(`{"versionId":"` + v.ID + `"}`)}
	// Chunk 1 per run: walk the fan-out by hand through the "after" cursor.
	var after string
	for i := 0; i < len(holders); i++ {
		payload := `{"versionId":"` + v.ID + `"}`
		if after != "" {
			payload = `{"versionId":"` + v.ID + `","after":"` + after + `","sourceEventId":"` + ev.ID + `"}`
		}
		ev.Payload = json.RawMessage(payload)
		if err := s.repo().InTx(ctx, func(tx pgx.Tx) error { return n.OnApprovalRequested(ctx, tx, ev) }); err != nil {
			t.Fatal(err)
		}
		after = maxOf(holders[:i+1])
	}
	if len(notes.intents) != 1 || notes.intents[0].RecipientUserID != s.approver || notes.intents[0].Category != application.SoftwareApprovalCategory ||
		notes.intents[0].LinkID != v.ID || notes.intents[0].DedupeKey != ev.ID+":"+s.approver {
		t.Fatalf("notifications: %+v", notes.intents)
	}
	// A decided version notifies nobody.
	v, _ = s.svc.ApproveVersion(ctx, s.approverCaller(), s.appr, v.ID, &v.Version)
	notes.intents = nil
	ev.Payload = json.RawMessage(`{"versionId":"` + v.ID + `"}`)
	_ = s.repo().InTx(ctx, func(tx pgx.Tx) error { return n.WithChunk(10).OnApprovalRequested(ctx, tx, ev) })
	if len(notes.intents) != 0 {
		t.Fatalf("stale event notified: %+v", notes.intents)
	}
	if err := n.OnApprovalRequested(ctx, nil, events.OutboxEvent{Payload: json.RawMessage(`{"versionId":"nope"}`)}); err == nil {
		t.Fatal("invalid payload accepted")
	}
}

func maxOf(ids []string) string {
	m := ""
	for _, id := range ids {
		if id > m {
			m = id
		}
	}
	return m
}
