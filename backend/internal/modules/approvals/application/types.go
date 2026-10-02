// Package application holds the shared Approval use cases: requesting an
// approval for a subject of another module, deciding it and listing a User's
// inbox (docs/domain/state-machines.md, "Approval").
package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Approval statuses.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

// Decisions.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
	maxLabel     = 200
	maxComment   = 1000
)

// Approval is one decision about a subject.
type Approval struct {
	ID           string
	SubjectType  string
	SubjectID    string
	SubjectLabel string
	StepIndex    int
	Status       string
	// Exactly one of ApproverUserID and ApproverTeamID is set.
	ApproverUserID  *string
	ApproverTeamID  *string
	ExcludedUserIDs []string
	RequestedByID   *string
	DecidedByUserID *string
	DecidedAt       *time.Time
	DecisionComment *string
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewApproval is the input of Store.InsertTx; the approval starts pending.
type NewApproval struct {
	SubjectType     string
	SubjectID       string
	SubjectLabel    string
	StepIndex       int
	ApproverUserID  *string
	ApproverTeamID  *string
	ExcludedUserIDs []string
	RequestedBy     *string
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
		return errors.New("approvals: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound = errors.New("approvals: not found")
	// ErrNotApprover means the caller is not an approver of this approval or is excluded from deciding.
	ErrNotApprover = errors.New("approvals: caller may not decide this approval")
	// ErrNotPending means the approval was already decided or cancelled.
	ErrNotPending      = errors.New("approvals: approval is not pending")
	ErrConflict        = errors.New("approvals: conflict")
	ErrVersionConflict = errors.New("approvals: version conflict")
	ErrInvalidCursor   = errors.New("approvals: invalid cursor")
	// ErrApproverInvalid means the approving User or Team is not active.
	ErrApproverInvalid = errors.New("approvals: approver is not an active user or team")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "approvals: invalid input: " + e.Message }

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
	Items      []Approval
	NextCursor string
}

// Event is a domain event recorded with a change.
type Event struct {
	Type    string
	Payload map[string]any
}

// InboxQuery selects approvals relevant to one User: pending ones they may
// decide (Pending) or ones they decided (Decided).
type InboxQuery struct {
	UserID  string
	TeamIDs []string
	// Status is "pending" (approvals the User may decide now) or "decided"
	// (approvals the User decided).
	Status string
	Page   Page
}

// Store is the persistence port of approvals.
type Store interface {
	// InsertTx creates a pending approval and emits ApprovalRequested inside the caller's transaction.
	InsertTx(ctx context.Context, tx pgx.Tx, c Caller, n NewApproval) (Approval, error)
	Get(ctx context.Context, id string) (Approval, error)
	// Decide locks the approval and applies decide, storing the result with
	// version+1, the audit event and the events in one transaction.
	Decide(ctx context.Context, c Caller, id string, decide func(Approval) (Approval, []Event, error)) (Approval, error)
	// CancelBySubjectTx cancels the pending approvals of a subject inside the caller's transaction.
	CancelBySubjectTx(ctx context.Context, tx pgx.Tx, c Caller, subjectType, subjectID string) (int, error)
	ForSubject(ctx context.Context, subjectType, subjectID string) ([]Approval, error)
	Inbox(ctx context.Context, q InboxQuery) (Result, error)
	// IsApproverFor reports whether the User (directly or through one of the
	// Teams) is or was an approver of any step of the subject.
	IsApproverFor(ctx context.Context, subjectType, subjectID, userID string, teamIDs []string) (bool, error)
}

// Directory answers the Organization questions approvals need.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	CurrentTeamIDs(ctx context.Context, userID string) ([]string, error)
	CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error)
}

func cleanLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxLabel || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("subject label must be 1-%d characters without control or invisible formatting characters", maxLabel)
	}
	return s, nil
}

func cleanComment(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > maxComment || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return nil, invalid("comment must be at most %d characters without control or invisible formatting characters", maxComment)
	}
	return &s, nil
}
