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
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *notifications.Service
	a, b string
	pfx  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	rnd := make([]byte, 6)
	_, _ = rand.Read(rnd)
	f := &fixture{t: t, pool: pool, svc: notifications.NewService(pool), pfx: hex.EncodeToString(rnd)}
	// Notifications reference no foreign table, so random ids isolate the tests.
	for _, dst := range []*string{&f.a, &f.b} {
		if err := pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM platform.notifications WHERE recipient_user_id = ANY($1::uuid[])`, []string{f.a, f.b})
	})
	return f
}

func (f *fixture) create(in notifications.Intent) bool {
	f.t.Helper()
	var created bool
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		var err error
		created, err = f.svc.Create(context.Background(), tx, in)
		return err
	})
	if err != nil {
		f.t.Fatalf("create: %v", err)
	}
	return created
}

func (f *fixture) intent(user, key string) notifications.Intent {
	return notifications.Intent{
		RecipientUserID: user, Category: "task.assigned", DedupeKey: key,
		Params: map[string]any{"title": "Replace toner"}, LinkType: "task", LinkID: f.b,
	}
}

func TestCreateIsIdempotentPerRecipient(t *testing.T) {
	f := newFixture(t)
	if !f.create(f.intent(f.a, "ev1")) {
		t.Fatal("first create must insert")
	}
	if f.create(f.intent(f.a, "ev1")) {
		t.Error("repeated dedupe key for the same recipient must be a no-op")
	}
	if !f.create(f.intent(f.b, "ev1")) {
		t.Error("the same dedupe key for another recipient is a different notification")
	}
	if n, _ := f.svc.UnreadCount(context.Background(), f.a); n != 1 {
		t.Errorf("unread = %d, want 1", n)
	}
}

func TestCreateValidatesAndRollsBackWithTheTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bad := map[string]notifications.Intent{
		"bad recipient":    {RecipientUserID: "x", Category: "task.assigned", DedupeKey: "k"},
		"bad category":     {RecipientUserID: f.a, Category: "nope", DedupeKey: "k"},
		"no dedupe":        {RecipientUserID: f.a, Category: "task.assigned"},
		"link type only":   {RecipientUserID: f.a, Category: "task.assigned", DedupeKey: "k", LinkType: "task"},
		"bad link id":      {RecipientUserID: f.a, Category: "task.assigned", DedupeKey: "k", LinkType: "task", LinkID: "x"},
		"oversized params": {RecipientUserID: f.a, Category: "task.assigned", DedupeKey: "k", Params: map[string]any{"t": strings.Repeat("x", 5000)}},
	}
	for name, in := range bad {
		err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
			_, err := f.svc.Create(ctx, tx, in)
			return err
		})
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	boom := errors.New("boom")
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := f.svc.Create(ctx, tx, f.intent(f.a, "rolled-back")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.a); n != 0 {
		t.Errorf("rolled back transaction left %d notifications", n)
	}
}

func TestListReadAndOwnership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, k := range []string{"1", "2", "3"} {
		f.create(f.intent(f.a, k))
	}
	f.create(f.intent(f.b, "other"))

	p1, err := f.svc.List(ctx, f.a, false, notifications.Page{Limit: 2})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor == "" {
		t.Fatalf("page 1 = %+v %v", p1, err)
	}
	p2, err := f.svc.List(ctx, f.a, false, notifications.Page{Limit: 2, Cursor: p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != "" {
		t.Fatalf("page 2 = %+v %v", p2, err)
	}
	if p1.Items[0].ID <= p1.Items[1].ID || p1.Items[1].ID <= p2.Items[0].ID {
		t.Error("notifications must be newest first")
	}
	if got := p1.Items[0]; got.Category != "task.assigned" || got.Params["title"] != "Replace toner" || got.LinkType == nil || *got.LinkType != "task" || got.ReadAt != nil {
		t.Errorf("notification = %+v", got)
	}
	if _, err := f.svc.List(ctx, f.a, false, notifications.Page{Cursor: "garbage"}); !errors.Is(err, notifications.ErrInvalidCursor) {
		t.Errorf("bad cursor: %v", err)
	}

	// Ownership: another user can neither read nor mark my notification.
	if err := f.svc.MarkRead(ctx, f.b, p1.Items[0].ID); !errors.Is(err, notifications.ErrNotFound) {
		t.Errorf("foreign mark read: %v, want ErrNotFound", err)
	}
	if err := f.svc.MarkRead(ctx, f.a, p1.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MarkRead(ctx, f.a, p1.Items[0].ID); err != nil {
		t.Errorf("marking read twice must be idempotent: %v", err)
	}
	if err := f.svc.MarkRead(ctx, f.a, "garbage"); !errors.Is(err, notifications.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.a); n != 2 {
		t.Errorf("unread = %d, want 2", n)
	}
	unread, _ := f.svc.List(ctx, f.a, true, notifications.Page{})
	if len(unread.Items) != 2 {
		t.Errorf("unread list = %d, want 2", len(unread.Items))
	}
	if n, err := f.svc.MarkAllRead(ctx, f.a); err != nil || n != 2 {
		t.Errorf("mark all = %d %v, want 2", n, err)
	}
	if n, _ := f.svc.UnreadCount(ctx, f.b); n != 1 {
		t.Errorf("mark all must not touch other users: unread(b) = %d", n)
	}
}

func TestUnreadCountIsCapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `
		INSERT INTO platform.notifications(recipient_user_id, category, dedupe_key)
		SELECT $1::uuid, 'task.assigned', 'k' || g FROM generate_series(1, $2) g`, f.a, notifications.MaxUnreadCount+20)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.svc.UnreadCount(ctx, f.a); err != nil || n != notifications.MaxUnreadCount {
		t.Errorf("unread = %d %v, want the cap %d", n, err, notifications.MaxUnreadCount)
	}
}

func TestConcurrentCreateWithSameKeyInsertsOnce(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f.create(f.intent(f.a, "same")) {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Errorf("created %d times, want 1", created)
	}
}

func TestSuppressWithinSkipsRepeatsForTheSameLink(t *testing.T) {
	f := newFixture(t)
	in := f.intent(f.a, "e1")
	in.SuppressWithin = time.Hour
	if !f.create(in) {
		t.Fatal("the first notification must be created")
	}
	in2 := f.intent(f.a, "e2") // a different event, same recipient, category and link
	in2.SuppressWithin = time.Hour
	if f.create(in2) {
		t.Error("a repeat for the same link within the window must be suppressed")
	}
	other := f.intent(f.a, "e3")
	other.SuppressWithin = time.Hour
	other.LinkID = f.a // another link
	if !f.create(other) {
		t.Error("another link is not a repeat")
	}
	unsuppressed := f.intent(f.a, "e4")
	if !f.create(unsuppressed) {
		t.Error("without SuppressWithin every new event creates a notification")
	}
	// Outside the window the notification is created again.
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.notifications SET created_at = now() - interval '2 hours' WHERE recipient_user_id = $1::uuid`, f.a); err != nil {
		t.Fatal(err)
	}
	in5 := f.intent(f.a, "e5")
	in5.SuppressWithin = time.Hour
	if !f.create(in5) {
		t.Error("a notification older than the window must not suppress a new one")
	}
}
