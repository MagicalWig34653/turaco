package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct {
	users map[string]bool
	teams map[string]bool
	mine  map[string][]string // user -> team ids
	mem   map[string][]string // team -> member ids
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
func (d dir) CurrentTeamIDs(_ context.Context, u string) ([]string, error)   { return d.mine[u], nil }
func (d dir) CurrentMemberIDs(_ context.Context, t string) ([]string, error) { return d.mem[t], nil }

type env struct {
	t                                               *testing.T
	pool                                            *pgxpool.Pool
	repo                                            *repository.Repository
	svc                                             *application.Service
	corr                                            string
	approver, requester, memberA, memberB, outsider string
	team, subject                                   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, repo: repository.New(pool), corr: "approvals-" + hex.EncodeToString(b)}
	for _, dst := range []*string{&e.approver, &e.requester, &e.memberA, &e.memberB, &e.outsider, &e.team, &e.subject} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	d := dir{
		users: map[string]bool{e.approver: true, e.requester: true, e.memberA: true, e.memberB: true, e.outsider: true},
		teams: map[string]bool{e.team: true},
		mine:  map[string][]string{e.memberA: {e.team}, e.memberB: {e.team}, e.requester: {e.team}},
		mem:   map[string][]string{e.team: {e.memberA, e.memberB, e.requester}},
	}
	e.svc = application.NewService(e.repo, d, nil)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM approvals.approvals WHERE subject_id = $1::uuid`, e.subject)
	})
	return e
}

func (e *env) caller(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) request(step int, in func(*application.RequestInput)) application.Approval {
	e.t.Helper()
	req := application.RequestInput{
		SubjectType: "service_request", SubjectID: e.subject, SubjectLabel: "REQ-1 · Laptop", StepIndex: step,
		ApproverUserID: &e.approver, ExcludedUserIDs: []string{e.requester}, RequestedBy: &e.requester,
	}
	if in != nil {
		in(&req)
	}
	var a application.Approval
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		var err error
		a, err = e.svc.RequestInTx(context.Background(), tx, e.caller(e.requester), req)
		return err
	})
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	return a
}

func TestRequestDecideAndAudit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.request(0, nil)
	if a.Status != "pending" || len(a.ExcludedUserIDs) != 1 || a.ExcludedUserIDs[0] != e.requester {
		t.Fatalf("approval = %+v", a)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ApprovalRequested'`, e.corr) != 1 ||
		e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'approvals.approval.requested'`, e.corr) != 1 {
		t.Error("requesting must write the audit event and ApprovalRequested")
	}
	got, err := e.svc.Decide(ctx, e.caller(e.approver), a.ID, "approve", "ok", nil)
	if err != nil || got.Status != "approved" || got.Version != 2 {
		t.Fatalf("decide = %+v %v", got, err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ApprovalDecided' AND payload->>'decision' = 'approve'`, e.corr) != 1 ||
		e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'approvals.approval.approved'`, e.corr) != 1 {
		t.Error("deciding must write the audit event and ApprovalDecided")
	}
	if _, err := e.svc.Decide(ctx, e.caller(e.approver), a.ID, "reject", "", nil); !errors.Is(err, application.ErrNotPending) {
		t.Errorf("second decision: %v", err)
	}
	if _, err := e.svc.Decide(ctx, e.caller(e.outsider), a.ID, "approve", "", nil); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a stranger: %v", err)
	}
}

func TestStepsAreUniquePerSubjectAndInOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.request(0, nil)
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.RequestInTx(ctx, tx, e.caller(e.requester), application.RequestInput{
			SubjectType: "service_request", SubjectID: e.subject, SubjectLabel: "x", StepIndex: 0, ApproverUserID: &e.approver,
		})
		return err
	})
	if !errors.Is(err, application.ErrConflict) {
		t.Errorf("a repeated step: %v", err)
	}
	e.request(1, func(r *application.RequestInput) { r.ApproverUserID, r.ApproverTeamID = nil, &e.team })
	list, err := e.svc.ForSubject(ctx, "service_request", e.subject)
	if err != nil || len(list) != 2 || list[0].StepIndex != 0 || list[1].StepIndex != 1 || list[1].ApproverTeamID == nil {
		t.Errorf("subject approvals = %+v %v", list, err)
	}
}

func TestInboxFollowsAssignmentAndExclusion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	direct := e.request(0, nil)
	viaTeam := e.request(1, func(r *application.RequestInput) { r.ApproverUserID, r.ApproverTeamID = nil, &e.team })

	ids := func(user, status string) map[string]bool {
		res, err := e.svc.Inbox(ctx, user, status, application.Page{Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, a := range res.Items {
			out[a.ID] = true
		}
		return out
	}
	if got := ids(e.approver, ""); !got[direct.ID] || got[viaTeam.ID] {
		t.Errorf("approver inbox = %v", got)
	}
	if got := ids(e.memberA, "pending"); !got[viaTeam.ID] || got[direct.ID] {
		t.Errorf("team member inbox = %v", got)
	}
	if got := ids(e.requester, "pending"); got[viaTeam.ID] || got[direct.ID] {
		t.Errorf("the requester (excluded) must not see approvals of their own request: %v", got)
	}
	if got := ids(e.outsider, "pending"); len(got) != 0 && (got[direct.ID] || got[viaTeam.ID]) {
		t.Errorf("outsider inbox = %v", got)
	}
	if _, err := e.svc.Decide(ctx, e.caller(e.memberA), viaTeam.ID, "approve", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := ids(e.memberB, "pending"); got[viaTeam.ID] {
		t.Error("a decided approval leaves every member's pending inbox")
	}
	if got := ids(e.memberA, "decided"); !got[viaTeam.ID] {
		t.Error("the decider sees it under decided")
	}
	if got := ids(e.memberB, "decided"); got[viaTeam.ID] {
		t.Error("another member did not decide it")
	}
	if _, err := e.svc.Inbox(ctx, e.approver, "pending", application.Page{Cursor: "%%%"}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor: %v", err)
	}
}

func TestConcurrentDecisionsOnATeamApprovalHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	a := e.request(0, func(r *application.RequestInput) { r.ApproverUserID, r.ApproverTeamID = nil, &e.team })
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i, user := range []string{e.memberA, e.memberB, e.memberA, e.memberB, e.memberA, e.memberB} {
		wg.Add(1)
		decision := "approve"
		if i%2 == 1 {
			decision = "reject"
		}
		go func() {
			defer wg.Done()
			_, err := e.svc.Decide(context.Background(), e.caller(user), a.ID, decision, "", nil)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won, late := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, application.ErrNotPending):
			late++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if won != 1 || late != 5 {
		t.Errorf("won=%d late=%d, want 1 and 5", won, late)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'ApprovalDecided'`, e.corr) != 1 {
		t.Error("exactly one ApprovalDecided event is expected")
	}
}

func TestCancelBySubjectAndIsApprover(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first := e.request(0, nil)
	e.request(1, func(r *application.RequestInput) { r.ApproverUserID, r.ApproverTeamID = nil, &e.team })
	if _, err := e.svc.Decide(ctx, e.caller(e.approver), first.ID, "approve", "", nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		n, err = e.svc.CancelBySubjectInTx(ctx, tx, e.caller(e.requester), "service_request", e.subject)
		return err
	}); err != nil || n != 1 {
		t.Fatalf("cancelled %d %v, want only the pending step", n, err)
	}
	list, _ := e.svc.ForSubject(ctx, "service_request", e.subject)
	if list[0].Status != "approved" || list[1].Status != "cancelled" {
		t.Errorf("statuses = %s, %s", list[0].Status, list[1].Status)
	}
	if _, err := e.svc.Decide(ctx, e.caller(e.memberA), list[1].ID, "approve", "", nil); !errors.Is(err, application.ErrNotPending) {
		t.Errorf("deciding a cancelled approval: %v", err)
	}
	for user, want := range map[string]bool{e.approver: true, e.memberA: true, e.outsider: false} {
		got, err := e.svc.CanView(ctx, "service_request", e.subject, user)
		if err != nil || got != want {
			t.Errorf("CanView(%s) = %v %v, want %v", user, got, err, want)
		}
	}
}

func TestDatabaseInvariants(t *testing.T) {
	e := newEnv(t)
	a := e.request(0, nil)
	for name, sql := range map[string]string{
		"both approvers":           `UPDATE approvals.approvals SET approver_team_id = uuidv7() WHERE id = $1`,
		"approved without decider": `UPDATE approvals.approvals SET status = 'approved' WHERE id = $1`,
		"decided while pending":    `UPDATE approvals.approvals SET decided_at = now(), decided_by_user_id = uuidv7() WHERE id = $1`,
		"unknown status":           `UPDATE approvals.approvals SET status = 'expired' WHERE id = $1`,
		"blank label":              `UPDATE approvals.approvals SET subject_label = ' ' WHERE id = $1`,
		"negative step":            `UPDATE approvals.approvals SET step_index = -1 WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(context.Background(), sql, a.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	_ = time.Now
}
