// Package application holds the Service Request use cases: submitting a
// request against a Catalog Item, driving its approval steps and fulfillment
// tasks, and reading it (docs/product/f3-requests-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Request statuses. The state machine's "approved" is transient: approval of
// the last step starts fulfillment in the same transaction.
const (
	StatusPendingApproval = "pending_approval"
	StatusInFulfillment   = "in_fulfillment"
	StatusWaiting         = "waiting"
	StatusCompleted       = "completed"
	StatusRejected        = "rejected"
	StatusCancelled       = "cancelled"
)

// Waiting reasons.
var WaitingReasons = []string{"stock", "supplier", "requester", "external_system"}

const (
	DefaultLimit  = 50
	MaxLimit      = 200
	SubjectType   = "service_request"
	maxReasonLen  = 500
	maxAnswerSize = 64 << 10
)

// Request is a Service Request.
type Request struct {
	ID               string
	Reference        string
	CatalogItemID    string
	CatalogItemKey   string
	CatalogItemTitle string
	// Definition is the snapshot of the catalog definition the request was submitted against.
	Definition     catalogpublic.Definition
	Answers        map[string]any
	RequesterID    string
	RequestedForID string
	Status         string
	WaitingReason  *string
	StatusReason   *string
	// CurrentStep is the pending approval step, set exactly while pending_approval.
	CurrentStep *int
	SubmittedAt time.Time
	CompletedAt *time.Time
	Version     int
	UpdatedAt   time.Time
}

// Terminal reports whether the request is completed, rejected or cancelled.
func (r Request) Terminal() bool {
	return r.Status == StatusCompleted || r.Status == StatusRejected || r.Status == StatusCancelled
}

// Label is the short display text used in approvals and notifications.
func (r Request) Label() string {
	label := r.Reference + " · " + r.CatalogItemTitle
	if runes := []rune(label); len(runes) > maxLabel {
		return string(runes[:maxLabel-1]) + "…"
	}
	return label
}

// maxLabel is the longest subject label the Approvals module accepts.
const maxLabel = 200

// NewRequest is the input of Store.InsertTx.
type NewRequest struct {
	CatalogItemID    string
	CatalogItemKey   string
	CatalogItemTitle string
	Snapshot         []byte
	Answers          map[string]any
	RequesterID      string
	RequestedForID   string
	Status           string
	CurrentStep      *int
}

// RequestTask links a task to its request.
type RequestTask struct {
	TaskID        string
	TemplateIndex int
	Mandatory     bool
}

// Principal is the caller's request authority. Every signed-in User may
// submit requests and see their own; View (requests.view) sees all; Manage
// (requests.manage) also changes any request.
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
		return errors.New("requests: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound      = errors.New("requests: not found")
	ErrForbidden     = errors.New("requests: forbidden")
	ErrInvalidCursor = errors.New("requests: invalid cursor")
	// ErrItemInactive means the catalog item is not offered.
	ErrItemInactive = errors.New("requests: catalog item is not available")
	// ErrNoEligibleApprover means an approval step cannot be resolved to an eligible approver.
	ErrNoEligibleApprover = errors.New("requests: no eligible approver for an approval step")
	ErrVersionConflict    = errors.New("requests: version conflict")
	// ErrRequestedForInvalid means the requested-for User is not active or not allowed for this item.
	ErrRequestedForInvalid = errors.New("requests: requested-for user is not allowed")
)

// InvalidTransitionError reports an operation the request's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("requests: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "requests: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key (descending, newest first).
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
	Items      []Request
	NextCursor string
}

// ListQuery selects requests: "mine" (requester or requested-for User) or all.
type ListQuery struct {
	UserID string
	// All lists every request (requires requests.view); otherwise only the User's own.
	All    bool
	Status string
	Page   Page
}

// Store is the persistence port. Mutating methods run in the caller's
// transaction so that the request, its approvals and its tasks change together.
type Store interface {
	InsertTx(ctx context.Context, tx pgx.Tx, n NewRequest) (Request, error)
	AddReferencesTx(ctx context.Context, tx pgx.Tx, requestID string, refs []catalogpublic.Reference) error
	// LockTx returns the request FOR UPDATE; ErrNotFound for an unknown id.
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Request, error)
	// UpdateTx stores status, waiting reason, status reason, current step and
	// completion time with version+1.
	UpdateTx(ctx context.Context, tx pgx.Tx, r Request) (Request, error)
	AddTaskTx(ctx context.Context, tx pgx.Tx, requestID string, t RequestTask) error
	TasksTx(ctx context.Context, tx pgx.Tx, requestID string) ([]RequestTask, error)
	// RequestOfTaskTx returns the id of the request a task belongs to, or "" when none.
	RequestOfTaskTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error)

	Get(ctx context.Context, id string) (Request, error)
	Tasks(ctx context.Context, requestID string) ([]RequestTask, error)
	References(ctx context.Context, requestID string) ([]catalogpublic.Reference, error)
	List(ctx context.Context, q ListQuery) (Result, error)
	// InTx runs fn in one transaction.
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
}
