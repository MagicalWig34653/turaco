package workitems_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/workitems"
)

// fake is an in-memory source over a list sorted in the shared order; the cursor is the index after the last entry.
type fake struct {
	key, module string
	entries     []workitems.Entry
	fail        bool
	badCursor   bool
	calls       int
	count       workitems.Count
	countErr    error
	seen        authorization.Principal
}

func (f *fake) Key() string    { return f.key }
func (f *fake) Module() string { return f.module }
func (f *fake) Items(_ context.Context, p authorization.Principal, cursor string, limit int) ([]workitems.Entry, error) {
	f.calls++
	f.seen = p
	if f.fail {
		return nil, errors.New("boom")
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || f.badCursor {
			return nil, workitems.ErrInvalidCursor
		}
		start = n
	}
	end := min(start+limit, len(f.entries))
	if start > end {
		return nil, nil
	}
	out := slices.Clone(f.entries[start:end])
	for i := range out {
		out[i].Cursor = strconv.Itoa(start + i + 1)
	}
	return out, nil
}
func (f *fake) Count(context.Context, authorization.Principal) (workitems.Count, error) {
	return f.count, f.countErr
}

type gate map[string]bool

func (g gate) Enabled(_ context.Context, key string) (bool, error) { return g[key], nil }

func entry(id string, due *time.Time, rank int) workitems.Entry {
	return workitems.Entry{Item: workitems.Item{ID: id, Title: id, DueAt: due}, Rank: rank}
}

func sortedShared(es []workitems.Entry) []workitems.Entry {
	slices.SortFunc(es, func(a, b workitems.Entry) int {
		switch {
		case a.Item.DueAt == nil && b.Item.DueAt != nil:
			return 1
		case a.Item.DueAt != nil && b.Item.DueAt == nil:
			return -1
		case a.Item.DueAt != nil && !a.Item.DueAt.Equal(*b.Item.DueAt):
			return a.Item.DueAt.Compare(*b.Item.DueAt)
		case a.Rank != b.Rank:
			return a.Rank - b.Rank
		}
		return compare(a.Item.ID, b.Item.ID)
	})
	return es
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

var me = authorization.Principal{UserID: "00000000-0000-7000-8000-000000000001", Permissions: map[string]struct{}{}}

func ids(items []workitems.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestMergePagesWithoutDuplicatesOrSkips(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	mk := func(prefix string, n int) []workitems.Entry {
		var es []workitems.Entry
		for i := 0; i < n; i++ {
			var due *time.Time
			if rng.Intn(3) > 0 {
				d := base.Add(time.Duration(rng.Intn(6)) * 24 * time.Hour)
				due = &d
			}
			es = append(es, entry(fmt.Sprintf("%s-%03d", prefix, i), due, rng.Intn(4)))
		}
		return sortedShared(es)
	}
	a := &fake{key: "a", module: "m1", entries: mk("a", 37)}
	b := &fake{key: "b", module: "m2", entries: mk("b", 23)}
	c := &fake{key: "c", module: "m1", entries: mk("c", 0)}
	svc, err := workitems.New(gate{"m1": true, "m2": true}, a, b, c)
	if err != nil {
		t.Fatal(err)
	}
	var want []workitems.Entry
	want = append(want, a.entries...)
	want = append(want, b.entries...)
	sortedShared(want)
	for _, limit := range []int{1, 5, 7, 50, 100} {
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 100 {
				t.Fatalf("limit %d: no end", limit)
			}
			page, err := svc.Items(context.Background(), me, nil, cursor, limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > limit {
				t.Fatalf("page of %d > limit %d", len(page.Items), limit)
			}
			got = append(got, ids(page.Items)...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if !slices.Equal(got, ids(itemsOf(want))) {
			t.Fatalf("limit %d: merged order differs\n got %v\nwant %v", limit, got, ids(itemsOf(want)))
		}
	}
	// Sources are filterable and every item names its source.
	page, _ := svc.Items(context.Background(), me, []string{"b"}, "", 100)
	if len(page.Items) != 23 || page.Items[0].Source != "b" {
		t.Errorf("source filter: %d items, first %+v", len(page.Items), page.Items[0])
	}
}

func itemsOf(es []workitems.Entry) []workitems.Item {
	out := make([]workitems.Item, len(es))
	for i, e := range es {
		out[i] = e.Item
	}
	return out
}

func TestFailingSourceIsReportedAndKeepsItsPosition(t *testing.T) {
	a := &fake{key: "a", module: "m", entries: []workitems.Entry{entry("a1", nil, 0), entry("a2", nil, 1), entry("a3", nil, 2)}}
	b := &fake{key: "b", module: "m", entries: []workitems.Entry{entry("b1", nil, 0), entry("b2", nil, 1)}, fail: true}
	svc, _ := workitems.New(gate{"m": true}, a, b)
	page, err := svc.Items(context.Background(), me, nil, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(page.Unavailable, []string{"b"}) || !slices.Equal(ids(page.Items), []string{"a1", "a2"}) || page.NextCursor == "" {
		t.Fatalf("page = %+v", page)
	}
	b.fail = false
	next, err := svc.Items(context.Background(), me, nil, page.NextCursor, 10)
	if err != nil || len(next.Unavailable) != 0 {
		t.Fatalf("next = %+v %v", next, err)
	}
	// The source that failed is retried from its old position: nothing it owns was skipped (its items arrive on a
	// later page, in the shared order among themselves).
	if !slices.Equal(ids(next.Items), []string{"b1", "b2", "a3"}) {
		t.Errorf("next = %v", ids(next.Items))
	}
	// Counts: a failing source is unavailable, never zero.
	a.count = workitems.Count{N: 3}
	b.countErr = errors.New("down")
	res, err := svc.Counts(context.Background(), me, nil)
	if err != nil || len(res) != 2 || res[0].Status != workitems.CountOK || res[0].N != 3 || res[1].Status != workitems.CountUnavailable || res[1].N != 0 {
		t.Errorf("counts = %+v %v", res, err)
	}
	a.count = workitems.Count{N: workitems.CountCap + 50}
	res, _ = svc.Counts(context.Background(), me, []string{"a"})
	if res[0].N != workitems.CountCap || !res[0].Capped {
		t.Errorf("capped count = %+v", res[0])
	}
}

func TestDisabledModulesAreNotExecuted(t *testing.T) {
	a := &fake{key: "a", module: "on", entries: []workitems.Entry{entry("a1", nil, 0)}}
	b := &fake{key: "b", module: "off", entries: []workitems.Entry{entry("b1", nil, 0)}}
	svc, _ := workitems.New(gate{"on": true}, a, b)
	page, err := svc.Items(context.Background(), me, nil, "", 10)
	if err != nil || !slices.Equal(ids(page.Items), []string{"a1"}) || b.calls != 0 {
		t.Fatalf("page = %+v %v, calls %d", page, err, b.calls)
	}
	res, _ := svc.Counts(context.Background(), me, nil)
	if len(res) != 1 || res[0].Source != "a" {
		t.Errorf("counts = %+v", res)
	}
	// Asking for the disabled source by name yields nothing and does not run it.
	page, _ = svc.Items(context.Background(), me, []string{"b"}, "", 10)
	if len(page.Items) != 0 || b.calls != 0 {
		t.Errorf("disabled source ran: %+v", page)
	}
}

func TestRequestValidation(t *testing.T) {
	a := &fake{key: "a", module: "m", entries: []workitems.Entry{entry("a1", nil, 0)}}
	svc, _ := workitems.New(gate{"m": true}, a)
	var inv *workitems.InvalidError
	if _, err := svc.Items(context.Background(), me, []string{"nope"}, "", 10); !errors.As(err, &inv) {
		t.Errorf("unknown source: %v", err)
	}
	if _, err := svc.Items(context.Background(), me, nil, "", workitems.MaxLimit+1); !errors.As(err, &inv) {
		t.Errorf("limit: %v", err)
	}
	if _, err := svc.Items(context.Background(), authorization.Principal{}, nil, "", 10); !errors.As(err, &inv) {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := svc.Counts(context.Background(), authorization.Principal{}, nil); !errors.As(err, &inv) {
		t.Errorf("anonymous counts: %v", err)
	}
	// Cursors: garbage, oversized, unknown source and a source's own rejection all end as invalid cursors.
	for name, c := range map[string]string{"garbage": "!!!", "json": "e30", "foreign source": "eyJ6IjoiMSJ9", "oversized": string(make([]byte, 5000))} {
		if _, err := svc.Items(context.Background(), me, nil, c, 10); c != "e30" && !errors.Is(err, workitems.ErrInvalidCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	a.badCursor = true
	bad := encodeFor(t, svc, map[string]string{"a": "7"})
	if _, err := svc.Items(context.Background(), me, nil, bad, 10); !errors.Is(err, workitems.ErrInvalidCursor) {
		t.Errorf("source-rejected cursor: %v", err)
	}
	if _, err := workitems.New(gate{}, a, a); err == nil {
		t.Error("duplicate source keys accepted")
	}
	// The source receives the caller as given: the merge cannot widen it.
	a.badCursor = false
	_, _ = svc.Items(context.Background(), me, nil, "", 1)
	if a.seen.UserID != me.UserID {
		t.Errorf("source saw %+v", a.seen)
	}
}

func encodeFor(t *testing.T, _ *workitems.Service, pos map[string]string) string {
	t.Helper()
	// Same encoding the service issues (base64url of the JSON map).
	return workitems.EncodeCursorForTest(pos)
}
