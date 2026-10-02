package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	maxTitle     = 200
	maxDesc      = 2000
)

var itemKey = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// Item is a Catalog Item: a user-facing offering with its definition.
type Item struct {
	ID          string
	Key         string
	Title       string
	Description string
	Definition  Definition
	Active      bool
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Principal is the caller's catalog authority: every signed-in User may read
// active items; Manage (catalog.manage) administers all of them.
type Principal struct {
	UserID string
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
		return errors.New("catalog: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound        = errors.New("catalog: not found")
	ErrForbidden       = errors.New("catalog: forbidden")
	ErrConflict        = errors.New("catalog: conflict")
	ErrVersionConflict = errors.New("catalog: version conflict")
	ErrInvalidCursor   = errors.New("catalog: invalid cursor")
	// ErrReferenceInvalid means the definition references an inactive or unknown user, team, product or category.
	ErrReferenceInvalid = errors.New("catalog: definition references an unknown or inactive record")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "catalog: invalid input: " + e.Message }

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
type Result struct {
	Items      []Item
	NextCursor string
}

// ListQuery selects items; ActiveOnly is set for callers without catalog.manage.
type ListQuery struct {
	ActiveOnly bool
	// Status filters managers' lists: "active" or "inactive"; empty means all.
	Status string
	Page   Page
}

// NewItem is the input of Store.Insert; the item starts active.
type NewItem struct {
	Key         string
	Title       string
	Description string
	Definition  []byte
}

// Change is a decision made inside the store's transaction (see tasks).
type Change struct {
	Next     Item
	NoChange bool
	Action   string
	Metadata map[string]any
}

// Store is the persistence port of Catalog Items.
type Store interface {
	Insert(ctx context.Context, c Caller, n NewItem) (Item, error)
	Get(ctx context.Context, id string) (Item, error)
	Change(ctx context.Context, c Caller, id string, decide func(Item) (Change, error)) (Item, error)
	List(ctx context.Context, q ListQuery) (Result, error)
}

// Directory answers the Organization questions the catalog needs.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
}

// ProductChoice is a product offered by a product field.
type ProductChoice struct {
	ID   string
	Name string
}

// Products answers the Products questions the catalog needs (adapter over
// products/public in the composition root).
type Products interface {
	// ActiveProducts returns id -> category id ("" when none) for active products among ids.
	ActiveProducts(ctx context.Context, ids []string) (map[string]string, error)
	// ProductNames returns id -> name for existing products.
	ProductNames(ctx context.Context, ids []string) (map[string]string, error)
	CategoriesExist(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveInCategory(ctx context.Context, categoryID string, limit int) ([]ProductChoice, error)
}
