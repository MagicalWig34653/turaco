package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

func TestProblemLifecycleKnownErrorAndTickets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	svc := application.NewProblemService(repository.New(e.pool), dir{active: map[string]bool{e.agent: true, e.bob: true}})
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[])`, []string{e.alice, e.bob})
		_, _ = e.pool.Exec(ctx, `DELETE FROM servicedesk.problems WHERE created_by = $1::uuid`, e.agent)
	})
	manage := application.ProblemPrincipal{UserID: e.agent, Staff: true, Manage: true}
	staff := application.ProblemPrincipal{UserID: e.viewer, Staff: true}
	employee := application.ProblemPrincipal{UserID: e.alice}
	var tr *application.InvalidTransitionError
	var inv *application.InvalidInputError

	if _, err := svc.CreateProblem(ctx, e.c(e.viewer), staff, "Docking drops", ""); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without problems.manage: %v", err)
	}
	p, err := svc.CreateProblem(ctx, e.c(e.agent), manage, "Docking station drops the network", "Reported by many")
	if err != nil || p.Status != "new" || p.Reference == "" || p.OwnerID == nil {
		t.Fatalf("create = %+v %v", p, err)
	}
	op := func(name, text string) (application.Problem, error) {
		return svc.Transition(ctx, e.c(e.agent), manage, p.ID, nil, name, application.ProblemParams{Text: text})
	}
	if _, err := op("mark_known_error", "Reboot the dock"); !errors.As(err, &tr) {
		t.Errorf("known error before the cause is identified: %v", err)
	}
	if _, err := op("identify_cause", ""); !errors.As(err, &inv) {
		t.Errorf("cause is required: %v", err)
	}
	if _, err := op("investigate", ""); err != nil {
		t.Fatal(err)
	}
	if p, err = op("identify_cause", "Firmware 2.1 loses the USB link"); err != nil || p.Status != "cause_identified" || p.Cause == nil {
		t.Fatalf("identify_cause = %+v %v", p, err)
	}
	if _, err := op("mark_known_error", " "); !errors.As(err, &inv) {
		t.Errorf("workaround is required: %v", err)
	}
	if p, err = op("mark_known_error", "Unplug and replug the dock"); err != nil || p.Status != "known_error" {
		t.Fatalf("mark_known_error = %+v %v", p, err)
	}

	// Tickets linked to a known error show its workaround to staff only.
	tk := e.raise()
	if err := svc.LinkTicket(ctx, e.c(e.viewer), staff, p.ID, tk.ID, true); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("link without problems.manage: %v", err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), manage, p.ID, tk.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), manage, p.ID, tk.ID, true); err != nil {
		t.Errorf("linking twice is idempotent: %v", err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), manage, p.ID, "00000000-0000-7000-8000-000000000009", true); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown ticket: %v", err)
	}
	known, err := svc.KnownErrorsOfTicket(ctx, staff, tk.ID)
	if err != nil || len(known) != 1 || known[0].Workaround == nil {
		t.Fatalf("known errors = %+v %v", known, err)
	}
	if _, err := svc.KnownErrorsOfTicket(ctx, employee, tk.ID); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("an employee reads known errors: %v", err)
	}
	if _, err := svc.Get(ctx, employee, p.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("an employee reads a problem: %v", err)
	}
	d, err := svc.Get(ctx, staff, p.ID)
	if err != nil || len(d.Tickets) != 1 || len(d.Operations) != 0 || d.Problem.Tickets != 1 {
		t.Errorf("staff view = %+v %v", d, err)
	}

	if p, err = op("plan_resolution", ""); err != nil || p.Status != "resolution_planned" {
		t.Fatalf("plan = %+v %v", p, err)
	}
	if _, err := svc.SetOwner(ctx, e.c(e.agent), manage, p.ID, nil, "00000000-0000-7000-8000-000000000009"); !errors.Is(err, application.ErrUserInvalid) {
		t.Errorf("owner must be an active user: %v", err)
	}
	if p, err = svc.SetOwner(ctx, e.c(e.agent), manage, p.ID, nil, e.bob); err != nil || *p.OwnerID != e.bob {
		t.Errorf("set owner = %+v %v", p, err)
	}
	if _, err := op("close", ""); !errors.As(err, &tr) {
		t.Errorf("close before resolve: %v", err)
	}
	if p, err = op("resolve", "Firmware 2.3 rolled out"); err != nil || p.ResolvedAt == nil {
		t.Fatalf("resolve = %+v %v", p, err)
	}
	if known, _ := svc.KnownErrorsOfTicket(ctx, staff, tk.ID); len(known) != 0 {
		t.Error("a resolved problem is no longer a known error")
	}
	if p, err = op("close", ""); err != nil || p.ClosedAt == nil {
		t.Fatalf("close = %+v %v", p, err)
	}
	if err := svc.LinkTicket(ctx, e.c(e.agent), manage, p.ID, tk.ID, true); !errors.As(err, &tr) {
		t.Errorf("link to a closed problem: %v", err)
	}
	for name, sql := range map[string]string{
		"known error without workaround": `UPDATE servicedesk.problems SET status = 'known_error', workaround = NULL WHERE id = $1::uuid`,
		"resolved without timestamp":     `UPDATE servicedesk.problems SET status = 'resolved', resolved_at = NULL WHERE id = $1::uuid`,
	} {
		if _, err := e.pool.Exec(ctx, sql, p.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND metadata::text LIKE '%Unplug and replug%'`, e.corr) != 0 {
		t.Error("workaround text must not be copied into audit")
	}
}
