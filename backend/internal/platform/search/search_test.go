package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

type fake struct {
	typ, module string
	hits        []Hit
	err         error
	delay       time.Duration
	panics      bool
	gotLimit    int
}

func (f *fake) Type() string   { return f.typ }
func (f *fake) Module() string { return f.module }
func (f *fake) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]Hit, error) {
	f.gotLimit = limit
	if f.panics {
		panic("boom")
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.hits, f.err
}

func TestSearchGroupsAndReportsUnavailable(t *testing.T) {
	ok := &fake{typ: "ticket", module: "servicedesk", hits: []Hit{{ID: "1", Title: "Etikettendrucker"}}}
	slow := &fake{typ: "asset", module: "assets", delay: time.Second}
	failing := &fake{typ: "change", module: "changes", err: errors.New("db down")}
	broken := &fake{typ: "request", module: "requests", panics: true}
	denied := &fake{typ: "user", module: "organization", err: ErrNotAllowed}
	s := New()
	s.Timeout = 50 * time.Millisecond
	s.Register(ok, slow, failing, broken, denied)

	res, err := s.Search(context.Background(), authorization.Principal{UserID: "u"}, Request{Query: " Etikett "})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Type != "ticket" || res.Items[0].ID != "1" {
		t.Fatalf("items = %+v", res.Items)
	}
	if got := res.Unavailable; len(got) != 3 || got[0] != "asset" || got[1] != "change" || got[2] != "request" {
		t.Errorf("unavailable = %v; a denied source must be absent, not unavailable", got)
	}
}

func TestSearchSelectionLimitsAndSwitches(t *testing.T) {
	a := &fake{typ: "ticket", module: "servicedesk"}
	b := &fake{typ: "asset", module: "assets"}
	for i := 0; i < 12; i++ {
		a.hits = append(a.hits, Hit{ID: string(rune('a' + i))})
		b.hits = append(b.hits, Hit{ID: string(rune('A' + i))})
	}
	s := New()
	s.Enabled = func(_ context.Context, m string) bool { return m != "assets" }
	s.Register(a, b)
	p := authorization.Principal{UserID: "u"}

	res, err := s.Search(context.Background(), p, Request{Query: "ab", Limit: 50})
	if err != nil || a.gotLimit != MaxPerType || len(res.Items) != MaxPerType || b.gotLimit != 0 {
		t.Fatalf("limit %d items %d err %v; the disabled module must not be asked (limit %d)", a.gotLimit, len(res.Items), err, b.gotLimit)
	}
	if res, _ = s.Search(context.Background(), p, Request{Query: "ab"}); len(res.Items) != DefaultPerType {
		t.Errorf("default limit gave %d items", len(res.Items))
	}
	s.Enabled = nil
	if res, _ = s.Search(context.Background(), p, Request{Query: "ab", Types: []string{"asset"}, Limit: 3}); len(res.Items) != 3 || res.Items[0].Type != "asset" {
		t.Errorf("type selection: %+v", res.Items)
	}
	if _, err = s.Search(context.Background(), p, Request{Query: "ab", Types: []string{"nope"}}); !errors.Is(err, ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
	for _, q := range []string{"", "a", " b ", string(make([]rune, MaxQueryLength+1))} {
		if _, err = s.Search(context.Background(), p, Request{Query: q}); !errors.Is(err, ErrQueryLength) {
			t.Errorf("query %q: %v", q, err)
		}
	}
}

func TestSearchTotalCap(t *testing.T) {
	s := New()
	for _, typ := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		f := &fake{typ: typ, module: typ}
		for i := 0; i < 5; i++ {
			f.hits = append(f.hits, Hit{ID: typ + string(rune('0'+i))})
		}
		s.Register(f)
	}
	res, _ := s.Search(context.Background(), authorization.Principal{UserID: "u"}, Request{Query: "ab"})
	if len(res.Items) != MaxTotal {
		t.Errorf("items = %d, want %d", len(res.Items), MaxTotal)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		q      string
		fields []string
		want   bool
	}{
		{"etikett", []string{"CHG-1", "Etikettendrucker tauschen"}, true},
		{"ETIKETT drucker", []string{"Etikettendrucker tauschen"}, true},
		{"etikett scanner", []string{"Etikettendrucker tauschen"}, false},
		{"chg-1", []string{"CHG-1", "x"}, true},
		{"  ", []string{"x"}, false},
	}
	for _, c := range cases {
		if got := Match(c.q, c.fields...); got != c.want {
			t.Errorf("Match(%q, %v) = %v", c.q, c.fields, got)
		}
	}
}
