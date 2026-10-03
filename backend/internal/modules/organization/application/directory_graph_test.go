package application

import (
	"context"
	"testing"
)

type recordingGraphStore struct {
	key   string
	limit int
	ids   []string
	cut   bool
}

func (r *recordingGraphStore) GroupsByExternalIDs(_ context.Context, key string, _ []string, limit int) ([]DirectoryGroupRef, bool, error) {
	r.key, r.limit = key, limit
	return nil, r.cut, nil
}
func (r *recordingGraphStore) GroupsByIDs(_ context.Context, key string, ids []string, limit int) ([]DirectoryGroupRef, bool, error) {
	r.key, r.limit, r.ids = key, limit, ids
	return nil, r.cut, nil
}
func (r *recordingGraphStore) NestingUp(_ context.Context, key string, ids []string, limit int) ([]GroupNestingEdge, bool, error) {
	r.key, r.limit, r.ids = key, limit, ids
	return nil, r.cut, nil
}
func (r *recordingGraphStore) NestingDown(_ context.Context, key string, ids []string, limit int) ([]GroupNestingEdge, bool, error) {
	r.key, r.limit, r.ids = key, limit, ids
	return nil, r.cut, nil
}
func (r *recordingGraphStore) UserMemberships(_ context.Context, key string, ids []string, limit int) ([]GroupUserMembership, bool, error) {
	r.key, r.limit, r.ids = key, limit, ids
	return nil, r.cut, nil
}
func (r *recordingGraphStore) GroupMembers(_ context.Context, key string, ids []string, limit int) ([]GroupUserMembership, bool, error) {
	r.key, r.limit, r.ids = key, limit, ids
	return nil, r.cut, nil
}
func (r *recordingGraphStore) UsersWithIdentity(_ context.Context, key string, ids []string) ([]string, error) {
	r.key, r.ids = key, ids
	return ids, nil
}

func TestDirectoryGraphPassesProviderKeyAndTruncationAndClampsLimits(t *testing.T) {
	ctx := context.Background()
	const id = "01a10340-b1d5-7456-a74e-4f4430cb368c"
	st := &recordingGraphStore{cut: true}
	g := NewDirectoryGraph(st)

	if _, cut, err := g.UserMemberships(ctx, "intune", []string{id, "bad"}, 1_000_000); err != nil || !cut {
		t.Fatalf("truncation flag not passed on: %v %v", cut, err)
	}
	if st.key != "intune" || st.limit != MaxGraphRows || len(st.ids) != 1 {
		t.Errorf("store saw key %q limit %d ids %v", st.key, st.limit, st.ids)
	}
	if _, _, err := g.GroupMembers(ctx, "intune", []string{id}, 7); err != nil || st.limit != 7 {
		t.Errorf("limit not passed: %d %v", st.limit, err)
	}
	// Without a provider key nothing is looked up: external ids of different providers must not be confused.
	st.key = "untouched"
	if got, cut, err := g.NestingUp(ctx, "", []string{id}, 10); err != nil || cut || got != nil || st.key != "untouched" {
		t.Errorf("empty provider key must not reach the store: %v %v %v", got, cut, err)
	}
	users, err := g.UsersWithIdentity(ctx, "intune", []string{id, "bad"})
	if err != nil || !users[id] || len(users) != 1 {
		t.Errorf("identities = %v %v", users, err)
	}
}
