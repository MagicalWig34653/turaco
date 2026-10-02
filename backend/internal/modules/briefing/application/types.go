// Package application holds the IT Briefing use cases. Briefing owns the
// editorial layer only: manual items today, references to authoritative
// records from other modules later (docs/architecture/module-boundaries.md).
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

// Item statuses (draft → published → withdrawn).
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusWithdrawn = "withdrawn"
)

// Severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

var (
	statuses   = []string{StatusDraft, StatusPublished, StatusWithdrawn}
	severities = []string{SeverityInfo, SeverityWarning, SeverityCritical}
)

const (
	maxTitleLength = 200
	maxBodyLength  = 10000
	DefaultLimit   = 50
	MaxLimit       = 200
)

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Item is a briefing item.
type Item struct {
	ID       string
	Title    string
	Body     string
	Severity string
	Status   string
	// ValidUntil hides a published item from viewers after this instant.
	ValidUntil        *time.Time
	AuthorUserID      *string
	PublishedAt       *time.Time
	PublishedByUserID *string
	WithdrawnAt       *time.Time
	WithdrawnByUserID *string
	// Version starts at 1 and increases with every change.
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Principal is the caller's briefing authority.
type Principal struct {
	UserID string
	// View (briefing.view) sees published, unexpired items.
	View bool
	// Manage (briefing.manage) sees every item and changes them.
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
		return errors.New("briefing: correlation id is required")
	}
	return nil
}

var (
	// ErrNotFound also hides items the caller may not see.
	ErrNotFound = errors.New("briefing: not found")
	// ErrForbidden means the caller may see the area but not do this.
	ErrForbidden = errors.New("briefing: forbidden")
	// ErrVersionConflict means the item changed since the caller read it.
	ErrVersionConflict = errors.New("briefing: version conflict")
	// ErrInvalidCursor is returned for a malformed pagination cursor.
	ErrInvalidCursor = errors.New("briefing: invalid cursor")
)

// InvalidTransitionError reports an operation the item's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("briefing: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "briefing: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is cursor pagination in the shared list order.
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

// ListQuery selects items. PublishedOnly restricts to published, unexpired
// items (what viewers see); Status filters managers' lists.
type ListQuery struct {
	PublishedOnly bool
	Status        string
	Page          Page
}

// Event is a domain event recorded with a change.
type Event struct {
	Type    string
	Payload map[string]any
}

// Change is a decision made inside the store's transaction (see tasks).
type Change struct {
	Next     Item
	NoChange bool
	Action   string
	Metadata map[string]any
	Events   []Event
}

// NewItem is the input of Store.Insert; the item starts as a draft.
type NewItem struct {
	Title      string
	Body       string
	Severity   string
	ValidUntil *time.Time
	Author     *string
}

// Store is the persistence port of briefing items.
type Store interface {
	Insert(ctx context.Context, c Caller, n NewItem) (Item, error)
	Get(ctx context.Context, id string) (Item, error)
	// Change locks the item and applies decide in one transaction with the
	// audit event and outbox events; ErrNotFound for an unknown id.
	Change(ctx context.Context, c Caller, id string, decide func(Item) (Change, error)) (Item, error)
	// Delete removes a draft (audited); decide may refuse.
	Delete(ctx context.Context, c Caller, id string, decide func(Item) error) error
	List(ctx context.Context, q ListQuery) (Result, error)
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxTitleLength || !utf8.ValidString(s) {
		return "", invalid("title must be 1-%d characters", maxTitleLength)
	}
	if safetext.ContainsUnsafe(s, false) {
		return "", invalid("title must not contain control or invisible formatting characters")
	}
	return s, nil
}

func cleanBody(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxBodyLength || !utf8.ValidString(s) {
		return "", invalid("body must be at most %d characters", maxBodyLength)
	}
	if safetext.ContainsUnsafe(s, true) {
		return "", invalid("body must not contain control or invisible formatting characters")
	}
	return s, nil
}

func validSeverity(s string) error {
	if !contains(severities, s) {
		return invalid("severity must be one of info, warning, critical")
	}
	return nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC().Truncate(time.Microsecond)
	return &u
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
