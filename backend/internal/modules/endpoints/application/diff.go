package application

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application/evaluation"
)

// Diff (F6 slice 4) compares two Devices or two Directory Groups with the evaluator and the stored assignments. It
// reads local data only, in one repository snapshot, and is bounded like the other views.

const diffBatch = 200

// mergeArtifactStreams unions two ascending artifact streams after a cursor. Each stream returns at most diffBatch
// artifacts; the union is complete up to the smaller last id of a full batch (bound), so the caller can continue
// after bound without missing an artifact of either stream. done is set when both streams ended.
func mergeArtifactStreams(left, right func(after string, limit int) ([]Artifact, error), after string) (arts []Artifact, bound string, done bool, err error) {
	l, err := left(after, diffBatch)
	if err != nil {
		return nil, "", false, err
	}
	r, err := right(after, diffBatch)
	if err != nil {
		return nil, "", false, err
	}
	if len(l) == diffBatch {
		bound = l[len(l)-1].ID
	}
	if len(r) == diffBatch && (bound == "" || r[len(r)-1].ID < bound) {
		bound = r[len(r)-1].ID
	}
	seen := map[string]bool{}
	for _, list := range [][]Artifact{l, r} {
		for _, a := range list {
			if (bound == "" || a.ID <= bound) && !seen[a.ID] {
				seen[a.ID] = true
				arts = append(arts, a)
			}
		}
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].ID < arts[j].ID })
	return arts, bound, bound == "", nil
}

// ---- Device vs Device ----

// DeviceDiff compares the management state of two Devices artifact by artifact: Assigned, Expected Applicable and
// Observed side by side. Needs management access and device access. Without assets.view the Users of the Devices
// are not looked up and user-targeted assignments stay unknown.
func (s *Service) DeviceDiff(ctx context.Context, p Principal, leftID, rightID string, f DiffFilter) (DeviceDiff, error) {
	if !p.canViewManagement() || !p.canView() {
		return DeviceDiff{}, ErrForbidden
	}
	if f.Kind != "" && !slices.Contains(ArtifactKinds, f.Kind) {
		return DeviceDiff{}, invalid("kind must be one of %s", strings.Join(ArtifactKinds, ", "))
	}
	if !validUUID(leftID) || !validUUID(rightID) {
		return DeviceDiff{}, ErrNotFound
	}
	if leftID == rightID {
		return DeviceDiff{}, invalid("otherDeviceId must differ from the device")
	}
	page := f.Page.Normalize()
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return DeviceDiff{}, ErrInvalidCursor
	}
	return inSnapshot(ctx, s, func(ctx context.Context) (DeviceDiff, error) {
		return s.deviceDiff(ctx, p, leftID, rightID, f, page)
	})
}

func (s *Service) deviceDiff(ctx context.Context, p Principal, leftID, rightID string, f DiffFilter, page Page) (DeviceDiff, error) {
	l, err := s.store.GetDevice(ctx, leftID)
	if err != nil {
		return DeviceDiff{}, err
	}
	r, err := s.store.GetDevice(ctx, rightID)
	if err != nil {
		return DeviceDiff{}, err
	}
	ec, err := s.newEvalCtx(ctx, []Device{l, r}, evalOpts{holders: p.AssetsView})
	if err != nil {
		return DeviceDiff{}, err
	}
	base := func(d Device) ReachQuery {
		return ReachQuery{Provider: s.viewProvider, DeviceID: d.ID, Kind: f.Kind, GroupExternalIDs: ec.groupsOf(d), AllDevices: true, AllUsers: true, AnyGroup: ec.anyGroupPossible(d)}
	}
	stream := func(q ReachQuery) func(string, int) ([]Artifact, error) {
		return func(after string, limit int) ([]Artifact, error) {
			q.AfterID, q.Limit = after, limit
			return s.store.ReachableArtifacts(ctx, q)
		}
	}
	out := DeviceDiff{LeftID: l.ID, LeftName: l.Name, RightID: r.ID, RightName: r.Name, Items: []DeviceDiffItem{}, Truncated: ec.truncated()}
	after, scanned := page.Cursor, 0
	for {
		arts, bound, done, err := mergeArtifactStreams(stream(base(l)), stream(base(r)), after)
		if err != nil {
			return DeviceDiff{}, err
		}
		if len(arts) > 0 {
			artIDs := make([]string, 0, len(arts))
			for _, a := range arts {
				artIDs = append(artIDs, a.ID)
			}
			assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
			if err != nil {
				return DeviceDiff{}, err
			}
			obsMap, err := s.observationMap(ctx, artIDs, []string{l.ID, r.ID})
			if err != nil {
				return DeviceDiff{}, err
			}
			names, err := s.resolveNames(ctx, p, assignmentGroups(assigns))
			if err != nil {
				return DeviceDiff{}, err
			}
			for _, a := range arts {
				scanned++
				item := DeviceDiffItem{Artifact: a, Left: diffSide(s.artifactStatus(ec, names, l, a, assigns[a.ID], obsMap)),
					Right: diffSide(s.artifactStatus(ec, names, r, a, assigns[a.ID], obsMap))}
				item.Class, item.Dimensions, item.Uncertain = classifyDevices(item.Left, item.Right)
				if f.OnlyDifferences && item.Class == DiffSame && !item.Uncertain {
					continue
				}
				if len(out.Items) == page.Limit {
					out.NextCursor = out.Items[len(out.Items)-1].Artifact.ID
					return out, nil
				}
				out.Items = append(out.Items, item)
			}
		}
		if done {
			return out, nil
		}
		after = bound
		if scanned >= MaxDiffScan {
			out.Truncated = true
			out.NextCursor = after
			if len(out.Items) == page.Limit {
				out.NextCursor = out.Items[len(out.Items)-1].Artifact.ID
			}
			return out, nil
		}
	}
}

func diffSide(st DeviceArtifactStatus) DiffSide {
	side := DiffSide{Assigned: st.Assigned, Assignments: st.Assignments, Expected: st.Expected, Observed: st.Observed, Mismatch: st.Mismatch}
	for _, t := range st.Assignments {
		if t.Mode == evaluation.ModeInclude && t.Match == string(evaluation.Unknown) {
			side.AssignedUnknown = true
		}
	}
	return side
}

// present means the artifact is in play for the Device: assigned, expected to apply or be excluded, or observed.
func (d DiffSide) present() bool {
	return d.Assigned || d.Observed != nil || d.Expected.Result == ExpectedApplicable || d.Expected.Result == ExpectedExcluded
}

// absent means the artifact is certainly not in play: not assigned, not observed and expected not applicable.
func (d DiffSide) absent() bool {
	return !d.Assigned && !d.AssignedUnknown && d.Observed == nil && d.Expected.Result == ExpectedNotApplicable
}

func (d DiffSide) assignedValue() string {
	switch {
	case d.Assigned:
		return "yes"
	case d.AssignedUnknown:
		return DimUnknown
	}
	return "no"
}

func (d DiffSide) observedValue() string {
	if d.Observed == nil {
		return ObservedNone
	}
	return d.Observed.State
}

// classifyDevices compares two sides. An unknown input never makes two sides equal or different: the dimension is
// unknown and the item uncertain.
func classifyDevices(l, r DiffSide) (class string, dims DiffDimensions, uncertain bool) {
	dims.Assigned = compareValue(l.assignedValue(), r.assignedValue())
	dims.Expected = compareValue(l.Expected.Result, r.Expected.Result)
	dims.Observed = compareValue(l.observedValue(), r.observedValue())
	uncertain = dims.Assigned == DimUnknown || dims.Expected == DimUnknown
	switch {
	case l.absent() && r.absent():
		return DiffSame, dims, false
	case l.present() && r.absent():
		return DiffOnlyLeft, dims, uncertain
	case r.present() && l.absent():
		return DiffOnlyRight, dims, uncertain
	case dims.Assigned == DimDifferent || dims.Expected == DimDifferent || dims.Observed == DimDifferent:
		return DiffDifferent, dims, uncertain
	}
	return DiffSame, dims, uncertain
}

func compareValue(l, r string) string {
	switch {
	case l == DimUnknown || r == DimUnknown:
		return DimUnknown
	case l == r:
		return DimSame
	}
	return DimDifferent
}

// ---- Directory Group vs Directory Group ----

// GroupDiff compares the artifacts that target two Directory Groups (directly or through a parent group): only on
// one side, on both with the same assignment properties, or on both with different mode, intent or filter. It
// compares configuration, not Devices, so it needs only management access and organization.directory.view.
// Artifacts that reach a group only through all_devices or all_users are not listed.
func (s *Service) GroupDiff(ctx context.Context, p Principal, leftID, rightID string, f DiffFilter) (GroupDiff, error) {
	if !p.canViewManagement() || !p.DirectoryView {
		return GroupDiff{}, ErrForbidden
	}
	if f.Kind != "" && !slices.Contains(ArtifactKinds, f.Kind) {
		return GroupDiff{}, invalid("kind must be one of %s", strings.Join(ArtifactKinds, ", "))
	}
	if !validUUID(leftID) || !validUUID(rightID) {
		return GroupDiff{}, ErrNotFound
	}
	if leftID == rightID {
		return GroupDiff{}, invalid("otherGroupId must differ from the group")
	}
	page := f.Page.Normalize()
	if page.Limit > MaxGroupViewArtifacts {
		page.Limit = MaxGroupViewArtifacts
	}
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return GroupDiff{}, ErrInvalidCursor
	}
	return inSnapshot(ctx, s, func(ctx context.Context) (GroupDiff, error) {
		return s.groupDiff(ctx, p, leftID, rightID, f, page)
	})
}

func (s *Service) groupDiff(ctx context.Context, p Principal, leftID, rightID string, f DiffFilter, page Page) (GroupDiff, error) {
	gs, _, err := s.dir.GroupsByIDs(ctx, s.viewProvider, []string{leftID, rightID})
	if err != nil {
		return GroupDiff{}, err
	}
	byID := map[string]DirectoryGroup{}
	for _, g := range gs {
		byID[g.ID] = g
	}
	lg, lok := byID[leftID]
	rg, rok := byID[rightID]
	if !lok || !rok {
		return GroupDiff{}, ErrNotFound
	}
	nesting := map[string][]string{}
	_, cut, err := s.nestingUp(ctx, []string{lg.ID, rg.ID}, nesting)
	if err != nil {
		return GroupDiff{}, err
	}
	reach := func(g DirectoryGroup) []string {
		return evaluation.NewClosure(evaluation.OriginDevice, []evaluation.Membership{{GroupExternalID: g.ExternalID}}, nesting).Groups()
	}
	lExts, rExts := reach(lg), reach(rg)
	stream := func(exts []string) func(string, int) ([]Artifact, error) {
		return func(after string, limit int) ([]Artifact, error) {
			return s.store.ArtifactsTargetingGroups(ctx, s.viewProvider, exts, after, limit)
		}
	}
	out := GroupDiff{Left: GroupDiffRef{ID: lg.ID, ExternalID: lg.ExternalID, Name: lg.Name}, Right: GroupDiffRef{ID: rg.ID, ExternalID: rg.ExternalID, Name: rg.Name},
		Items: []GroupDiffItem{}, Truncated: cut}
	after, scanned := page.Cursor, 0
	for {
		arts, bound, done, err := mergeArtifactStreams(stream(lExts), stream(rExts), after)
		if err != nil {
			return GroupDiff{}, err
		}
		if len(arts) > 0 {
			artIDs := make([]string, 0, len(arts))
			for _, a := range arts {
				artIDs = append(artIDs, a.ID)
			}
			assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
			if err != nil {
				return GroupDiff{}, err
			}
			names, err := s.resolveNames(ctx, p, assignmentGroups(assigns))
			if err != nil {
				return GroupDiff{}, err
			}
			names.byExt[lg.ExternalID], names.byExt[rg.ExternalID] = lg, rg
			for _, a := range arts {
				scanned++
				if f.Kind != "" && a.Kind != f.Kind {
					continue
				}
				item := GroupDiffItem{Artifact: a, Left: GroupDiffSide{Assignments: groupSide(names, lg, lExts, assigns[a.ID])},
					Right: GroupDiffSide{Assignments: groupSide(names, rg, rExts, assigns[a.ID])}}
				item.Class, item.Differences = classifyGroups(item.Left.Assignments, item.Right.Assignments)
				if f.OnlyDifferences && item.Class == DiffSame {
					continue
				}
				if len(out.Items) == page.Limit {
					out.NextCursor = out.Items[len(out.Items)-1].Artifact.ID
					return out, nil
				}
				out.Items = append(out.Items, item)
			}
		}
		if done {
			return out, nil
		}
		after = bound
		if scanned >= MaxDiffScan {
			out.Truncated = true
			out.NextCursor = after
			if len(out.Items) == page.Limit {
				out.NextCursor = out.Items[len(out.Items)-1].Artifact.ID
			}
			return out, nil
		}
	}
}

// groupSide lists the assignments that address the group (directly or through a parent).
func groupSide(names groupNames, g DirectoryGroup, exts []string, as []Assignment) []AssignedTarget {
	var out []AssignedTarget
	for _, a := range as {
		if a.TargetGroupExternalID == nil || !slices.Contains(exts, *a.TargetGroupExternalID) {
			continue
		}
		out = append(out, AssignedTarget{AssignmentID: a.ID, TargetKind: a.TargetKind, Group: names.ref(*a.TargetGroupExternalID), Mode: a.Mode, Intent: a.Intent,
			FilterMode: a.FilterMode, Filter: filterSummary(a), Match: string(evaluation.Yes), Nested: *a.TargetGroupExternalID != g.ExternalID,
			Source: a.Source, LastSyncedAt: a.LastSyncedAt})
	}
	return out
}

// classifyGroups compares the assignment properties that matter for the effect: mode, intent and the filter (its
// mode and which filter). Where an artifact reaches a group through several assignments the sets are compared.
func classifyGroups(l, r []AssignedTarget) (string, []string) {
	switch {
	case len(l) == 0 && len(r) == 0:
		return DiffSame, nil
	case len(r) == 0:
		return DiffOnlyLeft, nil
	case len(l) == 0:
		return DiffOnlyRight, nil
	}
	var diffs []string
	prop := map[string]func(AssignedTarget) string{
		"mode":   func(a AssignedTarget) string { return a.Mode },
		"intent": func(a AssignedTarget) string { return a.Intent },
		"filter": func(a AssignedTarget) string {
			if a.Filter == nil {
				return a.FilterMode
			}
			return a.FilterMode + ":" + a.Filter.ID
		},
	}
	for _, name := range []string{"mode", "intent", "filter"} {
		if !slices.Equal(valueSet(l, prop[name]), valueSet(r, prop[name])) {
			diffs = append(diffs, name)
		}
	}
	if len(diffs) == 0 {
		return DiffSame, nil
	}
	return DiffDifferent, diffs
}

func valueSet(as []AssignedTarget, f func(AssignedTarget) string) []string {
	var out []string
	for _, a := range as {
		out = append(out, f(a))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
