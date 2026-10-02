package notifications_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []smtp.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg smtp.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *fakeMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type contact struct {
	email, name string
	ok          bool
	err         error
}

type fakeContacts map[string]contact

func (f fakeContacts) EmailContact(_ context.Context, id string) (string, string, bool, error) {
	c := f[id]
	return c.email, c.name, c.ok, c.err
}

type emailFixture struct {
	*fixture
	svc    *notifications.Service
	mailer *fakeMailer
	send   *notifications.EmailSender
	cont   fakeContacts
}

func newEmailFixture(t *testing.T) *emailFixture {
	t.Helper()
	f := newFixture(t)
	e := &emailFixture{fixture: f, svc: notifications.NewService(f.pool).WithEmail(), mailer: &fakeMailer{}}
	e.cont = fakeContacts{f.a: {email: "ada@example.org", name: "Ada Lovelace", ok: true}}
	e.send = notifications.NewEmailSender(f.pool, e.mailer, e.cont, "https://turaco.example.org", "en")
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.notification_preferences WHERE user_id = ANY($1::uuid[])`, []string{f.a, f.b})
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' IN (
			SELECT d.id::text FROM platform.notification_deliveries d JOIN platform.notifications n ON n.id = d.notification_id
			WHERE n.recipient_user_id = ANY($2::uuid[]))`, notifications.EmailJobType, []string{f.a, f.b})
	})
	return e
}

func (e *emailFixture) createViaEmailService(user, key string) {
	e.t.Helper()
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := e.svc.Create(context.Background(), tx, e.intent(user, key))
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

type deliveryRow struct {
	ID       string
	Status   string
	Attempts int
	LastErr  *string
}

func (e *emailFixture) delivery(user string) (deliveryRow, bool) {
	e.t.Helper()
	var d deliveryRow
	err := e.pool.QueryRow(context.Background(), `
		SELECT d.id::text, d.status, d.attempts, d.last_error FROM platform.notification_deliveries d
		JOIN platform.notifications n ON n.id = d.notification_id
		WHERE n.recipient_user_id = $1::uuid`, user).Scan(&d.ID, &d.Status, &d.Attempts, &d.LastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryRow{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return d, true
}

func (e *emailFixture) job(d deliveryRow) jobs.Job {
	return jobs.Job{Type: notifications.EmailJobType, Payload: []byte(`{"deliveryId":"` + d.ID + `"}`), Attempts: 1, MaxAttempts: 5}
}

func (e *emailFixture) jobCount(d deliveryRow) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' = $2`,
		notifications.EmailJobType, d.ID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestEmailIsScheduledOnlyWhenEnabled(t *testing.T) {
	e := newEmailFixture(t)
	e.create(e.intent(e.b, "plain")) // fixture service has no email channel
	if _, ok := e.delivery(e.b); ok {
		t.Error("a service without the email channel must not create deliveries")
	}
	e.createViaEmailService(e.a, "k1")
	d, ok := e.delivery(e.a)
	if !ok || d.Status != "pending" || e.jobCount(d) != 1 {
		t.Fatalf("delivery = %+v ok=%v jobs=%d, want a pending delivery with one job", d, ok, e.jobCount(d))
	}
	// Re-creating the same notification (event redelivery) must not add a second delivery or job.
	e.createViaEmailService(e.a, "k1")
	if e.jobCount(d) != 1 {
		t.Errorf("redelivery created %d jobs", e.jobCount(d))
	}
}

func TestOptOutSuppressesTheDelivery(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	if err := e.svc.SetEmailPreference(ctx, e.a, "task.assigned", false); err != nil {
		t.Fatal(err)
	}
	e.createViaEmailService(e.a, "k1")
	if _, ok := e.delivery(e.a); ok {
		t.Error("an opted-out recipient must get no email delivery")
	}
	if n, _ := e.svc.UnreadCount(ctx, e.a); n != 1 {
		t.Errorf("the in-app notification must still exist: unread = %d", n)
	}
}

func TestPreferences(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	prefs, err := e.svc.Preferences(ctx, e.a)
	if err != nil || len(prefs) != len(notifications.Categories) {
		t.Fatalf("prefs = %+v %v", prefs, err)
	}
	for _, p := range prefs {
		if !p.Enabled || p.Channel != "email" {
			t.Errorf("default must be enabled: %+v", p)
		}
	}
	if err := e.svc.SetEmailPreference(ctx, e.a, "task.completed", false); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetEmailPreference(ctx, e.a, "task.completed", false); err != nil {
		t.Errorf("setting twice must be idempotent: %v", err)
	}
	prefs, _ = e.svc.Preferences(ctx, e.a)
	for _, p := range prefs {
		if p.Enabled != (p.Category != "task.completed") {
			t.Errorf("pref = %+v", p)
		}
	}
	if other, _ := e.svc.Preferences(ctx, e.b); !other[0].Enabled || !other[1].Enabled {
		t.Error("preferences must be per user")
	}
	if err := e.svc.SetEmailPreference(ctx, e.a, "nope", false); !errors.Is(err, notifications.ErrUnknownCategory) {
		t.Errorf("unknown category: %v", err)
	}
	if err := e.svc.SetEmailPreference(ctx, "garbage", "task.assigned", true); err == nil {
		t.Error("a malformed user id must be rejected")
	}
}

func TestSendDeliversOnceAndIsIdempotent(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)

	if err := e.send.Handle(ctx, e.job(d)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got, _ := e.delivery(e.a); got.Status != "delivered" || got.Attempts != 1 {
		t.Errorf("delivery = %+v", got)
	}
	if e.mailer.count() != 1 {
		t.Fatalf("sent %d mails, want 1", e.mailer.count())
	}
	m := e.mailer.sent[0]
	if m.To != `"Ada Lovelace" <ada@example.org>` || m.Subject != "Task assigned: Replace toner" || m.Text == "" || m.HTML == "" {
		t.Errorf("message = %+v", m)
	}
	// The same job again (duplicate delivery, crash recovery) sends nothing.
	if err := e.send.Handle(ctx, e.job(d)); err != nil || e.mailer.count() != 1 {
		t.Errorf("second run: err=%v sent=%d, want a no-op", err, e.mailer.count())
	}
}

func TestSendCancelsWhenRecipientCannotReceiveOrOptedOutLater(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()

	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	e.cont[e.a] = contact{email: "ada@example.org", name: "Ada", ok: false} // deactivated since scheduling
	if err := e.send.Handle(ctx, e.job(d)); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.delivery(e.a); got.Status != "cancelled" || e.mailer.count() != 0 {
		t.Errorf("inactive recipient: %+v sent=%d", got, e.mailer.count())
	}

	e.cont[e.b] = contact{email: "bob@example.org", name: "Bob", ok: true}
	e.createViaEmailService(e.b, "k2")
	d2, _ := e.delivery(e.b)
	if err := e.svc.SetEmailPreference(ctx, e.b, "task.assigned", false); err != nil { // opted out after scheduling
		t.Fatal(err)
	}
	if err := e.send.Handle(ctx, e.job(d2)); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.delivery(e.b); got.Status != "cancelled" || e.mailer.count() != 0 {
		t.Errorf("late opt-out: %+v sent=%d", got, e.mailer.count())
	}
}

func TestTransientFailureIsRetriedThenSucceeds(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)

	e.mailer.err = errors.New("connection reset")
	err := e.send.Handle(ctx, e.job(d))
	if err == nil || jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a retryable error", err)
	}
	if got, _ := e.delivery(e.a); got.Status != "pending" || got.Attempts != 1 || got.LastErr == nil {
		t.Errorf("after failure: %+v", got)
	}
	e.mailer.err = nil
	if err := e.send.Handle(ctx, e.job(d)); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.delivery(e.a); got.Status != "delivered" || got.Attempts != 2 {
		t.Errorf("after retry: %+v", got)
	}
}

func TestPermanentFailureFailsTheDelivery(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	e.mailer.err = &smtp.PermanentError{Err: errors.New("550 no such mailbox")}
	err := e.send.Handle(ctx, e.job(d))
	if !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a permanent job error", err)
	}
	if got, _ := e.delivery(e.a); got.Status != "failed" || got.LastErr == nil {
		t.Errorf("delivery = %+v", got)
	}
	// A failed delivery stays failed.
	e.mailer.err = nil
	if err := e.send.Handle(ctx, e.job(d)); err != nil || e.mailer.count() != 0 {
		t.Errorf("rerun of a failed delivery: err=%v sent=%d", err, e.mailer.count())
	}
}

func TestExhaustedAttemptsFailTheDelivery(t *testing.T) {
	e := newEmailFixture(t)
	ctx := context.Background()
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	e.mailer.err = errors.New("timeout")
	var last error
	for i := 0; i < 5; i++ {
		last = e.send.Handle(ctx, e.job(d))
	}
	if !jobs.IsPermanent(last) {
		t.Errorf("fifth failure = %v, want permanent", last)
	}
	if got, _ := e.delivery(e.a); got.Status != "failed" || got.Attempts != 5 {
		t.Errorf("delivery = %+v", got)
	}
}

func TestInvalidJobPayloadIsPermanent(t *testing.T) {
	e := newEmailFixture(t)
	for _, payload := range []string{`{}`, `{"deliveryId":"x"}`, `not json`} {
		err := e.send.Handle(context.Background(), jobs.Job{Payload: []byte(payload)})
		if !jobs.IsPermanent(err) {
			t.Errorf("payload %q: err = %v, want permanent", payload, err)
		}
	}
	// An unknown (deleted) delivery is a no-op, not an error.
	if err := e.send.Handle(context.Background(), jobs.Job{Payload: []byte(`{"deliveryId":"00000000-0000-7000-8000-000000000000"}`)}); err != nil {
		t.Errorf("unknown delivery: %v", err)
	}
}

func TestResolverErrorIsRetryable(t *testing.T) {
	e := newEmailFixture(t)
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	e.cont[e.a] = contact{err: errors.New("db down")}
	err := e.send.Handle(context.Background(), e.job(d))
	if err == nil || jobs.IsPermanent(err) {
		t.Errorf("err = %v, want retryable", err)
	}
	if got, _ := e.delivery(e.a); got.Status != "pending" {
		t.Errorf("delivery = %+v", got)
	}
}

func TestShutdownDuringSendDoesNotConsumeAnAttempt(t *testing.T) {
	e := newEmailFixture(t)
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	ctx, cancel := context.WithCancel(context.Background())
	e.mailer.err = context.Canceled
	e.send = notifications.NewEmailSender(e.pool, cancelingMailer{cancel: cancel}, e.cont, "https://turaco.example.org", "en")
	err := e.send.Handle(ctx, e.job(d))
	if err == nil || jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want a non-permanent error", err)
	}
	got, _ := e.delivery(e.a)
	if got.Status != "pending" || got.Attempts != 0 {
		t.Errorf("delivery = %+v, want pending with the attempt refunded", got)
	}
}

type cancelingMailer struct{ cancel context.CancelFunc }

func (m cancelingMailer) Send(ctx context.Context, _ smtp.Message) error {
	m.cancel() // the worker is shutting down while the relay dialogue runs
	return ctx.Err()
}

func TestCorruptParamsFailTheDeliveryInsteadOfLeavingItSending(t *testing.T) {
	e := newEmailFixture(t)
	e.createViaEmailService(e.a, "k1")
	d, _ := e.delivery(e.a)
	// params is jsonb, so a value that is valid JSON but not an object is the corruption we can store.
	if _, err := e.pool.Exec(context.Background(), `UPDATE platform.notifications SET params = '[1,2]'::jsonb
		WHERE id = (SELECT notification_id FROM platform.notification_deliveries WHERE id = $1::uuid)`, d.ID); err != nil {
		t.Fatal(err)
	}
	err := e.send.Handle(context.Background(), e.job(d))
	if !jobs.IsPermanent(err) {
		t.Fatalf("err = %v, want permanent", err)
	}
	if got, _ := e.delivery(e.a); got.Status != "failed" {
		t.Errorf("delivery = %+v, want failed", got)
	}
}
