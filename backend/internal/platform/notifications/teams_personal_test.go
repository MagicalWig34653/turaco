package notifications_test

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

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type personalPoster struct {
	mu    sync.Mutex
	sent  []notifications.PersonalCard
	to    [][2]string
	errs  []error
	calls int
}

func (p *personalPoster) SendPersonal(_ context.Context, tenant, object string, card notifications.PersonalCard) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return err
	}
	p.sent = append(p.sent, card)
	p.to = append(p.to, [2]string{tenant, object})
	return nil
}

type identities struct {
	mu     sync.Mutex
	linked map[string][2]string
}

func (i *identities) TeamsIdentity(_ context.Context, userID string) (string, string, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	v, ok := i.linked[userID]
	return v[0], v[1], ok, nil
}

type personalFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	category string
	reg      *notifications.Registry
	svc      *notifications.Service
	poster   *personalPoster
	ids      *identities
	sender   *notifications.TeamsPersonalSender
	on       bool
	user     string
	linkID   string
}

func newPersonalFixture(t *testing.T) *personalFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	rnd := make([]byte, 5)
	_, _ = rand.Read(rnd)
	pfx := hex.EncodeToString(rnd)
	f := &personalFixture{t: t, pool: pool, category: "t" + pfx + ".assigned", on: true, poster: &personalPoster{}, ids: &identities{linked: map[string][2]string{}}}
	var err error
	f.reg, err = notifications.NewRegistry(notifications.Category{
		Name: f.category, Owner: "servicedesk", LinkType: "ticket", LinkPath: "/support/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Ticket: %s", Intro: "News:", Action: "Open"}, "de": {Subject: "Ticket: %s", Intro: "Neu:", Action: "Öffnen"}},
		TeamsCard: &notifications.TeamsCard{Kind: notifications.CardKindAssigned, Texts: map[string]notifications.ChannelText{
			"en": {Headline: "A ticket was assigned to you", Action: "Open in Turaco"}, "de": {Headline: "Dir wurde ein Ticket zugewiesen", Action: "In Turaco öffnen"}}},
	}, notifications.Category{
		Name: "t" + pfx + ".plain", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Task: %s", Intro: "Task:", Action: "Open"}, "de": {Subject: "Aufgabe: %s", Intro: "Aufgabe:", Action: "Öffnen"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = notifications.NewService(pool, f.reg).WithTeamsPersonal(notifications.TeamsPersonalOptions{
		Enabled: func(context.Context) (bool, error) { return f.on, nil }, Identity: f.ids})
	f.sender = notifications.NewTeamsPersonalSender(pool, f.poster, f.ids, f.reg, "https://turaco.example.org/", "en")
	for _, dst := range []*string{&f.user, &f.linkID} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	f.ids.linked[f.user] = [2]string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' IN (
			SELECT d.id::text FROM platform.notification_deliveries d JOIN platform.notifications n ON n.id = d.notification_id WHERE n.recipient_user_id = $2::uuid)`,
			notifications.TeamsPersonalJobType, f.user)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notification_deliveries WHERE notification_id IN (SELECT id FROM platform.notifications WHERE recipient_user_id = $1::uuid)`, f.user)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notifications WHERE recipient_user_id = $1::uuid`, f.user)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notification_preferences WHERE user_id = $1::uuid`, f.user)
	})
	return f
}

func (f *personalFixture) notify(category, title, dedupe string) {
	f.t.Helper()
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		_, err := f.svc.Create(context.Background(), tx, notifications.Intent{RecipientUserID: f.user, Category: category,
			Params: map[string]any{"title": title}, LinkType: "ticket", LinkID: f.linkID, DedupeKey: dedupe})
		return err
	})
	if err != nil {
		f.t.Fatalf("create notification: %v", err)
	}
}

func (f *personalFixture) deliveries() []chanDelivery {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT d.id::text, d.channel, d.status, d.attempts, d.last_error
		FROM platform.notification_deliveries d JOIN platform.notifications n ON n.id = d.notification_id
		WHERE n.recipient_user_id = $1::uuid AND d.channel = 'teams' ORDER BY d.created_at, d.id`, f.user)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []chanDelivery
	for rows.Next() {
		var d chanDelivery
		if err := rows.Scan(&d.ID, &d.Key, &d.Status, &d.Attempts, &d.LastError); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func (f *personalFixture) run(id string) error {
	return f.sender.Handle(context.Background(), jobs.Job{Payload: []byte(`{"deliveryId":"` + id + `"}`)})
}

func TestPersonalCardIsCreatedSentAndReferenceOnly(t *testing.T) {
	f := newPersonalFixture(t)
	f.notify(f.category, "TKT-000123 · Printer jams on ward 3 (patient Meyer)", "d1")
	ds := f.deliveries()
	if len(ds) != 1 || ds[0].Status != "pending" {
		t.Fatalf("deliveries = %+v", ds)
	}
	if err := f.run(ds[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := f.deliveries(); got[0].Status != "delivered" {
		t.Fatalf("status = %s", got[0].Status)
	}
	if len(f.poster.sent) != 1 {
		t.Fatalf("cards sent = %d", len(f.poster.sent))
	}
	c := f.poster.sent[0]
	if c.Reference != "TKT-000123" || c.Kind != notifications.CardKindAssigned || c.Headline != "A ticket was assigned to you" ||
		c.LinkURL != "https://turaco.example.org/support/"+f.linkID || c.Locale != "en" {
		t.Errorf("card = %+v", c)
	}
	if f.poster.to[0] != [2]string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"} {
		t.Errorf("sent to %v", f.poster.to[0])
	}
	// The card carries no free text: no title word appears anywhere in it.
	if s := c.Headline + c.Action + c.Reference + c.LinkURL; contains(s, "Printer") || contains(s, "Meyer") {
		t.Errorf("card leaks record content: %+v", c)
	}
	// A second run does nothing (idempotent).
	if err := f.run(ds[0].ID); err != nil || len(f.poster.sent) != 1 {
		t.Errorf("second run: %v, sent %d", err, len(f.poster.sent))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPersonalCardNotCreatedWhenNotApplicable(t *testing.T) {
	f := newPersonalFixture(t)
	// Not a card category.
	f.notify("t"+f.category[1:len(f.category)-len("assigned")]+"plain", "TSK-1", "p1")
	// Module off.
	f.on = false
	f.notify(f.category, "TKT-1 · x", "p2")
	f.on = true
	// No identity link.
	delete(f.ids.linked, f.user)
	f.notify(f.category, "TKT-2 · x", "p3")
	f.ids.linked[f.user] = [2]string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
	// Opted out.
	if err := f.svc.SetTeamsPreference(context.Background(), f.user, f.category, false); err != nil {
		t.Fatal(err)
	}
	f.notify(f.category, "TKT-3 · x", "p4")
	if ds := f.deliveries(); len(ds) != 0 {
		t.Fatalf("deliveries = %+v", ds)
	}
	// Opting out of a category without card is refused; the preferences list shows the card channel only for card categories.
	if err := f.svc.SetTeamsPreference(context.Background(), f.user, "t"+f.category[1:len(f.category)-len("assigned")]+"plain", false); !errors.Is(err, notifications.ErrUnknownCategory) {
		t.Errorf("teams preference for a plain category: %v", err)
	}
	prefs, err := f.svc.Preferences(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	teamsRows := 0
	for _, p := range prefs {
		if p.Channel == notifications.ChannelTeamsPersonal {
			teamsRows++
			if p.Category != f.category || p.Enabled {
				t.Errorf("teams preference = %+v", p)
			}
		}
	}
	if teamsRows != 1 {
		t.Errorf("teams preference rows = %d", teamsRows)
	}
}

func TestPersonalCardFailureClasses(t *testing.T) {
	f := newPersonalFixture(t)
	send := func(dedupe string, errs ...error) chanDelivery {
		f.t.Helper()
		f.poster.errs = errs
		f.notify(f.category, "TKT-9 · x", dedupe)
		ds := f.deliveries()
		d := ds[len(ds)-1]
		_ = f.run(d.ID)
		ds = f.deliveries()
		return ds[len(ds)-1]
	}
	if d := send("e1", notifications.ErrRecipientUnreachable); d.Status != "cancelled" || d.LastError == nil || *d.LastError != "recipient_unreachable" {
		t.Errorf("unreachable: %+v", d)
	}
	if d := send("e2", &notifications.PermanentChannelError{Code: "http_403"}); d.Status != "failed" {
		t.Errorf("permanent: %+v", d)
	}
	d := send("e3", &notifications.TransientChannelError{Code: "rate_limited", RetryAfter: 5 * time.Second})
	if d.Status != "pending" || d.Attempts != 1 {
		t.Errorf("transient: %+v", d)
	}
	// No identity at send time cancels the delivery.
	f.notify(f.category, "TKT-10 · x", "e4")
	ds := f.deliveries()
	delete(f.ids.linked, f.user)
	_ = f.run(ds[len(ds)-1].ID)
	ds = f.deliveries()
	if last := ds[len(ds)-1]; last.Status != "cancelled" || last.LastError == nil || *last.LastError != "no_teams_identity" {
		t.Errorf("identity lost: %+v", last)
	}
	// A title that does not start with a reference number sends no card.
	f.ids.linked[f.user] = [2]string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
	f.notify(f.category, "Printer jams · TKT-1", "e5")
	ds = f.deliveries()
	_ = f.run(ds[len(ds)-1].ID)
	ds = f.deliveries()
	if last := ds[len(ds)-1]; last.Status != "cancelled" || last.LastError == nil || *last.LastError != "no_reference" {
		t.Errorf("no reference: %+v", last)
	}
}

func TestTeamsCardRegistryValidation(t *testing.T) {
	base := notifications.Category{Name: "x.y", Owner: "o", LinkType: "ticket", LinkPath: "/t/{id}",
		Email: map[string]notifications.EmailText{"en": {Subject: "%s", Intro: "i", Action: "a"}, "de": {Subject: "%s", Intro: "i", Action: "a"}}}
	ok := map[string]notifications.ChannelText{"en": {Headline: "h", Action: "a"}, "de": {Headline: "h", Action: "a"}}
	for name, c := range map[string]notifications.TeamsCard{
		"unknown kind": {Kind: "x", Texts: ok},
		"missing de":   {Kind: notifications.CardKindAssigned, Texts: map[string]notifications.ChannelText{"en": {Headline: "h", Action: "a"}}},
		"empty action": {Kind: notifications.CardKindAssigned, Texts: map[string]notifications.ChannelText{"en": {Headline: "h", Action: ""}, "de": {Headline: "h", Action: "a"}}},
	} {
		cat := base
		card := c
		cat.TeamsCard = &card
		if _, err := notifications.NewRegistry(cat); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	noLink := base
	noLink.LinkType, noLink.LinkPath = "", ""
	noLink.TeamsCard = &notifications.TeamsCard{Kind: notifications.CardKindAssigned, Texts: ok}
	if _, err := notifications.NewRegistry(noLink); err == nil {
		t.Error("a Teams card without a link was accepted")
	}
}

func TestPersonalCardOlderThanADayIsCancelledAndChannelIsChecked(t *testing.T) {
	f := newPersonalFixture(t)
	f.notify(f.category, "TKT-5 · x", "old1")
	ds := f.deliveries()
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.notifications SET created_at = now() - interval '25 hours' WHERE recipient_user_id = $1::uuid`, f.user); err != nil {
		t.Fatal(err)
	}
	if err := f.run(ds[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := f.deliveries()[0]; got.Status != "cancelled" || got.LastError == nil || *got.LastError != "no_longer_current" || len(f.poster.sent) != 0 {
		t.Errorf("stale card: %+v sent %d", got, len(f.poster.sent))
	}
	// The email job type cannot claim a Teams delivery (and the reverse).
	f.notify(f.category, "TKT-6 · x", "old2")
	ds = f.deliveries()
	mailer := &noMail{}
	es := notifications.NewEmailSender(f.pool, mailer, noContacts{}, f.reg, "https://turaco.example.org", "en")
	_ = es.Handle(context.Background(), jobs.Job{Payload: []byte(`{"deliveryId":"` + ds[len(ds)-1].ID + `"}`)})
	if mailer.sent != 0 {
		t.Errorf("the email sender sent a Teams delivery")
	}
	if got := f.deliveries(); got[len(got)-1].Status != "pending" {
		t.Errorf("Teams delivery status after an email job: %s", got[len(got)-1].Status)
	}
}

type noMail struct{ sent int }

func (m *noMail) Send(context.Context, notifications.EmailMessage) error { m.sent++; return nil }

type noContacts struct{}

func (noContacts) EmailContact(context.Context, string) (string, string, bool, error) {
	return "a@example.test", "A", true, nil
}
