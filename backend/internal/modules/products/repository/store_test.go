package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *Repository
	pfx  string
	corr string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, repo: New(pool), pfx: "zp" + hex.EncodeToString(b)}
	f.corr = "products-" + f.pfx
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM products.products WHERE name LIKE $1`, f.pfx+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM products.categories WHERE name LIKE $1`, f.pfx+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM products.manufacturers WHERE name LIKE $1`, f.pfx+"%")
	})
	return f
}

func (f *fixture) caller() application.Caller {
	return application.Caller{Actor: audit.SystemActor("test"), CorrelationID: f.corr}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestManufacturerLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.repo.InsertManufacturer(ctx, f.caller(), f.pfx+" Dell")
	if err != nil || m.Version != 1 {
		t.Fatalf("insert: %+v %v", m, err)
	}
	if _, err := f.repo.InsertManufacturer(ctx, f.caller(), f.pfx+" DELL"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate (case-insensitive): %v", err)
	}
	r, err := f.repo.ChangeManufacturer(ctx, f.caller(), m.ID, func(cur application.Manufacturer) (application.Change[application.Manufacturer], error) {
		n := cur
		n.Name = f.pfx + " Dell Inc."
		return application.Change[application.Manufacturer]{Next: n, Action: "products.manufacturer.renamed"}, nil
	})
	if err != nil || r.Name != f.pfx+" Dell Inc." || r.Version != 2 {
		t.Fatalf("rename: %+v %v", r, err)
	}
	if _, err := f.repo.ChangeManufacturer(ctx, f.caller(), "garbage", nil); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND target_type = 'manufacturer'`, f.corr); n != 2 {
		t.Errorf("audit events = %d, want created and renamed", n)
	}
	res, err := f.repo.ListManufacturers(ctx, f.pfx, application.Page{})
	if err != nil || len(res.Items) != 1 {
		t.Errorf("prefix list = %+v %v", res, err)
	}
	if res, _ := f.repo.ListManufacturers(ctx, f.pfx+"%", application.Page{}); len(res.Items) != 0 {
		t.Error("LIKE metacharacters must be literal")
	}
}

func TestCategoryTreeAndUniquenessPerParent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root, err := f.repo.InsertCategory(ctx, f.caller(), f.pfx+" Hardware", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.InsertCategory(ctx, f.caller(), f.pfx+" HARDWARE", nil); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate root: %v", err)
	}
	child, err := f.repo.InsertCategory(ctx, f.caller(), f.pfx+" Hardware", &root.ID) // same name under another parent
	if err != nil || child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("child: %+v %v", child, err)
	}
	if _, err := f.repo.InsertCategory(ctx, f.caller(), f.pfx+" Orphan", ptr("00000000-0000-7000-8000-000000000000")); !errors.Is(err, application.ErrReferenceNotFound) {
		t.Errorf("unknown parent: %v", err)
	}
	if _, err := f.repo.InsertCategory(ctx, f.caller(), "x", ptr("not-a-uuid")); !errors.Is(err, application.ErrReferenceNotFound) {
		t.Errorf("malformed parent: %v", err)
	}
}

func ptr(s string) *string { return &s }

func TestProductLifecycleReferencesAndFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mfr, _ := f.repo.InsertManufacturer(ctx, f.caller(), f.pfx+" Lenovo")
	cat, _ := f.repo.InsertCategory(ctx, f.caller(), f.pfx+" Laptops", nil)
	ipn := f.pfx + "-IPN"
	p, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{
		Name: f.pfx + " ThinkPad", ManufacturerID: &mfr.ID, CategoryID: &cat.ID, InternalPartNumber: &ipn, Serialized: true, StockManaged: false, AssetManaged: true,
	})
	if err != nil || !p.Active || p.Version != 1 || !p.Serialized || p.StockManaged {
		t.Fatalf("insert: %+v %v", p, err)
	}
	if _, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{Name: f.pfx + " Other", InternalPartNumber: ptr(f.pfx + "-ipn")}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate internal part number: %v", err)
	}
	if _, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{Name: f.pfx + " Bad", ManufacturerID: ptr("00000000-0000-7000-8000-000000000000")}); !errors.Is(err, application.ErrReferenceNotFound) {
		t.Errorf("unknown manufacturer: %v", err)
	}
	off, err := f.repo.ChangeProduct(ctx, f.caller(), p.ID, func(cur application.Product) (application.Change[application.Product], error) {
		n := cur
		n.Active = false
		return application.Change[application.Product]{Next: n, Action: "products.product.deactivated"}, nil
	})
	if err != nil || off.Active || off.Version != 2 {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	if got, err := f.repo.GetProduct(ctx, p.ID); err != nil || got.Active {
		t.Errorf("get: %+v %v", got, err)
	}
	if _, err := f.repo.GetProduct(ctx, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	yes, no := true, false
	list := func(fl application.ProductFilter) int {
		fl.Page = application.Page{Limit: 200}
		res, err := f.repo.ListProducts(ctx, fl)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, it := range res.Items {
			if it.Name == f.pfx+" ThinkPad" {
				n++
			}
		}
		return n
	}
	if list(application.ProductFilter{Active: &yes}) != 0 || list(application.ProductFilter{Active: &no}) != 1 {
		t.Error("active filter")
	}
	if list(application.ProductFilter{CategoryID: cat.ID}) != 1 || list(application.ProductFilter{ManufacturerID: mfr.ID}) != 1 {
		t.Error("reference filters")
	}
	if list(application.ProductFilter{CategoryID: "garbage"}) != 0 {
		t.Error("malformed filter id must match nothing")
	}
	if list(application.ProductFilter{TitlePrefix: f.pfx + " think"}) != 1 {
		t.Error("prefix filter is case-insensitive")
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND target_type = 'product'`, f.corr); n != 2 {
		t.Errorf("product audit events = %d", n)
	}
	if _, err := f.repo.ListProducts(ctx, application.ProductFilter{Page: application.Page{Cursor: "%%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor: %v", err)
	}
}

func TestPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{Name: f.pfx + " P" + string(rune('a'+i)), StockManaged: true}); err != nil {
			t.Fatal(err)
		}
	}
	seen, cursor := 0, ""
	for page := 0; page < 1000; page++ {
		res, err := f.repo.ListProducts(ctx, application.ProductFilter{TitlePrefix: f.pfx, Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil || len(res.Items) > 2 {
			t.Fatalf("page: %v (%d items)", err, len(res.Items))
		}
		seen += len(res.Items)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if seen != 5 {
		t.Errorf("saw %d of 5 products", seen)
	}
}

func TestDatabaseInvariants(t *testing.T) {
	f := newFixture(t)
	p, _ := f.repo.InsertProduct(context.Background(), f.caller(), application.NewProduct{Name: f.pfx + " X", StockManaged: true})
	for name, sql := range map[string]string{
		"blank name":   `UPDATE products.products SET name = '  ' WHERE id = $1`,
		"zero version": `UPDATE products.products SET version = 0 WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(context.Background(), sql, p.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSearchProductIDsMatchesNameManufacturerAndPartNumber(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mfr, _ := f.repo.InsertManufacturer(ctx, f.caller(), f.pfx+" Nordtech")
	mpn := f.pfx + "-MPN-77"
	p, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{Name: f.pfx + " Workstation WS-100", ManufacturerID: &mfr.ID, ManufacturerPartNumber: &mpn, AssetManaged: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.repo.InsertProduct(ctx, f.caller(), application.NewProduct{Name: f.pfx + " Monitor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"workstation", "WS-100", "nordtech", "mpn-77", f.pfx, "ws"} {
		ids, err := f.repo.SearchProductIDs(ctx, q, 50)
		if err != nil || !slices.Contains(ids, p.ID) {
			t.Errorf("%q misses the workstation: %v %v", q, ids, err)
		}
	}
	if ids, _ := f.repo.SearchProductIDs(ctx, "nordtech", 50); slices.Contains(ids, other.ID) {
		t.Error("the manufacturer match must not return products of other manufacturers")
	}
	if ids, _ := f.repo.SearchProductIDs(ctx, "%", 50); slices.Contains(ids, p.ID) || slices.Contains(ids, other.ID) {
		t.Errorf("a percent sign is a literal: %v", ids)
	}
}
