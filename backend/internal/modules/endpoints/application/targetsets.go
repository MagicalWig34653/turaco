package application

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Target Sets (F9 G2). Reads need deployments.view, deployments.manage or deployments.execute, or being an approver
// of a plan using the set (definition and counts only); changes need deployments.manage, and a definition naming
// Devices, groups or locations also endpoints.view. Explicit Device lists, example Devices and explanations need
// endpoints.view. Audit actions endpoints.target_set.<created|updated|archived> carry ids, the reference,
// counts and flags only; names, descriptions and filter values are never copied into audit.

type noApprovals struct{}

func (noApprovals) RequestInTx(context.Context, pgx.Tx, audit.Actor, string, string, string, Approver, []string) (string, error) {
	return "", ErrNoEligibleApprover
}
func (noApprovals) CancelBySubjectInTx(context.Context, pgx.Tx, audit.Actor, string, string) error {
	return nil
}
func (noApprovals) ForSubject(context.Context, string) ([]DeploymentApprovalInfo, error) {
	return nil, nil
}

type noApprovers struct{}

func (noApprovers) Permissions(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (noApprovers) TeamMemberIDs(context.Context, string) ([]string, error) { return nil, nil }
func (noApprovers) TeamIDsOfUser(context.Context, string) ([]string, error) { return nil, nil }

type noChanges struct{}

func (noChanges) Lookup(context.Context, []string) (map[string]ChangeWindow, error) {
	return map[string]ChangeWindow{}, nil
}

type noLocations struct{}

func (noLocations) Locations(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func cleanName(name string, max int) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && utf8.RuneCountInString(name) <= max && utf8.ValidString(name) && !safetext.ContainsUnsafe(name, false)
}

func cleanDescription(desc string) (*string, bool) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return nil, true
	}
	if utf8.RuneCountInString(desc) > 1000 || !utf8.ValidString(desc) || safetext.ContainsUnsafe(desc, true) {
		return nil, false
	}
	return &desc, true
}

// ownerOf returns the requested owner (the caller when empty); another User must exist.
func (s *Service) ownerOf(ctx context.Context, c Caller, owner string) (string, error) {
	if owner == "" || strings.EqualFold(owner, c.Actor.UserID) {
		return c.Actor.UserID, nil
	}
	if !validUUID(owner) {
		return "", invalid("ownerUserId must be a user id")
	}
	owner = strings.ToLower(owner)
	names, err := s.dir.UserNames(ctx, []string{owner})
	if err != nil {
		return "", err
	}
	if _, ok := names[owner]; !ok {
		return "", invalid("ownerUserId must name an existing user")
	}
	return owner, nil
}

func targetSetState(t TargetSet) map[string]any {
	return map[string]any{"version": t.Version, "allDevices": t.AllDevices, "highImpactReason": t.HighImpactReason, "archived": t.ArchivedAt != nil,
		"filters": definitionShape(t.Definition), "includeCount": len(t.Definition.IncludeDeviceIDs), "excludeCount": len(t.Definition.ExcludeDeviceIDs)}
}

// definitionShape names the clauses that are set (never their values).
func definitionShape(d TargetDefinition) []string {
	f := d.Filters
	var out []string
	for _, c := range []struct {
		name string
		set  bool
	}{{ClausePlatform, len(f.Platform) > 0}, {ClauseOSVersion, f.OSVersionPrefix != ""}, {ClauseOwnership, len(f.Ownership) > 0},
		{ClauseCompliance, len(f.Compliance) > 0}, {ClauseManufacturer, len(f.Manufacturer) > 0}, {ClauseModel, len(f.Model) > 0},
		{ClauseGroup, len(f.Groups) > 0}, {ClauseAssetLocation, len(f.AssetLocationIDs) > 0}} {
		if c.set {
			out = append(out, c.name)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *Service) targetSetPreamble(c Caller, p Principal) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.DeploymentsManage {
		return ErrForbidden
	}
	if c.Actor.UserID == "" {
		return invalid("target sets are changed by a person")
	}
	return nil
}

// setHighImpact returns why a definition is high impact: it selects all Devices, or it includes the nested groups of
// a root group (a Directory Group without a parent). A cut or failed directory lookup counts as a root (unknown
// groups only match their direct members and are not).
func (s *Service) setHighImpact(ctx context.Context, def TargetDefinition) (string, error) {
	if def.AllDevices() {
		return HighImpactAllDevices, nil
	}
	nested := def.nestedGroups()
	if len(nested) == 0 {
		return "", nil
	}
	gs, cut, err := s.dir.GroupsByExternalIDs(ctx, s.viewProvider, nested)
	if err != nil {
		return "", err
	}
	if cut {
		return HighImpactNestedRootGroup, nil
	}
	if len(gs) == 0 {
		return "", nil
	}
	ids := make([]string, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.ID)
	}
	edges, cut, err := s.dir.NestingUp(ctx, s.viewProvider, ids)
	if err != nil {
		return "", err
	}
	if cut {
		return HighImpactNestedRootGroup, nil
	}
	hasParent := map[string]bool{}
	for _, e := range edges {
		hasParent[e.ChildID] = true
	}
	for _, id := range ids {
		if !hasParent[id] {
			return HighImpactNestedRootGroup, nil
		}
	}
	return "", nil
}

func (s *Service) validTargetSetInput(ctx context.Context, c Caller, p Principal, in TargetSetInput) (TargetSet, error) {
	name, ok := cleanName(in.Name, 150)
	if !ok {
		return TargetSet{}, invalid("name must be 1-150 characters without control or invisible formatting characters")
	}
	desc, ok := cleanDescription(in.Description)
	if !ok {
		return TargetSet{}, invalid("description must be at most 1000 characters without control or invisible formatting characters")
	}
	def, err := NormalizeTargetDefinition(in.Definition)
	if err != nil {
		return TargetSet{}, err
	}
	if def.revealsDevices() && !p.canView() {
		return TargetSet{}, ErrForbidden
	}
	owner, err := s.ownerOf(ctx, c, in.OwnerUserID)
	if err != nil {
		return TargetSet{}, err
	}
	reason, err := s.setHighImpact(ctx, def)
	if err != nil {
		return TargetSet{}, err
	}
	return TargetSet{Name: name, Description: desc, OwnerUserID: owner, Definition: def, AllDevices: def.AllDevices(), HighImpactReason: strPtr(reason),
		UpdatedBy: c.Actor.UserID}, nil
}

// redactTargetSet removes the explicit Device lists for readers without endpoints.view (the counts stay).
func redactTargetSet(p Principal, t TargetSet) TargetSet {
	t.IncludeDeviceCount, t.ExcludeDeviceCount = len(t.Definition.IncludeDeviceIDs), len(t.Definition.ExcludeDeviceIDs)
	if !p.canView() {
		t.Definition.IncludeDeviceIDs, t.Definition.ExcludeDeviceIDs, t.DeviceListsRedacted = []string{}, []string{}, true
	}
	return t
}

func (s *Service) recordTargetSet(ctx context.Context, tx pgx.Tx, c Caller, op string, before *TargetSet, after TargetSet) error {
	var b any
	if before != nil {
		b = targetSetState(*before)
	}
	if err := audit.Record(ctx, tx, audit.Change{Action: "endpoints.target_set." + op, TargetType: "target_set", TargetID: after.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: b, After: targetSetState(after), Metadata: map[string]any{"reference": after.Reference}}); err != nil {
		return err
	}
	return publish(ctx, tx, c, EventTargetSetChanged, map[string]any{"targetSetId": after.ID, "operation": op, "version": after.Version, "allDevices": after.AllDevices})
}

// CreateTargetSet saves a Target Set. Requires deployments.manage.
func (s *Service) CreateTargetSet(ctx context.Context, c Caller, p Principal, in TargetSetInput) (TargetSet, error) {
	if err := s.targetSetPreamble(c, p); err != nil {
		return TargetSet{}, err
	}
	t, err := s.validTargetSetInput(ctx, c, p, in)
	if err != nil {
		return TargetSet{}, err
	}
	t.CreatedBy = c.Actor.UserID
	var out TargetSet
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if out, err = s.store.InsertTargetSetTx(ctx, tx, t); err != nil {
			return err
		}
		return s.recordTargetSet(ctx, tx, c, "created", nil, out)
	})
	return redactTargetSet(p, out), err
}

// UpdateTargetSet replaces name, description, owner and definition. A Target Set used by a plan in one of
// DeploymentBindingStatuses cannot change (ErrTargetSetInUse): the submitted plan binds its version. Requires
// deployments.manage and expectedVersion, and endpoints.view when the old or new definition names Devices, groups
// or locations.
func (s *Service) UpdateTargetSet(ctx context.Context, c Caller, p Principal, id string, expectedVersion *int, in TargetSetInput) (TargetSet, error) {
	if err := s.targetSetPreamble(c, p); err != nil {
		return TargetSet{}, err
	}
	if expectedVersion == nil {
		return TargetSet{}, invalid("expectedVersion is required")
	}
	if !validUUID(id) {
		return TargetSet{}, ErrNotFound
	}
	next, err := s.validTargetSetInput(ctx, c, p, in)
	if err != nil {
		return TargetSet{}, err
	}
	var out TargetSet
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTargetSetTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		if cur.ArchivedAt != nil {
			return &InvalidTransitionError{Operation: "update", From: "archived"}
		}
		if cur.Definition.revealsDevices() && !p.canView() {
			return ErrForbidden
		}
		inUse, err := s.store.TargetSetInUseTx(ctx, tx, cur.ID, DeploymentBindingStatuses)
		if err != nil {
			return err
		}
		if inUse {
			return ErrTargetSetInUse
		}
		upd := cur
		upd.Name, upd.Description, upd.OwnerUserID, upd.Definition, upd.AllDevices, upd.HighImpactReason, upd.UpdatedBy =
			next.Name, next.Description, next.OwnerUserID, next.Definition, next.AllDevices, next.HighImpactReason, c.Actor.UserID
		if out, err = s.store.UpdateTargetSetTx(ctx, tx, upd); err != nil {
			return err
		}
		return s.recordTargetSet(ctx, tx, c, "updated", &cur, out)
	})
	return redactTargetSet(p, out), err
}

// ArchiveTargetSet retires a Target Set; it can no longer be used by new rings and draft plans using it become
// invalid. A Target Set of a plan in one of DeploymentBindingStatuses cannot be archived. Requires
// deployments.manage and expectedVersion.
func (s *Service) ArchiveTargetSet(ctx context.Context, c Caller, p Principal, id string, expectedVersion *int) (TargetSet, error) {
	if err := s.targetSetPreamble(c, p); err != nil {
		return TargetSet{}, err
	}
	if expectedVersion == nil {
		return TargetSet{}, invalid("expectedVersion is required")
	}
	if !validUUID(id) {
		return TargetSet{}, ErrNotFound
	}
	var out TargetSet
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTargetSetTx(ctx, tx, strings.ToLower(id))
		if err != nil {
			return err
		}
		if *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		if cur.ArchivedAt != nil {
			return &InvalidTransitionError{Operation: "archive", From: "archived"}
		}
		inUse, err := s.store.TargetSetInUseTx(ctx, tx, cur.ID, DeploymentBindingStatuses)
		if err != nil {
			return err
		}
		if inUse {
			return ErrTargetSetInUse
		}
		upd := cur
		now := s.now()
		by := c.Actor.UserID
		upd.ArchivedAt, upd.ArchivedBy = &now, &by
		if out, err = s.store.UpdateTargetSetTx(ctx, tx, upd); err != nil {
			return err
		}
		return s.recordTargetSet(ctx, tx, c, "archived", &cur, out)
	})
	return redactTargetSet(p, out), err
}

// ListTargetSets lists Target Sets (without archived ones unless asked). Requires deployments read access.
func (s *Service) ListTargetSets(ctx context.Context, p Principal, f TargetSetFilter) (TargetSetResult, error) {
	if !p.canViewDeployments() {
		return TargetSetResult{}, ErrForbidden
	}
	f.Page = f.Page.Normalize()
	res, err := s.store.ListTargetSets(ctx, f)
	for i := range res.Items {
		res.Items[i] = redactTargetSet(p, res.Items[i])
	}
	return res, err
}

// maxApproverSetPlans bounds the plans checked when a plan approver reads a Target Set.
const maxApproverSetPlans = 20

// readTargetSet returns a Target Set the caller may read: with deployments read access, or as an approver of a plan
// that binds it (approverOnly; definition and counts only).
func (s *Service) readTargetSet(ctx context.Context, p Principal, id string) (t TargetSet, approverOnly bool, err error) {
	if !validUUID(id) {
		return TargetSet{}, false, ErrNotFound
	}
	id = strings.ToLower(id)
	if p.canViewDeployments() {
		t, err = s.store.GetTargetSet(ctx, id)
		return t, false, err
	}
	if p.UserID == "" {
		return TargetSet{}, false, ErrForbidden
	}
	plans, err := s.store.DeploymentsUsingTargetSet(ctx, id, DeploymentBindingStatuses, maxApproverSetPlans)
	if err != nil {
		return TargetSet{}, false, err
	}
	for _, dep := range plans {
		ok, err := s.isPlanApprover(ctx, dep, p.UserID)
		if err != nil {
			return TargetSet{}, false, err
		}
		if ok {
			t, err = s.store.GetTargetSet(ctx, id)
			return t, true, err
		}
	}
	return TargetSet{}, false, ErrForbidden
}

// GetTargetSet returns a Target Set. Requires deployments read access or being an approver of a plan using it.
func (s *Service) GetTargetSet(ctx context.Context, p Principal, id string) (TargetSet, error) {
	t, _, err := s.readTargetSet(ctx, p, id)
	if err != nil {
		return TargetSet{}, err
	}
	return redactTargetSet(p, t), nil
}

// beginEvaluation reserves the user's single evaluation slot (ErrEvaluationBusy when taken); the returned function
// releases it.
func (s *Service) beginEvaluation(userID string) (func(), error) {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	if s.evalBusy[userID] {
		return nil, ErrEvaluationBusy
	}
	s.evalBusy[userID] = true
	return func() {
		s.evalMu.Lock()
		delete(s.evalBusy, userID)
		s.evalMu.Unlock()
	}, nil
}

// evalBudget bounds the Devices one request scans and its wall time.
type evalBudget struct {
	scans    int
	deadline time.Time
}

func (s *Service) newBudget() *evalBudget {
	return &evalBudget{scans: s.evalScans, deadline: time.Now().Add(s.evalDeadline)}
}

func (b *evalBudget) exhausted() bool { return b.scans <= 0 || time.Now().After(b.deadline) }

// EvaluateTargetSet evaluates a Target Set now over the live Devices of the endpoint provider: the bounded count,
// counts by platform and compliance and a few example Devices. Examples need endpoints.view and deployments read
// access (approvers get counts only). One evaluation per user at a time. Requires deployments read access or being
// an approver of a plan using the set.
func (s *Service) EvaluateTargetSet(ctx context.Context, p Principal, id string) (TargetEvaluation, error) {
	t, approverOnly, err := s.readTargetSet(ctx, p, id)
	if err != nil {
		return TargetEvaluation{}, err
	}
	done, err := s.beginEvaluation(p.UserID)
	if err != nil {
		return TargetEvaluation{}, err
	}
	defer done()
	b := s.newBudget()
	ev, err := inSnapshot(ctx, s, func(ctx context.Context) (TargetEvaluation, error) { return s.evaluateDefinition(ctx, t.Definition, b) })
	if err != nil {
		return TargetEvaluation{}, err
	}
	if approverOnly || !p.canView() {
		ev.Examples, ev.ExamplesRedacted = []TargetExample{}, true
	}
	ev.deviceIDs = nil
	return ev, nil
}

// ExplainTargetSet says which clauses of a Target Set a live Device matches. Requires deployments read access and
// endpoints.view (it names a Device).
func (s *Service) ExplainTargetSet(ctx context.Context, p Principal, id, deviceID string) (TargetExplanation, error) {
	if !p.canViewDeployments() || !p.canView() {
		return TargetExplanation{}, ErrForbidden
	}
	t, _, err := s.readTargetSet(ctx, p, id)
	if err != nil {
		return TargetExplanation{}, err
	}
	if !validUUID(deviceID) {
		return TargetExplanation{}, ErrNotFound
	}
	return inSnapshot(ctx, s, func(ctx context.Context) (TargetExplanation, error) {
		ds, err := s.store.DevicesByIDs(ctx, []string{strings.ToLower(deviceID)})
		if err != nil {
			return TargetExplanation{}, err
		}
		if len(ds) == 0 || ds[0].DeletedObservedAt != nil || ds[0].Provider != s.viewProvider {
			return TargetExplanation{}, ErrNotFound
		}
		exts, incomplete, err := s.resolveTargetGroups(ctx, t.Definition.Filters.Groups)
		if err != nil {
			return TargetExplanation{}, err
		}
		r := resolveDefinition(t.Definition, exts)
		facts, err := s.targetFacts(ctx, r, ds)
		if err != nil {
			return TargetExplanation{}, err
		}
		matched, clauses := matchDevice(r, facts[0])
		return TargetExplanation{TargetSetID: t.ID, DeviceID: ds[0].ID, DeviceName: ds[0].Name, Matched: matched, Clauses: clauses,
			Incomplete: incomplete, EvaluatedAt: s.now()}, nil
	})
}

// resolveTargetGroups returns the provider group external ids a group clause matches: each listed group and, with
// includeNested, every Directory Group nested below it. incomplete is set when a directory lookup was cut.
func (s *Service) resolveTargetGroups(ctx context.Context, groups []TargetGroup) (map[string]bool, bool, error) {
	out := map[string]bool{}
	var nested []string
	for _, g := range groups {
		out[g.ExternalID] = true
		if g.IncludeNested {
			nested = append(nested, g.ExternalID)
		}
	}
	if len(nested) == 0 {
		return out, false, nil
	}
	gs, cut, err := s.dir.GroupsByExternalIDs(ctx, s.viewProvider, nested)
	if err != nil {
		return nil, false, err
	}
	var orgIDs []string
	for _, g := range gs {
		orgIDs = append(orgIDs, g.ID)
	}
	if len(orgIDs) == 0 {
		return out, cut, nil
	}
	_, exts, cutDown, err := s.descendants(ctx, orgIDs)
	if err != nil {
		return nil, false, err
	}
	for _, e := range exts {
		out[e] = true
	}
	return out, cut || cutDown, nil
}

// targetFacts loads the memberships and Asset locations the definition needs for a batch of Devices.
func (s *Service) targetFacts(ctx context.Context, r resolvedDefinition, devices []Device) ([]deviceFacts, error) {
	facts := make([]deviceFacts, len(devices))
	idx := make(map[string]int, len(devices))
	ids := make([]string, 0, len(devices))
	var assets []string
	for i, d := range devices {
		facts[i] = deviceFacts{Device: d, Groups: map[string]bool{}}
		idx[d.ID] = i
		ids = append(ids, d.ID)
		if d.AssetID != nil {
			assets = append(assets, *d.AssetID)
		}
	}
	if len(r.Filters.Groups) > 0 {
		ms, err := s.store.DeviceMemberships(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if i, ok := idx[m.DeviceID]; ok {
				facts[i].Groups[m.GroupExternalID] = true
			}
		}
	}
	if len(r.Filters.AssetLocationIDs) > 0 && len(assets) > 0 {
		locs, err := s.locations.Locations(ctx, uniq(assets))
		if err != nil {
			return nil, err
		}
		for i := range facts {
			if a := facts[i].Device.AssetID; a != nil {
				if l, ok := locs[strings.ToLower(*a)]; ok {
					facts[i].LocationID = &l
				}
			}
		}
	}
	return facts, nil
}

// evaluateDefinition scans the provider's live Devices in batches of targetBatch (at most MaxTargetScan) and keeps
// at most targetCap (MaxTargetDevices) matches. A definition without filters but with an explicit include list only
// reads the listed Devices. Every scanned Device is taken from the request budget b; an exhausted budget or passed
// deadline stops the scan and marks the evaluation incomplete.
func (s *Service) evaluateDefinition(ctx context.Context, def TargetDefinition, b *evalBudget) (TargetEvaluation, error) {
	ev := TargetEvaluation{ByPlatform: map[string]int{}, ByCompliance: map[string]int{}, EvaluatedAt: s.now(), Examples: []TargetExample{}}
	exts, incomplete, err := s.resolveTargetGroups(ctx, def.Filters.Groups)
	if err != nil {
		return TargetEvaluation{}, err
	}
	ev.Incomplete = incomplete
	if b.exhausted() {
		ev.Incomplete = true
		return ev, nil
	}
	r := resolveDefinition(def, exts)
	process := func(batch []Device) (bool, error) {
		facts, err := s.targetFacts(ctx, r, batch)
		if err != nil {
			return false, err
		}
		for _, f := range facts {
			if ok, _ := matchDevice(r, f); !ok {
				continue
			}
			if ev.Matched == s.targetCap {
				ev.Truncated = true
				return true, nil
			}
			ev.Matched++
			ev.ByPlatform[f.Device.OSPlatform]++
			ev.ByCompliance[f.Device.ComplianceState]++
			ev.deviceIDs = append(ev.deviceIDs, f.Device.ID)
			if len(ev.Examples) < MaxTargetExamples {
				ev.Examples = append(ev.Examples, TargetExample{DeviceID: f.Device.ID, Name: f.Device.Name, OSPlatform: f.Device.OSPlatform, ComplianceState: f.Device.ComplianceState})
			}
		}
		return false, nil
	}
	if def.Filters.empty() && len(def.IncludeDeviceIDs) > 0 {
		ds, err := s.store.DevicesByIDs(ctx, def.IncludeDeviceIDs)
		if err != nil {
			return TargetEvaluation{}, err
		}
		live := ds[:0]
		for _, d := range ds {
			if d.DeletedObservedAt == nil && d.Provider == s.viewProvider {
				live = append(live, d)
			}
		}
		ev.Scanned = len(live)
		b.scans -= len(live)
		_, err = process(live)
		return ev, err
	}
	after := ""
	for ev.Scanned < MaxTargetScan {
		if b.exhausted() {
			ev.Incomplete = true
			return ev, nil
		}
		page, err := s.store.LiveDevicesPage(ctx, s.viewProvider, after, targetBatch)
		if err != nil {
			return TargetEvaluation{}, err
		}
		if len(page) == 0 {
			return ev, nil
		}
		ev.Scanned += len(page)
		b.scans -= len(page)
		stop, err := process(page)
		if err != nil || stop {
			return ev, err
		}
		if len(page) < targetBatch {
			return ev, nil
		}
		after = page[len(page)-1].ID
	}
	// The scan limit was reached with a full last page: more Devices may exist.
	more, err := s.store.LiveDevicesPage(ctx, s.viewProvider, after, 1)
	if err != nil {
		return TargetEvaluation{}, err
	}
	ev.Truncated = ev.Truncated || len(more) > 0
	return ev, nil
}
