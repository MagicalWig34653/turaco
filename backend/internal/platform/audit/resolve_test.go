package audit

import (
	"context"
	"errors"
	"testing"
)

type fakeResolver struct {
	types []string
	names map[string]string
	err   error
	seen  []string
}

func (f *fakeResolver) Types() []string { return f.types }
func (f *fakeResolver) Names(_ context.Context, ids []string) (map[string]string, error) {
	f.seen = append(f.seen, ids...)
	return f.names, f.err
}

func TestResolveActorsAndTargets(t *testing.T) {
	users := &fakeResolver{types: []string{"user"}, names: map[string]string{"u1": "Anna Beispiel"}}
	teams := &fakeResolver{types: []string{"team"}, names: map[string]string{"t1": "Service Desk"}}
	rs := NewResolvers(users, teams)
	u1, u2 := "u1", "u2"
	got := rs.Resolve(context.Background(), []Event{
		{ActorID: &u1, TargetType: "team", TargetID: "t1"},
		{ActorID: &u2, TargetType: "team", TargetID: "t-gone"},
		{TargetType: "ticket", TargetID: "x"},
	})
	if got.Actors["u1"].Text != "Anna Beispiel" || got.Actors["u1"].Gone {
		t.Errorf("actor u1: %+v", got.Actors["u1"])
	}
	if !got.Actors["u2"].Gone {
		t.Errorf("actor u2 must be gone: %+v", got.Actors["u2"])
	}
	if got.Targets[TargetKey("team", "t1")].Text != "Service Desk" || !got.Targets[TargetKey("team", "t-gone")].Gone {
		t.Errorf("targets: %+v", got.Targets)
	}
	if _, ok := got.Targets[TargetKey("ticket", "x")]; ok {
		t.Error("a type without a resolver must not be labelled")
	}
}

func TestResolveBatchesAndDegrades(t *testing.T) {
	users := &fakeResolver{types: []string{"user"}, err: errors.New("boom")}
	rs := NewResolvers(users)
	u := "u1"
	got := rs.Resolve(context.Background(), []Event{{ActorID: &u, TargetType: "user", TargetID: "u1"}, {ActorID: &u, TargetType: "user", TargetID: "u1"}})
	if len(users.seen) != 1 {
		t.Errorf("ids must be deduplicated and batched once: %v", users.seen)
	}
	if len(got.Unavailable) != 1 || got.Unavailable[0] != "user" || len(got.Actors) != 0 {
		t.Errorf("a failing resolver must degrade to ids: %+v", got)
	}
	var none *Resolvers
	if r := none.Resolve(context.Background(), []Event{{TargetType: "x", TargetID: "y"}}); len(r.Actors) != 0 || len(r.Targets) != 0 {
		t.Errorf("nil resolvers: %+v", r)
	}
}

func TestResolveCapsIDsPerType(t *testing.T) {
	users := &fakeResolver{types: []string{"user"}, names: map[string]string{}}
	rs := NewResolvers(users)
	var events []Event
	for i := 0; i < maxResolveIDs+50; i++ {
		id := "u" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		events = append(events, Event{TargetType: "user", TargetID: id})
	}
	rs.Resolve(context.Background(), events)
	if len(users.seen) > maxResolveIDs {
		t.Errorf("resolved %d ids, cap is %d", len(users.seen), maxResolveIDs)
	}
}
