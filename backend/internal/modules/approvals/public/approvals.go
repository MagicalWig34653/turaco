// Package public is the Approvals module's contract for other modules: ask
// for approvals about a record of the caller, cancel them and read their
// state, without touching Approvals storage.
package public

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Caller identifies who requests or cancels approvals and the operation they belong to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

// Request describes one approval step.
type Request struct {
	SubjectType  string
	SubjectID    string
	SubjectLabel string
	StepIndex    int
	// Exactly one approver.
	ApproverUserID *string
	ApproverTeamID *string
	// ExcludedUserIDs can never decide this approval (requester, requested-for User).
	ExcludedUserIDs []string
	RequestedBy     *string
}

// Status values.
const (
	StatusPending   = application.StatusPending
	StatusApproved  = application.StatusApproved
	StatusRejected  = application.StatusRejected
	StatusCancelled = application.StatusCancelled
)

// ErrApproverInvalid means the approving User or Team is not active.
var ErrApproverInvalid = application.ErrApproverInvalid

// InvalidInputError is returned for unusable input.
type InvalidInputError = application.InvalidInputError

// Approval is the read view other modules get.
type Approval struct {
	ID              string
	StepIndex       int
	Status          string
	ApproverUserID  *string
	ApproverTeamID  *string
	DecidedByUserID *string
	DecidedAt       *time.Time
	DecisionComment *string
}

// Approvals is the module's public service.
type Approvals struct{ svc *application.Service }

func New(svc *application.Service) *Approvals { return &Approvals{svc: svc} }

// PendingForUserCount exposes only this User's actionable count; the caller
// must pass the authenticated User ID, never an untrusted request parameter.
func (a *Approvals) PendingForUserCount(ctx context.Context, userID string) (int, error) {
	return a.svc.PendingForUserCount(ctx, userID)
}

func ac(c Caller) application.Caller {
	return application.Caller{Actor: c.Actor, CorrelationID: c.CorrelationID}
}

// RequestInTx creates a pending approval in the caller's transaction and
// returns its id; ApprovalRequested is emitted with it.
func (a *Approvals) RequestInTx(ctx context.Context, tx pgx.Tx, c Caller, r Request) (string, error) {
	ap, err := a.svc.RequestInTx(ctx, tx, ac(c), application.RequestInput{
		SubjectType: r.SubjectType, SubjectID: r.SubjectID, SubjectLabel: r.SubjectLabel, StepIndex: r.StepIndex,
		ApproverUserID: r.ApproverUserID, ApproverTeamID: r.ApproverTeamID,
		ExcludedUserIDs: r.ExcludedUserIDs, RequestedBy: r.RequestedBy,
	})
	if err != nil {
		return "", err
	}
	return ap.ID, nil
}

// CancelBySubjectInTx cancels the pending approvals of a subject.
func (a *Approvals) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, c Caller, subjectType, subjectID string) (int, error) {
	return a.svc.CancelBySubjectInTx(ctx, tx, ac(c), subjectType, subjectID)
}

// ForSubject returns the approvals of a subject in step order.
func (a *Approvals) ForSubject(ctx context.Context, subjectType, subjectID string) ([]Approval, error) {
	list, err := a.svc.ForSubject(ctx, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	out := make([]Approval, 0, len(list))
	for _, x := range list {
		out = append(out, Approval{ID: x.ID, StepIndex: x.StepIndex, Status: x.Status, ApproverUserID: x.ApproverUserID,
			ApproverTeamID: x.ApproverTeamID, DecidedByUserID: x.DecidedByUserID, DecidedAt: x.DecidedAt, DecisionComment: x.DecisionComment})
	}
	return out, nil
}

// CanView reports whether the User is or was an approver of the subject.
func (a *Approvals) CanView(ctx context.Context, subjectType, subjectID, userID string) (bool, error) {
	return a.svc.CanView(ctx, subjectType, subjectID, userID)
}
