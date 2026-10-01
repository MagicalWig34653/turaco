package audit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type seeded struct {
	reader *Reader
	pfx    string
	ids    []string // oldest first
	actor  string
	base   time.Time
}

// seed inserts five events with action prefix "zt<rand>." and returns them.
func seed(t *testing.T) *seeded {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	s := &seeded{reader: NewReader(pool), pfx: "zt" + hex.EncodeToString(b), base: time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)}
	ctx := context.Background()
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`, s.pfx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&s.actor); err != nil {
		t.Fatal(err)
	}
	actions := []string{".a.one", ".a.two", ".b.one", ".b.two", ".b.three"}
	for i, a := range actions {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var id string
			if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
				return err
			}
			var actor *string
			if i%2 == 0 {
				actor = &s.actor
			}
			e := Entry{ID: id, OccurredAt: s.base.Add(time.Duration(i) * time.Minute), ActorID: actor, Action: s.pfx + a, TargetType: "thing",
				TargetID: s.pfx + "-t" + string(rune('0'+i%2)), CorrelationID: s.pfx + "-c", Metadata: []byte(`{"k":"v"}`)}
			if i == 0 {
				e.Before, e.After = []byte(`{"x":1}`), []byte(`{"x":2}`)
			}
			s.ids = append(s.ids, id)
			return Insert(ctx, tx, e)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestListFilters(t *testing.T) {
	s := seed(t)
	ctx := context.Background()
	list := func(f Filter, p Page) Result {
		t.Helper()
		r, err := s.reader.List(ctx, f, p)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		return r
	}
	all := list(Filter{CorrelationID: s.pfx + "-c"}, Page{})
	if len(all.Items) != 5 {
		t.Fatalf("items = %d", len(all.Items))
	}
	for i, e := range all.Items { // newest first
		if e.ID != s.ids[4-i] {
			t.Fatalf("order: item %d = %s, want %s", i, e.ID, s.ids[4-i])
		}
	}
	last := all.Items[4]
	if string(last.Before) != `{"x": 1}` || string(last.After) != `{"x": 2}` || string(last.Metadata) != `{"k": "v"}` || last.ActorID == nil || *last.ActorID != s.actor {
		t.Fatalf("event = %+v %s", last, last.Before)
	}
	if all.Items[0].Before != nil {
		t.Fatalf("expected NULL before, got %s", all.Items[0].Before)
	}

	if n := len(list(Filter{Action: s.pfx + ".a.one"}, Page{}).Items); n != 1 {
		t.Fatalf("exact action = %d", n)
	}
	if n := len(list(Filter{ActionPrefix: s.pfx + ".b."}, Page{}).Items); n != 3 {
		t.Fatalf("prefix = %d", n)
	}
	if n := len(list(Filter{ActionPrefix: s.pfx + "_b."}, Page{}).Items); n != 0 { // _ is literal
		t.Fatalf("escaped prefix = %d", n)
	}
	if n := len(list(Filter{ActionPrefix: s.pfx + "%"}, Page{}).Items); n != 0 { // % is literal
		t.Fatalf("percent prefix = %d", n)
	}
	if n := len(list(Filter{ActorID: s.actor, CorrelationID: s.pfx + "-c"}, Page{}).Items); n != 3 {
		t.Fatalf("actor = %d", n)
	}
	if n := len(list(Filter{TargetType: "thing", TargetID: s.pfx + "-t1"}, Page{}).Items); n != 2 {
		t.Fatalf("target = %d", n)
	}
	from, to := s.base.Add(time.Minute), s.base.Add(3*time.Minute)
	if n := len(list(Filter{CorrelationID: s.pfx + "-c", From: &from, To: &to}, Page{}).Items); n != 2 { // [1,3)
		t.Fatalf("time range = %d", n)
	}

	p1 := list(Filter{CorrelationID: s.pfx + "-c"}, Page{Limit: 2})
	if len(p1.Items) != 2 || p1.NextCursor != encodeCursor(p1.Items[1]) || p1.NextCursor == s.ids[3] {
		t.Fatalf("page1 = %d %q", len(p1.Items), p1.NextCursor)
	}
	p3 := list(Filter{CorrelationID: s.pfx + "-c"}, Page{Limit: 2, Cursor: list(Filter{CorrelationID: s.pfx + "-c"}, Page{Limit: 2, Cursor: p1.NextCursor}).NextCursor})
	if len(p3.Items) != 1 || p3.NextCursor != "" || p3.Items[0].ID != s.ids[0] {
		t.Fatalf("page3 = %+v", p3)
	}
}

func TestListRejectsInvalidInput(t *testing.T) {
	s := seed(t)
	ctx := context.Background()
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	t0 := time.Now()
	for _, f := range []Filter{{ActorID: "nope"}, {Action: string(long)}, {TargetType: string(make([]byte, 101))}, {Action: "a\x00"}, {From: &t0, To: &t0}, {TargetID: "x"}, {ActionPrefix: "a\xff"}} {
		if _, err := s.reader.List(ctx, f, Page{}); !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("filter %+v err = %v", f, err)
		}
	}
	for _, c := range []string{"x", "!!!", base64.RawURLEncoding.EncodeToString([]byte("1.nope")), base64.RawURLEncoding.EncodeToString([]byte("abc.00000000-0000-7000-8000-000000000001")),
		base64.RawURLEncoding.EncodeToString([]byte("99999999999999999999999.00000000-0000-7000-8000-000000000001")), "00000000-0000-7000-8000-000000000001"} {
		if _, err := s.reader.List(ctx, Filter{}, Page{Cursor: c}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q err = %v", c, err)
		}
	}
	if _, err := s.reader.List(ctx, Filter{}, Page{Limit: -1}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("limit err = %v", err)
	}
	r, err := s.reader.List(ctx, Filter{}, Page{Limit: 100000})
	if err != nil || len(r.Items) > MaxLimit {
		t.Fatalf("clamp: %d, %v", len(r.Items), err)
	}
}

// Events that share occurred_at are ordered and paged by id, and an event
// whose id order disagrees with its occurred_at order is still paged correctly.
func TestListKeysetOrdersByOccurredAtThenID(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	pfx := "zt" + hex.EncodeToString(b)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`, pfx)
	})
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	// Insertion (id) order: same, same, older-by-time-but-newer-id, same.
	times := []time.Time{base, base, base.Add(-time.Minute), base}
	var ids []string
	for _, at := range times {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var id string
			if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			return Insert(ctx, tx, Entry{ID: id, OccurredAt: at, Action: pfx + ".x", TargetType: "thing", TargetID: "t", CorrelationID: pfx + "-c", Metadata: []byte(`{}`)})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{ids[3], ids[1], ids[0], ids[2]} // time DESC, then id DESC
	r := NewReader(pool)
	var got []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		res, err := r.List(ctx, Filter{CorrelationID: pfx + "-c"}, Page{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range res.Items {
			got = append(got, e.ID)
		}
		if cursor = res.NextCursor; cursor == "" {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPrefixUpperBound(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"abc", "abd", true},
		{"organization.", "organization/", true},
		{"a\u00e9", "a\u00ea", true},
		{"a\U0010FFFF", "b", true},
		{"\uD7FF", "\uE000", true},
		{"\U0010FFFF", "", false},
		{"", "", false},
	} {
		got, ok := prefixUpperBound(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("prefixUpperBound(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
