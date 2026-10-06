package softwaremgmt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
)

var _ softwaremgmt.Provider = softwaremgmt.NotConfigured{}
var _ softwaremgmt.Provider = (*softwaremgmt.Fake)(nil)

func TestNotConfiguredFailsEveryCall(t *testing.T) {
	ctx := context.Background()
	p := softwaremgmt.NotConfigured{}
	if _, err := p.SearchCatalog(ctx, "x"); !errors.Is(err, softwaremgmt.ErrNotConfigured) {
		t.Fatalf("search: %v", err)
	}
	if _, err := p.Package(ctx, softwaremgmt.PackageRequest{}, "k"); !errors.Is(err, softwaremgmt.ErrNotConfigured) {
		t.Fatalf("package: %v", err)
	}
	if _, err := p.Publish(ctx, softwaremgmt.PublishRequest{ProviderPackageID: "p", Target: softwaremgmt.ProviderTarget{ManagementProvider: "intune"}, ExpectedInstallerSHA256: "ab"}, "k"); !errors.Is(err, softwaremgmt.ErrNotConfigured) {
		t.Fatalf("publish: %v", err)
	}
	if _, err := p.PackageStatus(ctx, []string{"p"}); !errors.Is(err, softwaremgmt.ErrNotConfigured) {
		t.Fatalf("status: %v", err)
	}
}

func TestFakeIsIdempotentPerOperationKey(t *testing.T) {
	ctx := context.Background()
	f := softwaremgmt.NewFake()
	req := softwaremgmt.PackageRequest{ProductKey: "prod", Version: "1.0", InstallerSHA256: "ab"}
	a, err := f.Package(ctx, req, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Package(ctx, req, "op-1")
	if err != nil || a.ProviderPackageID != b.ProviderPackageID || f.Packages() != 1 {
		t.Fatalf("repeat package: %v %+v %+v", err, a, b)
	}
	if c, _ := f.Package(ctx, req, "op-2"); c.ProviderPackageID == a.ProviderPackageID {
		t.Fatal("another key must create another package")
	}
	target := softwaremgmt.ProviderTarget{ManagementProvider: "intune"}
	pub := softwaremgmt.PublishRequest{ProviderPackageID: a.ProviderPackageID, Target: target, ExpectedInstallerSHA256: "ab"}
	wrong := pub
	wrong.ExpectedInstallerSHA256 = "cd"
	if _, err := f.Publish(ctx, wrong, "pub-0"); !errors.Is(err, softwaremgmt.ErrHashDiffers) {
		t.Fatalf("publish with another expected hash: %v", err)
	}
	p1, err := f.Publish(ctx, pub, "pub-1")
	if err != nil || p1.Status != softwaremgmt.StatusPublished || p1.ManagementArtifactExternalID != "app-pkg-1" {
		t.Fatalf("publish: %v %+v", err, p1)
	}
	if p2, err := f.Publish(ctx, pub, "pub-1"); err != nil || p2 != p1 {
		t.Fatalf("repeat publish: %v %+v", err, p2)
	}
	if _, err := f.Publish(ctx, softwaremgmt.PublishRequest{ProviderPackageID: "pkg-99", Target: target, ExpectedInstallerSHA256: "ab"}, "pub-x"); !errors.Is(err, softwaremgmt.ErrNotFound) {
		t.Fatalf("unknown package: %v", err)
	}
	recs, err := f.PackageStatus(ctx, []string{"pkg-1", "nope"})
	if err != nil || len(recs) != 1 || recs[0].Status != softwaremgmt.StatusPublished {
		t.Fatalf("status: %v %+v", err, recs)
	}
}

func TestFakeFailureInjectionAndHashReport(t *testing.T) {
	ctx := context.Background()
	f := softwaremgmt.NewFake()
	f.SetCatalog(softwaremgmt.CatalogEntry{ProviderID: "Mozilla.Firefox", Name: "Firefox"}, softwaremgmt.CatalogEntry{ProviderID: "7zip.7zip", Name: "7-Zip"})
	if got, err := f.SearchCatalog(ctx, "fire"); err != nil || len(got) != 1 {
		t.Fatalf("search: %v %+v", err, got)
	}
	boom := errors.New("boom")
	f.FailOperation("package", boom)
	if _, err := f.Package(ctx, softwaremgmt.PackageRequest{}, "k"); !errors.Is(err, boom) {
		t.Fatalf("injected: %v", err)
	}
	f.FailOperation("package", nil)
	f.SetInitialStatus(softwaremgmt.StatusBuilding)
	rec, err := f.Package(ctx, softwaremgmt.PackageRequest{InstallerSHA256: "aa"}, "k")
	if err != nil || rec.Status != softwaremgmt.StatusBuilding {
		t.Fatalf("package: %v %+v", err, rec)
	}
	if _, err := f.Publish(ctx, softwaremgmt.PublishRequest{ProviderPackageID: rec.ProviderPackageID, Target: softwaremgmt.ProviderTarget{ManagementProvider: "intune"}, ExpectedInstallerSHA256: "aa"}, "p"); err == nil {
		t.Fatal("a building package must not publish")
	}
	f.ReportHash(rec.ProviderPackageID, "bb")
	recs, _ := f.PackageStatus(ctx, []string{rec.ProviderPackageID})
	if recs[0].InstallerSHA256 != "bb" {
		t.Fatalf("reported hash: %+v", recs[0])
	}
	f.FailWith(boom)
	if _, err := f.PackageStatus(ctx, nil); !errors.Is(err, boom) {
		t.Fatalf("fail all: %v", err)
	}
	if f.Calls("package") != 2 {
		t.Fatalf("calls: %d", f.Calls("package"))
	}
}
