// Package application holds the Knowledge use cases: articles with an
// audience and a lifecycle, and search (docs/product/f5-service-desk-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Article statuses and audiences.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusRetired   = "retired"

	AudienceInternal = "internal"
	AudienceEmployee = "employee"

	DefaultLimit = 50
	MaxLimit     = 200
	maxTitle     = 200
	maxSummary   = 500
	maxBody      = 20000
)

// Article is a piece of reusable knowledge.
type Article struct {
	ID          string
	Reference   string
	Title       string
	Summary     string
	Body        string
	Audience    string
	Status      string
	AuthorID    *string
	PublishedAt *time.Time
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Principal is the caller's authority. Every signed-in User reads published
// employee articles; View (knowledge.view) also reads published internal
// ones; Manage (knowledge.manage) reads and writes everything.
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
		return errors.New("knowledge: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound        = errors.New("knowledge: not found")
	ErrForbidden       = errors.New("knowledge: forbidden")
	ErrInvalidCursor   = errors.New("knowledge: invalid cursor")
	ErrVersionConflict = errors.New("knowledge: version conflict")
)

// InvalidTransitionError reports an operation the article's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("knowledge: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "knowledge: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key (newest first) or, for a search, by rank position.
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

// Result is one page; NextCursor is empty on the last page and for searches.
type Result struct {
	Items      []Article
	NextCursor string
}

// Query selects articles; the repository applies the visibility the service computed.
type Query struct {
	Text string
	// Statuses and Audiences restrict what may be returned (the caller's visibility).
	Statuses  []string
	Audiences []string
	Page      Page
}

// Store is the persistence port.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	InsertTx(ctx context.Context, tx pgx.Tx, a Article) (Article, error)
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Article, error)
	UpdateTx(ctx context.Context, tx pgx.Tx, a Article) (Article, error)
	Get(ctx context.Context, id string) (Article, error)
	List(ctx context.Context, q Query) (Result, error)
}
