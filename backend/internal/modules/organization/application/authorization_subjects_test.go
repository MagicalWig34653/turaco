package application

import (
	"context"
	"testing"
)

type fakeSubjectStore struct {
	calls     int
	usernames map[string][]string
	emails    map[string]string
	exists    map[string]bool
}

func (f *fakeSubjectStore) GroupIDsOfUser(context.Context, string) ([]string, error) {
	f.calls++
	return []string{"g"}, nil
}
func (f *fakeSubjectStore) GroupMemberUserIDs(context.Context, []string, int) ([]string, error) {
	return nil, nil
}

func (f *fakeSubjectStore) UserExists(_ context.Context, id string) (bool, error) {
	f.calls++
	return f.exists[id], nil
}
func (f *fakeSubjectStore) DirectoryGroupObserved(context.Context, string) (bool, error) {
	f.calls++
	return true, nil
}
func (f *fakeSubjectStore) DisplayNames(context.Context, []string, []string) (map[string]string, error) {
	f.calls++
	return map[string]string{"x": "y"}, nil
}
func (f *fakeSubjectStore) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	f.calls++
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (f *fakeSubjectStore) UsersByUsername(_ context.Context, u string) ([]string, error) {
	f.calls++
	return f.usernames[u], nil
}
func (f *fakeSubjectStore) UserByEmail(_ context.Context, e string) (string, bool, error) {
	f.calls++
	id, ok := f.emails[e]
	return id, ok, nil
}

func TestAuthorizationSubjectsMalformedIDsNeverReachStorage(t *testing.T) {
	st := &fakeSubjectStore{}
	s := NewAuthorizationSubjects(st)
	ctx := context.Background()
	if g, err := s.GroupIDsOfUser(ctx, "nope"); err != nil || g == nil || len(g) != 0 {
		t.Fatalf("groups = %v, %v", g, err)
	}
	if ok, _ := s.UserExists(ctx, "nope"); ok {
		t.Fatal("malformed user exists")
	}
	if ok, _ := s.DirectoryGroupObserved(ctx, "nope"); ok {
		t.Fatal("malformed group observed")
	}
	if n, err := s.DisplayNames(ctx, []string{"nope"}, nil); err != nil || len(n) != 0 {
		t.Fatalf("names = %v, %v", n, err)
	}
	if a, err := s.ActiveUsers(ctx, []string{"nope"}); err != nil || len(a) != 0 {
		t.Fatalf("active = %v, %v", a, err)
	}
	const valid = "00000000-0000-7000-8000-000000000001"
	if a, err := s.ActiveUsers(ctx, []string{"nope", valid}); err != nil || len(a) != 1 || !a[valid] {
		t.Fatalf("active = %v, %v", a, err)
	}
	if st.calls != 1 { // only the valid id reached storage
		t.Fatalf("storage calls = %d", st.calls)
	}
}

func TestFindUserResolution(t *testing.T) {
	const id = "00000000-0000-7000-8000-000000000001"
	st := &fakeSubjectStore{
		exists:    map[string]bool{id: true},
		usernames: map[string][]string{"alice": {"u1"}, "dup": {"u1", "u2"}, "bob@example.test": nil},
		emails:    map[string]string{"bob@example.test": "u3"},
	}
	s := NewAuthorizationSubjects(st)
	for _, tc := range []struct {
		ref   string
		want  string
		found bool
	}{
		{id, id, true},
		{" alice ", "u1", true},
		{"dup", "", false},               // ambiguous
		{"bob@example.test", "u3", true}, // email fallback
		{"carol", "", false},             // unknown, not an email
		{"carol@example.test", "", false},
		{"", "", false},
	} {
		got, found, err := s.FindUser(context.Background(), tc.ref)
		if err != nil || found != tc.found || got != tc.want {
			t.Fatalf("FindUser(%q) = %q, %v, %v", tc.ref, got, found, err)
		}
	}
}
