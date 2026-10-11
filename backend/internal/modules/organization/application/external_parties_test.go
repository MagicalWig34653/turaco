package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

const testID = "0198a7c0-1111-7222-8333-444455556666"

type fakePartyStore struct {
	ExternalPartyStore
	created NewExternalPartyInput
	changed ExternalPartyChange
	called  string
}

func (f *fakePartyStore) ListExternalParties(context.Context) ([]ExternalParty, error) {
	f.called = "list"
	return nil, nil
}
func (f *fakePartyStore) CreateExternalParty(_ context.Context, _ Caller, in NewExternalPartyInput) (ExternalParty, error) {
	f.called, f.created = "create", in
	return ExternalParty{ID: testID}, nil
}
func (f *fakePartyStore) UpdateExternalParty(_ context.Context, _ Caller, _ string, in ExternalPartyChange) (ExternalParty, error) {
	f.called, f.changed = "update", in
	return ExternalParty{ID: testID}, nil
}

func partyCaller(manage bool) Caller {
	return Caller{Actor: audit.UserActor(testID), CorrelationID: "corr", ExternalPartiesManage: manage}
}

func TestExternalPartyOperationsNeedThePermissionAndValidInput(t *testing.T) {
	store := &fakePartyStore{}
	svc := NewExternalParties(store)
	ctx := context.Background()
	in := NewExternalPartyInput{Kind: "vendor", Name: "  Acme Ltd  ", SponsorUserID: strings.ToUpper(testID), Notes: " renewal in May "}
	if _, err := svc.Create(ctx, partyCaller(false), in); !errors.Is(err, ErrForbidden) {
		t.Errorf("create without the permission = %v", err)
	}
	if _, err := svc.List(ctx, partyCaller(false)); !errors.Is(err, ErrForbidden) {
		t.Errorf("list without the permission = %v", err)
	}
	if _, _, err := svc.Get(ctx, partyCaller(false), testID); !errors.Is(err, ErrForbidden) {
		t.Errorf("get without the permission = %v", err)
	}
	if _, err := svc.Update(ctx, partyCaller(false), testID, ExternalPartyChange{ExpectedVersion: 1}); !errors.Is(err, ErrForbidden) {
		t.Errorf("update without the permission = %v", err)
	}
	if store.called != "" {
		t.Fatalf("the store was reached without the permission: %s", store.called)
	}
	if _, err := svc.Create(ctx, partyCaller(true), in); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if store.created.Name != "Acme Ltd" || store.created.SponsorUserID != testID || store.created.Notes != "renewal in May" {
		t.Errorf("input is normalized before it reaches the store: %+v", store.created)
	}
	bad := map[string]NewExternalPartyInput{
		"kind":    {Kind: "supplier", Name: "A", SponsorUserID: testID},
		"name":    {Kind: "vendor", Name: " ", SponsorUserID: testID},
		"long":    {Kind: "vendor", Name: strings.Repeat("a", 201), SponsorUserID: testID},
		"control": {Kind: "vendor", Name: "A‮B", SponsorUserID: testID},
		"sponsor": {Kind: "vendor", Name: "A", SponsorUserID: "someone"},
		"notes":   {Kind: "vendor", Name: "A", SponsorUserID: testID, Notes: strings.Repeat("n", 501)},
	}
	for name, b := range bad {
		var inv *InvalidInputError
		if _, err := svc.Create(ctx, partyCaller(true), b); !errors.As(err, &inv) {
			t.Errorf("create with bad %s = %v", name, err)
		}
	}
	name := " Renamed "
	var inv *InvalidInputError
	if _, err := svc.Update(ctx, partyCaller(true), testID, ExternalPartyChange{Name: &name}); !errors.As(err, &inv) {
		t.Errorf("update needs expectedVersion: %v", err)
	}
	if _, err := svc.Update(ctx, partyCaller(true), testID, ExternalPartyChange{ExpectedVersion: 1}); !errors.As(err, &inv) {
		t.Errorf("update needs a field: %v", err)
	}
	if _, err := svc.Update(ctx, partyCaller(true), testID, ExternalPartyChange{ExpectedVersion: 1, Name: &name}); err != nil || *store.changed.Name != "Renamed" {
		t.Errorf("valid update = %v %+v", err, store.changed)
	}
}

type fakePeopleStore struct {
	PeopleStore
	created NewUserInput
}

func (f *fakePeopleStore) CreateLocalUser(_ context.Context, _ Caller, in NewUserInput) (User, error) {
	f.created = in
	return User{ID: testID}, nil
}

func TestCreateExternalAccountValidation(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	store := &fakePeopleStore{}
	svc := NewPeople(store).WithClock(func() time.Time { return now })
	ctx := context.Background()
	email := "vera@vendor.example.test"
	party := strings.ToUpper(testID)
	until := now.AddDate(0, 0, 90)
	valid := func() NewUserInput {
		return NewUserInput{DisplayName: "Vera Vendor", PrimaryEmail: &email, AccountKind: AccountKindExternal, ExternalPartyID: &party, AccessExpiresAt: &until}
	}
	if _, err := svc.CreateUser(ctx, partyCaller(false), valid()); !errors.Is(err, ErrExternalNeedsPermission) {
		t.Errorf("without the permission = %v", err)
	}
	if _, err := svc.CreateUser(ctx, partyCaller(true), valid()); err != nil {
		t.Fatalf("valid external account: %v", err)
	}
	if store.created.ExternalPartyID == nil || *store.created.ExternalPartyID != testID {
		t.Errorf("party id is normalized: %+v", store.created.ExternalPartyID)
	}
	maxOK := now.AddDate(0, 0, MaxAccessDays)
	tooLong := now.AddDate(0, 0, MaxAccessDays+1)
	past := now.Add(-time.Hour)
	dept := testID
	number := "E1"
	cases := map[string]func(*NewUserInput){
		"no party":     func(in *NewUserInput) { in.ExternalPartyID = nil },
		"bad party":    func(in *NewUserInput) { x := "nope"; in.ExternalPartyID = &x },
		"no email":     func(in *NewUserInput) { in.PrimaryEmail = nil },
		"no expiry":    func(in *NewUserInput) { in.AccessExpiresAt = nil },
		"past expiry":  func(in *NewUserInput) { in.AccessExpiresAt = &past },
		"too long":     func(in *NewUserInput) { in.AccessExpiresAt = &tooLong },
		"department":   func(in *NewUserInput) { in.DepartmentID = &dept },
		"location":     func(in *NewUserInput) { in.LocationID = &dept },
		"employee no.": func(in *NewUserInput) { in.EmployeeNumber = &number },
	}
	for name, mutate := range cases {
		in := valid()
		mutate(&in)
		var inv *InvalidInputError
		if _, err := svc.CreateUser(ctx, partyCaller(true), in); !errors.As(err, &inv) {
			t.Errorf("%s = %v", name, err)
		}
	}
	in := valid()
	in.AccessExpiresAt = &maxOK
	if _, err := svc.CreateUser(ctx, partyCaller(true), in); err != nil {
		t.Errorf("exactly %d days is allowed: %v", MaxAccessDays, err)
	}
	// Employees carry none of the external fields; the kind is a closed list.
	emp := NewUserInput{DisplayName: "Emil", ExternalPartyID: &party}
	var inv *InvalidInputError
	if _, err := svc.CreateUser(ctx, partyCaller(true), emp); !errors.As(err, &inv) {
		t.Errorf("employee with a party = %v", err)
	}
	if _, err := svc.CreateUser(ctx, partyCaller(true), NewUserInput{DisplayName: "X", AccountKind: "guest"}); !errors.As(err, &inv) {
		t.Errorf("unknown kind = %v", err)
	}
}

type fakeExpiryStore struct {
	expireErr, remindErr error
	expired, reminded    int
	at                   time.Time
	leads                []int
}

func (f *fakeExpiryStore) ExpireAccess(_ context.Context, now time.Time, limit int) (int, error) {
	f.expired++
	f.at = now
	if limit != ExpireAccessBatch {
		return 0, errors.New("unexpected limit")
	}
	return 0, f.expireErr
}

func (f *fakeExpiryStore) RemindSponsors(_ context.Context, _ time.Time, leads []int, _ int) (int, error) {
	f.reminded++
	f.leads = leads
	return 0, f.remindErr
}

func TestExpireHandlerRunsBothPartsAndReportsFailures(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	store := &fakeExpiryStore{}
	var handler jobs.Handler = NewExpireHandler(store, func() time.Time { return now })
	if err := handler(context.Background(), jobs.Job{}); err != nil || store.expired != 1 || store.reminded != 1 || !store.at.Equal(now) {
		t.Fatalf("run = %v %+v", err, store)
	}
	if len(store.leads) != 2 || store.leads[0] != 14 || store.leads[1] != 3 {
		t.Errorf("lead days = %v, the design reminds 14 and 3 days before", store.leads)
	}
	// A failing expiry does not stop the reminders, and the job fails so it is retried.
	store = &fakeExpiryStore{expireErr: errors.New("db down")}
	handler = NewExpireHandler(store, func() time.Time { return now })
	if err := handler(context.Background(), jobs.Job{}); err == nil || store.reminded != 1 {
		t.Errorf("failing expiry: err=%v reminded=%d", err, store.reminded)
	}
}

func TestSponsorReminderCategoryIsComplete(t *testing.T) {
	cats := NotificationCategories()
	if len(cats) != 1 || cats[0].Name != NotificationAccessExpiring || cats[0].Email["en"].Subject == "" || cats[0].Email["de"].Subject == "" || cats[0].LinkType != "user" {
		t.Errorf("categories = %+v", cats)
	}
}
