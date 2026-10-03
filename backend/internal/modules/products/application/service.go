package application

import "context"

// Service performs Products operations. Audit actions: products.manufacturer.
// created/renamed, products.category.created/renamed, products.product.
// created/updated/activated/deactivated.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) canView(p Principal) bool { return p.View || p.Manage }

// ---- manufacturers ----

func (s *Service) CreateManufacturer(ctx context.Context, c Caller, p Principal, name string) (Manufacturer, error) {
	if err := c.validate(); err != nil {
		return Manufacturer{}, err
	}
	if !p.Manage {
		return Manufacturer{}, ErrForbidden
	}
	n, err := cleanName("name", name)
	if err != nil {
		return Manufacturer{}, err
	}
	return s.store.InsertManufacturer(ctx, c, n)
}

func (s *Service) RenameManufacturer(ctx context.Context, c Caller, p Principal, id string, expected int, name string) (Manufacturer, error) {
	if err := c.validate(); err != nil {
		return Manufacturer{}, err
	}
	if !p.Manage {
		return Manufacturer{}, ErrForbidden
	}
	n, err := cleanName("name", name)
	if err != nil {
		return Manufacturer{}, err
	}
	return s.store.ChangeManufacturer(ctx, c, id, func(cur Manufacturer) (Change[Manufacturer], error) {
		if expected != cur.Version {
			return Change[Manufacturer]{}, ErrVersionConflict
		}
		if n == cur.Name {
			return Change[Manufacturer]{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.Name = n
		return Change[Manufacturer]{Next: next, Action: "products.manufacturer.renamed"}, nil
	})
}

func (s *Service) ListManufacturers(ctx context.Context, p Principal, prefix string, page Page) (Result[Manufacturer], error) {
	if !s.canView(p) {
		return Result[Manufacturer]{Items: []Manufacturer{}}, ErrForbidden
	}
	return s.store.ListManufacturers(ctx, prefix, page.Normalize())
}

// ---- categories ----

func (s *Service) CreateCategory(ctx context.Context, c Caller, p Principal, name string, parentID *string) (Category, error) {
	if err := c.validate(); err != nil {
		return Category{}, err
	}
	if !p.Manage {
		return Category{}, ErrForbidden
	}
	n, err := cleanName("name", name)
	if err != nil {
		return Category{}, err
	}
	return s.store.InsertCategory(ctx, c, n, parentID)
}

func (s *Service) RenameCategory(ctx context.Context, c Caller, p Principal, id string, expected int, name string) (Category, error) {
	if err := c.validate(); err != nil {
		return Category{}, err
	}
	if !p.Manage {
		return Category{}, ErrForbidden
	}
	n, err := cleanName("name", name)
	if err != nil {
		return Category{}, err
	}
	return s.store.ChangeCategory(ctx, c, id, func(cur Category) (Change[Category], error) {
		if expected != cur.Version {
			return Change[Category]{}, ErrVersionConflict
		}
		if n == cur.Name {
			return Change[Category]{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.Name = n
		return Change[Category]{Next: next, Action: "products.category.renamed"}, nil
	})
}

func (s *Service) ListCategories(ctx context.Context, p Principal, prefix string, page Page) (Result[Category], error) {
	if !s.canView(p) {
		return Result[Category]{Items: []Category{}}, ErrForbidden
	}
	return s.store.ListCategories(ctx, prefix, page.Normalize())
}

// ---- products ----

// ProductInput is the input of CreateProduct.
type ProductInput struct {
	Name                   string
	ManufacturerID         *string
	CategoryID             *string
	ManufacturerPartNumber string
	InternalPartNumber     string
	Serialized             bool
	// StockManaged defaults to true when nil (the table default).
	StockManaged *bool
	AssetManaged bool
}

func (s *Service) CreateProduct(ctx context.Context, c Caller, p Principal, in ProductInput) (Product, error) {
	if err := c.validate(); err != nil {
		return Product{}, err
	}
	if !p.Manage {
		return Product{}, ErrForbidden
	}
	name, err := cleanName("name", in.Name)
	if err != nil {
		return Product{}, err
	}
	mpn, err := cleanPart("manufacturer part number", in.ManufacturerPartNumber)
	if err != nil {
		return Product{}, err
	}
	ipn, err := cleanPart("internal part number", in.InternalPartNumber)
	if err != nil {
		return Product{}, err
	}
	stock := true
	if in.StockManaged != nil {
		stock = *in.StockManaged
	}
	return s.store.InsertProduct(ctx, c, NewProduct{
		Name: name, ManufacturerID: in.ManufacturerID, CategoryID: in.CategoryID,
		ManufacturerPartNumber: mpn, InternalPartNumber: ipn,
		Serialized: in.Serialized, StockManaged: stock, AssetManaged: in.AssetManaged,
	})
}

func (s *Service) GetProduct(ctx context.Context, p Principal, id string) (Product, error) {
	if !s.canView(p) {
		return Product{}, ErrForbidden
	}
	return s.store.GetProduct(ctx, id)
}

func (s *Service) ListProducts(ctx context.Context, p Principal, f ProductFilter) (Result[Product], error) {
	if !s.canView(p) {
		return Result[Product]{Items: []Product{}}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	return s.store.ListProducts(ctx, f)
}

// UpdateProductInput changes a product; nil fields stay unchanged. Part
// numbers: an empty string clears. ClearManufacturer/ClearCategory remove the
// reference and exclude setting it.
type UpdateProductInput struct {
	Name                   *string
	ManufacturerID         *string
	ClearManufacturer      bool
	CategoryID             *string
	ClearCategory          bool
	ManufacturerPartNumber *string
	InternalPartNumber     *string
	Serialized             *bool
	StockManaged           *bool
	AssetManaged           *bool
}

func (in UpdateProductInput) empty() bool {
	return in.Name == nil && in.ManufacturerID == nil && !in.ClearManufacturer && in.CategoryID == nil && !in.ClearCategory &&
		in.ManufacturerPartNumber == nil && in.InternalPartNumber == nil && in.Serialized == nil && in.StockManaged == nil && in.AssetManaged == nil
}

func (s *Service) UpdateProduct(ctx context.Context, c Caller, p Principal, id string, expected int, in UpdateProductInput) (Product, error) {
	if err := c.validate(); err != nil {
		return Product{}, err
	}
	if !p.Manage {
		return Product{}, ErrForbidden
	}
	if in.empty() {
		return Product{}, invalid("at least one field to change is required")
	}
	if (in.ClearManufacturer && in.ManufacturerID != nil) || (in.ClearCategory && in.CategoryID != nil) {
		return Product{}, invalid("clearing a reference excludes setting it")
	}
	var name *string
	if in.Name != nil {
		n, err := cleanName("name", *in.Name)
		if err != nil {
			return Product{}, err
		}
		name = &n
	}
	var mpn, ipn *string
	if in.ManufacturerPartNumber != nil {
		v, err := cleanPart("manufacturer part number", *in.ManufacturerPartNumber)
		if err != nil {
			return Product{}, err
		}
		mpn = v
	}
	if in.InternalPartNumber != nil {
		v, err := cleanPart("internal part number", *in.InternalPartNumber)
		if err != nil {
			return Product{}, err
		}
		ipn = v
	}
	return s.store.ChangeProduct(ctx, c, id, func(cur Product) (Change[Product], error) {
		if expected != cur.Version {
			return Change[Product]{}, ErrVersionConflict
		}
		next := cur
		var changed []string
		if name != nil && *name != cur.Name {
			next.Name = *name
			changed = append(changed, "name")
		}
		switch {
		case in.ClearManufacturer && cur.ManufacturerID != nil:
			next.ManufacturerID = nil
			changed = append(changed, "manufacturer")
		case in.ManufacturerID != nil && !equalPtr(in.ManufacturerID, cur.ManufacturerID):
			next.ManufacturerID = in.ManufacturerID
			changed = append(changed, "manufacturer")
		}
		switch {
		case in.ClearCategory && cur.CategoryID != nil:
			next.CategoryID = nil
			changed = append(changed, "category")
		case in.CategoryID != nil && !equalPtr(in.CategoryID, cur.CategoryID):
			next.CategoryID = in.CategoryID
			changed = append(changed, "category")
		}
		if in.ManufacturerPartNumber != nil && !equalPtr(mpn, cur.ManufacturerPartNumber) {
			next.ManufacturerPartNumber = mpn
			changed = append(changed, "manufacturerPartNumber")
		}
		if in.InternalPartNumber != nil && !equalPtr(ipn, cur.InternalPartNumber) {
			next.InternalPartNumber = ipn
			changed = append(changed, "internalPartNumber")
		}
		if in.Serialized != nil && *in.Serialized != cur.Serialized {
			next.Serialized = *in.Serialized
			changed = append(changed, "serialized")
		}
		if in.StockManaged != nil && *in.StockManaged != cur.StockManaged {
			next.StockManaged = *in.StockManaged
			changed = append(changed, "stockManaged")
		}
		if in.AssetManaged != nil && *in.AssetManaged != cur.AssetManaged {
			next.AssetManaged = *in.AssetManaged
			changed = append(changed, "assetManaged")
		}
		if len(changed) == 0 {
			return Change[Product]{NoChange: true, Next: cur}, nil
		}
		return Change[Product]{Next: next, Action: "products.product.updated", Metadata: map[string]any{"changedFields": changed}}, nil
	})
}

// SetProductActive activates or deactivates a product; deactivated products
// are kept for history but are not offered. Repeating the state is a no-op.
func (s *Service) SetProductActive(ctx context.Context, c Caller, p Principal, id string, expected *int, active bool) (Product, error) {
	if err := c.validate(); err != nil {
		return Product{}, err
	}
	if !p.Manage {
		return Product{}, ErrForbidden
	}
	return s.store.ChangeProduct(ctx, c, id, func(cur Product) (Change[Product], error) {
		if expected != nil && *expected != cur.Version {
			return Change[Product]{}, ErrVersionConflict
		}
		if cur.Active == active {
			return Change[Product]{NoChange: true, Next: cur}, nil
		}
		next := cur
		next.Active = active
		action := "products.product.deactivated"
		if active {
			action = "products.product.activated"
		}
		return Change[Product]{Next: next, Action: action}, nil
	})
}
