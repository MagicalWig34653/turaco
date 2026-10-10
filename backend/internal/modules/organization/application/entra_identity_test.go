package application

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

const (
	entraTestTenant = "11111111-2222-3333-4444-555555555555"
	entraTestObject = "0f0f0f0f-0000-1111-2222-333333333333"
)

type fakeEntraStore struct {
	linked  []string
	user    User
	linkErr error
}

func (s *fakeEntraStore) LinkEntraIdentity(_ context.Context, _ Caller, _ string, _ int, tenantID, objectID string) (EntraLinkResult, error) {
	if s.linkErr != nil {
		return EntraLinkResult{}, s.linkErr
	}
	s.linked = append(s.linked, tenantID+"/"+objectID)
	return EntraLinkResult{User: s.user}, nil
}

func (s *fakeEntraStore) UnlinkEntraIdentity(context.Context, Caller, string, string) (EntraLinkResult, error) {
	return EntraLinkResult{User: s.user}, nil
}

type fakeNotifier struct {
	events []string
	err    error
}

func (n *fakeNotifier) NotifyIdentityChange(_ context.Context, to, _, event string) error {
	n.events = append(n.events, event+" "+to)
	return n.err
}

func entraCaller() Caller {
	return Caller{Actor: audit.UserActor("00000000-0000-7000-8000-000000000001"), CorrelationID: "c", PlatformAdmin: true}
}

func TestEntraLinkingValidatesBeforeTheStore(t *testing.T) {
	email := "ana@example.test"
	store := &fakeEntraStore{user: User{PrimaryEmail: &email, DisplayName: "Ana"}}
	n := &fakeNotifier{}
	svc := NewEntraLinking(store, []string{entraTestTenant}, n)
	var inv *InvalidInputError
	cases := map[string]struct {
		version         int
		tenant, object  string
		want            error
		wantInvalidType bool
	}{
		"no version":        {0, entraTestTenant, entraTestObject, nil, true},
		"object not a guid": {1, entraTestTenant, "anna@example.org", nil, true},
		"tenant not a guid": {1, "common", entraTestObject, nil, true},
		"foreign tenant":    {1, "99999999-2222-3333-4444-555555555555", entraTestObject, ErrEntraTenantNotAllowed, false},
		"consumer tenant":   {1, "9188040d-6c67-4c5b-b112-36a304b66dad", entraTestObject, ErrEntraTenantNotAllowed, false},
	}
	for name, tc := range cases {
		_, err := svc.Link(context.Background(), entraCaller(), "u", tc.version, tc.tenant, tc.object)
		if tc.wantInvalidType && !errors.As(err, &inv) || !tc.wantInvalidType && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(store.linked) != 0 || len(n.events) != 0 {
		t.Fatalf("a refused request reached the store or sent mail: %v %v", store.linked, n.events)
	}
	// Upper-case input is normalized, the notice goes to the stored address.
	res, err := svc.Link(context.Background(), entraCaller(), "u", 1, " "+upper(entraTestTenant), upper(entraTestObject))
	if err != nil || !res.NoticeSent || len(store.linked) != 1 || store.linked[0] != entraTestTenant+"/"+entraTestObject || n.events[0] != "linked ana@example.test" {
		t.Fatalf("%+v %v %v %v", res, err, store.linked, n.events)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestEntraLinkingNotConfiguredAndMailFailure(t *testing.T) {
	store := &fakeEntraStore{}
	if _, err := NewEntraLinking(store, nil, nil).Link(context.Background(), entraCaller(), "u", 1, entraTestTenant, entraTestObject); !errors.Is(err, ErrEntraNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	if NewEntraLinking(store, nil, nil).Configured() {
		t.Fatal("no tenant means not configured")
	}
	// Without an address or with a failing relay the change still succeeds and reports no notice.
	svc := NewEntraLinking(store, []string{entraTestTenant}, &fakeNotifier{err: errors.New("down")})
	email := "ana@example.test"
	store.user = User{PrimaryEmail: &email}
	if res, err := svc.Link(context.Background(), entraCaller(), "u", 1, entraTestTenant, entraTestObject); err != nil || res.NoticeSent {
		t.Fatalf("failing relay: %+v %v", res, err)
	}
	store.user = User{}
	svc = NewEntraLinking(store, []string{entraTestTenant}, &fakeNotifier{})
	if res, err := svc.Link(context.Background(), entraCaller(), "u", 1, entraTestTenant, entraTestObject); err != nil || res.NoticeSent {
		t.Fatalf("no address: %+v %v", res, err)
	}
	// A caller without actor or correlation id is refused before anything happens.
	if _, err := svc.Link(context.Background(), Caller{}, "u", 1, entraTestTenant, entraTestObject); err == nil {
		t.Fatal("anonymous caller accepted")
	}
}

func TestValidateProvisionProfile(t *testing.T) {
	name, email, err := ValidateProvisionProfile("  Anna Beispiel ", "Anna@Example.org")
	if err != nil || name != "Anna Beispiel" || email != "Anna@Example.org" {
		t.Fatalf("%q %q %v", name, email, err)
	}
	for _, c := range [][2]string{{"", "a@example.org"}, {"Anna", ""}, {"Anna", "not-an-address"}, {"Anna\x00", "a@example.org"}, {"Anna", "a@example.org\r\nBcc: x@example.org"}} {
		if _, _, err := ValidateProvisionProfile(c[0], c[1]); err == nil {
			t.Errorf("accepted %q %q", c[0], c[1])
		}
	}
}
