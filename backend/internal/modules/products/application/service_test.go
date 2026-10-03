package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// stubStore applies decide on one held row per kind, like the real store.
type stubStore struct {
	Store
	manufacturer Manufacturer
	category     Category
	product      Product
	inserted     NewProduct
	calls        int
}

func (s *stubStore) InsertManufacturer(_ context.Context, _ Caller, name string) (Manufacturer, error) {
	s.calls++
	return Manufacturer{Name: name, Version: 1}, nil
}
func (s *stubStore) ChangeManufacturer(_ context.Context, _ Caller, _ string, decide func(Manufacturer) (Change[Manufacturer], error)) (Manufacturer, error) {
	s.calls++
	ch, err := decide(s.manufacturer)
	if err != nil || ch.NoChange {
		return s.manufacturer, err
	}
	return ch.Next, nil
}
func (s *stubStore) InsertCategory(_ context.Context, _ Caller, name string, parent *string) (Category, error) {
	s.calls++
	return Category{Name: name, ParentID: parent, Version: 1}, nil
}
func (s *stubStore) ChangeCategory(_ context.Context, _ Caller, _ string, decide func(Category) (Change[Category], error)) (Category, error) {
	s.calls++
	ch, err := decide(s.category)
	if err != nil || ch.NoChange {
		return s.category, err
	}
	return ch.Next, nil
}
func (s *stubStore) InsertProduct(_ context.Context, _ Caller, n NewProduct) (Product, error) {
	s.calls++
	s.inserted = n
	return Product{Name: n.Name, Active: true, Version: 1, StockManaged: n.StockManaged}, nil
}
func (s *stubStore) ChangeProduct(_ context.Context, _ Caller, _ string, decide func(Product) (Change[Product], error)) (Product, error) {
	s.calls++
	ch, err := decide(s.product)
	if err != nil || ch.NoChange {
		return s.product, err
	}
	return ch.Next, nil
}

var (
	manager = Principal{UserID: "00000000-0000-7000-8000-0000000000a1", Manage: true}
	viewer  = Principal{UserID: "00000000-0000-7000-8000-0000000000b1", View: true}
	nobody  = Principal{UserID: "00000000-0000-7000-8000-0000000000c1"}
)

func cl() Caller          { return Caller{Actor: audit.UserActor(manager.UserID), CorrelationID: "c"} }
func sp(s string) *string { return &s }

func TestWritesRequireManage(t *testing.T) {
	st := &stubStore{}
	s := NewService(st)
	ctx := context.Background()
	for _, p := range []Principal{viewer, nobody} {
		if _, err := s.CreateManufacturer(ctx, cl(), p, "x"); !errors.Is(err, ErrForbidden) {
			t.Errorf("manufacturer: %v", err)
		}
		if _, err := s.RenameManufacturer(ctx, cl(), p, "id", 1, "x"); !errors.Is(err, ErrForbidden) {
			t.Errorf("rename manufacturer: %v", err)
		}
		if _, err := s.CreateCategory(ctx, cl(), p, "x", nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("category: %v", err)
		}
		if _, err := s.RenameCategory(ctx, cl(), p, "id", 1, "x"); !errors.Is(err, ErrForbidden) {
			t.Errorf("rename category: %v", err)
		}
		if _, err := s.CreateProduct(ctx, cl(), p, ProductInput{Name: "x"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("product: %v", err)
		}
		if _, err := s.UpdateProduct(ctx, cl(), p, "id", 1, UpdateProductInput{Name: sp("x")}); !errors.Is(err, ErrForbidden) {
			t.Errorf("update product: %v", err)
		}
		if _, err := s.SetProductActive(ctx, cl(), p, "id", nil, false); !errors.Is(err, ErrForbidden) {
			t.Errorf("activate: %v", err)
		}
	}
	if st.calls != 0 {
		t.Errorf("store called %d times without permission", st.calls)
	}
	for _, p := range []Principal{nobody} {
		if _, err := s.ListProducts(ctx, p, ProductFilter{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("list without view: %v", err)
		}
		if _, err := s.GetProduct(ctx, p, "id"); !errors.Is(err, ErrForbidden) {
			t.Errorf("get without view: %v", err)
		}
	}
	for _, p := range []Principal{viewer, manager} {
		if _, err := s.ListProducts(ctx, p, ProductFilter{}); errors.Is(err, ErrForbidden) {
			t.Errorf("list with %+v: %v", p, err)
		}
	}
}

func TestNamesAreValidated(t *testing.T) {
	s := NewService(&stubStore{})
	ctx := context.Background()
	for name, in := range map[string]string{"blank": "  ", "long": strings.Repeat("x", 201), "control": "a\x00b", "override": "Dell‮"} {
		var inv *InvalidInputError
		if _, err := s.CreateManufacturer(ctx, cl(), manager, in); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := s.CreateProduct(ctx, cl(), manager, ProductInput{Name: in}); !errors.As(err, &inv) {
			t.Errorf("product %s: %v", name, err)
		}
	}
	var inv *InvalidInputError
	if _, err := s.CreateProduct(ctx, cl(), manager, ProductInput{Name: "x", InternalPartNumber: strings.Repeat("9", 101)}); !errors.As(err, &inv) {
		t.Errorf("long part number: %v", err)
	}
}

func TestCreateProductDefaultsAndTrimming(t *testing.T) {
	st := &stubStore{}
	s := NewService(st)
	p, err := s.CreateProduct(context.Background(), cl(), manager, ProductInput{Name: "  Latitude 7450 ", InternalPartNumber: " LAT-7450 ", ManufacturerPartNumber: "  "})
	if err != nil || p.Name != "Latitude 7450" || !p.StockManaged {
		t.Fatalf("product = %+v %v", p, err)
	}
	if st.inserted.InternalPartNumber == nil || *st.inserted.InternalPartNumber != "LAT-7450" || st.inserted.ManufacturerPartNumber != nil {
		t.Errorf("part numbers = %v %v", st.inserted.InternalPartNumber, st.inserted.ManufacturerPartNumber)
	}
	off := false
	if _, err := s.CreateProduct(context.Background(), cl(), manager, ProductInput{Name: "Cable", StockManaged: &off}); err != nil || st.inserted.StockManaged {
		t.Errorf("explicit stockManaged=false must be kept: %v", err)
	}
}

func TestRenameUsesVersionAndIsIdempotent(t *testing.T) {
	st := &stubStore{manufacturer: Manufacturer{Name: "Dell", Version: 3}, category: Category{Name: "Laptops", Version: 2}}
	s := NewService(st)
	ctx := context.Background()
	if _, err := s.RenameManufacturer(ctx, cl(), manager, "id", 2, "Dell Inc."); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale manufacturer: %v", err)
	}
	if m, err := s.RenameManufacturer(ctx, cl(), manager, "id", 3, "Dell Inc."); err != nil || m.Name != "Dell Inc." {
		t.Errorf("rename = %+v %v", m, err)
	}
	if _, err := s.RenameCategory(ctx, cl(), manager, "id", 1, "x"); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale category: %v", err)
	}
	if c, err := s.RenameCategory(ctx, cl(), manager, "id", 2, "Laptops"); err != nil || c.Name != "Laptops" {
		t.Errorf("unchanged rename: %+v %v", c, err)
	}
}

func TestUpdateProduct(t *testing.T) {
	mfr, cat := "00000000-0000-7000-8000-0000000000e1", "00000000-0000-7000-8000-0000000000e2"
	st := &stubStore{product: Product{Name: "old", ManufacturerID: &mfr, CategoryID: &cat, InternalPartNumber: sp("A"), Version: 4, Active: true, StockManaged: true}}
	s := NewService(st)
	ctx := context.Background()
	var inv *InvalidInputError
	if _, err := s.UpdateProduct(ctx, cl(), manager, "id", 4, UpdateProductInput{}); !errors.As(err, &inv) {
		t.Errorf("empty: %v", err)
	}
	if _, err := s.UpdateProduct(ctx, cl(), manager, "id", 4, UpdateProductInput{ClearCategory: true, CategoryID: &cat}); !errors.As(err, &inv) {
		t.Errorf("clear and set: %v", err)
	}
	if _, err := s.UpdateProduct(ctx, cl(), manager, "id", 3, UpdateProductInput{Name: sp("x")}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale: %v", err)
	}
	yes := true
	u, err := s.UpdateProduct(ctx, cl(), manager, "id", 4, UpdateProductInput{
		Name: sp(" new "), ClearManufacturer: true, InternalPartNumber: sp(""), Serialized: &yes,
	})
	if err != nil || u.Name != "new" || u.ManufacturerID != nil || u.CategoryID == nil || u.InternalPartNumber != nil || !u.Serialized {
		t.Fatalf("update = %+v %v", u, err)
	}
	if n, err := s.UpdateProduct(ctx, cl(), manager, "id", 4, UpdateProductInput{Name: sp("old"), CategoryID: &cat}); err != nil || n.Name != "old" {
		t.Errorf("no-op update: %+v %v", n, err)
	}
}

func TestSetProductActiveIsIdempotent(t *testing.T) {
	st := &stubStore{product: Product{Active: true, Version: 1}}
	s := NewService(st)
	ctx := context.Background()
	if p, err := s.SetProductActive(ctx, cl(), manager, "id", nil, true); err != nil || !p.Active {
		t.Errorf("activating an active product: %+v %v", p, err)
	}
	if p, err := s.SetProductActive(ctx, cl(), manager, "id", nil, false); err != nil || p.Active {
		t.Errorf("deactivate: %+v %v", p, err)
	}
	stale := 9
	if _, err := s.SetProductActive(ctx, cl(), manager, "id", &stale, false); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale: %v", err)
	}
}

func TestCallerIsRequired(t *testing.T) {
	s := NewService(&stubStore{})
	if _, err := s.CreateManufacturer(context.Background(), Caller{}, manager, "x"); err == nil {
		t.Error("an invalid caller must be rejected")
	}
}

func (s *stubStore) ListProducts(context.Context, ProductFilter) (Result[Product], error) {
	return Result[Product]{Items: []Product{}}, nil
}
func (s *stubStore) GetProduct(context.Context, string) (Product, error) { return Product{}, nil }
