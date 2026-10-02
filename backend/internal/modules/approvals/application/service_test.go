package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type memStore struct {
	Store
	approvals map[string]Approval
	inserted  []NewApproval
}

func (m *memStore) InsertTx(_ context.Context, _ pgx.Tx, _ Caller, n NewApproval) (Approval, error) {
	m.inserted = append(m.inserted, n)
	return Approval{ID: "a1", Status: StatusPending}, nil
}
func (m *memStore) Get(_ context.Context, id string) (Approval, error) {
	a, ok := m.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	return a, nil
}
func (m *memStore) Decide(_ context.Context, _ Caller, id string, decide func(Approval) (Approval, []Event, error)) (Approval, error) {
	cur, ok := m.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	next, _, err := decide(cur)
	if err != nil {
		return Approval{}, err
	}
	next.Version = cur.Version + 1
	m.approvals[id] = next
	return next, nil
}

type dir struct {
	users map[string]bool
	teams map[string]bool
	mine  []string
}

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = d.users[i]
	}
	return out, nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = d.teams[i]
	}
	return out, nil
}
func (d dir) CurrentTeamIDs(context.Context, string) ([]string, error)   { return d.mine, nil }
func (d dir) CurrentMemberIDs(context.Context, string) ([]string, error) { return nil, nil }

const (
	approver  = "00000000-0000-7000-8000-0000000000a1"
	requester = "00000000-0000-7000-8000-0000000000b1"
	member    = "00000000-0000-7000-8000-0000000000c1"
	team      = "00000000-0000-7000-8000-0000000000e1"
	subject   = "00000000-0000-7000-8000-0000000000f1"
)

func caller(user string) Caller { return Caller{Actor: audit.UserActor(user), CorrelationID: "c"} }
func sp(s string) *string       { return &s }

func newSvc(a Approval, mine ...string) (*Service, *memStore) {
	st := &memStore{approvals: map[string]Approval{"a1": a}}
	d := dir{users: map[string]bool{approver: true, requester: true, member: true}, teams: map[string]bool{team: true}, mine: mine}
	return NewService(st, d, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }), st
}

func pending(mutate func(*Approval)) Approval {
	a := Approval{ID: "a1", SubjectType: "service_request", SubjectID: subject, SubjectLabel: "REQ-1", Status: StatusPending, ApproverUserID: sp(approver), Version: 1, ExcludedUserIDs: []string{requester}}
	if mutate != nil {
		mutate(&a)
	}
	return a
}

func TestDecideByTheApproverUser(t *testing.T) {
	s, st := newSvc(pending(nil))
	a, err := s.Decide(context.Background(), caller(approver), "a1", DecisionApprove, "  looks fine ", nil)
	if err != nil || a.Status != StatusApproved || a.DecidedByUserID == nil || *a.DecidedByUserID != approver || a.DecisionComment == nil || *a.DecisionComment != "looks fine" || a.DecidedAt == nil {
		t.Fatalf("decision = %+v %v", a, err)
	}
	if _, err := s.Decide(context.Background(), caller(approver), "a1", DecisionReject, "", nil); !errors.Is(err, ErrNotPending) {
		t.Errorf("decisions are immutable: %v", err)
	}
	if st.approvals["a1"].Status != StatusApproved {
		t.Error("the first decision must stand")
	}
}

func TestReject(t *testing.T) {
	s, _ := newSvc(pending(nil))
	if a, err := s.Decide(context.Background(), caller(approver), "a1", DecisionReject, "", nil); err != nil || a.Status != StatusRejected || a.DecisionComment != nil {
		t.Errorf("reject = %+v %v", a, err)
	}
}

func TestOnlyApproversMayDecideAndOthersDoNotSeeTheApproval(t *testing.T) {
	s, _ := newSvc(pending(nil))
	ctx := context.Background()
	if _, err := s.Decide(ctx, caller(member), "a1", DecisionApprove, "", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger = %v, want ErrNotFound (not visible)", err)
	}
	if _, err := s.Get(ctx, member, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get by a stranger: %v", err)
	}
	if _, err := s.Get(ctx, approver, "a1"); err != nil {
		t.Errorf("get by the approver: %v", err)
	}
	if _, err := s.Decide(ctx, Caller{Actor: audit.SystemActor("x"), CorrelationID: "c"}, "a1", DecisionApprove, "", nil); !errors.Is(err, ErrNotApprover) {
		t.Errorf("a system actor cannot decide: %v", err)
	}
}

func TestTeamApprovalAcceptsAnyMemberButNotExcludedOnes(t *testing.T) {
	a := pending(func(a *Approval) {
		a.ApproverUserID, a.ApproverTeamID = nil, sp(team)
		a.ExcludedUserIDs = []string{requester}
	})
	s, _ := newSvc(a, team)
	if got, err := s.Decide(context.Background(), caller(member), "a1", DecisionApprove, "", nil); err != nil || got.Status != StatusApproved {
		t.Errorf("team member: %+v %v", got, err)
	}
	// The requester is a member of the approver team but may not approve their own request.
	s2, _ := newSvc(a, team)
	if _, err := s2.Decide(context.Background(), caller(requester), "a1", DecisionApprove, "", nil); !errors.Is(err, ErrNotApprover) {
		t.Errorf("requester in the approver team: %v, want ErrNotApprover", err)
	}
	// Someone outside the team does not see it.
	s3, _ := newSvc(a)
	if _, err := s3.Decide(context.Background(), caller(member), "a1", DecisionApprove, "", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("outside the team: %v", err)
	}
}

func TestDecisionValidation(t *testing.T) {
	s, st := newSvc(pending(nil))
	ctx := context.Background()
	var inv *InvalidInputError
	for name, c := range map[string]struct{ decision, comment string }{
		"unknown decision": {"maybe", ""}, "long comment": {DecisionApprove, strings.Repeat("x", 1001)},
		"override char": {DecisionApprove, "ok‮"}, "control char": {DecisionApprove, "a\x00b"},
	} {
		if _, err := s.Decide(ctx, caller(approver), "a1", c.decision, c.comment, nil); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	stale := 7
	if _, err := s.Decide(ctx, caller(approver), "a1", DecisionApprove, "", &stale); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if st.approvals["a1"].Status != StatusPending {
		t.Error("rejected input must not change the approval")
	}
	if _, err := s.Decide(ctx, Caller{}, "a1", DecisionApprove, "", nil); err == nil {
		t.Error("an invalid caller must be rejected")
	}
	if _, err := s.Decide(ctx, caller(approver), "a1", DecisionApprove, "multi\nline ok", nil); err != nil {
		t.Errorf("multi-line comments are allowed: %v", err)
	}
}

func TestCancelledApprovalCannotBeDecided(t *testing.T) {
	s, _ := newSvc(pending(func(a *Approval) { a.Status = StatusCancelled }))
	if _, err := s.Decide(context.Background(), caller(approver), "a1", DecisionApprove, "", nil); !errors.Is(err, ErrNotPending) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestGetShowsTheDecider(t *testing.T) {
	a := pending(func(a *Approval) {
		a.ApproverUserID, a.ApproverTeamID = nil, sp(team)
		a.Status, a.DecidedByUserID = StatusApproved, sp(member)
	})
	s, _ := newSvc(a) // the decider is no longer in the team
	if _, err := s.Get(context.Background(), member, "a1"); err != nil {
		t.Errorf("the decider keeps access: %v", err)
	}
	if _, err := s.Get(context.Background(), requester, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

func TestInboxStatusValidation(t *testing.T) {
	s, _ := newSvc(pending(nil))
	var inv *InvalidInputError
	if _, err := s.Inbox(context.Background(), approver, "archived", Page{}); !errors.As(err, &inv) {
		t.Errorf("unknown status: %v", err)
	}
}

func TestRequestInTxValidates(t *testing.T) {
	s, st := newSvc(pending(nil))
	ctx := context.Background()
	other := "00000000-0000-7000-8000-0000000000ee"
	good := RequestInput{SubjectType: "service_request", SubjectID: subject, SubjectLabel: " REQ-2026-000001 · Laptop ", ApproverUserID: sp(approver), ExcludedUserIDs: []string{requester}}
	if _, err := s.RequestInTx(ctx, nil, caller(requester), good); err != nil {
		t.Fatal(err)
	}
	if st.inserted[0].SubjectLabel != "REQ-2026-000001 · Laptop" {
		t.Errorf("label = %q", st.inserted[0].SubjectLabel)
	}
	bad := map[string]func(*RequestInput){
		"both approvers":    func(r *RequestInput) { r.ApproverTeamID = sp(team) },
		"no approver":       func(r *RequestInput) { r.ApproverUserID = nil },
		"inactive user":     func(r *RequestInput) { r.ApproverUserID = &other },
		"approver excluded": func(r *RequestInput) { r.ApproverUserID = sp(requester) },
		"bad subject type":  func(r *RequestInput) { r.SubjectType = "Service Request" },
		"bad subject id":    func(r *RequestInput) { r.SubjectID = "nope" },
		"blank label":       func(r *RequestInput) { r.SubjectLabel = " " },
		"override label":    func(r *RequestInput) { r.SubjectLabel = "x‮" },
		"negative step":     func(r *RequestInput) { r.StepIndex = -1 },
		"inactive team": func(r *RequestInput) {
			r.ApproverUserID, r.ApproverTeamID = nil, &other
		},
	}
	for name, mutate := range bad {
		in := good
		mutate(&in)
		if _, err := s.RequestInTx(ctx, nil, caller(requester), in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(st.inserted) != 1 {
		t.Errorf("%d approvals inserted, rejected requests must not reach the store", len(st.inserted))
	}
}
