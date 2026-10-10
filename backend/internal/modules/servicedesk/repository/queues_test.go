package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// fakeMem is the membership and permission source of the tests.
type fakeMem struct {
	mu    sync.Mutex
	teams map[string][]string
	roles map[string][]string
	perms map[string]map[string]struct{}
}

func (m *fakeMem) TeamIDs(_ context.Context, u string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.teams[u], nil
}
func (m *fakeMem) RoleIDs(_ context.Context, u string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.roles[u], nil
}
func (m *fakeMem) Permissions(_ context.Context, u string) (map[string]struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.perms[u], nil
}

// qenv is an isolated environment for Queue tests: fresh users and Queues with random prefixes.
type qenv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	svc    *application.Service
	corr   string
	mem    *fakeMem
	admin  application.Principal // global tickets.manage + queues.manage
	global application.Principal // tickets.view + tickets.manage
	alice  string                // employee
	bob    string                // employee
	u1, u2 string                // people with queue grants only
	team   string
	role   string
	queues []string
}

func newQEnv(t *testing.T) *qenv {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &qenv{t: t, pool: pool, corr: "queues-" + hex.EncodeToString(b), mem: &fakeMem{teams: map[string][]string{}, roles: map[string][]string{}, perms: map[string]map[string]struct{}{}}}
	var agent string
	active := map[string]bool{}
	for _, dst := range []*string{&e.alice, &e.bob, &e.u1, &e.u2, &e.team, &e.role, &agent} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
		active[*dst] = true
	}
	e.svc = application.NewService(repository.New(pool), dir{active: active}, device{}).WithMemberships(e.mem)
	e.admin = application.Principal{UserID: agent, View: true, Manage: true, QueuesManage: true}
	e.global = application.Principal{UserID: agent, View: true, Manage: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE reporter_user_id = ANY($1::uuid[]) OR queue_id = ANY($2::uuid[])
			OR id IN (SELECT ticket_id FROM servicedesk.reference_registry WHERE queue_id = ANY($2::uuid[]))`, []string{e.alice, e.bob, e.u1, e.u2}, e.queues)
		_, _ = pool.Exec(ctx, `DELETE FROM servicedesk.queues WHERE id = ANY($1::uuid[])`, e.queues)
	})
	return e
}

func (e *qenv) c(user string) application.Caller {
	return application.Caller{Actor: audit.UserActor(user), CorrelationID: e.corr}
}

func (e *qenv) employee(id string) application.Principal { return application.Principal{UserID: id} }

func randLetters(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i := range b {
		out[i] = 'A' + b[i]%26
	}
	return string(out)
}

// queue creates an active Queue with a unique prefix.
func (e *qenv) queue(visibility string) application.Queue {
	e.t.Helper()
	pfx := "Q" + randLetters(5)
	q, err := e.svc.CreateQueue(context.Background(), e.c(e.admin.UserID), e.admin, application.QueueInput{
		Key: strings.ToLower(pfx), Prefix: pfx, Name: "Desk " + pfx, Visibility: visibility, RoutingMode: application.RoutingBoth, NumberPadding: 4})
	if err != nil {
		e.t.Fatalf("create queue: %v", err)
	}
	e.queues = append(e.queues, q.ID)
	return q
}

func (e *qenv) grant(q application.Queue, grants ...application.GrantInput) application.Queue {
	e.t.Helper()
	cur, err := e.svc.GetQueue(context.Background(), e.admin, q.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := e.svc.ReplaceGrants(context.Background(), e.c(e.admin.UserID), e.admin, q.ID, &cur.Queue.Version, grants)
	if err != nil {
		e.t.Fatalf("grants: %v", err)
	}
	return out.Queue
}

func user(id, level string) application.GrantInput {
	return application.GrantInput{SubjectType: "user", SubjectID: id, Level: level}
}

func (e *qenv) raise(p application.Principal, q application.Queue) application.Ticket {
	e.t.Helper()
	tk, err := e.svc.Create(context.Background(), e.c(p.UserID), p, application.CreateInput{Title: "Printer jams", QueueID: &q.ID})
	if err != nil {
		e.t.Fatalf("raise into %s: %v", q.Prefix, err)
	}
	return tk
}

func (e *qenv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestLegacyTicketsKeepTheirReferencesInTheDefaultQueue(t *testing.T) {
	e := newQEnv(t)
	// The migration seeded the default Queue with the legacy prefix and width.
	var key, prefix string
	var pad int
	if err := e.pool.QueryRow(context.Background(), `SELECT key, prefix, number_padding FROM servicedesk.queues WHERE default_for_intake`).Scan(&key, &prefix, &pad); err != nil {
		t.Fatal(err)
	}
	if key != "it" || prefix != "TKT" || pad != 6 {
		t.Fatalf("default queue = %s %s %d", key, prefix, pad)
	}
	// Every ticket of the default Queue still has its TKT-<number> reference, and every ticket has exactly one
	// current registry entry.
	if n := e.count(`SELECT count(*) FROM servicedesk.tickets t JOIN servicedesk.queues q ON q.id = t.queue_id AND q.key = 'it'
		WHERE t.reference <> 'TKT-' || CASE WHEN length(t.number::text) >= 6 THEN t.number::text ELSE lpad(t.number::text, 6, '0') END`); n != 0 {
		t.Errorf("%d tickets of the default queue changed their reference", n)
	}
	if n := e.count(`SELECT count(*) FROM servicedesk.tickets t WHERE (SELECT count(*) FROM servicedesk.reference_registry r
		WHERE r.ticket_id = t.id AND r.kind = 'current' AND r.reference = t.reference) <> 1`); n != 0 {
		t.Errorf("%d tickets without exactly one current registry entry", n)
	}
	// A new ticket without a chosen queue continues the legacy numbering.
	tk, err := e.svc.Create(context.Background(), e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Legacy path"})
	if err != nil || !strings.HasPrefix(tk.Reference, "TKT-") || len(tk.Reference) < len("TKT-000001") {
		t.Fatalf("default intake = %+v %v", tk, err)
	}
	if tk.Queue == nil || tk.Queue.Key != "it" {
		t.Errorf("queue = %+v", tk.Queue)
	}
}

func TestParallelCreationNeverCollidesAndNumbersAreNeverReused(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	a, b := e.queue(application.QueuePublic), e.queue(application.QueuePublic)
	const n = 40
	refs := make(chan string, 2*n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		for _, q := range []application.Queue{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "parallel", QueueID: &q.ID})
				if err != nil {
					t.Errorf("create: %v", err)
					return
				}
				refs <- tk.Reference
			}()
		}
	}
	wg.Wait()
	close(refs)
	seen := map[string]bool{}
	for r := range refs {
		if seen[r] {
			t.Fatalf("reference %s issued twice", r)
		}
		seen[r] = true
	}
	if len(seen) != 2*n {
		t.Fatalf("%d references, want %d", len(seen), 2*n)
	}
	// Each Queue issued exactly 1..n without a gap, in its own prefix and width.
	for _, q := range []application.Queue{a, b} {
		for i := 1; i <= n; i++ {
			if want := fmt.Sprintf("%s-%04d", q.Prefix, i); !seen[want] {
				t.Fatalf("missing %s", want)
			}
		}
	}
	// A rolled-back issue leaves no gap: the counter rolls back with the transaction ...
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var num int64
	var ref string
	if err := tx.QueryRow(ctx, `SELECT o_number, o_reference FROM servicedesk.issue_reference($1::uuid)`, a.ID).Scan(&num, &ref); err != nil || num != n+1 {
		t.Fatalf("issue = %d %s %v", num, ref, err)
	}
	_ = tx.Rollback(ctx)
	// ... but a committed number is never issued again, even when its ticket is gone or the queue was archived.
	last := e.raise(e.employee(e.alice), a)
	if last.Reference != fmt.Sprintf("%s-%04d", a.Prefix, n+1) {
		t.Fatalf("after rollback = %s", last.Reference)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE id = $1::uuid`, last.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE queue_id = $1::uuid`, a.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.svc.GetQueue(ctx, e.admin, a.ID)
	arch, err := e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, a.ID, &cur.Queue.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "x", QueueID: &a.ID}); err == nil {
		t.Error("created a ticket in an archived queue")
	}
	if _, err := e.svc.RestoreQueue(ctx, e.c(e.admin.UserID), e.admin, a.ID, &arch.Version); err != nil {
		t.Fatal(err)
	}
	if again := e.raise(e.employee(e.alice), a); again.Reference != fmt.Sprintf("%s-%04d", a.Prefix, n+2) {
		t.Errorf("number after delete, archive and restore = %s, want %s-%04d", again.Reference, a.Prefix, n+2)
	}
	// The database refuses to rewrite what was issued.
	if _, err := e.pool.Exec(ctx, `UPDATE servicedesk.queues SET prefix = 'ZZZ' WHERE id = $1::uuid`, a.ID); err == nil {
		t.Error("prefix changed")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE servicedesk.queues SET next_number = 1 WHERE id = $1::uuid`, a.ID); err == nil {
		t.Error("counter moved backwards")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE servicedesk.tickets SET reference = 'ZZZ-0001' WHERE queue_id = $1::uuid`, a.ID); err == nil {
		t.Error("reference changed without a move")
	}
}

func TestMoveIssuesANewNumberAndKeepsEveryOldOneAsAlias(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	a, b, c := e.queue(application.QueuePublic), e.queue(application.QueuePublic), e.queue(application.QueueInternal)
	tk := e.raise(e.employee(e.alice), a)
	first := tk.Reference
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &tk.Version, b.ID, "misrouted")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Reference != b.Prefix+"-0001" || moved.Version != tk.Version+1 || moved.ID != tk.ID {
		t.Fatalf("moved = %+v", moved)
	}
	// Chain: the second move keeps both earlier numbers.
	moved2, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &moved.Version, c.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{first, moved.Reference} {
		m, err := e.svc.FindByReference(ctx, e.global, strings.ToLower(old))
		if err != nil || m.TicketID != tk.ID || !m.Alias || m.Reference != moved2.Reference {
			t.Errorf("alias %s = %+v %v", old, m, err)
		}
	}
	if m, err := e.svc.FindByReference(ctx, e.global, moved2.Reference); err != nil || m.Alias || m.TicketID != tk.ID {
		t.Errorf("current = %+v %v", m, err)
	}
	// Detail shows the aliases to staff; the audit trail and the event carry ids and references only.
	d, err := e.svc.Get(ctx, e.global, tk.ID)
	if err != nil || len(d.Ticket.Aliases) != 2 || d.Ticket.Aliases[0] != moved.Reference || d.Ticket.Aliases[1] != first {
		t.Fatalf("aliases = %+v %v", d.Ticket.Aliases, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.queue_moved'`, e.corr) != 2 {
		t.Error("moves must be audited")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.queue_moved' AND metadata::text LIKE '%Printer%'`, e.corr) != 0 {
		t.Error("the title was copied into the audit trail")
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'TicketQueueChanged'`, e.corr) != 2 {
		t.Error("TicketQueueChanged must be published")
	}
	// The registry holds all three numbers, one of them current, and an alias cannot be reissued.
	if e.count(`SELECT count(*) FROM servicedesk.reference_registry WHERE ticket_id = $1::uuid`, tk.ID) != 3 ||
		e.count(`SELECT count(*) FROM servicedesk.reference_registry WHERE ticket_id = $1::uuid AND kind = 'current'`, tk.ID) != 1 {
		t.Error("registry does not hold the chain of numbers")
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO servicedesk.reference_registry (reference, ticket_id, queue_id, kind) VALUES ($1, $2::uuid, $3::uuid, 'current')`, first, tk.ID, a.ID); err == nil {
		t.Error("an alias was issued a second time")
	}
	// Version required and checked; the same queue and an archived target are refused.
	var inv *application.InvalidInputError
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, nil, a.ID, "misrouted"); !errors.As(err, &inv) {
		t.Errorf("move without version: %v", err)
	}
	stale := moved.Version
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &stale, a.ID, "misrouted"); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale move: %v", err)
	}
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &moved2.Version, c.ID, "misrouted"); !errors.Is(err, application.ErrQueueSame) {
		t.Errorf("same queue: %v", err)
	}
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &moved2.Version, a.ID, "nonsense"); !errors.As(err, &inv) {
		t.Errorf("bad reason: %v", err)
	}
	cb, _ := e.svc.GetQueue(ctx, e.admin, b.ID)
	if _, err := e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, b.ID, &cb.Queue.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &moved2.Version, b.ID, "misrouted"); !errors.Is(err, application.ErrQueueArchived) {
		t.Errorf("archived target: %v", err)
	}
}

func TestMoveClearsAnAssigneeWhoLosesAccess(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	a, b := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(a, user(e.u1, "work"), user(e.u2, "work"))
	e.grant(b, user(e.u2, "work"))
	in := e.raise(e.global, a)
	// u1 (grant in a only) is assigned and starts the ticket; u2 holds grants in both.
	got, err := e.svc.Assign(ctx, e.c(e.u1), e.employee(e.u1), in.ID, nil, &e.u1, nil)
	if err != nil || got.AssigneeID == nil {
		t.Fatalf("assign by work grant: %v", err)
	}
	started, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), in.ID, nil, application.OpStart, application.Params{})
	if err != nil || started.Status != "in_progress" {
		t.Fatalf("start by work grant: %+v %v", started, err)
	}
	// The assignee must be able to see the queue: u1 has no grant in b.
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, in.ID, nil, &e.u1, nil); err != nil {
		t.Fatalf("reassigning the same assignee: %v", err)
	}
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.admin.UserID), e.admin, in.ID, &started.Version, b.ID, "reorganization")
	if err != nil {
		t.Fatal(err)
	}
	if moved.AssigneeID != nil || moved.Status != "open" {
		t.Fatalf("assignee kept access it does not have: assignee=%v status=%s", moved.AssigneeID, moved.Status)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, in.ID, nil, &e.u1, nil); !errors.Is(err, application.ErrAssigneeNoAccess) {
		t.Errorf("assigning someone without access: %v", err)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, in.ID, nil, &e.u2, nil); err != nil {
		t.Errorf("assigning someone with access: %v", err)
	}
	// A person with global tickets.view/manage keeps access in every queue.
	e.mem.perms[e.u1] = map[string]struct{}{"tickets.manage": {}}
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, in.ID, nil, &e.u1, nil); err != nil {
		t.Errorf("assigning a global worker: %v", err)
	}
}

func TestMoveAuthorizationUsesTicketSourceAndTargetTogether(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	src, hidden, open := e.queue(application.QueueInternal), e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(src, user(e.u1, "work"), user(e.u2, "view"))
	e.grant(open, user(e.u1, "create"))
	tk := e.raise(e.global, src)
	u1, u2 := e.employee(e.u1), e.employee(e.u2)
	// Needs work in the source: view is not enough, and a stranger sees nothing (404, not 403).
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.u2), u2, tk.ID, &tk.Version, open.ID, "misrouted"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("move with view only: %v", err)
	}
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.bob), e.employee(e.bob), tk.ID, &tk.Version, open.ID, "misrouted"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("move by a stranger: %v", err)
	}
	// Needs create in the target; an unknown and a forbidden target are indistinguishable.
	_, errHidden := e.svc.MoveToQueue(ctx, e.c(e.u1), u1, tk.ID, &tk.Version, hidden.ID, "misrouted")
	_, errUnknown := e.svc.MoveToQueue(ctx, e.c(e.u1), u1, tk.ID, &tk.Version, "00000000-0000-7000-8000-000000000000", "misrouted")
	if !errors.Is(errHidden, application.ErrQueueNotPermitted) || errHidden.Error() != errUnknown.Error() {
		t.Errorf("target oracle: %v / %v", errHidden, errUnknown)
	}
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.u1), u1, tk.ID, &tk.Version, open.ID, "misrouted")
	if err != nil {
		t.Fatalf("move with work and create: %v", err)
	}
	// The mover lost sight of the ticket (create is not view): the answer discloses the queue (create grant) but
	// they cannot read the ticket any more.
	if _, err := e.svc.Get(ctx, u1, tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("mover still reads the ticket: %v", err)
	}
	if moved.Queue == nil || moved.Queue.ID != open.ID {
		t.Errorf("moved = %+v", moved)
	}
	// servicedesk.queues.manage moves into a queue the actor holds no grant in.
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.admin.UserID), e.admin, tk.ID, &moved.Version, hidden.ID, "misrouted"); err != nil {
		t.Errorf("move by a queue administrator: %v", err)
	}
}

func TestConcurrentMoves(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	a, b := e.queue(application.QueuePublic), e.queue(application.QueuePublic)
	// The same ticket, the same version, many movers: exactly one wins.
	tk := e.raise(e.employee(e.alice), a)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &tk.Version, b.ID, "misrouted")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, application.ErrVersionConflict):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d movers won, want 1", won)
	}
	// Different tickets into one queue get distinct numbers.
	c := e.queue(application.QueuePublic)
	var tickets []application.Ticket
	for i := 0; i < 12; i++ {
		tickets = append(tickets, e.raise(e.employee(e.alice), a))
	}
	refs := make(chan string, len(tickets))
	for _, x := range tickets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, x.ID, &x.Version, c.ID, "misrouted")
			if err != nil {
				t.Errorf("move: %v", err)
				return
			}
			refs <- m.Reference
		}()
	}
	wg.Wait()
	close(refs)
	seen := map[string]bool{}
	for r := range refs {
		if seen[r] {
			t.Fatalf("number %s issued to two tickets", r)
		}
		seen[r] = true
	}
	for i := 1; i <= len(tickets); i++ {
		if !seen[fmt.Sprintf("%s-%04d", c.Prefix, i)] {
			t.Errorf("missing %s-%04d", c.Prefix, i)
		}
	}
	// Moving into a queue while it is archived: either the archive is refused (an open ticket arrived first) or the
	// move is refused; an archived queue never holds an open ticket.
	for i := 0; i < 15; i++ {
		target := e.queue(application.QueuePublic)
		x := e.raise(e.employee(e.alice), a)
		v := target.Version
		var moveErr, archErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, moveErr = e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, x.ID, &x.Version, target.ID, "misrouted")
		}()
		go func() {
			defer wg.Done()
			_, archErr = e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, target.ID, &v)
		}()
		wg.Wait()
		if moveErr == nil && archErr == nil {
			t.Fatalf("round %d: the ticket moved into a queue that was archived", i)
		}
		if moveErr != nil && !errors.Is(moveErr, application.ErrQueueArchived) {
			t.Fatalf("round %d: move: %v", i, moveErr)
		}
		if archErr != nil && !errors.Is(archErr, application.ErrQueueHasOpenTickets) {
			t.Fatalf("round %d: archive: %v", i, archErr)
		}
		if e.count(`SELECT count(*) FROM servicedesk.tickets t JOIN servicedesk.queues q ON q.id = t.queue_id
			WHERE q.id = $1::uuid AND q.status = 'archived' AND t.status IN ('new','open','in_progress','waiting')`, target.ID) != 0 {
			t.Fatalf("round %d: open ticket in an archived queue", i)
		}
	}
}

func TestQueueGrantMatrix(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	other := e.queue(application.QueueInternal)
	e.mem.teams[e.u2] = []string{e.team}
	e.mem.roles[e.u2] = []string{e.role}
	e.grant(q, user(e.u1, "view"), application.GrantInput{SubjectType: "team", SubjectID: e.team, Level: "work"}, user(e.alice, "create"))
	tk := e.raise(e.global, q)
	foreign := e.raise(e.global, other)
	u1, u2 := e.employee(e.u1), e.employee(e.u2)

	// create without view: alice may raise into the queue but not read other people's tickets in it.
	own := e.raise(e.employee(e.alice), q)
	if _, err := e.svc.Get(ctx, e.employee(e.alice), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("create grant reads foreign ticket: %v", err)
	}
	if d, err := e.svc.Get(ctx, e.employee(e.alice), own.ID); err != nil || d.Ticket.Queue == nil {
		t.Errorf("reporter reads own ticket: %+v %v", d.Ticket, err)
	}
	// Bob has no grant: cannot raise (the same answer for a queue that does not exist).
	_, errForbidden := e.svc.Create(ctx, e.c(e.bob), e.employee(e.bob), application.CreateInput{Title: "x", QueueID: &q.ID})
	unknown := "00000000-0000-7000-8000-000000000000"
	_, errUnknown := e.svc.Create(ctx, e.c(e.bob), e.employee(e.bob), application.CreateInput{Title: "x", QueueID: &unknown})
	if !errors.Is(errForbidden, application.ErrQueueNotPermitted) || errForbidden.Error() != errUnknown.Error() {
		t.Errorf("queue oracle on create: %v / %v", errForbidden, errUnknown)
	}
	// view: read ticket and the internal comments, but not work it.
	if d, err := e.svc.Get(ctx, u1, tk.ID); err != nil || len(d.Allowed) != 0 {
		t.Errorf("view grant: %+v %v", d.Allowed, err)
	}
	if _, err := e.svc.Get(ctx, u1, foreign.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("view grant reads another queue: %v", err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u1), u1, tk.ID, nil, application.OpStart, application.Params{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view grant works the ticket: %v", err)
	}
	if _, err := e.svc.AddComment(ctx, e.c(e.u1), u1, tk.ID, "internal", true); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view grant comments internally: %v", err)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.u1), u1, tk.ID, nil, &e.u1, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view grant assigns: %v", err)
	}
	// work through a Team grant (membership resolved at use) and through nothing else.
	if _, err := e.svc.AddComment(ctx, e.c(e.u2), u2, tk.ID, "internal note", true); err != nil {
		t.Errorf("team work grant cannot comment internally: %v", err)
	}
	if d, err := e.svc.Get(ctx, u1, tk.ID); err != nil || len(d.Comments) != 1 {
		t.Errorf("view grant sees internal comment: %d %v", len(d.Comments), err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u2), u2, tk.ID, nil, application.OpStart, application.Params{}); err != nil {
		t.Errorf("team work grant cannot start: %v", err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u2), u2, foreign.ID, nil, application.OpStart, application.Params{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("work grant works another queue: %v", err)
	}
	// Leaving the Team removes the access at once.
	e.mem.mu.Lock()
	e.mem.teams[e.u2] = nil
	e.mem.mu.Unlock()
	if _, err := e.svc.Get(ctx, u2, tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("former team member still reads: %v", err)
	}
	// A role grant.
	e.grant(other, application.GrantInput{SubjectType: "role", SubjectID: e.role, Level: "view"})
	e.mem.mu.Lock()
	e.mem.roles[e.u2] = []string{e.role}
	e.mem.mu.Unlock()
	if _, err := e.svc.Get(ctx, u2, foreign.ID); err != nil {
		t.Errorf("role grant: %v", err)
	}
	// Global tickets.view still sees every queue and tickets.manage still works every queue (compatibility).
	view := application.Principal{UserID: e.u1, View: true}
	if _, err := e.svc.Get(ctx, view, foreign.ID); err != nil {
		t.Errorf("tickets.view lost access: %v", err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.global.UserID), e.global, foreign.ID, nil, application.OpStart, application.Params{}); err != nil {
		t.Errorf("tickets.manage lost access: %v", err)
	}
}

func TestRequesterSeesOnlyWhatTheDeskDisclosure(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	pub, internal := e.queue(application.QueuePublic), e.queue(application.QueueInternal)
	tk := e.raise(e.employee(e.alice), pub)
	alice := e.employee(e.alice)

	// Moved into an internal desk: the requester keeps the number they know and gets only the neutral label.
	moved, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &tk.Version, internal.ID, "misrouted")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Get(ctx, alice, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ticket.Reference != tk.Reference || d.Ticket.Queue != nil || d.Ticket.QueueID != "" || d.Ticket.QueueLabel == "" || len(d.Ticket.Aliases) != 0 {
		t.Errorf("requester sees %+v", d.Ticket)
	}
	if strings.Contains(d.Ticket.Reference, internal.Prefix) {
		t.Error("the internal prefix reached the requester")
	}
	// Lists and queries apply the same rule; staff see the real number.
	mine, err := e.svc.List(ctx, alice, false, application.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range mine.Items {
		if it.ID == tk.ID && (it.Reference != tk.Reference || it.Queue != nil || it.QueueTeamID != nil) {
			t.Errorf("list leaks: %+v", it)
		}
	}
	page, err := e.svc.Query(ctx, alice, query.Request{Limit: 100}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.ID == tk.ID && (it.Reference != tk.Reference || it.Queue != nil) {
			t.Errorf("query leaks: %+v", it)
		}
	}
	if sd, _ := e.svc.Get(ctx, e.global, tk.ID); sd.Ticket.Reference != moved.Reference || sd.Ticket.Queue == nil {
		t.Errorf("staff sees %+v", sd.Ticket)
	}
	// Alias lookup applies the current visibility: the requester resolves their own number, a stranger cannot tell
	// the ticket from an unknown reference.
	if m, err := e.svc.FindByReference(ctx, alice, tk.Reference); err != nil || m.TicketID != tk.ID || m.Reference != tk.Reference {
		t.Errorf("requester lookup = %+v %v", m, err)
	}
	_, errStranger := e.svc.FindByReference(ctx, e.employee(e.bob), tk.Reference)
	_, errUnknown := e.svc.FindByReference(ctx, e.employee(e.bob), "ZZZZ-999999")
	_, errMalformed := e.svc.FindByReference(ctx, e.employee(e.bob), "not a reference")
	if !errors.Is(errStranger, application.ErrNotFound) || errStranger.Error() != errUnknown.Error() || errUnknown.Error() != errMalformed.Error() {
		t.Errorf("reference oracle: %v / %v / %v", errStranger, errUnknown, errMalformed)
	}
	if _, err := e.svc.FindByReference(ctx, e.employee(e.bob), moved.Reference); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("stranger resolves the internal number: %v", err)
	}
	// Moved to another public desk the requester learns the new number and desk.
	pub2 := e.queue(application.QueuePublic)
	m2, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &moved.Version, pub2.ID, "misrouted")
	if err != nil {
		t.Fatal(err)
	}
	d, _ = e.svc.Get(ctx, alice, tk.ID)
	if d.Ticket.Reference != m2.Reference || d.Ticket.Queue == nil || d.Ticket.Queue.ID != pub2.ID {
		t.Errorf("requester after public move: %+v", d.Ticket)
	}
	for _, a := range d.Ticket.Aliases {
		if strings.Contains(a, internal.Prefix) {
			t.Errorf("alias of the internal desk reached the requester: %s", a)
		}
	}
}

func TestQueueScopedQueriesCannotBeUsedAsAnOracle(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "view"))
	e.grant(q2, user(e.u1, "create"), user(e.u2, "work"))
	t1 := e.raise(e.global, q1)
	// u1 reports a ticket into q2 (create grant only) and someone assigns it to u2.
	own := e.raise(e.employee(e.u1), q2)
	if _, err := e.svc.Assign(ctx, e.c(e.u2), e.employee(e.u2), own.ID, nil, &e.u2, nil); err != nil {
		t.Fatal(err)
	}
	u1 := e.employee(e.u1)
	ids := func(p query.Page[application.Ticket]) map[string]bool {
		out := map[string]bool{}
		for _, it := range p.Items {
			out[it.ID] = true
		}
		return out
	}
	// Without a routing filter u1 sees q1's tickets and their own ticket in q2.
	page, err := e.svc.QueryScoped(ctx, u1, query.Request{Limit: 100}, application.ScopeAuto, nil, "")
	if err != nil || !ids(page)[t1.ID] || !ids(page)[own.ID] {
		t.Fatalf("auto scope = %v %v", ids(page), err)
	}
	// A routing filter narrows to the queues u1 views: the assignee of the q2 ticket cannot be probed.
	assignee := query.Request{Limit: 100, Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "assignee", Op: "is_not_empty"}}}
	page, err = e.svc.QueryScoped(ctx, u1, assignee, application.ScopeAuto, nil, "")
	if err != nil || ids(page)[own.ID] {
		t.Errorf("assignee filter reached an own ticket of an undisclosed queue: %v %v", ids(page), err)
	}
	// The queue field works for u1 only for queues they view.
	inQ := func(id string) query.Request {
		return query.Request{Limit: 100, Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "queue", Op: "equals", Value: []byte(`"` + id + `"`)}}}
	}
	page, err = e.svc.QueryScoped(ctx, u1, inQ(q2.ID), application.ScopeAuto, nil, "")
	if err != nil || len(page.Items) != 0 {
		t.Errorf("queue filter on an undisclosed queue = %v %v", ids(page), err)
	}
	page, err = e.svc.QueryScoped(ctx, u1, inQ(q1.ID), application.ScopeAuto, nil, "")
	if err != nil || !ids(page)[t1.ID] || len(page.Items) != 1 {
		t.Errorf("queue filter on a viewed queue = %v %v", ids(page), err)
	}
	// inQueue narrows like a filter; an unviewed queue answers like an empty one.
	page, err = e.svc.QueryScoped(ctx, u1, query.Request{Limit: 100}, application.ScopeAuto, nil, q2.ID)
	if err != nil || len(page.Items) != 0 {
		t.Errorf("inQueue on an unviewed queue = %v %v", ids(page), err)
	}
	// Employees cannot use the field at all, exactly like an unknown field.
	_, e1 := e.svc.Query(ctx, e.employee(e.bob), inQ(q1.ID), false, nil)
	unknown := query.Request{Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: "nonexistent", Op: "equals", Value: []byte(`"` + q1.ID + `"`)}}}
	_, e2 := e.svc.Query(ctx, e.employee(e.bob), unknown, false, nil)
	if e1 == nil || e2 == nil || strings.ReplaceAll(e1.Error(), "queue", "x") != strings.ReplaceAll(e2.Error(), "nonexistent", "x") {
		t.Errorf("queue field answers differently from an unknown field: %v / %v", e1, e2)
	}
	// Counts go through the same predicate.
	cnt := query.Request{Limit: 1, Count: true}
	page, err = e.svc.QueryScoped(ctx, u1, cnt, application.ScopeAuto, nil, q1.ID)
	if err != nil || page.Count == nil || *page.Count != 1 {
		t.Errorf("count in a viewed queue = %v %v", page.Count, err)
	}
	// The plain list endpoint follows the same scopes.
	if _, err := e.svc.List(ctx, e.employee(e.bob), true, application.Filter{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("scope=all without any view access: %v", err)
	}
	list, err := e.svc.List(ctx, u1, true, application.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, it := range list.Items {
		seen[it.ID] = true
	}
	if !seen[t1.ID] || !seen[own.ID] {
		t.Errorf("list for a queue viewer = %v", seen)
	}
	narrowed, err := e.svc.List(ctx, u1, true, application.Filter{AssigneeID: e.u2})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range narrowed.Items {
		if it.ID == own.ID {
			t.Error("assignee filter of the plain list reached an undisclosed queue")
		}
	}
}

func TestQueueAdministration(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	var inv *application.InvalidInputError
	in := application.QueueInput{Key: "hr-" + strings.ToLower(randLetters(4)), Prefix: "H" + randLetters(4), Name: "HR"}
	if _, err := e.svc.CreateQueue(ctx, e.c(e.global.UserID), e.global, in); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without servicedesk.queues.manage: %v", err)
	}
	for name, bad := range map[string]application.QueueInput{
		"prefix":     {Key: "ok-key", Prefix: "x", Name: "n"},
		"prefix low": {Key: "ok-key", Prefix: "abc", Name: "n"},
		"key":        {Key: "A", Prefix: "ABC", Name: "n"},
		"name":       {Key: "ok-key", Prefix: "ABC", Name: "  "},
		"visibility": {Key: "ok-key", Prefix: "ABC", Name: "n", Visibility: "secret"},
	} {
		if _, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, bad); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	q, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	e.queues = append(e.queues, q.ID)
	if q.Version != 1 || q.Status != "active" || q.DefaultIntake {
		t.Errorf("queue = %+v", q)
	}
	if _, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, application.QueueInput{Key: "other-" + strings.ToLower(randLetters(3)), Prefix: in.Prefix, Name: "Dup"}); !errors.Is(err, application.ErrQueuePrefixTaken) {
		t.Errorf("duplicate prefix: %v", err)
	}
	if _, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, application.QueueInput{Key: in.Key, Prefix: "N" + randLetters(4), Name: "Dup"}); !errors.Is(err, application.ErrQueueKeyTaken) {
		t.Errorf("duplicate key: %v", err)
	}
	if _, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, application.QueueInput{Key: "legacy", Prefix: "TKT", Name: "Legacy"}); !errors.Is(err, application.ErrQueuePrefixTaken) {
		t.Errorf("the legacy prefix must stay reserved: %v", err)
	}
	// Update: version required and checked; key and prefix are not editable.
	name := "Human Resources"
	if _, err := e.svc.UpdateQueue(ctx, e.c(e.admin.UserID), e.admin, q.ID, nil, application.QueueUpdate{Name: &name}); !errors.As(err, &inv) {
		t.Errorf("update without version: %v", err)
	}
	stale := 7
	if _, err := e.svc.UpdateQueue(ctx, e.c(e.admin.UserID), e.admin, q.ID, &stale, application.QueueUpdate{Name: &name}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	if _, err := e.svc.UpdateQueue(ctx, e.c(e.global.UserID), e.global, q.ID, &q.Version, application.QueueUpdate{Name: &name}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("update by a ticket manager: %v", err)
	}
	up, err := e.svc.UpdateQueue(ctx, e.c(e.admin.UserID), e.admin, q.ID, &q.Version, application.QueueUpdate{Name: &name})
	if err != nil || up.Name != name || up.Version != 2 || up.Prefix != q.Prefix {
		t.Fatalf("update = %+v %v", up, err)
	}
	// The intake queue stays public and cannot be archived; another public queue can take over.
	var def application.Queue
	qs, _ := e.svc.ListQueues(ctx, e.admin, false)
	for _, v := range qs {
		if v.Queue.DefaultIntake {
			def = v.Queue
		}
	}
	internal := "internal"
	if _, err := e.svc.UpdateQueue(ctx, e.c(e.admin.UserID), e.admin, def.ID, &def.Version, application.QueueUpdate{Visibility: &internal}); !errors.As(err, &inv) {
		t.Errorf("intake queue made internal: %v", err)
	}
	if _, err := e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, def.ID, &def.Version); !errors.Is(err, application.ErrQueueIsDefault) {
		t.Errorf("archive the intake queue: %v", err)
	}
	// Archive is refused while open tickets exist, and works after they moved.
	other := e.queue(application.QueuePublic)
	tk := e.raise(e.global, up)
	if _, err := e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, q.ID, &up.Version); !errors.Is(err, application.ErrQueueHasOpenTickets) {
		t.Errorf("archive with an open ticket: %v", err)
	}
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.admin.UserID), e.admin, tk.ID, &tk.Version, other.ID, "reorganization"); err != nil {
		t.Fatal(err)
	}
	arch, err := e.svc.ArchiveQueue(ctx, e.c(e.admin.UserID), e.admin, q.ID, &up.Version)
	if err != nil || arch.Status != "archived" || arch.ArchivedAt == nil {
		t.Fatalf("archive = %+v %v", arch, err)
	}
	// Archived queues are hidden from everybody but administrators; the prefix stays taken.
	if _, err := e.svc.GetQueue(ctx, e.global, q.ID); !errors.Is(err, application.ErrQueueNotFound) {
		t.Errorf("archived queue visible to staff: %v", err)
	}
	if _, err := e.svc.GetQueue(ctx, e.admin, q.ID); err != nil {
		t.Errorf("archived queue hidden from the administrator: %v", err)
	}
	if _, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, application.QueueInput{Key: "again-" + strings.ToLower(randLetters(3)), Prefix: in.Prefix, Name: "Again"}); !errors.Is(err, application.ErrQueuePrefixTaken) {
		t.Errorf("prefix reused after archive: %v", err)
	}
	// Make-default moves the intake role; the old one is demoted.
	cur, _ := e.svc.GetQueue(ctx, e.admin, other.ID)
	made, err := e.svc.MakeDefaultQueue(ctx, e.c(e.admin.UserID), e.admin, other.ID, &cur.Queue.Version)
	if err != nil || !made.DefaultIntake {
		t.Fatalf("make default = %+v %v", made, err)
	}
	if e.count(`SELECT count(*) FROM servicedesk.queues WHERE default_for_intake`) != 1 {
		t.Error("exactly one intake queue must exist")
	}
	// Restore the legacy intake queue so other tests keep a stable default.
	oldDef, _ := e.svc.GetQueue(ctx, e.admin, def.ID)
	if _, err := e.svc.MakeDefaultQueue(ctx, e.c(e.admin.UserID), e.admin, def.ID, &oldDef.Queue.Version); err != nil {
		t.Fatal(err)
	}
	// Audit: administration is recorded with ids, never with names.
	for _, a := range []string{"created", "updated", "archived", "made_default"} {
		if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = $2`, e.corr, "servicedesk.queue."+a) == 0 {
			t.Errorf("servicedesk.queue.%s is not audited", a)
		}
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action LIKE 'servicedesk.queue.%' AND (before_data::text LIKE '%Human Resources%' OR after_data::text LIKE '%Human Resources%' OR metadata::text LIKE '%Human Resources%')`, e.corr) != 0 {
		t.Error("a queue name was copied into the audit trail")
	}
}

func TestGrantReplacementIsValidatedAuditedAndVersioned(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	cur, _ := e.svc.GetQueue(ctx, e.admin, q.ID)
	v := cur.Queue.Version
	var inv *application.InvalidInputError
	for name, g := range map[string]application.GrantInput{
		"level":   {SubjectType: "user", SubjectID: e.u1, Level: "root"},
		"subject": {SubjectType: "group", SubjectID: e.u1, Level: "view"},
		"id":      {SubjectType: "user", SubjectID: "nope", Level: "view"},
	} {
		if _, err := e.svc.ReplaceGrants(ctx, e.c(e.admin.UserID), e.admin, q.ID, &v, []application.GrantInput{g}); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ghost := "00000000-0000-7000-8000-0000000000ff"
	if _, err := e.svc.ReplaceGrants(ctx, e.c(e.admin.UserID), e.admin, q.ID, &v, []application.GrantInput{user(ghost, "view")}); !errors.Is(err, application.ErrUserInvalid) {
		t.Errorf("grant to an unknown user: %v", err)
	}
	if _, err := e.svc.ReplaceGrants(ctx, e.c(e.global.UserID), e.global, q.ID, &v, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("grants by a ticket manager: %v", err)
	}
	out, err := e.svc.ReplaceGrants(ctx, e.c(e.admin.UserID), e.admin, q.ID, &v, []application.GrantInput{user(e.u1, "view"), user(e.u1, "view"), user(e.u2, "create")})
	if err != nil || len(out.Grants) != 2 || out.Queue.Version != v+1 {
		t.Fatalf("replace = %+v %v", out, err)
	}
	if _, err := e.svc.ReplaceGrants(ctx, e.c(e.admin.UserID), e.admin, q.ID, &v, nil); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale grants: %v", err)
	}
	// Access follows the replacement immediately, and the grants are visible only to administrators.
	tk := e.raise(e.global, q)
	if _, err := e.svc.Get(ctx, e.employee(e.u1), tk.ID); err != nil {
		t.Errorf("granted view: %v", err)
	}
	if _, err := e.svc.ReplaceGrants(ctx, e.c(e.admin.UserID), e.admin, q.ID, &out.Queue.Version, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, e.employee(e.u1), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("revoked view still reads: %v", err)
	}
	if v, err := e.svc.GetQueue(ctx, e.employee(e.u2), q.ID); !errors.Is(err, application.ErrQueueNotFound) || len(v.Grants) != 0 {
		t.Errorf("a create-only user learned about the queue: %+v %v", v, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.queue.grants_replaced'`, e.corr) != 2 {
		t.Error("grant replacements must be audited")
	}
}

func TestQueueListsFollowRoutingAndDisclosure(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	pubChoice := e.queue(application.QueuePublic)
	internalGranted := e.queue(application.QueueInternal)
	internalHidden := e.queue(application.QueueInternal)
	auto, err := e.svc.CreateQueue(ctx, e.c(e.admin.UserID), e.admin, application.QueueInput{Key: "auto-" + strings.ToLower(randLetters(4)), Prefix: "A" + randLetters(4), Name: "Auto",
		Visibility: application.QueuePublic, RoutingMode: application.RoutingAutomatic})
	if err != nil {
		t.Fatal(err)
	}
	e.queues = append(e.queues, auto.ID)
	e.grant(internalGranted, user(e.alice, "create"))
	names := func(vs []application.QueueView) map[string]bool {
		out := map[string]bool{}
		for _, v := range vs {
			out[v.Queue.ID] = true
		}
		return out
	}
	// Intake list of an employee: public + employee choice, plus the queues with a create grant; never hidden or automatic ones.
	create, err := e.svc.ListQueues(ctx, e.employee(e.alice), true)
	if err != nil {
		t.Fatal(err)
	}
	got := names(create)
	if !got[pubChoice.ID] || !got[internalGranted.ID] || got[internalHidden.ID] || got[auto.ID] {
		t.Errorf("employee intake list = %v", got)
	}
	// The browsing list shows only queues the employee may know: a create grant alone does not make a queue
	// browsable, and routing internals are not part of the answer.
	browse, _ := e.svc.ListQueues(ctx, e.employee(e.alice), false)
	if names(browse)[internalHidden.ID] || names(browse)[internalGranted.ID] {
		t.Errorf("employee browse list = %v", names(browse))
	}
	for _, v := range create {
		if v.Queue.RoutingMode != "" || v.Queue.DefaultTeamID != nil {
			t.Errorf("routing internals reached an employee: %+v", v.Queue)
		}
	}
	// Staff who work queues also see the automatically routed ones; the administrator sees everything.
	staff, _ := e.svc.ListQueues(ctx, e.global, true)
	if !names(staff)[auto.ID] {
		t.Error("staff cannot pick the automatically routed queue")
	}
	all, _ := e.svc.ListQueues(ctx, e.admin, false)
	if !names(all)[internalHidden.ID] {
		t.Error("administrator misses a queue")
	}
	// Employees cannot choose an automatically routed queue by id either.
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "x", QueueID: &auto.ID}); !errors.Is(err, application.ErrQueueNotPermitted) {
		t.Errorf("employee chose an automatic queue: %v", err)
	}
	// Staff can create on behalf of someone in a queue they hold create for, and set routing there.
	if _, err := e.svc.Create(ctx, e.c(e.admin.UserID), e.admin, application.CreateInput{Title: "x", QueueID: &auto.ID, AffectedUserID: &e.alice, Priority: "high"}); err != nil {
		t.Errorf("staff create: %v", err)
	}
	// Priority and routing hints need work access in the chosen queue.
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "x", QueueID: &internalGranted.ID, Priority: "urgent"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("urgent by an employee: %v", err)
	}
}

func TestSidebarScope(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "view"))
	e.grant(q2, user(e.u1, "create"))
	sc, err := e.svc.SidebarScope(ctx, e.employee(e.u1))
	if err != nil || !sc.AnyView || len(sc.Queues) != 1 || sc.Queues[0].ID != q1.ID {
		t.Fatalf("scope = %+v %v", sc, err)
	}
	none, _ := e.svc.SidebarScope(ctx, e.employee(e.bob))
	if none.AnyView || len(none.Queues) != 0 {
		t.Errorf("employee scope = %+v", none)
	}
	e.grant(q1, user(e.u1, "view"), user(e.u1, "work"))
	changed, _ := e.svc.SidebarScope(ctx, e.employee(e.u1))
	if changed.Key != sc.Key {
		t.Error("scope key must depend on the viewable queue set, not on the level")
	}
	e.grant(q2, user(e.u1, "view"))
	more, _ := e.svc.SidebarScope(ctx, e.employee(e.u1))
	if more.Key == sc.Key || len(more.Queues) != 2 {
		t.Errorf("scope key did not change with the viewable queue set: %+v", more)
	}
}

func TestMyWorkTicketSources(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "work"))
	e.grant(q2, user(e.u1, "work"))
	e.mem.teams[e.u1] = []string{e.team}
	a := e.raise(e.global, q1)
	b := e.raise(e.global, q2)
	c := e.raise(e.global, q1)
	for _, tk := range []application.Ticket{a, b} {
		if _, err := e.svc.Assign(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, &e.u1, nil); err != nil {
			t.Fatal(err)
		}
	}
	// c is routed to u1's Team, unassigned.
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, c.ID, nil, nil, &e.team); err != nil {
		t.Fatal(err)
	}
	u1 := e.employee(e.u1)
	rows, err := e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, "", 50)
	if err != nil || len(rows) != 2 {
		t.Fatalf("assigned = %d %v", len(rows), err)
	}
	team, err := e.svc.MyWorkTickets(ctx, u1, application.WorkTeam, "", 50)
	if err != nil || len(team) != 1 || team[0].Ticket.ID != c.ID {
		t.Fatalf("team = %+v %v", team, err)
	}
	// Keyset paging in the shared order.
	first, _ := e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, "", 1)
	second, err := e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, first[0].Cursor, 1)
	if err != nil || len(second) != 1 || second[0].Ticket.ID == first[0].Ticket.ID {
		t.Errorf("second page = %+v %v", second, err)
	}
	if _, err := e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, "garbage", 5); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("garbage cursor: %v", err)
	}
	if n, err := e.svc.MyWorkTicketCount(ctx, u1, application.WorkAssigned, 1000); err != nil || n != 2 {
		t.Errorf("count = %d %v", n, err)
	}
	// Losing access to a queue removes its tickets from My Work at once (per-item authorization).
	e.grant(q2) // revoke every grant of q2
	rows, _ = e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, "", 50)
	if len(rows) != 1 || rows[0].Ticket.ID != a.ID {
		t.Errorf("after revoking q2: %+v", rows)
	}
	if n, _ := e.svc.MyWorkTicketCount(ctx, u1, application.WorkAssigned, 1000); n != 1 {
		t.Errorf("count after revoke = %d", n)
	}
	// Resolved tickets are not work any more.
	if _, err := e.svc.Transition(ctx, e.c(e.u1), u1, a.ID, nil, application.OpResolve, application.Params{Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	if rows, _ = e.svc.MyWorkTickets(ctx, u1, application.WorkAssigned, "", 50); len(rows) != 0 {
		t.Errorf("resolved ticket in My Work: %+v", rows)
	}
	// Someone else never sees them.
	if rows, _ = e.svc.MyWorkTickets(ctx, e.employee(e.bob), application.WorkAssigned, "", 50); len(rows) != 0 {
		t.Errorf("foreign work list: %+v", rows)
	}
}

// Regression (simulation round 3): a team lead with the global ticket view (no Queue grants needed) got the
// "team_tickets" source unavailable because the work predicate did not reference the caller bind parameter.
func TestMyWorkTeamTicketsForGlobalViewer(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1 := e.queue(application.QueueInternal)
	e.mem.teams[e.u1] = []string{e.team}
	c := e.raise(e.global, q1)
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, c.ID, nil, nil, &e.team); err != nil {
		t.Fatal(err)
	}
	lead := application.Principal{UserID: e.u1, View: true}
	rows, err := e.svc.MyWorkTickets(ctx, lead, application.WorkTeam, "", 50)
	if err != nil || len(rows) != 1 || rows[0].Ticket.ID != c.ID {
		t.Fatalf("team list for a global viewer = %+v %v", rows, err)
	}
	if n, err := e.svc.MyWorkTicketCount(ctx, lead, application.WorkTeam, 1000); err != nil || n != 1 {
		t.Fatalf("team count for a global viewer = %d %v", n, err)
	}
	if rows, err = e.svc.MyWorkTickets(ctx, lead, application.WorkAssigned, "", 50); err != nil || len(rows) != 0 {
		t.Fatalf("assigned list = %+v %v", rows, err)
	}
}

func TestTicketHistoryShowsAssignmentsWithActorAndReason(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1 := e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "work"))
	e.mem.teams[e.u1] = []string{e.team}
	tk := e.raise(e.global, q1)
	// Assigned by another person with a reason, routed to a Team, then started by the assignee, then unassigned.
	if _, err := e.svc.AssignWithReason(ctx, e.c(e.admin.UserID), e.admin, tk.ID, nil, &e.u1, &e.team, "Specialist for ORBIS"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, application.OpStart, application.Params{}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := e.svc.Assign(ctx, e.c(e.admin.UserID), e.admin, tk.ID, nil, &empty, nil); err != nil {
		t.Fatal(err)
	}
	entries, names, err := e.svc.History(ctx, e.admin, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var assigned, routed, unassigned, status *application.HistoryEntry
	for i := range entries {
		switch entries[i].Kind {
		case application.HistoryAssigned:
			assigned = &entries[i]
		case application.HistoryTeamRouted:
			routed = &entries[i]
		case application.HistoryUnassigned:
			unassigned = &entries[i]
		case application.HistoryStatusChanged:
			status = &entries[i]
		}
	}
	if entries[0].Kind != application.HistoryCreated {
		t.Errorf("first entry = %+v", entries[0])
	}
	if assigned == nil || assigned.ToUserID != e.u1 || assigned.ActorID != e.admin.UserID || assigned.Reason != "Specialist for ORBIS" || assigned.Via != "assigned" {
		t.Errorf("assigned entry = %+v", assigned)
	}
	if routed == nil || routed.ToTeamID != e.team {
		t.Errorf("routed entry = %+v", routed)
	}
	if unassigned == nil || unassigned.FromUserID != e.u1 {
		t.Errorf("unassigned entry = %+v", unassigned)
	}
	if status == nil || status.ToStatus == "" {
		t.Errorf("status entry = %+v", status)
	}
	_ = names
	// Reporters and people without view access in the Queue do not get the staff history.
	if _, _, err := e.svc.History(ctx, e.employee(e.bob), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("outsider history: %v", err)
	}
}

func TestMarkDuplicateIsAnExplicitGuardedOperation(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "work"))
	a, b, c := e.raise(e.global, q1), e.raise(e.global, q1), e.raise(e.global, q1)
	other := e.raise(e.global, q2)
	lead := e.employee(e.u1)
	v := a.Version
	// Guards: version, self, a target in a Queue the caller cannot view (same answer as an unknown one), employees.
	stale := v + 5
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, &stale, b.ID, ""); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, &v, a.ID, ""); !errors.Is(err, application.ErrDuplicateTarget) {
		t.Errorf("self: %v", err)
	}
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, &v, other.ID, ""); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("target in an unknown queue: %v", err)
	}
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, &v, "00000000-0000-7000-8000-000000000001", ""); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown target: %v", err)
	}
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.bob), e.employee(e.bob), a.ID, &v, b.ID, ""); err == nil {
		t.Error("an employee must not mark duplicates")
	}
	// The operation cancels the duplicate with the reason code and links it.
	out, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, &v, b.ID, "Same printer, same error")
	if err != nil || out.Status != application.StatusCancelled || out.DuplicateOfID == nil || *out.DuplicateOfID != b.ID ||
		out.StatusReason == nil || *out.StatusReason != application.StatusReasonDuplicate || out.Version != v+1 {
		t.Fatalf("mark duplicate = %+v %v", out, err)
	}
	// No chains: the target must not be a duplicate, and a ticket that has duplicates cannot become one.
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, c.ID, nil, a.ID, ""); !errors.Is(err, application.ErrDuplicateTarget) {
		t.Errorf("duplicate of a duplicate: %v", err)
	}
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, b.ID, nil, c.ID, ""); !errors.Is(err, application.ErrDuplicateTarget) {
		t.Errorf("master becoming a duplicate: %v", err)
	}
	// A finished ticket cannot be marked.
	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, a.ID, nil, c.ID, ""); !errors.As(err, new(*application.InvalidTransitionError)) {
		t.Errorf("already cancelled: %v", err)
	}
	// History and audit show who did it, why and for which ticket.
	entries, _, err := e.svc.History(ctx, lead, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != application.HistoryMarkedDuplicate || last.DuplicateOfID != b.ID || last.ActorID != e.u1 || last.Reason != "Same printer, same error" {
		t.Errorf("history = %+v", last)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.marked_duplicate' AND target_id = $2`, e.corr, a.ID) != 1 {
		t.Error("exactly one marked_duplicate audit event")
	}
}

func TestPatientImpactRaisesAnEmployeesPriorityToHighAtMost(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.alice, "create"))
	tk, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Printer in ER", QueueID: &q.ID, PatientImpact: true})
	if err != nil || tk.Priority != "high" || !tk.PatientImpact {
		t.Fatalf("patient impact = %+v %v", tk, err)
	}
	plain, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Printer", QueueID: &q.ID})
	if err != nil || plain.Priority != "normal" || plain.PatientImpact {
		t.Fatalf("without the signal = %+v %v", plain, err)
	}
	// The impact choices of the form: patient_care is the same signal; the others are stored and change nothing else.
	viaEnum, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "ER printer", QueueID: &q.ID, Impact: "patient_care"})
	if err != nil || viaEnum.Priority != "high" || !viaEnum.PatientImpact || viaEnum.ReportedImpact != "patient_care" {
		t.Errorf("impact patient_care = %+v %v", viaEnum, err)
	}
	blocked, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "Cannot work", QueueID: &q.ID, Impact: "blocked"})
	if err != nil || blocked.Priority != "normal" || blocked.PatientImpact || blocked.ReportedImpact != "blocked" {
		t.Errorf("impact blocked = %+v %v", blocked, err)
	}
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "x", QueueID: &q.ID, Impact: "catastrophic"}); err == nil {
		t.Error("an unknown impact must be refused")
	}
	// Urgent stays out of reach of an employee, with or without the signal.
	if _, err := e.svc.Create(ctx, e.c(e.alice), e.employee(e.alice), application.CreateInput{Title: "x", QueueID: &q.ID, PatientImpact: true, Priority: "urgent"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("urgent by an employee: %v", err)
	}
	// Staff keep their explicit priority; the flag is stored either way.
	st, err := e.svc.Create(ctx, e.c(e.global.UserID), e.global, application.CreateInput{Title: "x", QueueID: &q.ID, PatientImpact: true, Priority: "low"})
	if err != nil || st.Priority != "low" || !st.PatientImpact {
		t.Errorf("staff explicit priority = %+v %v", st, err)
	}
	// Staff can filter on it.
	page, err := e.svc.QueryScoped(ctx, e.global, query.Request{Limit: 100, Filter: &query.Filter{V: 1, Root: &query.Node{Type: "group", Logic: "and", Children: []query.Node{query.Cond("patient_impact", query.OpIsTrue, nil)}}}}, application.ScopeAll, nil, q.ID)
	if err != nil || len(page.Items) != 3 {
		t.Errorf("patient_impact filter = %d %v", len(page.Items), err)
	}
}

type searchDir struct {
	dir
	ids []string
}

func (d searchDir) SearchUserIDs(_ context.Context, text string, _ int) ([]string, error) {
	if text == "brandt" {
		return d.ids, nil
	}
	return nil, nil
}

func TestTicketSearchMatchesPeopleAndDeviceRespectingVisibility(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "view"))
	// alice reports in both Queues; the search text matches her by name only through the directory.
	for _, q := range []application.Queue{q1, q2} {
		if _, err := e.svc.Create(ctx, e.c(e.global.UserID), e.global, application.CreateInput{Title: "Monitor flickers", QueueID: &q.ID, AffectedUserID: &e.alice}); err != nil {
			t.Fatal(err)
		}
	}
	devTicket := e.raise(e.global, q1)
	active := map[string]bool{e.alice: true, e.global.UserID: true, e.u1: true}
	svc := application.NewService(repository.New(e.pool), searchDir{dir: dir{active: active}, ids: []string{e.alice}}, device{}).WithMemberships(e.mem)
	byName := func(p application.Principal) []application.Ticket {
		page, err := svc.QueryScoped(ctx, p, query.Request{Limit: 100, Search: "brandt"}, application.ScopeAuto, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	if got := byName(e.global); len(got) != 2 {
		t.Errorf("staff search by the affected person's name = %d tickets", len(got))
	}
	// A caller who views only Queue 1 finds only that Queue's ticket: the name never reaches into Queue 2.
	if got := byName(e.employee(e.u1)); len(got) != 1 || got[0].QueueID != q1.ID {
		t.Errorf("queue-scoped search = %+v", got)
	}
	// Employees (no queue view) get no name expansion at all.
	if got := byName(e.employee(e.alice)); len(got) != 0 {
		t.Errorf("employee search by name = %d", len(got))
	}
	// Device text: serial number, asset tag and reference of the device named on the ticket.
	if _, err := e.pool.Exec(ctx, `UPDATE servicedesk.tickets SET device_snapshot = '{"reference":"AST-9","product":"Zebra ZT411","serialNumber":"SN-ZX81-77","assetTag":"TAG-4711"}'::jsonb WHERE id = $1::uuid`, devTicket.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"sn-zx81", "tag-4711", "zebra", "AST-9"} {
		page, err := svc.QueryScoped(ctx, e.global, query.Request{Limit: 100, Search: s}, application.ScopeAll, nil, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != devTicket.ID {
			t.Errorf("device search %q = %d tickets %v", s, len(page.Items), err)
		}
	}
}

// A requester whose ticket moved to a Queue they cannot know gets none of the staff-only fields (assignee,
// location, patient impact) in the list, the query, the detail or the names; in a visible Queue they stay.
func TestHiddenQueueWithholdsStaffOnlyFieldsFromTheRequester(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	pub, internal := e.queue(application.QueuePublic), e.queue(application.QueueInternal)
	e.grant(pub, user(e.u1, "work"))
	e.grant(internal, user(e.u1, "work"))
	alice := e.employee(e.alice)
	tk, err := e.svc.Create(ctx, e.c(e.alice), alice, application.CreateInput{Title: "Printer jams", QueueID: &pub.ID, PatientImpact: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Assign(ctx, e.c(e.global.UserID), e.global, tk.ID, nil, &e.u1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE servicedesk.tickets SET affected_location_id = uuidv7() WHERE id = $1::uuid`, tk.ID); err != nil {
		t.Fatal(err)
	}
	// Visible Queue: the requester keeps seeing the assignee and the name.
	d, err := e.svc.Get(ctx, alice, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ticket.AssigneeID == nil {
		t.Fatalf("visible queue must keep the assignee: %+v", d.Ticket)
	}
	cur, _ := e.svc.Get(ctx, e.global, tk.ID)
	if _, err := e.svc.MoveToQueue(ctx, e.c(e.global.UserID), e.global, tk.ID, &cur.Ticket.Version, internal.ID, "misrouted"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, e.global, tk.ID); got.Ticket.AssigneeID == nil || got.Ticket.AffectedLocationID == nil || !got.Ticket.PatientImpact {
		t.Fatalf("staff view changed: %+v", got.Ticket)
	}
	hidden := func(where string, it application.Ticket) {
		if it.AssigneeID != nil || it.AffectedLocationID != nil || it.PatientImpact {
			t.Errorf("%s leaks staff-only fields: %+v", where, it)
		}
	}
	d, err = e.svc.Get(ctx, alice, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	hidden("detail", d.Ticket)
	if _, ok := d.Names[e.u1]; ok {
		t.Errorf("the assignee name was resolved: %+v", d.Names)
	}
	mine, err := e.svc.List(ctx, alice, false, application.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range mine.Items {
		if it.ID == tk.ID {
			hidden("list", it)
		}
	}
	page, err := e.svc.Query(ctx, alice, query.Request{Limit: 100}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range page.Items {
		if it.ID == tk.ID {
			hidden("query", it)
		}
	}
}

type countingDir struct {
	dir
	calls *int
}

func (d countingDir) SearchUserIDs(context.Context, string, int) ([]string, error) {
	*d.calls++
	return nil, nil
}

// The person lookup runs only for a request that passed the length check and holds a rate-limit token, and takes
// exactly one token with the query itself.
func TestPersonSearchIsGuardedBeforeTheLookup(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	secret := []byte("0123456789abcdef0123456789abcdef")
	calls := 0
	active := map[string]bool{e.global.UserID: true}
	mk := func(burst int) *application.Service {
		eng := query.NewEngine(secret).WithLimiter(query.NewLimiter(0.0001, burst))
		return application.NewService(repository.New(e.pool), countingDir{dir: dir{active: active}, calls: &calls}, device{}).WithMemberships(e.mem).WithQueryEngine(eng)
	}
	run := func(svc *application.Service, search string) error {
		_, err := svc.QueryScoped(ctx, e.global, query.Request{Limit: 5, Search: search}, application.ScopeAll, nil, "")
		return err
	}
	if err := run(mk(5), strings.Repeat("a", query.MaxSearchLength+1)); err == nil || calls != 0 {
		t.Errorf("over-long search: err=%v lookups=%d", err, calls)
	}
	svc := mk(1)
	if err := run(svc, "brandt"); err != nil || calls != 1 {
		t.Fatalf("first request: err=%v lookups=%d (one token must serve lookup and query)", err, calls)
	}
	if err := run(svc, "brandt"); err == nil || calls != 1 {
		t.Errorf("rate-limited request: err=%v lookups=%d", err, calls)
	}
}
