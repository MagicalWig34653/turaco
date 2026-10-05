package application

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Audit actions: approvals.approval.requested, .approved, .rejected,
// .cancelled. Comments are written to the audit log (they are free text of
// up to 1000 characters and audit records cannot be redacted).

var subjectType = regexp.MustCompile(`^[a-z][a-z_]{1,39}$`)

// Service performs approval operations.
type Service struct {
	store Store
	dir   Directory
	now   func() time.Time
}

func NewService(store Store, dir Directory, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, dir: dir, now: now}
}

// RequestInput describes a pending approval for a subject.
type RequestInput struct {
	SubjectType  string
	SubjectID    string
	SubjectLabel string
	StepIndex    int
	// Exactly one of ApproverUserID and ApproverTeamID.
	ApproverUserID  *string
	ApproverTeamID  *string
	ExcludedUserIDs []string
	RequestedBy     *string
}

// RequestInTx creates a pending approval in the caller's transaction (the
// caller commits). The approver must be an active User or Team; Users in
// ExcludedUserIDs (the requester, the requested-for User) can never decide
// it, and a single approver User who is excluded is refused outright because
// nobody could decide.
func (s *Service) RequestInTx(ctx context.Context, tx pgx.Tx, c Caller, in RequestInput) (Approval, error) {
	if err := c.validate(); err != nil {
		return Approval{}, err
	}
	if !subjectType.MatchString(in.SubjectType) || len(in.SubjectID) != 36 {
		return Approval{}, invalid("a subject needs a type and an id")
	}
	if in.StepIndex < 0 {
		return Approval{}, invalid("step index must not be negative")
	}
	if (in.ApproverUserID == nil) == (in.ApproverTeamID == nil) {
		return Approval{}, invalid("exactly one approver (user or team) is required")
	}
	label, err := cleanLabel(in.SubjectLabel)
	if err != nil {
		return Approval{}, err
	}
	if in.ApproverUserID != nil {
		active, err := s.dir.ActiveUsers(ctx, []string{*in.ApproverUserID})
		if err != nil {
			return Approval{}, fmt.Errorf("check approver: %w", err)
		}
		if !active[*in.ApproverUserID] {
			return Approval{}, ErrApproverInvalid
		}
		for _, ex := range in.ExcludedUserIDs {
			if ex == *in.ApproverUserID {
				return Approval{}, invalid("the approver may not be the requester")
			}
		}
	} else {
		active, err := s.dir.ActiveTeams(ctx, []string{*in.ApproverTeamID})
		if err != nil {
			return Approval{}, fmt.Errorf("check approver: %w", err)
		}
		if !active[*in.ApproverTeamID] {
			return Approval{}, ErrApproverInvalid
		}
		// A Team nobody in could decide (empty, or only excluded Users) would strand the subject.
		members, err := s.dir.CurrentMemberIDs(ctx, *in.ApproverTeamID)
		if err != nil {
			return Approval{}, fmt.Errorf("check approver team members: %w", err)
		}
		candidates := slices.DeleteFunc(slices.Clone(members), func(id string) bool { return slices.Contains(in.ExcludedUserIDs, id) })
		activeMembers, err := s.dir.ActiveUsers(ctx, candidates)
		if err != nil {
			return Approval{}, fmt.Errorf("check approver team members: %w", err)
		}
		if !slices.ContainsFunc(candidates, func(id string) bool { return activeMembers[id] }) {
			return Approval{}, ErrApproverInvalid
		}
	}
	return s.store.InsertTx(ctx, tx, c, NewApproval{
		SubjectType: in.SubjectType, SubjectID: in.SubjectID, SubjectLabel: label, StepIndex: in.StepIndex,
		ApproverUserID: in.ApproverUserID, ApproverTeamID: in.ApproverTeamID,
		ExcludedUserIDs: in.ExcludedUserIDs, RequestedBy: in.RequestedBy,
	})
}

// CancelBySubjectInTx cancels the pending approvals of a subject.
func (s *Service) CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, c Caller, subjectType, subjectID string) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	return s.store.CancelBySubjectTx(ctx, tx, c, subjectType, subjectID)
}

func (s *Service) teams(ctx context.Context, userID string) (map[string]struct{}, error) {
	ids, err := s.dir.CurrentTeamIDs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load teams of caller: %w", err)
	}
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m, nil
}

func isApprover(a Approval, userID string, teams map[string]struct{}) bool {
	if a.ApproverUserID != nil && *a.ApproverUserID == userID {
		return true
	}
	if a.ApproverTeamID != nil {
		_, ok := teams[*a.ApproverTeamID]
		return ok
	}
	return false
}

func excluded(a Approval, userID string) bool {
	for _, ex := range a.ExcludedUserIDs {
		if ex == userID {
			return true
		}
	}
	return false
}

// Get returns an approval the caller may see: its approver, or whoever
// decided it. Everybody else gets ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string) (Approval, error) {
	a, err := s.store.Get(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	teams, err := s.teams(ctx, userID)
	if err != nil {
		return Approval{}, err
	}
	if !isApprover(a, userID, teams) && (a.DecidedByUserID == nil || *a.DecidedByUserID != userID) {
		return Approval{}, ErrNotFound
	}
	return a, nil
}

// Inbox lists the caller's pending approvals (those they may decide) or the
// approvals they decided, newest first.
func (s *Service) Inbox(ctx context.Context, userID, status string, page Page) (Result, error) {
	if status == "" {
		status = StatusPending
	}
	if status != StatusPending && status != "decided" {
		return Result{}, invalid("status must be pending or decided")
	}
	teams, err := s.teams(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	ids := make([]string, 0, len(teams))
	for id := range teams {
		ids = append(ids, id)
	}
	return s.store.Inbox(ctx, InboxQuery{UserID: userID, TeamIDs: ids, Status: status, Page: page.Normalize()})
}

// PendingForUserCount counts decisions currently available to this User, including
// current Team assignments and excluding conflicted requesters.
func (s *Service) PendingForUserCount(ctx context.Context, userID string) (int, error) {
	teams, err := s.dir.CurrentTeamIDs(ctx, userID)
	if err != nil {
		return 0, err
	}
	counter, ok := s.store.(interface {
		CountPending(ctx context.Context, userID string, teamIDs []string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("approvals: count is unavailable")
	}
	return counter.CountPending(ctx, userID, teams)
}

// ForSubject returns the approvals of a subject (all steps, in order).
func (s *Service) ForSubject(ctx context.Context, subjectType, subjectID string) ([]Approval, error) {
	return s.store.ForSubject(ctx, subjectType, subjectID)
}

// CanView reports whether the User is or was an approver of the subject, which
// lets the subject's module show the request to them.
func (s *Service) CanView(ctx context.Context, subjectType, subjectID, userID string) (bool, error) {
	teams, err := s.teams(ctx, userID)
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(teams))
	for id := range teams {
		ids = append(ids, id)
	}
	return s.store.IsApproverFor(ctx, subjectType, subjectID, userID, ids)
}

// Decide approves or rejects a pending approval. Only an approver (the User,
// or a member of the approver Team) who is not excluded may decide; the first
// decision wins and is immutable. comment is optional.
func (s *Service) Decide(ctx context.Context, c Caller, id, decision, comment string, expected *int) (Approval, error) {
	if err := c.validate(); err != nil {
		return Approval{}, err
	}
	if decision != DecisionApprove && decision != DecisionReject {
		return Approval{}, invalid("decision must be approve or reject")
	}
	cmt, err := cleanComment(comment)
	if err != nil {
		return Approval{}, err
	}
	userID := c.Actor.UserID
	if userID == "" {
		return Approval{}, ErrNotApprover
	}
	teams, err := s.teams(ctx, userID)
	if err != nil {
		return Approval{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	return s.store.Decide(ctx, c, id, func(cur Approval) (Approval, []Event, error) {
		if !isApprover(cur, userID, teams) {
			return Approval{}, nil, ErrNotFound // not visible to others
		}
		if expected != nil && *expected != cur.Version {
			return Approval{}, nil, ErrVersionConflict
		}
		if cur.Status != StatusPending {
			return Approval{}, nil, ErrNotPending
		}
		if excluded(cur, userID) {
			return Approval{}, nil, ErrNotApprover
		}
		next := cur
		next.Status = StatusApproved
		if decision == DecisionReject {
			next.Status = StatusRejected
		}
		next.DecidedByUserID, next.DecidedAt, next.DecisionComment = &userID, &now, cmt
		return next, []Event{{Type: "ApprovalDecided", Payload: map[string]any{
			"approvalId": cur.ID, "subjectType": cur.SubjectType, "subjectId": cur.SubjectID,
			"stepIndex": cur.StepIndex, "decision": decision,
		}}}, nil
	})
}
