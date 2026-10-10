package notifications_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type fakePoster struct {
	mu    sync.Mutex
	cards []notifications.ChannelCard
	keys  []string
	errs  []error
}

func (p *fakePoster) PostToChannel(_ context.Context, key string, card notifications.ChannelCard) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return err
	}
	p.cards = append(p.cards, card)
	p.keys = append(p.keys, key)
	return nil
}

type channelFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	category string
	keyA     string
	keyB     string
	actor    string
	on       bool
	reg      *notifications.Registry
	svc      *notifications.Service
	poster   *fakePoster
	sender   *notifications.ChannelSender
	linkID   string
}

func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	pool := dbtest.Pool(t)
	rnd := make([]byte, 5)
	_, _ = rand.Read(rnd)
	pfx := hex.EncodeToString(rnd)
	f := &channelFixture{t: t, pool: pool, category: "t" + pfx + ".declared", keyA: "ka-" + pfx, keyB: "kb-" + pfx, on: true, poster: &fakePoster{}}
	text := func(h, a string) notifications.ChannelText { return notifications.ChannelText{Headline: h, Action: a} }
	var err error
	f.reg, err = notifications.NewRegistry(notifications.Category{
		Name: f.category, Owner: "servicedesk", LinkType: "major_incident", LinkPath: "/incidents/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Incident: %s", Intro: "News:", Action: "Open"}, "de": {Subject: "Störung: %s", Intro: "Neu:", Action: "Öffnen"}},
		Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{
			notifications.PostKindDeclared: {"en": text("A major incident was declared", "Open in Turaco"), "de": text("Eine Großstörung wurde ausgerufen", "In Turaco öffnen")},
		}},
	}, notifications.Category{
		Name: "t" + pfx + ".personal", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Task: %s", Intro: "Task:", Action: "Open"}, "de": {Subject: "Aufgabe: %s", Intro: "Aufgabe:", Action: "Öffnen"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = notifications.NewService(pool, f.reg).WithChannelPosts(notifications.ChannelOptions{
		Enabled:         func(context.Context) (bool, error) { return f.on, nil },
		DestinationKeys: func() []string { return []string{f.keyA, f.keyB} },
		Mode:            "fake",
	})
	f.sender = notifications.NewChannelSender(pool, f.poster, f.reg, "https://turaco.example.org/", "en")
	for _, dst := range []*string{&f.actor, &f.linkID} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' IN (
			SELECT id::text FROM platform.notification_deliveries WHERE channel = 'teams_channel' AND destination_key = ANY($2))`,
			notifications.ChannelPostJobType, []string{f.keyA, f.keyB})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notification_deliveries WHERE channel = 'teams_channel' AND destination_key = ANY($1)`, []string{f.keyA, f.keyB})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.notification_channel_routes WHERE category = $1`, f.category)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE action LIKE 'notifications.teams.route_%' AND (after_data->>'category' = $1 OR before_data->>'category' = $1)`, f.category)
	})
	return f
}

func (f *channelFixture) route(key string) notifications.Route {
	f.t.Helper()
	r, err := f.svc.CreateRoute(context.Background(), f.actor, true, f.category, key, "test")
	if err != nil {
		f.t.Fatalf("create route: %v", err)
	}
	return r
}

func (f *channelFixture) post(dedupe string) (int, error) {
	f.t.Helper()
	var n int
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		var err error
		n, err = f.svc.PostToChannels(context.Background(), tx, notifications.ChannelPost{
			Category: f.category, Kind: notifications.PostKindDeclared, Reference: "MI-000012",
			LinkType: "major_incident", LinkID: f.linkID, DedupeKey: dedupe})
		return err
	})
	return n, err
}

type chanDelivery struct {
	ID, Key, Status string
	Attempts        int
	LastError       *string
}

func (f *channelFixture) deliveries() []chanDelivery {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT id::text, destination_key, status, attempts, last_error FROM platform.notification_deliveries
		WHERE channel = 'teams_channel' AND destination_key = ANY($1) ORDER BY destination_key`, []string{f.keyA, f.keyB})
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

func (f *channelFixture) handle(d chanDelivery, attempt int) error {
	return f.sender.Handle(context.Background(), jobs.Job{Type: notifications.ChannelPostJobType,
		Payload: []byte(`{"deliveryId":"` + d.ID + `"}`), Attempts: attempt, MaxAttempts: 8})
}

func TestRegistryValidatesBroadcastCategories(t *testing.T) {
	both := map[string]notifications.EmailText{
		"en": {Subject: "S %s", Intro: "I", Action: "A"}, "de": {Subject: "S %s", Intro: "I", Action: "A"}}
	ok := notifications.ChannelText{Headline: "h", Action: "a"}
	good := map[string]map[string]notifications.ChannelText{"declared": {"en": ok, "de": ok}}
	bad := map[string]notifications.Category{
		"no link":        {Name: "a.b", Owner: "x", Email: both, Broadcast: &notifications.Broadcast{Texts: good}},
		"no texts":       {Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t/{id}", Broadcast: &notifications.Broadcast{}},
		"unknown kind":   {Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t/{id}", Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{"resolved": {"en": ok, "de": ok}}}},
		"missing german": {Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t/{id}", Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{"declared": {"en": ok}}}},
		"empty headline": {Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t/{id}", Broadcast: &notifications.Broadcast{Texts: map[string]map[string]notifications.ChannelText{"declared": {"en": {Action: "a"}, "de": ok}}}},
	}
	for name, c := range bad {
		if _, err := notifications.NewRegistry(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	r, err := notifications.NewRegistry(notifications.Category{Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t/{id}", Broadcast: &notifications.Broadcast{Texts: good}},
		notifications.Category{Name: "a.c", Owner: "x", Email: both})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Broadcastable(), ","); got != "a.b" {
		t.Errorf("broadcastable = %q", got)
	}
}

func TestPostToChannelsCreatesOneDeliveryPerRouteAndIsIdempotent(t *testing.T) {
	f := newChannelFixture(t)
	if n, err := f.post("ev0"); err != nil || n != 0 {
		t.Fatalf("without a route: n=%d err=%v, want nothing", n, err)
	}
	f.route(f.keyA)
	f.route(f.keyB)
	n, err := f.post("ev1")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2", n, err)
	}
	if n, _ := f.post("ev1"); n != 0 {
		t.Errorf("a repeated event must create nothing, got %d", n)
	}
	if n, _ := f.post("ev2"); n != 2 {
		t.Errorf("a new event posts again, got %d", n)
	}
	ds := f.deliveries()
	if len(ds) != 4 || ds[0].Status != "pending" {
		t.Fatalf("deliveries = %+v", ds)
	}
	var jobsN int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.jobs WHERE job_type = $1 AND payload->>'deliveryId' = ANY($2)`,
		notifications.ChannelPostJobType, []string{ds[0].ID, ds[1].ID, ds[2].ID, ds[3].ID}).Scan(&jobsN); err != nil || jobsN != 4 {
		t.Errorf("jobs = %d err=%v, want one per delivery", jobsN, err)
	}
	var email int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.notification_deliveries WHERE channel = 'email' AND destination_key IS NOT NULL`).Scan(&email); err != nil || email != 0 {
		t.Errorf("email deliveries with a destination: %d %v", email, err)
	}
}

func TestPostToChannelsModuleOffCreatesNothing(t *testing.T) {
	f := newChannelFixture(t)
	f.route(f.keyA)
	f.on = false
	if n, err := f.post("ev1"); err != nil || n != 0 {
		t.Fatalf("module off: n=%d err=%v", n, err)
	}
	if len(f.deliveries()) != 0 {
		t.Error("no delivery may exist")
	}
	// A service without the channel options never posts either.
	plain := notifications.NewService(f.pool, f.reg)
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		n, err := plain.PostToChannels(context.Background(), tx, notifications.ChannelPost{Category: f.category, Kind: "declared", Reference: "MI-1", LinkType: "major_incident", LinkID: f.linkID, DedupeKey: "x"})
		if n != 0 {
			t.Errorf("plain service created %d", n)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostToChannelsRefusesFreeTextAndPersonalCategories(t *testing.T) {
	f := newChannelFixture(t)
	f.route(f.keyA)
	base := notifications.ChannelPost{Category: f.category, Kind: "declared", Reference: "MI-000012", LinkType: "major_incident", LinkID: f.linkID, DedupeKey: "k"}
	bad := map[string]func(*notifications.ChannelPost){
		"title in the reference": func(p *notifications.ChannelPost) { p.Reference = "MI-000012 · Dialysis outage" },
		"empty reference":        func(p *notifications.ChannelPost) { p.Reference = "" },
		"personal category": func(p *notifications.ChannelPost) {
			p.Category = strings.Replace(f.category, "declared", "personal", 1)
		},
		"unknown category":        func(p *notifications.ChannelPost) { p.Category = "nope.nothing" },
		"kind the category lacks": func(p *notifications.ChannelPost) { p.Kind = "scheduled" },
		"no dedupe key":           func(p *notifications.ChannelPost) { p.DedupeKey = "" },
		"link id is not a uuid":   func(p *notifications.ChannelPost) { p.LinkID = "1" },
	}
	for name, mutate := range bad {
		p := base
		mutate(&p)
		err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
			_, err := f.svc.PostToChannels(context.Background(), tx, p)
			return err
		})
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(f.deliveries()) != 0 {
		t.Error("refused posts must create nothing")
	}
}

func TestRouteManagementIsAuthorizedValidatedAndAudited(t *testing.T) {
	f := newChannelFixture(t)
	ctx := context.Background()
	if _, err := f.svc.CreateRoute(ctx, f.actor, false, f.category, f.keyA, "c"); !errors.Is(err, notifications.ErrForbidden) {
		t.Errorf("without permission: %v", err)
	}
	if _, err := f.svc.CreateRoute(ctx, f.actor, true, strings.Replace(f.category, "declared", "personal", 1), f.keyA, "c"); !errors.Is(err, notifications.ErrNotBroadcastable) {
		t.Errorf("personal category: %v", err)
	}
	if _, err := f.svc.CreateRoute(ctx, f.actor, true, f.category, "missing", "c"); !errors.Is(err, notifications.ErrUnknownDestination) {
		t.Errorf("unknown destination: %v", err)
	}
	r := f.route(f.keyA)
	if _, err := f.svc.CreateRoute(ctx, f.actor, true, f.category, f.keyA, "c"); !errors.Is(err, notifications.ErrRouteExists) {
		t.Errorf("duplicate: %v", err)
	}
	routes, err := f.svc.ListRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range routes {
		found = found || x.ID == r.ID && x.Category == f.category && x.DestinationKey == f.keyA
	}
	if !found {
		t.Errorf("route not listed: %+v", routes)
	}
	var created int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'notifications.teams.route_created' AND target_id = $1 AND actor_id = $2::uuid`, r.ID, f.actor).Scan(&created); err != nil || created != 1 {
		t.Errorf("route_created audit rows = %d err=%v", created, err)
	}
	if err := f.svc.DeleteRoute(ctx, f.actor, false, r.ID, "c"); !errors.Is(err, notifications.ErrForbidden) {
		t.Errorf("delete without permission: %v", err)
	}
	if err := f.svc.DeleteRoute(ctx, f.actor, true, r.ID, "c"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteRoute(ctx, f.actor, true, r.ID, "c"); !errors.Is(err, notifications.ErrRouteNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := f.svc.DeleteRoute(ctx, f.actor, true, "x", "c"); !errors.Is(err, notifications.ErrRouteNotFound) {
		t.Errorf("bad id: %v", err)
	}
	var deleted int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'notifications.teams.route_deleted' AND target_id = $1`, r.ID).Scan(&deleted); err != nil || deleted != 1 {
		t.Errorf("route_deleted audit rows = %d err=%v", deleted, err)
	}
	info := f.svc.ChannelInfo()
	if info.Mode != "fake" || len(info.Destinations) != 2 || len(info.Categories) != 1 {
		t.Errorf("info = %+v", info)
	}
}

func (f *channelFixture) onePending() chanDelivery {
	f.t.Helper()
	f.route(f.keyA)
	if n, err := f.post("ev"); err != nil || n != 1 {
		f.t.Fatalf("post: n=%d err=%v", n, err)
	}
	ds := f.deliveries()
	if len(ds) != 1 {
		f.t.Fatalf("deliveries = %+v", ds)
	}
	return ds[0]
}

func TestChannelSenderPostsReferenceOnlyCardOnce(t *testing.T) {
	f := newChannelFixture(t)
	d := f.onePending()
	if err := f.handle(d, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.poster.cards) != 1 || f.poster.keys[0] != f.keyA {
		t.Fatalf("cards = %+v", f.poster.cards)
	}
	c := f.poster.cards[0]
	if c.Reference != "MI-000012" || c.Headline != "A major incident was declared" || c.Action != "Open in Turaco" ||
		c.LinkURL != "https://turaco.example.org/incidents/"+f.linkID || c.Locale != "en" || c.Kind != "declared" {
		t.Errorf("card = %+v", c)
	}
	got := f.deliveries()[0]
	if got.Status != "delivered" || got.LastError != nil {
		t.Errorf("delivery = %+v", got)
	}
	if err := f.handle(d, 2); err != nil || len(f.poster.cards) != 1 {
		t.Errorf("a delivered post must not be sent again: err=%v cards=%d", err, len(f.poster.cards))
	}
}

func TestChannelSenderRetriesTransientFailuresHonouringRetryAfter(t *testing.T) {
	f := newChannelFixture(t)
	d := f.onePending()
	f.poster.errs = []error{&notifications.TransientChannelError{Code: "rate_limited", RetryAfter: 5 * time.Minute}}
	err := f.handle(d, 1)
	if err == nil || jobs.IsPermanent(err) {
		t.Fatalf("a rate limited post must be retried: %v", err)
	}
	var te *notifications.TransientChannelError
	if !errors.As(err, &te) {
		t.Errorf("err = %v", err)
	}
	got := f.deliveries()[0]
	if got.Status != "pending" || got.LastError == nil || *got.LastError != "rate_limited" {
		t.Errorf("delivery = %+v, want pending with the machine code", got)
	}
	if err := f.handle(d, 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := f.deliveries()[0]; got.Status != "delivered" {
		t.Errorf("after retry = %+v", got)
	}
}

func TestChannelSenderFailsPermanentlyAndWhenAttemptsAreExhausted(t *testing.T) {
	f := newChannelFixture(t)
	d := f.onePending()
	f.poster.errs = []error{&notifications.PermanentChannelError{Code: "http_404"}}
	if err := f.handle(d, 1); !jobs.IsPermanent(err) {
		t.Fatalf("permanent failure: %v", err)
	}
	if got := f.deliveries()[0]; got.Status != "failed" || got.LastError == nil || *got.LastError != "http_404" {
		t.Errorf("delivery = %+v", got)
	}
	if err := f.handle(d, 2); err != nil || len(f.poster.cards) != 0 {
		t.Errorf("a failed delivery is left alone: %v", err)
	}

	f2 := newChannelFixture(t)
	d2 := f2.onePending()
	if _, err := f2.pool.Exec(context.Background(), `UPDATE platform.notification_deliveries SET attempts = 7 WHERE id = $1::uuid`, d2.ID); err != nil {
		t.Fatal(err)
	}
	f2.poster.errs = []error{&notifications.TransientChannelError{Code: "http_503"}}
	if err := f2.handle(d2, 8); !jobs.IsPermanent(err) {
		t.Errorf("last attempt: %v", err)
	}
	if got := f2.deliveries()[0]; got.Status != "failed" {
		t.Errorf("exhausted delivery = %+v", got)
	}
}

func TestChannelSenderCancelsRemovedRoutesAndStalePosts(t *testing.T) {
	f := newChannelFixture(t)
	r := f.route(f.keyA)
	if _, err := f.post("ev"); err != nil {
		t.Fatal(err)
	}
	d := f.deliveries()[0]
	if err := f.svc.DeleteRoute(context.Background(), f.actor, true, r.ID, "c"); err != nil {
		t.Fatal(err)
	}
	if err := f.handle(d, 1); err != nil || len(f.poster.cards) != 0 {
		t.Fatalf("removed route: err=%v cards=%d", err, len(f.poster.cards))
	}
	if got := f.deliveries()[0]; got.Status != "cancelled" || got.LastError == nil || *got.LastError != "route_removed" {
		t.Errorf("delivery = %+v", got)
	}

	f2 := newChannelFixture(t)
	d2 := f2.onePending()
	if _, err := f2.pool.Exec(context.Background(), `UPDATE platform.notification_deliveries SET created_at = now() - interval '7 hours' WHERE id = $1::uuid`, d2.ID); err != nil {
		t.Fatal(err)
	}
	if err := f2.handle(d2, 1); err != nil || len(f2.poster.cards) != 0 {
		t.Fatalf("stale: err=%v cards=%d", err, len(f2.poster.cards))
	}
	if got := f2.deliveries()[0]; got.Status != "cancelled" || got.LastError == nil || *got.LastError != "stale" {
		t.Errorf("delivery = %+v", got)
	}
}

func TestChannelSenderRejectsInvalidJobPayload(t *testing.T) {
	f := newChannelFixture(t)
	err := f.sender.Handle(context.Background(), jobs.Job{Payload: []byte(`{"deliveryId":"x"}`)})
	if !jobs.IsPermanent(err) {
		t.Errorf("invalid payload: %v", err)
	}
}
