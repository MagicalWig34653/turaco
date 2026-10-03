package application

import (
	"context"
	"sort"
)

// History (F6 slice 4) is derived from the interval and change tables the ingestion already keeps; nothing is
// recorded per sync. Entries are ordered newest first with a keyset cursor over (occurredAt, key).

// ArtifactHistory returns the meaningful assignment history of an artifact: assignments added, changed (target,
// mode, intent or filter) and removed, and the artifact's removal. Needs endpoint.management.view or
// endpoints.manage; provider group ids and names only with organization.directory.view. Edits of a filter's rule
// are not history (a filter has no revision table); only the assignment's choice of filter is.
func (s *Service) ArtifactHistory(ctx context.Context, p Principal, artifactID string, page Page) (ArtifactHistory, error) {
	if !p.canViewManagement() {
		return ArtifactHistory{}, ErrForbidden
	}
	if !validUUID(artifactID) {
		return ArtifactHistory{}, ErrNotFound
	}
	page = page.Normalize()
	cur, err := DecodeHistoryCursor(page.Cursor)
	if err != nil {
		return ArtifactHistory{}, err
	}
	return inSnapshot(ctx, s, func(ctx context.Context) (ArtifactHistory, error) {
		art, err := s.store.GetArtifact(ctx, artifactID)
		if err != nil {
			return ArtifactHistory{}, err
		}
		rows, _, err := s.store.AssignmentEvents(ctx, AssignmentEventQuery{ArtifactID: art.ID, After: cur, Limit: page.Limit + 1})
		if err != nil {
			return ArtifactHistory{}, err
		}
		entries := assignmentEntries(rows)
		if art.DeletedObservedAt != nil {
			e := HistoryEntry{Kind: HistoryArtifactRemoved, OccurredAt: *art.DeletedObservedAt, Source: art.Source, ObservedAt: *art.DeletedObservedAt,
				Artifact: &HistoryArtifact{ID: art.ID, Name: art.Name, Kind: art.Kind, Deleted: true}, key: "t:" + art.ID}
			if cur.allows(e.OccurredAt, e.key) {
				entries = append(entries, e)
			}
		}
		items, next, err := s.finishHistory(ctx, p, entries, page.Limit)
		if err != nil {
			return ArtifactHistory{}, err
		}
		return ArtifactHistory{Artifact: art, Items: items, NextCursor: next}, nil
	})
}

// DeviceHistory returns the Device's meaningful management history: changes of the normalized observation state per
// artifact, group membership intervals, and the assignment changes that address the Device itself (all_devices, or a
// provider group the Device belonged to at the time). Assignments that reach the Device only through its User are
// not derived (AssignmentScope says so). Needs management access and device access; group ids and names only with
// organization.directory.view.
func (s *Service) DeviceHistory(ctx context.Context, p Principal, deviceID string, page Page) (DeviceHistory, error) {
	if !p.canViewManagement() || !p.canView() {
		return DeviceHistory{}, ErrForbidden
	}
	if !validUUID(deviceID) {
		return DeviceHistory{}, ErrNotFound
	}
	page = page.Normalize()
	cur, err := DecodeHistoryCursor(page.Cursor)
	if err != nil {
		return DeviceHistory{}, err
	}
	return inSnapshot(ctx, s, func(ctx context.Context) (DeviceHistory, error) {
		d, err := s.store.GetDevice(ctx, deviceID)
		if err != nil {
			return DeviceHistory{}, err
		}
		limit := page.Limit + 1
		arows, truncated, err := s.store.AssignmentEvents(ctx, AssignmentEventQuery{DeviceID: d.ID, Provider: s.viewProvider, After: cur, Limit: limit})
		if err != nil {
			return DeviceHistory{}, err
		}
		orows, err := s.store.ObservationEvents(ctx, d.ID, cur, limit)
		if err != nil {
			return DeviceHistory{}, err
		}
		mrows, err := s.store.MembershipEvents(ctx, d.ID, cur, limit)
		if err != nil {
			return DeviceHistory{}, err
		}
		entries := assignmentEntries(arows)
		for _, o := range orows {
			kind := HistoryObservationChanged
			ob := &ObservationChange{State: o.State, RawStatus: o.RawStatus}
			if o.PrevState == nil {
				kind = HistoryObservationFirstSeen
			} else {
				ob.PreviousState = *o.PrevState
			}
			entries = append(entries, HistoryEntry{Kind: kind, OccurredAt: o.OccurredAt, Source: o.Source, ObservedAt: o.OccurredAt,
				Artifact:    &HistoryArtifact{ID: o.ArtifactID, Name: o.ArtifactName, Kind: o.ArtifactKind, Deleted: o.ArtifactDeleted},
				Observation: ob, key: o.Key})
		}
		groups := map[string]*string{}
		for _, m := range mrows {
			ext := m.GroupExternalID
			groups[m.Key] = &ext
			entries = append(entries, HistoryEntry{Kind: m.Kind, OccurredAt: m.OccurredAt, Source: m.Source, ObservedAt: m.OccurredAt, key: m.Key})
		}
		items, next, err := s.finishHistory(ctx, p, entries, page.Limit, groups)
		if err != nil {
			return DeviceHistory{}, err
		}
		return DeviceHistory{DeviceID: d.ID, Items: items, NextCursor: next, AssignmentScope: AssignmentScopeDevice, Truncated: truncated}, nil
	})
}

func assignmentEntries(rows []AssignmentEventRow) []HistoryEntry {
	out := make([]HistoryEntry, 0, len(rows))
	for _, r := range rows {
		ch := &AssignmentChange{AssignmentID: r.AssignmentID, ProviderAssignmentID: r.ProviderAssignmentID, Current: snapshotOf(r.Cur)}
		if r.Kind == HistoryAssignmentChanged && r.Prev != nil {
			prev := snapshotOf(*r.Prev)
			ch.Previous = &prev
			ch.Changes = assignmentChanges(r.Cur, *r.Prev)
		}
		out = append(out, HistoryEntry{Kind: r.Kind, OccurredAt: r.OccurredAt, Source: r.Source, ObservedAt: r.ObservedAt,
			Artifact:   &HistoryArtifact{ID: r.ArtifactID, Name: r.ArtifactName, Kind: r.ArtifactKind, Deleted: r.ArtifactDeleted},
			Assignment: ch, key: r.Key})
	}
	return out
}

// snapshotOf copies the values; the group is resolved (or redacted) later by finishHistory.
func snapshotOf(v AssignmentValues) AssignmentSnapshot {
	return AssignmentSnapshot{groupExt: v.Group, TargetKind: v.TargetKind, Mode: v.Mode, Intent: v.Intent, FilterMode: v.FilterMode, FilterID: v.FilterID, FilterName: v.FilterName}
}

// assignmentChanges names the meaningful fields that differ.
func assignmentChanges(cur, prev AssignmentValues) []string {
	var out []string
	if cur.TargetKind != prev.TargetKind || !sameStr(cur.Group, prev.Group) {
		out = append(out, ChangeTarget)
	}
	if cur.Mode != prev.Mode {
		out = append(out, ChangeMode)
	}
	if cur.Intent != prev.Intent {
		out = append(out, ChangeIntent)
	}
	if cur.FilterMode != prev.FilterMode || !sameStr(cur.FilterID, prev.FilterID) {
		out = append(out, ChangeFilter)
	}
	return out
}

// finishHistory orders the merged entries newest first, cuts the page, and resolves group references (redacted
// without organization.directory.view). The assignment group ids are carried in the rows; membership group ids in
// groups (entry key -> external id).
func (s *Service) finishHistory(ctx context.Context, p Principal, entries []HistoryEntry, limit int, memberGroups ...map[string]*string) ([]HistoryEntry, string, error) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].OccurredAt.Equal(entries[j].OccurredAt) {
			return entries[i].OccurredAt.After(entries[j].OccurredAt)
		}
		return entries[i].key > entries[j].key
	})
	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[limit-1]
		next = EncodeHistoryCursor(HistoryCursor{At: last.OccurredAt, Key: last.key})
	}
	return entries, next, s.resolveHistoryGroups(ctx, p, entries, memberGroups...)
}

// resolveHistoryGroups fills the group references of the entries (redacted without organization.directory.view).
func (s *Service) resolveHistoryGroups(ctx context.Context, p Principal, entries []HistoryEntry, memberGroups ...map[string]*string) error {
	var exts []string
	for i := range entries {
		e := &entries[i]
		if e.Assignment != nil {
			if g := e.Assignment.Current.groupExt; g != nil {
				exts = append(exts, *g)
			}
			if e.Assignment.Previous != nil && e.Assignment.Previous.groupExt != nil {
				exts = append(exts, *e.Assignment.Previous.groupExt)
			}
		}
		for _, m := range memberGroups {
			if g := m[e.key]; g != nil {
				exts = append(exts, *g)
			}
		}
	}
	names, err := s.resolveNames(ctx, p, exts)
	if err != nil {
		return err
	}
	ref := func(g *string) *GroupRef {
		if g == nil {
			return nil
		}
		return names.ref(*g)
	}
	for i := range entries {
		e := &entries[i]
		if e.Assignment != nil {
			e.Assignment.Current.Group = ref(e.Assignment.Current.groupExt)
			if e.Assignment.Previous != nil {
				e.Assignment.Previous.Group = ref(e.Assignment.Previous.groupExt)
			}
		}
		for _, m := range memberGroups {
			if g := m[e.key]; g != nil {
				e.Group = ref(g)
			}
		}
	}
	return nil
}
