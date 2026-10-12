package repository_test

import (
	"context"
	"testing"
)

// TimeAccess is the Service Desk contract of Time Tracking (F17 T-A): view and work access per Ticket from global
// permissions and Queue grants, the Queue of the Ticket, and a reference only for people who may view it.
func TestTimeAccessFollowsQueueGrants(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue("internal")
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.global, q)

	got, err := e.svc.TimeAccess(ctx, e.u1, []string{tk.ID, "not-a-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if a := got[tk.ID]; !a.Found || !a.CanView || !a.CanWork || a.QueueID != q.ID || a.Reference != tk.Reference {
		t.Errorf("work grant: %+v", a)
	}
	if a := got["not-a-uuid"]; a.Found || a.CanView || a.CanWork {
		t.Errorf("unknown id: %+v", a)
	}
	got, _ = e.svc.TimeAccess(ctx, e.u2, []string{tk.ID})
	if a := got[tk.ID]; !a.Found || !a.CanView || a.CanWork || a.Reference == "" {
		t.Errorf("view grant: %+v", a)
	}
	got, _ = e.svc.TimeAccess(ctx, e.alice, []string{tk.ID})
	if a := got[tk.ID]; !a.Found || a.CanView || a.CanWork || a.Reference != "" {
		t.Errorf("no grant (internal Queue): %+v", a)
	}
	e.mem.mu.Lock()
	e.mem.perms[e.bob] = map[string]struct{}{"tickets.manage": {}}
	e.mem.mu.Unlock()
	got, _ = e.svc.TimeAccess(ctx, e.bob, []string{tk.ID})
	if a := got[tk.ID]; !a.CanView || !a.CanWork {
		t.Errorf("tickets.manage: %+v", a)
	}
}
