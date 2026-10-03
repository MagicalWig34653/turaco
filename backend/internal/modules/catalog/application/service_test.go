package application

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type stubStore struct {
	Store
	item     Item
	inserted NewItem
	calls    int
	query    ListQuery
}

func (s *stubStore) Insert(_ context.Context, _ Caller, n NewItem) (Item, error) {
	s.calls++
	s.inserted = n
	return Item{Key: n.Key, Title: n.Title, Active: true, Version: 1}, nil
}
func (s *stubStore) Get(context.Context, string) (Item, error) { return s.item, nil }
func (s *stubStore) Change(_ context.Context, _ Caller, _ string, decide func(Item) (Change, error)) (Item, error) {
	s.calls++
	ch, err := decide(s.item)
	if err != nil || ch.NoChange {
		return s.item, err
	}
	return ch.Next, nil
}
func (s *stubStore) List(_ context.Context, q ListQuery) (Result, error) {
	s.query = q
	return Result{Items: []Item{s.item}}, nil
}

type dir struct{ users, teams map[string]bool }

func (d dir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = d.users[i]
	}
	return out, nil
}
func (d dir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, i := range ids {
		out[i] = d.teams[i]
	}
	return out, nil
}

type prods struct{}

func (prods) ActiveProducts(_ context.Context, ids []string) (map[string]string, error) {
	return activeProducts.ActiveProducts(nil, ids)
}
func (prods) ProductNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Product " + id[len(id)-2:]
	}
	return out, nil
}
func (prods) CategoriesExist(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = id == catLap || id == catMon
	}
	return out, nil
}
func (prods) ActiveInCategory(_ context.Context, cat string, _ int) ([]ProductChoice, error) {
	if cat == catLap {
		return []ProductChoice{{ID: prodA, Name: "Latitude"}, {ID: prodB, Name: "ThinkPad"}}, nil
	}
	return nil, nil
}

var (
	manager  = Principal{UserID: userA, Manage: true}
	employee = Principal{UserID: "00000000-0000-7000-8000-0000000000d1"}
)

func newSvc(st *stubStore) *Service {
	return NewService(st, dir{users: map[string]bool{userA: true}, teams: map[string]bool{teamA: true}}, prods{})
}

func cl() Caller          { return Caller{Actor: audit.UserActor(userA), CorrelationID: "c"} }
func sp(s string) *string { return &s }

const goodDef = `{"fields":[{"key":"reason","type":"text","label":"Reason","required":true}],"approvals":[{"approverTeamId":"` + teamA + `"}],"fulfillment":[{"title":"Prepare","assignedTeamId":"` + teamA + `"}]}`

func TestOnlyManagersAdministerItems(t *testing.T) {
	st := &stubStore{}
	s := newSvc(st)
	ctx := context.Background()
	if _, err := s.Create(ctx, cl(), employee, CreateInput{Key: "laptop", Title: "x", Definition: []byte(`{}`)}); !errors.Is(err, ErrForbidden) {
		t.Errorf("create: %v", err)
	}
	if _, err := s.Update(ctx, cl(), employee, "id", 1, UpdateInput{Title: sp("x")}); !errors.Is(err, ErrForbidden) {
		t.Errorf("update: %v", err)
	}
	if _, err := s.SetActive(ctx, cl(), employee, "id", nil, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("activate: %v", err)
	}
	if st.calls != 0 {
		t.Errorf("store called %d times", st.calls)
	}
}

func TestCreateValidatesEverythingBeforeStoring(t *testing.T) {
	st := &stubStore{}
	s := newSvc(st)
	ctx := context.Background()
	other := "00000000-0000-7000-8000-0000000000ee"
	bad := map[string]CreateInput{
		"bad key":          {Key: "Laptop", Title: "x", Definition: []byte(`{}`)},
		"short key":        {Key: "a", Title: "x", Definition: []byte(`{}`)},
		"blank title":      {Key: "laptop", Title: " ", Definition: []byte(`{}`)},
		"override title":   {Key: "laptop", Title: "x‮", Definition: []byte(`{}`)},
		"invalid def":      {Key: "laptop", Title: "x", Definition: []byte(`{"fields":[{"key":"A"}]}`)},
		"huge def":         {Key: "laptop", Title: "x", Definition: append([]byte(`{"x":"`), make([]byte, MaxDefinitionBytes)...)},
		"unknown team":     {Key: "laptop", Title: "x", Definition: []byte(`{"approvals":[{"approverTeamId":"` + other + `"}]}`)},
		"unknown user":     {Key: "laptop", Title: "x", Definition: []byte(`{"fulfillment":[{"title":"t","assignedUserId":"` + other + `"}]}`)},
		"unknown product":  {Key: "laptop", Title: "x", Definition: []byte(`{"fields":[{"key":"p","type":"product","label":"P","productIds":["` + other + `"]}]}`)},
		"unknown category": {Key: "laptop", Title: "x", Definition: []byte(`{"fields":[{"key":"p","type":"product","label":"P","categoryId":"` + other + `"}]}`)},
	}
	for name, in := range bad {
		if _, err := s.Create(ctx, cl(), manager, in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if st.calls != 0 {
		t.Errorf("rejected input reached the store (%d calls)", st.calls)
	}
	if err := func() error {
		_, err := s.Create(ctx, cl(), manager, CreateInput{Key: "laptop", Title: "x", Definition: []byte(`{"approvals":[{"approverTeamId":"` + "00000000-0000-7000-8000-0000000000ee" + `"}]}`)})
		return err
	}(); !errors.Is(err, ErrReferenceInvalid) {
		t.Errorf("a dangling reference must be ErrReferenceInvalid: %v", err)
	}
	it, err := s.Create(ctx, cl(), manager, CreateInput{Key: "laptop", Title: "  Laptop request ", Description: " desc ", Definition: []byte(goodDef)})
	if err != nil || it.Title != "Laptop request" || st.inserted.Description != "desc" {
		t.Fatalf("create = %+v %v", it, err)
	}
	if _, err := ParseDefinition(st.inserted.Definition); err != nil {
		t.Errorf("the stored form must be canonical and valid: %v", err)
	}
}

func TestUpdateAndActivate(t *testing.T) {
	def := mustParse(t, goodDef)
	st := &stubStore{item: Item{Key: "laptop", Title: "old", Description: "d", Definition: def, Active: true, Version: 3}}
	s := newSvc(st)
	ctx := context.Background()
	var inv *InvalidInputError
	if _, err := s.Update(ctx, cl(), manager, "id", 3, UpdateInput{}); !errors.As(err, &inv) {
		t.Errorf("empty update: %v", err)
	}
	if _, err := s.Update(ctx, cl(), manager, "id", 2, UpdateInput{Title: sp("x")}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale: %v", err)
	}
	u, err := s.Update(ctx, cl(), manager, "id", 3, UpdateInput{Title: sp(" new "), Definition: []byte(`{"allowRequestedFor":true}`)})
	if err != nil || u.Title != "new" || !u.Definition.AllowRequestedFor {
		t.Errorf("update = %+v %v", u, err)
	}
	n := st.calls
	if _, err := s.Update(ctx, cl(), manager, "id", 3, UpdateInput{Title: sp("old"), Definition: []byte(goodDef)}); err != nil || st.calls != n+1 {
		t.Errorf("unchanged update: %v", err)
	}
	if off, err := s.SetActive(ctx, cl(), manager, "id", nil, false); err != nil || off.Active {
		t.Errorf("deactivate: %+v %v", off, err)
	}
	if same, err := s.SetActive(ctx, cl(), manager, "id", nil, true); err != nil || !same.Active {
		t.Errorf("activating an active item: %+v %v", same, err)
	}
}

func TestEmployeesSeeOnlyActiveItemsAndNoApprovalDetails(t *testing.T) {
	def := mustParse(t, formDef)
	st := &stubStore{item: Item{Key: "laptop", Title: "L", Definition: def, Active: false, Version: 1}}
	s := newSvc(st)
	ctx := context.Background()
	if _, err := s.Get(ctx, employee, "id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("inactive item for an employee: %v", err)
	}
	if _, err := s.Form(ctx, employee, "id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("inactive form for an employee: %v", err)
	}
	if _, err := s.Get(ctx, manager, "id"); err != nil {
		t.Errorf("manager sees inactive items: %v", err)
	}
	if _, err := s.List(ctx, employee, "inactive", Page{}); err != nil || !st.query.ActiveOnly || st.query.Status != "" {
		t.Errorf("employee list query = %+v %v", st.query, err)
	}
	if _, err := s.List(ctx, manager, "inactive", Page{}); err != nil || st.query.ActiveOnly || st.query.Status != "inactive" {
		t.Errorf("manager list query = %+v %v", st.query, err)
	}
	var inv *InvalidInputError
	if _, err := s.List(ctx, manager, "archived", Page{}); !errors.As(err, &inv) {
		t.Errorf("unknown status: %v", err)
	}
}

func TestFormResolvesProductOptions(t *testing.T) {
	def := mustParse(t, formDef)
	st := &stubStore{item: Item{Key: "laptop", Title: "L", Definition: def, Active: true, Version: 1}}
	s := newSvc(st)
	f, err := s.Form(context.Background(), employee, "id")
	if err != nil {
		t.Fatal(err)
	}
	var device, extra FormField
	for _, ff := range f.Fields {
		switch ff.Key {
		case "device":
			device = ff
		case "extra":
			extra = ff
		}
	}
	if len(device.ProductOptions) != 2 || device.ProductOptions[0].Name != "Latitude" {
		t.Errorf("category options = %+v", device.ProductOptions)
	}
	if len(extra.ProductOptions) != 1 || extra.ProductOptions[0].ID != prodA || extra.ProductOptions[0].Name == "" {
		t.Errorf("listed options = %+v", extra.ProductOptions)
	}
}
