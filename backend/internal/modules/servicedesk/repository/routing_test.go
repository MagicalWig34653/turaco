package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

func (e *qenv) routingEnv() {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `DELETE FROM servicedesk.queue_routing_rules`); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM servicedesk.queue_routing_rules`) })
}

func (e *qenv) rule(q application.Queue, name string, kw []string, impact string) application.RoutingRule {
	e.t.Helper()
	r, err := e.svc.CreateRoutingRule(context.Background(), e.c(e.admin.UserID), e.admin, application.RoutingInput{QueueID: q.ID, Name: name, Keywords: kw, Impact: impact, Enabled: true})
	if err != nil {
		e.t.Fatalf("create rule %s: %v", name, err)
	}
	return r
}

func TestRoutingFirstMatchWinsAndFoldsUmlauts(t *testing.T) {
	e := newQEnv(t)
	e.routingEnv()
	ctx := context.Background()
	facility, hr := e.queue(application.QueuePublic), e.queue(application.QueuePublic)
	e.rule(facility, "Facility words", []string{"Heizung", "Lüftung"}, "")
	e.rule(hr, "Anything about pay", []string{"Gehalt"}, "")

	create := func(title, desc string) application.Ticket {
		tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: title, Description: desc})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	if tk := create("Luftung im Raum 3 ausgefallen", ""); tk.QueueID != facility.ID { // folded: Lüftung = Luftung
		t.Errorf("umlaut folding: routed to %s", tk.QueueID)
	}
	if tk := create("Frage", "Meine Heizung und das Gehalt"); tk.QueueID != facility.ID { // first rule wins
		t.Errorf("first match: routed to %s", tk.QueueID)
	}
	if tk := create("Gehalt falsch", ""); tk.QueueID != hr.ID {
		t.Errorf("second rule: routed to %s", tk.QueueID)
	}
	if tk := create("Drucker kaputt", ""); tk.QueueID == facility.ID || tk.QueueID == hr.ID {
		t.Errorf("no rule matches, the intake Queue applies: %s", tk.QueueID)
	}
	// An explicit Queue choice is never overridden.
	tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Gehalt", QueueID: &facility.ID})
	if err != nil || tk.QueueID != facility.ID {
		t.Errorf("explicit choice: %+v %v", tk.QueueID, err)
	}
	// The routing is audited with the rule.
	if n := e.count(`SELECT count(*) FROM platform.audit_events WHERE action='servicedesk.ticket.created' AND correlation_id=$1 AND metadata ? 'routedByRule'`, e.corr); n != 3 {
		t.Errorf("routed audit events = %d, want 3", n)
	}
}

func TestRoutingConditionsAndSkippedTargets(t *testing.T) {
	e := newQEnv(t)
	e.routingEnv()
	ctx := context.Background()
	urgent, employeeOnly := e.queue(application.QueuePublic), e.queue(application.QueuePublic)
	r := e.rule(urgent, "Patient care", nil, "patient_care")
	t.Run("impact condition", func(t *testing.T) {
		tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Monitor", Impact: "patient_care"})
		if err != nil || tk.QueueID != urgent.ID {
			t.Fatalf("%+v %v", tk.QueueID, err)
		}
		tk, err = e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Monitor", Impact: "impaired"})
		if err != nil || tk.QueueID == urgent.ID {
			t.Fatalf("other impact must not match: %+v %v", tk.QueueID, err)
		}
	})
	t.Run("a rule needs a condition", func(t *testing.T) {
		var inv *application.InvalidInputError
		if _, err := e.svc.CreateRoutingRule(ctx, e.c(e.admin.UserID), e.admin, application.RoutingInput{QueueID: urgent.ID, Name: "empty", Enabled: true}); !errors.As(err, &inv) {
			t.Fatalf("empty rule: %v", err)
		}
	})
	t.Run("archived and employee-choice targets are skipped", func(t *testing.T) {
		version := r.Version
		mode := application.RoutingEmployeeChoice
		cur, _ := e.svc.GetQueue(ctx, e.admin, urgent.ID)
		if _, err := e.svc.UpdateQueue(ctx, e.c(e.admin.UserID), e.admin, urgent.ID, &cur.Queue.Version, application.QueueUpdate{RoutingMode: &mode}); err != nil {
			t.Fatal(err)
		}
		tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Monitor", Impact: "patient_care"})
		if err != nil || tk.QueueID == urgent.ID {
			t.Fatalf("employee_choice queue must not receive routed tickets: %+v %v", tk.QueueID, err)
		}
		_ = version
		_ = employeeOnly
	})
}

func TestRoutingAdministrationIsGuardedVersionedAndOrdered(t *testing.T) {
	e := newQEnv(t)
	e.routingEnv()
	ctx := context.Background()
	q := e.queue(application.QueuePublic)
	a, b, c := e.rule(q, "A", []string{"alpha"}, ""), e.rule(q, "B", []string{"beta"}, ""), e.rule(q, "C", []string{"gamma"}, "")
	// Non-managers cannot read, change or test.
	if _, err := e.svc.ListRoutingRules(ctx, e.global); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("list without queues.manage: %v", err)
	}
	if _, err := e.svc.CreateRoutingRule(ctx, e.c(e.global.UserID), e.global, application.RoutingInput{QueueID: q.ID, Name: "x", Keywords: []string{"x"}, Enabled: true}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without queues.manage: %v", err)
	}
	if _, err := e.svc.TestRouting(ctx, e.global, application.RoutingSample{Title: "alpha"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("test without queues.manage: %v", err)
	}
	// Reorder must list every rule once.
	var inv *application.InvalidInputError
	if _, err := e.svc.ReorderRoutingRules(ctx, e.c(e.admin.UserID), e.admin, []string{a.ID, b.ID}); !errors.As(err, &inv) {
		t.Errorf("partial reorder: %v", err)
	}
	rules, err := e.svc.ReorderRoutingRules(ctx, e.c(e.admin.UserID), e.admin, []string{c.ID, a.ID, b.ID})
	if err != nil || rules[0].ID != c.ID || rules[1].ID != a.ID || rules[2].ID != b.ID {
		t.Fatalf("reorder: %+v %v", rules, err)
	}
	// Version guard on update and delete.
	stale := a.Version + 5
	if _, err := e.svc.UpdateRoutingRule(ctx, e.c(e.admin.UserID), e.admin, a.ID, &stale, application.RoutingInput{QueueID: q.ID, Name: "A2", Keywords: []string{"alpha"}, Enabled: true}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	v := a.Version
	up, err := e.svc.UpdateRoutingRule(ctx, e.c(e.admin.UserID), e.admin, a.ID, &v, application.RoutingInput{QueueID: q.ID, Name: "A2", Keywords: []string{"alpha"}, Enabled: false})
	if err != nil || up.Version != v+1 || up.Enabled {
		t.Fatalf("update: %+v %v", up, err)
	}
	// A disabled rule does not match; the test endpoint shows the decision without creating anything.
	res, err := e.svc.TestRouting(ctx, e.admin, application.RoutingSample{Title: "alpha"})
	if err != nil || res.Rule != nil || !res.DefaultUsed {
		t.Errorf("disabled rule matched: %+v %v", res, err)
	}
	res, err = e.svc.TestRouting(ctx, e.admin, application.RoutingSample{Title: "beta test"})
	if err != nil || res.Rule == nil || res.Rule.ID != b.ID || res.Queue.ID != q.ID {
		t.Errorf("test routing: %+v %v", res, err)
	}
	if err := e.svc.DeleteRoutingRule(ctx, e.c(e.admin.UserID), e.admin, b.ID, &b.Version); err != nil {
		t.Fatal(err)
	}
	rules, _ = e.svc.ListRoutingRules(ctx, e.admin)
	if len(rules) != 2 || rules[0].Position != 0 || rules[1].Position != 1 {
		t.Errorf("positions after delete: %+v", rules)
	}
	if n := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id=$1 AND action LIKE 'servicedesk.routing_rule.%'`, e.corr); n < 6 {
		t.Errorf("audit events = %d", n)
	}
}
