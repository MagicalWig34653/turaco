package audit

import (
	"context"
	"strings"
)

// maxResolveIDs bounds the ids resolved per target type for one page.
const maxResolveIDs = 200

// Resolver turns the ids of one or more target types into display names. Implementations live in the composition
// root and call the owning module's public contract; they must return names only (never titles, comments or
// addresses). Unknown ids are absent from the result.
type Resolver interface {
	// Types are the audit target types this resolver understands.
	Types() []string
	Names(ctx context.Context, ids []string) (map[string]string, error)
}

// Label is the resolved display text of an actor or target. Gone marks an id the owning module no longer knows.
type Label struct {
	Text string
	Gone bool
}

// Resolved holds the labels of one page of events. Actors is keyed by user id, Targets by TargetKey.
type Resolved struct {
	Actors  map[string]Label
	Targets map[string]Label
	// Unavailable lists target types whose resolver failed; their events keep ids only.
	Unavailable []string
}

// TargetKey is the key of a target in Resolved.Targets.
func TargetKey(targetType, targetID string) string { return targetType + "/" + targetID }

// Resolvers resolves ids per target type. The "user" target type also resolves actors. A nil or empty Resolvers
// resolves nothing.
type Resolvers struct {
	byType map[string]Resolver
}

// NewResolvers registers resolvers by their types; a later resolver wins for a type.
func NewResolvers(rs ...Resolver) *Resolvers {
	out := &Resolvers{byType: map[string]Resolver{}}
	for _, r := range rs {
		for _, t := range r.Types() {
			out.byType[t] = r
		}
	}
	return out
}

// Resolve labels the actors and targets of events. It never fails: a failing resolver is reported in Unavailable.
// Callers must have authorized the page before calling it.
func (rs *Resolvers) Resolve(ctx context.Context, events []Event) Resolved {
	out := Resolved{Actors: map[string]Label{}, Targets: map[string]Label{}}
	if rs == nil || len(rs.byType) == 0 {
		return out
	}
	byType := map[string]map[string]bool{}
	want := func(t, id string) {
		if id == "" || len(id) > 200 || strings.ContainsRune(id, 0) {
			return
		}
		if byType[t] == nil {
			byType[t] = map[string]bool{}
		}
		if len(byType[t]) < maxResolveIDs {
			byType[t][id] = true
		}
	}
	for _, e := range events {
		if e.ActorID != nil {
			want("user", *e.ActorID)
		}
		want(e.TargetType, e.TargetID)
	}
	names := map[string]map[string]string{}
	failed := map[string]bool{}
	for t, ids := range byType {
		r, ok := rs.byType[t]
		if !ok {
			continue
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		got, err := r.Names(ctx, list)
		if err != nil {
			failed[t] = true
			out.Unavailable = append(out.Unavailable, t)
			continue
		}
		names[t] = got
	}
	for _, e := range events {
		if e.ActorID != nil {
			if _, done := out.Actors[*e.ActorID]; !done && !failed["user"] {
				if _, resolvable := rs.byType["user"]; resolvable {
					n, ok := names["user"][*e.ActorID]
					out.Actors[*e.ActorID] = Label{Text: n, Gone: !ok}
				}
			}
		}
		if _, resolvable := rs.byType[e.TargetType]; resolvable && !failed[e.TargetType] {
			n, ok := names[e.TargetType][e.TargetID]
			out.Targets[TargetKey(e.TargetType, e.TargetID)] = Label{Text: n, Gone: !ok}
		}
	}
	return out
}
