package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type memStore struct {
	items   map[string]Item
	seq     int
	changes []Change
}

func newMemStore() *memStore { return &memStore{items: map[string]Item{}} }

func (m *memStore) Insert(_ context.Context, _ Caller, n NewItem) (Item, error) {
	m.seq++
	it := Item{
		ID: "00000000-0000-7000-8000-00000000000" + string(rune('0'+m.seq)), Title: n.Title, Body: n.Body, Severity: n.Severity,
		Status: StatusDraft, ValidUntil: n.ValidUntil, AuthorUserID: n.Author, Version: 1,
	}
	m.items[it.ID] = it
	return it, nil
}

func (m *memStore) Get(_ context.Context, id string) (Item, error) {
	it, ok := m.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	return it, nil
}

func (m *memStore) Change(_ context.Context, _ Caller, id string, decide func(Item) (Change, error)) (Item, error) {
	cur, ok := m.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	ch, err := decide(cur)
	if err != nil {
		return Item{}, err
	}
	if ch.NoChange {
		return cur, nil
	}
	ch.Next.Version = cur.Version + 1
	m.items[id] = ch.Next
	m.changes = append(m.changes, ch)
	return ch.Next, nil
}

func (m *memStore) Delete(_ context.Context, _ Caller, id string, decide func(Item) error) error {
	cur, ok := m.items[id]
	if !ok {
		return ErrNotFound
	}
	if err := decide(cur); err != nil {
		return err
	}
	delete(m.items, id)
	return nil
}

func (m *memStore) List(_ context.Context, q ListQuery) (Result, error) {
	var out []Item
	for _, it := range m.items {
		if q.PublishedOnly && it.Status != StatusPublished {
			continue
		}
		if q.Status != "" && it.Status != q.Status {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return Result{Items: out}, nil
}

var (
	pManager = Principal{UserID: "00000000-0000-7000-8000-0000000000a1", Manage: true}
	pViewer  = Principal{UserID: "00000000-0000-7000-8000-0000000000b1", View: true}
	pNobody  = Principal{UserID: "00000000-0000-7000-8000-0000000000c1"}
	noon     = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
)

func caller(p Principal) Caller {
	return Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "corr"}
}

func newSvc() (*Service, *memStore) {
	st := newMemStore()
	return NewService(st, func() time.Time { return noon }), st
}

func mustCreate(t *testing.T, s *Service, in CreateInput) Item {
	t.Helper()
	it, err := s.Create(context.Background(), caller(pManager), pManager, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return it
}

func TestOnlyManagersChangeItems(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	it := mustCreate(t, s, CreateInput{Title: "Maintenance"})
	for _, p := range []Principal{pViewer, pNobody} {
		c := caller(p)
		if _, err := s.Create(ctx, c, p, CreateInput{Title: "x"}); !errors.Is(err, ErrForbidden) {
			t.Errorf("create %+v: %v", p, err)
		}
		if _, err := s.Update(ctx, c, p, it.ID, nil, UpdateInput{Title: ptr("x")}); !errors.Is(err, ErrForbidden) {
			t.Errorf("update: %v", err)
		}
		if _, err := s.Publish(ctx, c, p, it.ID, nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("publish: %v", err)
		}
		if _, err := s.Withdraw(ctx, c, p, it.ID, nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("withdraw: %v", err)
		}
		if err := s.Delete(ctx, c, p, it.ID, nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("delete: %v", err)
		}
	}
	if len(st.items) != 1 || st.items[it.ID].Status != StatusDraft {
		t.Error("an item was changed without permission")
	}
}

func ptr(s string) *string { return &s }

func TestCreateValidates(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	for name, in := range map[string]CreateInput{
		"empty title":    {Title: " "},
		"long title":     {Title: strings.Repeat("x", 201)},
		"override title": {Title: "Notice ‮fdp.exe"},
		"bad severity":   {Title: "x", Severity: "fatal"},
		"long body":      {Title: "x", Body: strings.Repeat("x", 10001)},
		"control body":   {Title: "x", Body: "a\x00b"},
		"override body":  {Title: "x", Body: "a⁦b"},
	} {
		var inv *InvalidInputError
		if _, err := s.Create(ctx, caller(pManager), pManager, in); !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want InvalidInputError", name, err)
		}
	}
	if len(st.items) != 0 {
		t.Errorf("%d items stored by rejected requests", len(st.items))
	}
	it, err := s.Create(ctx, caller(pManager), pManager, CreateInput{Title: "  VPN maintenance ", Body: "Line 1\nLine 2"})
	if err != nil || it.Title != "VPN maintenance" || it.Severity != SeverityInfo || it.Status != StatusDraft || it.Version != 1 {
		t.Errorf("created = %+v %v", it, err)
	}
	if it.AuthorUserID == nil || *it.AuthorUserID != pManager.UserID {
		t.Errorf("author = %v", it.AuthorUserID)
	}
}

func TestLifecycle(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	c := caller(pManager)
	it := mustCreate(t, s, CreateInput{Title: "t", Severity: SeverityWarning})

	pub, err := s.Publish(ctx, c, pManager, it.ID, nil)
	if err != nil || pub.Status != StatusPublished || pub.PublishedAt == nil || !pub.PublishedAt.Equal(noon) || pub.PublishedByUserID == nil {
		t.Fatalf("publish = %+v %v", pub, err)
	}
	ev := st.changes[len(st.changes)-1].Events
	if len(ev) != 1 || ev[0].Type != "BriefingItemPublished" || ev[0].Payload["itemId"] != it.ID || ev[0].Payload["severity"] != SeverityWarning {
		t.Errorf("events = %+v", ev)
	}
	var tr *InvalidTransitionError
	if _, err := s.Publish(ctx, c, pManager, it.ID, nil); !errors.As(err, &tr) {
		t.Errorf("publishing twice: %v", err)
	}
	if _, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{Title: ptr("changed")}); !errors.As(err, &tr) {
		t.Errorf("editing a published item: %v (published items are immutable)", err)
	}
	if err := s.Delete(ctx, c, pManager, it.ID, nil); !errors.As(err, &tr) {
		t.Errorf("deleting a published item: %v", err)
	}
	wd, err := s.Withdraw(ctx, c, pManager, it.ID, nil)
	if err != nil || wd.Status != StatusWithdrawn || wd.WithdrawnAt == nil || wd.WithdrawnByUserID == nil {
		t.Fatalf("withdraw = %+v %v", wd, err)
	}
	if _, err := s.Withdraw(ctx, c, pManager, it.ID, nil); !errors.As(err, &tr) {
		t.Errorf("withdrawing twice: %v", err)
	}
	if _, err := s.Publish(ctx, c, pManager, it.ID, nil); !errors.As(err, &tr) {
		t.Errorf("republishing a withdrawn item: %v", err)
	}
	if err := s.Delete(ctx, c, pManager, it.ID, nil); !errors.As(err, &tr) {
		t.Errorf("deleting a withdrawn item: %v (history is kept)", err)
	}
	draft := mustCreate(t, s, CreateInput{Title: "d"})
	if _, err := s.Withdraw(ctx, c, pManager, draft.ID, nil); !errors.As(err, &tr) {
		t.Errorf("withdrawing a draft: %v", err)
	}
	if err := s.Delete(ctx, c, pManager, draft.ID, nil); err != nil {
		t.Errorf("deleting a draft: %v", err)
	}
	if err := s.Delete(ctx, c, pManager, draft.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestUpdateDraft(t *testing.T) {
	s, st := newSvc()
	ctx := context.Background()
	c := caller(pManager)
	soon := noon.Add(48 * time.Hour)
	it := mustCreate(t, s, CreateInput{Title: "old", Body: "b", ValidUntil: &soon})

	var inv *InvalidInputError
	if _, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{}); !errors.As(err, &inv) {
		t.Errorf("empty update: %v", err)
	}
	if _, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{ClearValidUntil: true, ValidUntil: &soon}); !errors.As(err, &inv) {
		t.Errorf("clear and set: %v", err)
	}
	if _, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{Severity: ptr("fatal")}); !errors.As(err, &inv) {
		t.Errorf("bad severity: %v", err)
	}
	u, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{Title: ptr("new"), Severity: ptr(SeverityCritical), ClearValidUntil: true})
	if err != nil || u.Title != "new" || u.Severity != SeverityCritical || u.ValidUntil != nil || u.Body != "b" {
		t.Fatalf("update = %+v %v", u, err)
	}
	if got := strings.Join(st.changes[len(st.changes)-1].Metadata["changedFields"].([]string), ","); got != "title,severity,validUntil" {
		t.Errorf("changed fields = %s", got)
	}
	n := len(st.changes)
	if _, err := s.Update(ctx, c, pManager, it.ID, nil, UpdateInput{Title: ptr("new")}); err != nil || len(st.changes) != n {
		t.Errorf("unchanged update must be a no-op: %v", err)
	}
	stale := 1
	if _, err := s.Update(ctx, c, pManager, it.ID, &stale, UpdateInput{Title: ptr("late")}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := s.Publish(ctx, c, pManager, it.ID, &stale); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale publish: %v", err)
	}
}

func TestExpiredDraftCannotBePublished(t *testing.T) {
	s, _ := newSvc()
	past := noon.Add(-time.Hour)
	it := mustCreate(t, s, CreateInput{Title: "old news", ValidUntil: &past})
	var inv *InvalidInputError
	if _, err := s.Publish(context.Background(), caller(pManager), pManager, it.ID, nil); !errors.As(err, &inv) {
		t.Errorf("publishing an expired draft: %v", err)
	}
}

func TestVisibility(t *testing.T) {
	st := newMemStore()
	clock := noon
	s := NewService(st, func() time.Time { return clock })
	ctx := context.Background()
	c := caller(pManager)
	draft := mustCreate(t, s, CreateInput{Title: "draft"})
	published := mustCreate(t, s, CreateInput{Title: "published"})
	expiring := mustCreate(t, s, CreateInput{Title: "expiring", ValidUntil: ptrTime(noon.Add(time.Hour))})
	withdrawn := mustCreate(t, s, CreateInput{Title: "withdrawn"})
	for _, it := range []Item{published, expiring, withdrawn} {
		if _, err := s.Publish(ctx, c, pManager, it.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Withdraw(ctx, c, pManager, withdrawn.ID, nil); err != nil {
		t.Fatal(err)
	}

	for _, it := range []Item{published, expiring} {
		if _, err := s.Get(ctx, pViewer, it.ID); err != nil {
			t.Errorf("viewer must see %q: %v", it.Title, err)
		}
	}
	for _, it := range []Item{draft, withdrawn} {
		if _, err := s.Get(ctx, pViewer, it.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("viewer must not see %q (want ErrNotFound): %v", it.Title, err)
		}
	}
	if _, err := s.Get(ctx, pNobody, published.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user without briefing permissions sees nothing: %v", err)
	}
	for _, it := range []Item{draft, published, expiring, withdrawn} {
		if _, err := s.Get(ctx, pManager, it.ID); err != nil {
			t.Errorf("manager must see %q: %v", it.Title, err)
		}
	}
	clock = noon.Add(2 * time.Hour) // the expiring item has expired
	if _, err := s.Get(ctx, pViewer, expiring.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired item must disappear for viewers: %v", err)
	}
	if _, err := s.Get(ctx, pManager, expiring.ID); err != nil {
		t.Errorf("managers still see expired items: %v", err)
	}

	count := func(p Principal, status string) int {
		res, err := s.List(ctx, p, status, Page{})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Items)
	}
	if n := count(pManager, ""); n != 4 {
		t.Errorf("manager list = %d, want 4", n)
	}
	if n := count(pManager, StatusDraft); n != 1 {
		t.Errorf("manager draft list = %d", n)
	}
	if n := count(pViewer, ""); n != 2 {
		t.Errorf("viewer list = %d, want only the published items the store returns", n)
	}
	// A viewer cannot widen the list with a status filter.
	if n := count(pViewer, StatusDraft); n != 2 {
		t.Errorf("viewer with status=draft = %d, want the published-only list", n)
	}
	if n := count(pNobody, ""); n != 0 {
		t.Errorf("no permission list = %d", n)
	}
	var inv *InvalidInputError
	if _, err := s.List(ctx, pManager, "archived", Page{}); !errors.As(err, &inv) {
		t.Errorf("unknown status: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
