// Package application holds the Products use cases: manufacturers, product
// categories and products (docs/domain/core-data-model.md, "Products").
package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

const (
	DefaultLimit  = 50
	MaxLimit      = 200
	maxNameLength = 200
	maxPartLength = 100
)

// Manufacturer is the maker of a product (distinct from a Supplier).
type Manufacturer struct {
	ID        string
	Name      string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Category is a node of the product category tree. The parent is fixed at
// creation, so the tree cannot contain cycles.
type Category struct {
	ID        string
	Name      string
	ParentID  *string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Product is a generic catalog/procurement/inventory item (not an Asset).
type Product struct {
	ID                     string
	Name                   string
	ManufacturerID         *string
	CategoryID             *string
	ManufacturerPartNumber *string
	InternalPartNumber     *string
	Serialized             bool
	StockManaged           bool
	AssetManaged           bool
	Active                 bool
	Version                int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Principal is the caller's products authority.
type Principal struct {
	UserID string
	View   bool
	Manage bool
}

// Caller identifies who performs a mutation and the request it belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

func (c Caller) validate() error {
	if err := c.Actor.Validate(); err != nil {
		return err
	}
	if c.CorrelationID == "" {
		return errors.New("products: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound        = errors.New("products: not found")
	ErrForbidden       = errors.New("products: forbidden")
	ErrConflict        = errors.New("products: conflict")
	ErrVersionConflict = errors.New("products: version conflict")
	// ErrReferenceNotFound means a referenced manufacturer or category does not exist.
	ErrReferenceNotFound = errors.New("products: referenced manufacturer or category not found")
	ErrInvalidCursor     = errors.New("products: invalid cursor")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "products: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key (ascending).
type Page struct {
	Limit  int
	Cursor string
}

func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// Result is one page; NextCursor is empty on the last page.
type Result[T any] struct {
	Items      []T
	NextCursor string
}

// ProductFilter selects products; all fields are optional.
type ProductFilter struct {
	TitlePrefix    string
	CategoryID     string
	ManufacturerID string
	// Active filters by activity when non-nil.
	Active *bool
	Page   Page
}

// Change is a decision made inside the store's transaction (see tasks).
type Change[T any] struct {
	Next     T
	NoChange bool
	Action   string
	Metadata map[string]any
}

// Store is the persistence port of the Products module.
type Store interface {
	InsertManufacturer(ctx context.Context, c Caller, name string) (Manufacturer, error)
	ChangeManufacturer(ctx context.Context, c Caller, id string, decide func(Manufacturer) (Change[Manufacturer], error)) (Manufacturer, error)
	ListManufacturers(ctx context.Context, prefix string, p Page) (Result[Manufacturer], error)

	InsertCategory(ctx context.Context, c Caller, name string, parentID *string) (Category, error)
	ChangeCategory(ctx context.Context, c Caller, id string, decide func(Category) (Change[Category], error)) (Category, error)
	ListCategories(ctx context.Context, prefix string, p Page) (Result[Category], error)
	// CategoriesByIDs returns id -> true for existing categories among ids.
	CategoriesByIDs(ctx context.Context, ids []string) (map[string]bool, error)

	InsertProduct(ctx context.Context, c Caller, n NewProduct) (Product, error)
	GetProduct(ctx context.Context, id string) (Product, error)
	ChangeProduct(ctx context.Context, c Caller, id string, decide func(Product) (Change[Product], error)) (Product, error)
	ListProducts(ctx context.Context, f ProductFilter) (Result[Product], error)
}

// NewProduct is the input of Store.InsertProduct; the product starts active.
type NewProduct struct {
	Name                   string
	ManufacturerID         *string
	CategoryID             *string
	ManufacturerPartNumber *string
	InternalPartNumber     *string
	Serialized             bool
	StockManaged           bool
	AssetManaged           bool
}

func cleanName(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxNameLength || !utf8.ValidString(s) {
		return "", invalid("%s must be 1-%d characters", field, maxNameLength)
	}
	if safetext.ContainsUnsafe(s, false) {
		return "", invalid("%s must not contain control or invisible formatting characters", field)
	}
	return s, nil
}

// cleanPart trims an optional part number; empty means absent.
func cleanPart(field, s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > maxPartLength || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return nil, invalid("%s must be at most %d characters without control characters", field, maxPartLength)
	}
	return &s, nil
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
