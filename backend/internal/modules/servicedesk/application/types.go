// Package application holds the Service Desk use cases: tickets (incidents)
// that employees raise with almost no friction and IT works, with public and
// internal comments (docs/product/f5-service-desk-design.md).
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Ticket statuses (docs/domain/state-machines.md).
const (
	StatusNew        = "new"
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusWaiting    = "waiting"
	StatusResolved   = "resolved"
	StatusClosed     = "closed"
	StatusCancelled  = "cancelled"
)

// Statuses lists every ticket status.
var Statuses = []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting, StatusResolved, StatusClosed, StatusCancelled}

// WaitingReasons are the reasons a ticket waits.
var WaitingReasons = []string{"customer", "vendor", "external_service", "scheduled_change", "hardware"}

// Priorities lists the ticket priorities.
var Priorities = []string{"low", "normal", "high", "urgent"}

const (
	DefaultLimit = 50
	MaxLimit     = 200
	maxTitle     = 200
	maxText      = 5000
	maxReason    = 500
)

// Ticket is a tracked support record.
type Ticket struct {
	ID             string
	Reference      string
	Kind           string
	Title          string
	Description    *string
	Status         string
	WaitingReason  *string
	StatusReason   *string
	Resolution     *string
	Priority       string
	ReporterID     string
	AffectedUserID string
	QueueTeamID    *string
	AssigneeID     *string
	AssetID        *string
	DeviceSnapshot map[string]any
	ResolvedAt     *time.Time
	ClosedAt       *time.Time
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Comment is a note on a ticket.
type Comment struct {
	ID        string
	TicketID  string
	AuthorID  string
	Body      string
	Internal  bool
	CreatedAt time.Time
}

// Principal is the caller's authority. Every signed-in User may raise tickets
// and read those they reported or are affected by; View (tickets.view) reads
// all tickets and internal comments; Manage (tickets.manage) works them.
type Principal struct {
	UserID string
	View   bool
	Manage bool
}

func (p Principal) staff() bool { return p.View || p.Manage }

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
		return errors.New("servicedesk: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound        = errors.New("servicedesk: not found")
	ErrForbidden       = errors.New("servicedesk: forbidden")
	ErrInvalidCursor   = errors.New("servicedesk: invalid cursor")
	ErrVersionConflict = errors.New("servicedesk: version conflict")
	ErrUserInvalid     = errors.New("servicedesk: user does not exist or is not active")
	ErrTeamInvalid     = errors.New("servicedesk: team does not exist or is not active")
	ErrDeviceInvalid   = errors.New("servicedesk: the device does not exist or is not assigned to the affected user")
)

// InvalidTransitionError reports an operation the ticket's status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("servicedesk: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "servicedesk: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key (newest first).
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
	Items      []Ticket
	NextCursor string
}

// Filter selects tickets.
type Filter struct {
	// UserID restricts to tickets the User reported or is affected by (set for "mine").
	UserID     string
	Status     string
	AssigneeID string
	QueueID    string
	// OpenOnly hides resolved, closed and cancelled tickets.
	OpenOnly bool
	Page     Page
}

// Store is the persistence port. Mutating methods run in the caller's
// transaction so changes, audit and events commit together.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	InsertTx(ctx context.Context, tx pgx.Tx, t Ticket) (Ticket, error)
	LockTx(ctx context.Context, tx pgx.Tx, id string) (Ticket, error)
	UpdateTx(ctx context.Context, tx pgx.Tx, t Ticket) (Ticket, error)
	Get(ctx context.Context, id string) (Ticket, error)
	List(ctx context.Context, f Filter) (Result, error)
	InsertCommentTx(ctx context.Context, tx pgx.Tx, c Comment) (Comment, error)
	Comments(ctx context.Context, ticketID string, includeInternal bool) ([]Comment, error)
}

// Directory answers the Organization questions the service desk needs.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	TeamNames(ctx context.Context, ids []string) (map[string]string, error)
}

// Device is the Assets contract the service desk uses to remember a device.
type Device interface {
	// Snapshot returns the device; holder, when set, must currently hold it. A missing
	// or foreign device is ErrNotFound.
	Snapshot(ctx context.Context, assetID, holderUserID string) (map[string]any, error)
}
