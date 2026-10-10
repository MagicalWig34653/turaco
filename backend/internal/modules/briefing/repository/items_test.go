package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *Repository
	corr string
	ids  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, repo: New(pool), corr: "briefing-test-" + hex.EncodeToString(b)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, f.corr)
		for _, id := range f.ids {
			_, _ = pool.Exec(ctx, `DELETE FROM briefing.items WHERE id = $1`, id)
		}
	})
	return f
}

func (f *fixture) caller() application.Caller {
	return application.Caller{Actor: audit.SystemActor("test"), CorrelationID: f.corr}
}

func (f *fixture) insert(title string) application.Item {
	f.t.Helper()
	it, err := f.repo.Insert(context.Background(), f.caller(), application.NewItem{Title: title, Body: "secret body text", Severity: "warning"})
	if err != nil {
		f.t.Fatal(err)
	}
	f.ids = append(f.ids, it.ID)
	return it
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func publish(it application.Item) func(application.Item) (application.Change, error) {
	return func(cur application.Item) (application.Change, error) {
		n := cur
		now := time.Now().UTC().Truncate(time.Microsecond)
		n.Status, n.PublishedAt = application.StatusPublished, &now
		return application.Change{Next: n, Action: "briefing.item.published", Events: []application.Event{
			{Type: "BriefingItemPublished", Payload: map[string]any{"itemId": cur.ID, "severity": cur.Severity}},
		}}, nil
	}
}

func TestInsertGetAndAuditWithoutContent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it := f.insert("Planned VPN maintenance")
	got, err := f.repo.Get(ctx, it.ID)
	if err != nil || got.Title != "Planned VPN maintenance" || got.Status != "draft" || got.Version != 1 || got.Severity != "warning" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := f.repo.Get(ctx, "00000000-0000-7000-8000-000000000000"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := f.repo.Get(ctx, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action = 'briefing.item.created'`, it.ID); n != 1 {
		t.Errorf("created audit events = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND (before_data::text LIKE '%Planned VPN%' OR after_data::text LIKE '%Planned VPN%' OR after_data::text LIKE '%secret body%')`, it.ID); n != 0 {
		t.Errorf("audit leaks %d rows with content", n)
	}
}

func TestPublishIsAtomicWithAuditAndOutbox(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it := f.insert("t")
	out, err := f.repo.Change(ctx, f.caller(), it.ID, publish(it))
	if err != nil || out.Status != "published" || out.Version != 2 || out.PublishedAt == nil {
		t.Fatalf("publish = %+v %v", out, err)
	}
	if n := f.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'BriefingItemPublished' AND payload->>'itemId' = $2`, f.corr, it.ID); n != 1 {
		t.Errorf("outbox events = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action = 'briefing.item.published'`, it.ID); n != 1 {
		t.Errorf("published audit events = %d", n)
	}
	// An unregistered event type fails the whole change.
	_, err = f.repo.Change(ctx, f.caller(), it.ID, func(cur application.Item) (application.Change, error) {
		n := cur
		n.Title = "changed"
		return application.Change{Next: n, Action: "briefing.item.updated", Events: []application.Event{{Type: "NoSuchEvent"}}}, nil
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if got, _ := f.repo.Get(ctx, it.ID); got.Title != "t" || got.Version != 2 {
		t.Errorf("failed change leaked: %+v", got)
	}
}

func TestConcurrentPublishPublishesOnce(t *testing.T) {
	f := newFixture(t)
	it := f.insert("t")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repo.Change(context.Background(), f.caller(), it.ID, func(cur application.Item) (application.Change, error) {
				if cur.Status != "draft" {
					return application.Change{}, &application.InvalidTransitionError{Operation: "publish", From: cur.Status}
				}
				return publish(cur)(cur)
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		var tr *application.InvalidTransitionError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &tr):
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || f.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'BriefingItemPublished'`, f.corr) != 1 {
		t.Errorf("published %d times, want exactly once", ok)
	}
}

func TestDeleteIsAudited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it := f.insert("t")
	boom := errors.New("refused")
	if err := f.repo.Delete(ctx, f.caller(), it.ID, func(application.Item) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("decide error: %v", err)
	}
	if _, err := f.repo.Get(ctx, it.ID); err != nil {
		t.Fatalf("a refused delete must keep the item: %v", err)
	}
	if err := f.repo.Delete(ctx, f.caller(), it.ID, func(application.Item) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Get(ctx, it.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action = 'briefing.item.deleted'`, it.ID); n != 1 {
		t.Errorf("deleted audit events = %d", n)
	}
	if err := f.repo.Delete(ctx, f.caller(), it.ID, func(application.Item) error { return nil }); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestListOrderFiltersAndPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	draft := f.insert("a-draft")
	first := f.insert("b-first-published")
	second := f.insert("c-second-published")
	expired := f.insert("d-expired")
	for _, it := range []application.Item{second, first} { // publication order: second, then first
		if _, err := f.repo.Change(ctx, f.caller(), it.ID, publish(it)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := f.repo.Change(ctx, f.caller(), expired.ID, func(cur application.Item) (application.Change, error) {
		n := cur
		now := time.Now().UTC().Add(-2 * time.Hour)
		past := time.Now().UTC().Add(-time.Hour)
		n.Status, n.PublishedAt, n.ValidUntil = application.StatusPublished, &now, &past
		return application.Change{Next: n, Action: "briefing.item.published"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{draft.ID: true, first.ID: true, second.ID: true, expired.ID: true}

	collect := func(q application.ListQuery) []string {
		var out []string
		cursor := ""
		for page := 0; page < 1000; page++ {
			q.Page = application.Page{Limit: 2, Cursor: cursor}
			res, err := f.repo.List(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) > 2 {
				t.Fatalf("page of %d items exceeds the limit", len(res.Items))
			}
			for _, it := range res.Items {
				if known[it.ID] {
					out = append(out, it.ID)
				}
			}
			if res.NextCursor == "" {
				return out
			}
			cursor = res.NextCursor
		}
		t.Fatal("pagination did not terminate")
		return nil
	}

	viewer := collect(application.ListQuery{PublishedOnly: true})
	if len(viewer) != 2 || viewer[0] != first.ID || viewer[1] != second.ID {
		t.Errorf("viewer list = %v, want the two unexpired published items, newest publication first", viewer)
	}
	manager := collect(application.ListQuery{})
	if len(manager) != 4 {
		t.Errorf("manager list has %d of 4 items", len(manager))
	}
	if drafts := collect(application.ListQuery{Status: "draft"}); len(drafts) != 1 || drafts[0] != draft.ID {
		t.Errorf("draft filter = %v", drafts)
	}
	if _, err := f.repo.List(ctx, application.ListQuery{Page: application.Page{Cursor: "%%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("invalid cursor: %v", err)
	}
}

func TestDatabaseInvariants(t *testing.T) {
	f := newFixture(t)
	it := f.insert("t")
	for name, sql := range map[string]string{
		"published without time": `UPDATE briefing.items SET status = 'published' WHERE id = $1`,
		"time while draft":       `UPDATE briefing.items SET published_at = now() WHERE id = $1`,
		"withdrawn without time": `UPDATE briefing.items SET status = 'withdrawn', published_at = now() WHERE id = $1`,
		"unknown severity":       `UPDATE briefing.items SET severity = 'fatal' WHERE id = $1`,
		"blank title":            `UPDATE briefing.items SET title = '  ' WHERE id = $1`,
		"zero version":           `UPDATE briefing.items SET version = 0 WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(context.Background(), sql, it.ID); err == nil {
			t.Errorf("%s: database accepted inconsistent state", name)
		}
	}
}

func TestAudienceIsStoredAndFiltered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it, err := f.repo.Insert(ctx, f.caller(), application.NewItem{Title: "For all", Body: "b", Severity: "info", Audience: "all"})
	if err != nil {
		t.Fatal(err)
	}
	f.ids = append(f.ids, it.ID)
	internal := f.insert("IT only")
	if it.Audience != "all" || internal.Audience != "it" {
		t.Fatalf("audiences = %q %q", it.Audience, internal.Audience)
	}
	res, err := f.repo.List(ctx, application.ListQuery{Audience: "all", Page: application.Page{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	var seenAll, seenIT bool
	for _, x := range res.Items {
		if x.Audience != "all" {
			t.Errorf("audience filter returned %q", x.Audience)
		}
		seenAll = seenAll || x.ID == it.ID
		seenIT = seenIT || x.ID == internal.ID
	}
	if !seenAll || seenIT {
		t.Errorf("filter: all=%v it=%v", seenAll, seenIT)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE briefing.items SET audience = 'x' WHERE id = $1`, it.ID); err == nil {
		t.Error("the database must reject unknown audiences")
	}
}
