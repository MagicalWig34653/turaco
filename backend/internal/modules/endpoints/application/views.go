package application

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application/evaluation"
)

// The management views (F6 slice 3) compute Expected Applicability on demand from local normalized data:
// current assignments, Device group memberships, the Organization directory graph and the Asset holder of a
// Device. They never call the provider. The three states stay separate in every result: Assigned (configured
// assignments), Expected (Turaco-derived, with confidence, reasons and path) and Observed (the provider's latest
// result with freshness).

type emptyDirectory struct{}

func (emptyDirectory) GroupsByExternalIDs(context.Context, []string) ([]DirectoryGroup, error) {
	return nil, nil
}
func (emptyDirectory) GroupsByIDs(context.Context, []string) ([]DirectoryGroup, error) {
	return nil, nil
}
func (emptyDirectory) NestingUp(context.Context, []string) ([]NestingEdge, error)   { return nil, nil }
func (emptyDirectory) NestingDown(context.Context, []string) ([]NestingEdge, error) { return nil, nil }
func (emptyDirectory) UserMemberships(context.Context, []string) ([]UserMembership, error) {
	return nil, nil
}
func (emptyDirectory) GroupMembers(context.Context, []string, int) ([]UserMembership, error) {
	return nil, nil
}
func (emptyDirectory) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type noHolders struct{}

func (noHolders) UserHolders(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (noHolders) AssetsHeldByUsers(context.Context, []string, int) (map[string][]string, error) {
	return map[string][]string{}, nil
}

// ---- evaluation context ----

type evalCtx struct {
	now     time.Time
	nesting map[string][]string
	devMem  map[string][]evaluation.Membership
	userOf  map[string]string
	userMem map[string][]evaluation.Membership
}

func uniq(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func ids(ds []Device) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

// newEvalCtx loads everything the evaluator needs for the devices: their group memberships, their primary
// Users (the holder of the linked Asset), the Users' directory memberships and the group nesting.
func (s *Service) newEvalCtx(ctx context.Context, devices []Device) (*evalCtx, error) {
	c := &evalCtx{now: s.now(), nesting: map[string][]string{}, devMem: map[string][]evaluation.Membership{},
		userOf: map[string]string{}, userMem: map[string][]evaluation.Membership{}}
	mems, err := s.store.DeviceMemberships(ctx, ids(devices))
	if err != nil {
		return nil, err
	}
	var devExts []string
	for _, m := range mems {
		c.devMem[m.DeviceID] = append(c.devMem[m.DeviceID], evaluation.Membership{GroupExternalID: m.GroupExternalID, ObservedAt: m.LastSyncedAt})
		devExts = append(devExts, m.GroupExternalID)
	}
	var assetIDs []string
	deviceOfAsset := map[string][]string{}
	for _, d := range devices {
		if d.AssetID != nil {
			assetIDs = append(assetIDs, *d.AssetID)
			deviceOfAsset[*d.AssetID] = append(deviceOfAsset[*d.AssetID], d.ID)
		}
	}
	holders, err := s.holders.UserHolders(ctx, uniq(assetIDs))
	if err != nil {
		return nil, err
	}
	var users []string
	for asset, user := range holders {
		for _, dev := range deviceOfAsset[asset] {
			c.userOf[dev] = user
		}
		users = append(users, user)
	}
	users = uniq(users)
	userGroups := map[string][]string{} // user -> org group ids
	userObserved := map[string]map[string]time.Time{}
	var orgIDs []string
	if len(users) > 0 {
		ums, err := s.dir.UserMemberships(ctx, users)
		if err != nil {
			return nil, err
		}
		for _, m := range ums {
			userGroups[m.UserID] = append(userGroups[m.UserID], m.GroupID)
			if userObserved[m.UserID] == nil {
				userObserved[m.UserID] = map[string]time.Time{}
			}
			userObserved[m.UserID][m.GroupID] = m.ObservedAt
			orgIDs = append(orgIDs, m.GroupID)
		}
	}
	if len(devExts) > 0 {
		gs, err := s.dir.GroupsByExternalIDs(ctx, uniq(devExts))
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			orgIDs = append(orgIDs, g.ID)
		}
	}
	orgIDs = uniq(orgIDs)
	extOf, err := s.nestingUp(ctx, orgIDs, c.nesting)
	if err != nil {
		return nil, err
	}
	for u, gids := range userGroups {
		for _, gid := range gids {
			if ext, ok := extOf[gid]; ok {
				c.userMem[u] = append(c.userMem[u], evaluation.Membership{GroupExternalID: ext, ObservedAt: userObserved[u][gid]})
			}
		}
	}
	return c, nil
}

// nestingUp fills nesting (child external id -> parent external ids) for the ancestors of the groups and returns
// the external id of every group id it saw.
func (s *Service) nestingUp(ctx context.Context, orgIDs []string, nesting map[string][]string) (map[string]string, error) {
	extOf := map[string]string{}
	if len(orgIDs) == 0 {
		return extOf, nil
	}
	edges, err := s.dir.NestingUp(ctx, orgIDs)
	if err != nil {
		return nil, err
	}
	all := append([]string(nil), orgIDs...)
	for _, e := range edges {
		all = append(all, e.ChildID, e.ParentID)
	}
	gs, err := s.dir.GroupsByIDs(ctx, uniq(all))
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		extOf[g.ID] = g.ExternalID
	}
	for _, e := range edges {
		child, ok1 := extOf[e.ChildID]
		parent, ok2 := extOf[e.ParentID]
		if ok1 && ok2 && !slices.Contains(nesting[child], parent) {
			nesting[child] = append(nesting[child], parent)
		}
	}
	return extOf, nil
}

func evalDevice(d Device) evaluation.Device {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return evaluation.Device{Platform: d.OSPlatform, Ownership: d.Ownership, Manufacturer: str(d.Manufacturer), Model: str(d.Model),
		OSVersion: str(d.OSVersion), SyncedAt: d.LastSyncedAt}
}

func evalAssignments(as []Assignment) []evaluation.Assignment {
	out := make([]evaluation.Assignment, 0, len(as))
	for _, a := range as {
		ea := evaluation.Assignment{ID: a.ID, TargetKind: a.TargetKind, Mode: a.Mode, Intent: a.Intent, FilterMode: a.FilterMode, LastSyncedAt: a.LastSyncedAt}
		if a.TargetGroupExternalID != nil {
			ea.GroupExternalID = *a.TargetGroupExternalID
		}
		if a.Filter != nil {
			ea.Filter = &evaluation.Filter{ID: a.Filter.ID, Name: a.Filter.Name, Rule: a.Filter.Rule, Deleted: a.Filter.Deleted}
		}
		out = append(out, ea)
	}
	return out
}

func (c *evalCtx) evaluate(d Device, as []Assignment) evaluation.Result {
	user, known := c.userOf[d.ID]
	return evaluation.Evaluate(evaluation.Input{Now: c.now, Device: evalDevice(d), DeviceMemberships: c.devMem[d.ID],
		UserKnown: known, UserMemberships: c.userMem[user], Nesting: c.nesting, Assignments: evalAssignments(as)})
}

// groupsOf returns every group external id the Device or its User reaches, nesting included.
func (c *evalCtx) groupsOf(d Device) []string {
	out := evaluation.Groups(c.devMem[d.ID], c.nesting)
	if user, ok := c.userOf[d.ID]; ok {
		out = append(out, evaluation.Groups(c.userMem[user], c.nesting)...)
	}
	return uniq(out)
}

// ---- group references ----

type groupNames struct {
	visible bool
	byExt   map[string]DirectoryGroup
}

// resolveNames looks up Directory Groups for names, but only for callers who may view the directory.
func (s *Service) resolveNames(ctx context.Context, p Principal, exts []string) (groupNames, error) {
	g := groupNames{visible: p.DirectoryView, byExt: map[string]DirectoryGroup{}}
	if !g.visible || len(exts) == 0 {
		return g, nil
	}
	gs, err := s.dir.GroupsByExternalIDs(ctx, uniq(exts))
	if err != nil {
		return g, err
	}
	for _, d := range gs {
		g.byExt[d.ExternalID] = d
	}
	return g, nil
}

func (g groupNames) ref(ext string) *GroupRef {
	if !g.visible {
		return &GroupRef{Redacted: true}
	}
	e := ext
	r := &GroupRef{ExternalID: &e}
	if dg, ok := g.byExt[ext]; ok {
		n := dg.Name
		r.Name = &n
	}
	return r
}

func assignmentGroups(as map[string][]Assignment) []string {
	var out []string
	for _, list := range as {
		for _, a := range list {
			if a.TargetGroupExternalID != nil {
				out = append(out, *a.TargetGroupExternalID)
			}
		}
	}
	return out
}

func filterSummary(a Assignment) *FilterSummary { return a.Filter }

// assignedTargets lists the assignments the evaluator found to address (or possibly address) the subject.
func assignedTargets(g groupNames, as []Assignment, res evaluation.Result) []AssignedTarget {
	var out []AssignedTarget
	for i, a := range as {
		r := res.Assignments[i]
		if r.Target == evaluation.No {
			continue
		}
		t := AssignedTarget{AssignmentID: a.ID, TargetKind: a.TargetKind, Mode: a.Mode, Intent: a.Intent, FilterMode: a.FilterMode,
			Filter: filterSummary(a), Match: string(r.Target), Source: a.Source, LastSyncedAt: a.LastSyncedAt}
		if a.TargetGroupExternalID != nil {
			t.Group = g.ref(*a.TargetGroupExternalID)
		}
		for _, tr := range r.Traces {
			if !slices.Contains(t.Origins, tr.Origin) {
				t.Origins = append(t.Origins, tr.Origin)
			}
		}
		out = append(out, t)
	}
	return out
}

func expected(res evaluation.Result, at time.Time) ExpectedApplicability {
	return ExpectedApplicability{Result: res.Result, Confidence: res.Confidence, Reasons: res.Reasons, EvaluatedAt: at}
}

func (s *Service) observedState(o *Observation) *ObservedState {
	if o == nil {
		return nil
	}
	return &ObservedState{State: o.NormalizedState, RawStatus: o.RawStatus, Source: o.Source, ObservedAt: o.ObservedAt, LastSyncedAt: o.LastSyncedAt,
		Stale: s.now().Sub(o.LastSyncedAt) > StaleObservation}
}

func mismatchOf(as []AssignedTarget, exp string, obs *ObservedState) string {
	assigned := false
	for _, a := range as {
		if a.Mode == evaluation.ModeInclude && a.Match == string(evaluation.Yes) {
			assigned = true
		}
	}
	switch {
	case assigned && obs == nil && exp != ExpectedExcluded:
		return MismatchAssignedNotObserved
	case exp == ExpectedApplicable && obs != nil && obs.State != "applied":
		return MismatchExpectedNotApplied
	case obs != nil && slices.Contains([]string{"applied", "pending", "failed", "conflict"}, obs.State) &&
		(exp == ExpectedExcluded || exp == ExpectedNotApplicable):
		return MismatchObservedNotExpected
	}
	return ""
}

type obsKey struct{ artifact, device string }

func (s *Service) observationMap(ctx context.Context, artifactIDs, deviceIDs []string) (map[obsKey]Observation, error) {
	obs, err := s.store.ObservationsOf(ctx, artifactIDs, deviceIDs)
	if err != nil {
		return nil, err
	}
	m := make(map[obsKey]Observation, len(obs))
	for _, o := range obs {
		m[obsKey{o.ArtifactID, o.DeviceID}] = o
	}
	return m, nil
}

func (p Principal) canSeeDevices() bool { return p.canView() }

// ---- Device view ----

const deviceViewBatch = 200

// DeviceManagement lists the Management Artifacts relevant to a Device with Assigned, Expected Applicable and
// Observed kept apart. Needs management access and device access.
func (s *Service) DeviceManagement(ctx context.Context, p Principal, deviceID string, f DeviceManagementFilter) (DeviceManagement, error) {
	if !p.canViewManagement() || !p.canView() {
		return DeviceManagement{}, ErrForbidden
	}
	if f.Kind != "" && !slices.Contains(ArtifactKinds, f.Kind) {
		return DeviceManagement{}, invalid("kind must be one of %s", strings.Join(ArtifactKinds, ", "))
	}
	if f.State != "" && f.State != ObservedNone && !slices.Contains(ObservationStates, f.State) {
		return DeviceManagement{}, invalid("state must be one of %s, %s", strings.Join(ObservationStates, ", "), ObservedNone)
	}
	if f.Expected != "" && !slices.Contains(ExpectedResults, f.Expected) {
		return DeviceManagement{}, invalid("expected must be one of %s", strings.Join(ExpectedResults, ", "))
	}
	if f.Mismatch != "" && !slices.Contains(Mismatches, f.Mismatch) {
		return DeviceManagement{}, invalid("mismatch must be one of %s", strings.Join(Mismatches, ", "))
	}
	if !validUUID(deviceID) {
		return DeviceManagement{}, ErrNotFound
	}
	page := f.Page.Normalize()
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return DeviceManagement{}, ErrInvalidCursor
	}
	d, err := s.store.GetDevice(ctx, deviceID)
	if err != nil {
		return DeviceManagement{}, err
	}
	ec, err := s.newEvalCtx(ctx, []Device{d})
	if err != nil {
		return DeviceManagement{}, err
	}
	_, userKnown := ec.userOf[d.ID]
	q := ReachQuery{DeviceID: d.ID, Kind: f.Kind, GroupExternalIDs: ec.groupsOf(d), AllDevices: true, AllUsers: true, AnyGroup: !userKnown,
		AfterID: page.Cursor, Limit: deviceViewBatch}
	out := DeviceManagement{Items: []DeviceArtifactStatus{}}
	scanned := 0
	for {
		arts, err := s.store.ReachableArtifacts(ctx, q)
		if err != nil {
			return DeviceManagement{}, err
		}
		if len(arts) == 0 {
			return out, nil
		}
		artIDs := make([]string, 0, len(arts))
		for _, a := range arts {
			artIDs = append(artIDs, a.ID)
		}
		assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
		if err != nil {
			return DeviceManagement{}, err
		}
		obsMap, err := s.observationMap(ctx, artIDs, []string{d.ID})
		if err != nil {
			return DeviceManagement{}, err
		}
		names, err := s.resolveNames(ctx, p, assignmentGroups(assigns))
		if err != nil {
			return DeviceManagement{}, err
		}
		for _, a := range arts {
			res := ec.evaluate(d, assigns[a.ID])
			st := DeviceArtifactStatus{Artifact: a, Expected: expected(res, ec.now), Assignments: assignedTargets(names, assigns[a.ID], res)}
			for _, t := range st.Assignments {
				st.Assigned = st.Assigned || t.Match == string(evaluation.Yes)
			}
			if o, ok := obsMap[obsKey{a.ID, d.ID}]; ok {
				st.Observed = s.observedState(&o)
			}
			st.Mismatch = mismatchOf(st.Assignments, res.Result, st.Observed)
			q.AfterID = a.ID
			scanned++
			if !deviceItemMatches(f, st) {
				continue
			}
			out.Items = append(out.Items, st)
			if len(out.Items) == page.Limit {
				out.NextCursor = a.ID
				return out, nil
			}
		}
		if len(arts) < q.Limit {
			return out, nil
		}
		if scanned >= MaxViewScan {
			out.Truncated, out.NextCursor = true, q.AfterID
			return out, nil
		}
	}
}

func deviceItemMatches(f DeviceManagementFilter, st DeviceArtifactStatus) bool {
	if f.Expected != "" && st.Expected.Result != f.Expected {
		return false
	}
	if f.Mismatch != "" && st.Mismatch != f.Mismatch {
		return false
	}
	switch f.State {
	case "":
	case ObservedNone:
		return st.Observed == nil
	default:
		return st.Observed != nil && st.Observed.State == f.State
	}
	return true
}

// ---- Assignment Path ----

// AssignmentPath explains why an artifact is (not) expected to apply to a Device. Needs management access and
// device access; Directory Group and User details only with organization.directory.view (the User also needs
// assets.view, because it is derived from the Asset's holder).
func (s *Service) AssignmentPath(ctx context.Context, p Principal, deviceID, artifactID string) (AssignmentPathView, error) {
	if !p.canViewManagement() || !p.canView() {
		return AssignmentPathView{}, ErrForbidden
	}
	if !validUUID(deviceID) || !validUUID(artifactID) {
		return AssignmentPathView{}, ErrNotFound
	}
	d, err := s.store.GetDevice(ctx, deviceID)
	if err != nil {
		return AssignmentPathView{}, err
	}
	art, err := s.store.GetArtifact(ctx, artifactID)
	if err != nil {
		return AssignmentPathView{}, err
	}
	if art.DeletedObservedAt != nil {
		return AssignmentPathView{}, ErrNotFound
	}
	ec, err := s.newEvalCtx(ctx, []Device{d})
	if err != nil {
		return AssignmentPathView{}, err
	}
	assigns, err := s.store.CurrentAssignmentsOf(ctx, []string{art.ID})
	if err != nil {
		return AssignmentPathView{}, err
	}
	res := ec.evaluate(d, assigns[art.ID])
	var exts []string
	for _, st := range res.Path {
		if st.GroupExternalID != "" {
			exts = append(exts, st.GroupExternalID)
		}
	}
	names, err := s.resolveNames(ctx, p, append(exts, assignmentGroups(assigns)...))
	if err != nil {
		return AssignmentPathView{}, err
	}
	view := AssignmentPathView{Device: d, Artifact: art, Expected: expected(res, ec.now), Assignments: assignedTargets(names, assigns[art.ID], res)}
	for _, st := range res.Path {
		ps := PathStep{Kind: st.Kind, Origin: st.Origin, AssignmentID: st.AssignmentID, Mode: st.Mode, Intent: st.Intent, TargetKind: st.TargetKind,
			FilterResult: st.FilterResult, Result: st.Result}
		if st.GroupExternalID != "" {
			ps.Group = names.ref(st.GroupExternalID)
		}
		view.Path = append(view.Path, ps)
	}
	if user, ok := ec.userOf[d.ID]; ok {
		ref := &UserRef{Redacted: true}
		if p.DirectoryView && p.AssetsView {
			name := user
			if n, err := s.dir.UserNames(ctx, []string{user}); err == nil && n[user] != "" {
				name = n[user]
			}
			uid := user
			ref = &UserRef{ID: &uid, Name: &name}
		}
		view.User = ref
	}
	obsMap, err := s.observationMap(ctx, []string{art.ID}, []string{d.ID})
	if err != nil {
		return AssignmentPathView{}, err
	}
	if o, ok := obsMap[obsKey{art.ID, d.ID}]; ok {
		view.Observed = s.observedState(&o)
	}
	return view, nil
}

// ---- counts over candidate devices ----

// evaluateDevices evaluates the artifact on the devices and counts results and observed states.
func (s *Service) evaluateDevices(ec *evalCtx, devices []Device, as []Assignment, artifactID string, obs map[obsKey]Observation, candidatesTruncated bool) Evaluation {
	ev := Evaluation{Shown: true, Evaluated: len(devices), Truncated: candidatesTruncated, Expected: ResultCounts{}, Observed: map[string]int{}}
	perResult := map[string]int{}
	for _, d := range devices {
		res := ec.evaluate(d, as)
		ev.Expected[res.Result]++
		state := ObservedNone
		if o, ok := obs[obsKey{artifactID, d.ID}]; ok {
			state = o.NormalizedState
		}
		ev.Observed[state]++
		if len(ev.Examples) < MaxViewExamples && perResult[res.Result] < 3 {
			perResult[res.Result]++
			ev.Examples = append(ev.Examples, DeviceExample{DeviceID: d.ID, Name: d.Name, Result: res.Result, Confidence: res.Confidence, Observed: state})
		}
	}
	return ev
}

func sortDevices(ds []Device) {
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
}

// mergeDevices unions device lists by id, ordered, and cuts at MaxEvalDevices.
func mergeDevices(lists ...[]Device) (out []Device, truncated bool) {
	seen := map[string]bool{}
	for _, l := range lists {
		for _, d := range l {
			if !seen[d.ID] {
				seen[d.ID] = true
				out = append(out, d)
			}
		}
	}
	sortDevices(out)
	if len(out) > MaxEvalDevices {
		out, truncated = out[:MaxEvalDevices], true
	}
	return out, truncated
}

// descendants returns the organization group ids and external ids of the groups and everything nested below them.
func (s *Service) descendants(ctx context.Context, orgIDs []string) (allIDs, exts []string, err error) {
	edges, err := s.dir.NestingDown(ctx, orgIDs)
	if err != nil {
		return nil, nil, err
	}
	allIDs = append(allIDs, orgIDs...)
	for _, e := range edges {
		allIDs = append(allIDs, e.ChildID, e.ParentID)
	}
	allIDs = uniq(allIDs)
	gs, err := s.dir.GroupsByIDs(ctx, allIDs)
	if err != nil {
		return nil, nil, err
	}
	for _, g := range gs {
		exts = append(exts, g.ExternalID)
	}
	return allIDs, uniq(exts), nil
}

// candidateDevices returns the live Devices that belong to the groups (external ids) directly or whose primary
// User is a member of one of the groups (orgIDs); the latter only when withUsers.
func (s *Service) candidateDevices(ctx context.Context, exts, orgIDs []string, withUsers bool) ([]Device, bool, error) {
	byGroup, err := s.store.LiveDevicesInGroups(ctx, exts, MaxEvalDevices+1)
	if err != nil {
		return nil, false, err
	}
	var byUser []Device
	if withUsers && len(orgIDs) > 0 {
		members, err := s.dir.GroupMembers(ctx, orgIDs, MaxEvalDevices+1)
		if err != nil {
			return nil, false, err
		}
		var users []string
		for _, m := range members {
			users = append(users, m.UserID)
		}
		if users = uniq(users); len(users) > 0 {
			held, err := s.holders.AssetsHeldByUsers(ctx, users, MaxEvalDevices+1)
			if err != nil {
				return nil, false, err
			}
			var assets []string
			for _, a := range held {
				assets = append(assets, a...)
			}
			if byUser, err = s.store.LiveDevicesByAssetIDs(ctx, uniq(assets), MaxEvalDevices+1); err != nil {
				return nil, false, err
			}
		}
	}
	out, trunc := mergeDevices(byGroup, byUser)
	return out, trunc || len(byGroup) > MaxEvalDevices || len(byUser) > MaxEvalDevices, nil
}

// ---- Directory Group view ----

// GroupManagement lists the artifacts targeting a Directory Group (directly or through a parent group) with the
// bounded expected effect on the Devices that belong to the group. It needs management access and
// organization.directory.view; the Device counts and examples additionally need device access, and Devices of the
// group's User members assets.view.
func (s *Service) GroupManagement(ctx context.Context, p Principal, groupID string, page Page) (GroupManagement, error) {
	if !p.canViewManagement() || !p.DirectoryView {
		return GroupManagement{}, ErrForbidden
	}
	if !validUUID(groupID) {
		return GroupManagement{}, ErrNotFound
	}
	page = page.Normalize()
	if page.Limit > MaxGroupViewArtifacts {
		page.Limit = MaxGroupViewArtifacts
	}
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return GroupManagement{}, ErrInvalidCursor
	}
	gs, err := s.dir.GroupsByIDs(ctx, []string{groupID})
	if err != nil {
		return GroupManagement{}, err
	}
	if len(gs) == 0 {
		return GroupManagement{}, ErrNotFound
	}
	g := gs[0]
	nesting := map[string][]string{}
	extOf, err := s.nestingUp(ctx, []string{g.ID}, nesting)
	if err != nil {
		return GroupManagement{}, err
	}
	targetExts := evaluation.Groups([]evaluation.Membership{{GroupExternalID: g.ExternalID}}, nesting)
	_ = extOf
	arts, err := s.store.ArtifactsTargetingGroups(ctx, targetExts, page.Cursor, page.Limit+1)
	if err != nil {
		return GroupManagement{}, err
	}
	out := GroupManagement{GroupID: g.ID, ExternalID: g.ExternalID, Name: g.Name, Items: []GroupArtifact{}}
	if len(arts) > page.Limit {
		arts = arts[:page.Limit]
		out.NextCursor = arts[page.Limit-1].ID
	}
	var devices []Device
	var ec *evalCtx
	showDevices := p.canSeeDevices()
	if showDevices {
		orgIDs, exts, err := s.descendants(ctx, []string{g.ID})
		if err != nil {
			return GroupManagement{}, err
		}
		devices, out.CandidatesTruncated, err = s.candidateDevices(ctx, exts, orgIDs, p.AssetsView)
		if err != nil {
			return GroupManagement{}, err
		}
		out.CandidateDevices = len(devices)
		if ec, err = s.newEvalCtx(ctx, devices); err != nil {
			return GroupManagement{}, err
		}
	}
	artIDs := make([]string, 0, len(arts))
	for _, a := range arts {
		artIDs = append(artIDs, a.ID)
	}
	assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
	if err != nil {
		return GroupManagement{}, err
	}
	var obs map[obsKey]Observation
	if showDevices {
		if obs, err = s.observationMap(ctx, artIDs, ids(devices)); err != nil {
			return GroupManagement{}, err
		}
	}
	names := groupNames{visible: true, byExt: map[string]DirectoryGroup{g.ExternalID: g}}
	if more, err := s.resolveNames(ctx, p, assignmentGroups(assigns)); err == nil {
		names = more
	} else {
		return GroupManagement{}, err
	}
	for _, a := range arts {
		item := GroupArtifact{Artifact: a}
		for _, as := range assigns[a.ID] {
			if as.TargetGroupExternalID == nil || !slices.Contains(targetExts, *as.TargetGroupExternalID) {
				continue
			}
			item.Assignments = append(item.Assignments, AssignedTarget{AssignmentID: as.ID, TargetKind: as.TargetKind, Group: names.ref(*as.TargetGroupExternalID),
				Mode: as.Mode, Intent: as.Intent, FilterMode: as.FilterMode, Filter: filterSummary(as), Match: string(evaluation.Yes),
				Nested: *as.TargetGroupExternalID != g.ExternalID, Source: as.Source, LastSyncedAt: as.LastSyncedAt})
		}
		if showDevices {
			item.Evaluation = s.evaluateDevices(ec, devices, assigns[a.ID], a.ID, obs, out.CandidatesTruncated)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ---- User view ----

// UserManagement lists the artifacts that reach a User through User targeting (their groups, nesting included, or
// all users) and, separately, the expected and observed result on each Device of the User. It needs management
// access and organization.directory.view; the Devices need device access and assets.view.
func (s *Service) UserManagement(ctx context.Context, p Principal, userID string, page Page) (UserManagement, error) {
	if !p.canViewManagement() || !p.DirectoryView {
		return UserManagement{}, ErrForbidden
	}
	if !validUUID(userID) {
		return UserManagement{}, ErrNotFound
	}
	page = page.Normalize()
	if page.Limit > MaxGroupViewArtifacts {
		page.Limit = MaxGroupViewArtifacts
	}
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return UserManagement{}, ErrInvalidCursor
	}
	names, err := s.dir.UserNames(ctx, []string{userID})
	if err != nil {
		return UserManagement{}, err
	}
	name, exists := names[userID]
	if !exists {
		return UserManagement{}, ErrNotFound
	}
	out := UserManagement{UserID: userID, Name: name, Items: []UserArtifact{}, DevicesShown: p.canSeeDevices() && p.AssetsView}

	ums, err := s.dir.UserMemberships(ctx, []string{userID})
	if err != nil {
		return UserManagement{}, err
	}
	var gids []string
	for _, m := range ums {
		gids = append(gids, m.GroupID)
	}
	nesting := map[string][]string{}
	extOf, err := s.nestingUp(ctx, uniq(gids), nesting)
	if err != nil {
		return UserManagement{}, err
	}
	var direct []evaluation.Membership
	for _, m := range ums {
		if ext, ok := extOf[m.GroupID]; ok {
			direct = append(direct, evaluation.Membership{GroupExternalID: ext, ObservedAt: m.ObservedAt})
		}
	}
	userGroups := evaluation.Groups(direct, nesting)
	arts, err := s.store.ReachableArtifacts(ctx, ReachQuery{GroupExternalIDs: userGroups, AllUsers: true, AfterID: page.Cursor, Limit: page.Limit + 1})
	if err != nil {
		return UserManagement{}, err
	}
	if len(arts) > page.Limit {
		arts = arts[:page.Limit]
		out.NextCursor = arts[page.Limit-1].ID
	}
	artIDs := make([]string, 0, len(arts))
	for _, a := range arts {
		artIDs = append(artIDs, a.ID)
	}
	assigns, err := s.store.CurrentAssignmentsOf(ctx, artIDs)
	if err != nil {
		return UserManagement{}, err
	}
	gn, err := s.resolveNames(ctx, p, assignmentGroups(assigns))
	if err != nil {
		return UserManagement{}, err
	}

	var devices []Device
	var ec *evalCtx
	var obs map[obsKey]Observation
	if out.DevicesShown {
		held, err := s.holders.AssetsHeldByUsers(ctx, []string{userID}, MaxUserViewDevices+1)
		if err != nil {
			return UserManagement{}, err
		}
		devices, err = s.store.LiveDevicesByAssetIDs(ctx, uniq(held[userID]), MaxUserViewDevices+1)
		if err != nil {
			return UserManagement{}, err
		}
		if len(devices) > MaxUserViewDevices {
			devices, out.DevicesTruncated = devices[:MaxUserViewDevices], true
		}
		if ec, err = s.newEvalCtx(ctx, devices); err != nil {
			return UserManagement{}, err
		}
		if obs, err = s.observationMap(ctx, artIDs, ids(devices)); err != nil {
			return UserManagement{}, err
		}
	}
	now := s.now()
	for _, a := range arts {
		// User targeting only: device targets and device filters do not describe the User.
		var userAs []Assignment
		for _, as := range assigns[a.ID] {
			if as.TargetKind == "all_devices" {
				continue
			}
			c := as
			c.Filter, c.FilterMode, c.FilterID = nil, "none", nil
			userAs = append(userAs, c)
		}
		res := evaluation.Evaluate(evaluation.Input{Now: now, UserKnown: true, UserMemberships: direct, Nesting: nesting, Assignments: evalAssignments(userAs)})
		item := UserArtifact{Artifact: a, UserResult: res.Result}
		for _, t := range assignedTargets(gn, userAs, res) {
			// Show the filter the provider configured even though it does not take part in the User result.
			for _, orig := range assigns[a.ID] {
				if orig.ID == t.AssignmentID {
					t.FilterMode, t.Filter = orig.FilterMode, filterSummary(orig)
				}
			}
			item.Targeting = append(item.Targeting, t)
		}
		for _, d := range devices {
			dres := ec.evaluate(d, assigns[a.ID])
			ud := UserDevice{DeviceID: d.ID, Name: d.Name, Expected: expected(dres, ec.now)}
			if o, ok := obs[obsKey{a.ID, d.ID}]; ok {
				ud.Observed = s.observedState(&o)
			}
			item.Devices = append(item.Devices, ud)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// ---- reverse lookup ----

// ArtifactTargets shows where an artifact applies: its assigned targets, the expected effect on a bounded set of
// candidate Devices (counts by result and capped examples) and the observed counts by state. Needs management
// access; the expected effect needs device access; Devices of group User members need assets.view.
func (s *Service) ArtifactTargets(ctx context.Context, p Principal, artifactID string) (ArtifactTargets, error) {
	if !p.canViewManagement() {
		return ArtifactTargets{}, ErrForbidden
	}
	if !validUUID(artifactID) {
		return ArtifactTargets{}, ErrNotFound
	}
	art, err := s.store.GetArtifact(ctx, artifactID)
	if err != nil {
		return ArtifactTargets{}, err
	}
	if art.DeletedObservedAt != nil {
		return ArtifactTargets{}, ErrNotFound
	}
	assigns, err := s.store.CurrentAssignmentsOf(ctx, []string{art.ID})
	if err != nil {
		return ArtifactTargets{}, err
	}
	as := assigns[art.ID]
	gn, err := s.resolveNames(ctx, p, assignmentGroups(assigns))
	if err != nil {
		return ArtifactTargets{}, err
	}
	out := ArtifactTargets{Artifact: art, Assignments: []AssignedTarget{}}
	for _, a := range as {
		t := AssignedTarget{AssignmentID: a.ID, TargetKind: a.TargetKind, Mode: a.Mode, Intent: a.Intent, FilterMode: a.FilterMode,
			Filter: filterSummary(a), Match: string(evaluation.Yes), Source: a.Source, LastSyncedAt: a.LastSyncedAt}
		if a.TargetGroupExternalID != nil {
			t.Group = gn.ref(*a.TargetGroupExternalID)
		}
		out.Assignments = append(out.Assignments, t)
	}
	if out.ObservedTotal, err = s.store.ArtifactObservationCounts(ctx, art.ID); err != nil {
		return ArtifactTargets{}, err
	}
	if !p.canSeeDevices() {
		return out, nil
	}

	// Candidate Devices: everything for all_devices/all_users, else the members of the including groups.
	var devices []Device
	truncated := false
	var includeExts []string
	everyone := false
	for _, a := range as {
		switch {
		case a.Mode != evaluation.ModeInclude:
		case a.TargetKind == "group" && a.TargetGroupExternalID != nil:
			includeExts = append(includeExts, *a.TargetGroupExternalID)
		default:
			everyone = true
		}
	}
	if everyone {
		first, err := s.store.LiveDevicesFirst(ctx, MaxEvalDevices+1)
		if err != nil {
			return ArtifactTargets{}, err
		}
		devices, truncated = mergeDevices(first)
		truncated = truncated || len(first) > MaxEvalDevices
	} else if len(includeExts) > 0 {
		roots, err := s.dir.GroupsByExternalIDs(ctx, uniq(includeExts))
		if err != nil {
			return ArtifactTargets{}, err
		}
		var rootIDs []string
		for _, g := range roots {
			rootIDs = append(rootIDs, g.ID)
		}
		orgIDs, exts, err := s.descendants(ctx, uniq(rootIDs))
		if err != nil {
			return ArtifactTargets{}, err
		}
		devices, truncated, err = s.candidateDevices(ctx, uniq(append(exts, includeExts...)), orgIDs, p.AssetsView)
		if err != nil {
			return ArtifactTargets{}, err
		}
	}
	ec, err := s.newEvalCtx(ctx, devices)
	if err != nil {
		return ArtifactTargets{}, err
	}
	obs, err := s.observationMap(ctx, []string{art.ID}, ids(devices))
	if err != nil {
		return ArtifactTargets{}, err
	}
	out.Evaluation = s.evaluateDevices(ec, devices, as, art.ID, obs, truncated)
	return out, nil
}
