package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Operations a Change's status offers the caller (the UI shows them; every
// operation is authorized again by the backend).
const (
	OpUpdate   = "update"
	OpSubmit   = "submit"
	OpAssess   = "assess"
	OpSchedule = "schedule"
	OpStart    = "start"
	OpComplete = "complete"
	OpFail     = "fail"
	OpReview   = "review"
	OpClose    = "close"
	OpCancel   = "cancel"
	OpAddTask  = "add_task"
	OpAffected = "edit_affected"
)

// AllowedOperations lists the operations the status and the caller's authority
// allow, leaving out steps separation of duties forbids the caller.
func AllowedOperations(c Change, p Principal) []string {
	ops := []string{}
	mayAssess := p.UserID != c.RequesterID && !slices.Contains(c.Editors, p.UserID)
	emergencyApprover := c.EmergencyApprovedBy != nil && *c.EmergencyApprovedBy == p.UserID
	if p.Manage {
		switch c.Status {
		case StatusDraft:
			ops = append(ops, OpUpdate, OpAffected, OpSubmit, OpCancel)
		case StatusAssessment:
			ops = append(ops, OpUpdate, OpAffected)
			if mayAssess {
				ops = append(ops, OpAssess)
			}
			ops = append(ops, OpCancel)
		case StatusPendingApproval, StatusApproved:
			if c.Status == StatusApproved {
				ops = append(ops, OpSchedule)
			}
			ops = append(ops, OpCancel)
		case StatusScheduled:
			ops = append(ops, OpAddTask, OpCancel)
		case StatusInProgress:
			ops = append(ops, OpAddTask)
		case StatusCompleted, StatusFailed:
			if !emergencyApprover {
				ops = append(ops, OpReview)
				if !c.ReviewRequired() {
					ops = append(ops, OpClose)
				}
			}
		case StatusReview:
			if !emergencyApprover {
				ops = append(ops, OpClose)
			}
		}
	}
	if p.canExecute(c) {
		switch c.Status {
		case StatusScheduled:
			ops = append(ops, OpStart)
			if !p.Manage {
				ops = append(ops, OpAddTask)
			}
		case StatusInProgress:
			ops = append(ops, OpComplete, OpFail)
			if !p.Manage {
				ops = append(ops, OpAddTask)
			}
		}
	}
	return ops
}

// canRead reports whether the caller may read the Change: general read access,
// the requester, the owner, or somebody who is or was its approver. Anyone else
// gets ErrNotFound from the callers, never a hint that it exists.
func (s *Service) canRead(ctx context.Context, p Principal, c Change) (bool, error) {
	if p.readsAll() {
		return true, nil
	}
	if p.UserID == "" {
		return false, nil
	}
	if p.UserID == c.RequesterID || c.OwnerID != nil && *c.OwnerID == p.UserID {
		return true, nil
	}
	ok, err := s.approvals.CanView(ctx, c.ID, p.UserID)
	if err != nil {
		return false, fmt.Errorf("check approver: %w", err)
	}
	return ok, nil
}

func (s *Service) readable(ctx context.Context, p Principal, id string) (Change, error) {
	if !uuidPattern.MatchString(id) {
		return Change{}, ErrNotFound
	}
	c, err := s.store.Get(ctx, strings.ToLower(id))
	if err != nil {
		return Change{}, err
	}
	ok, err := s.canRead(ctx, p, c)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, ErrNotFound
	}
	return c, nil
}

// List lists Changes, newest first. Callers with changes.view, manage or
// execute see all Changes; everybody else sees only the Changes they requested
// or own.
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Result[Change], error) {
	if p.UserID == "" {
		return Result[Change]{}, ErrForbidden
	}
	if f.Status != "" && !oneOf(f.Status, Statuses) {
		return Result[Change]{}, invalid("unknown status")
	}
	if f.Risk != "" && !oneOf(f.Risk, Risks) {
		return Result[Change]{}, invalid("unknown risk")
	}
	if f.Kind != "" && !oneOf(f.Kind, Kinds) {
		return Result[Change]{}, invalid("unknown kind")
	}
	for _, id := range []string{f.OwnerID, f.RequesterID} {
		if id != "" && !uuidPattern.MatchString(id) {
			return Result[Change]{}, invalid("ids must be UUIDs")
		}
	}
	if (f.AffectedType == "") != (f.AffectedID == "") {
		return Result[Change]{}, invalid("affectedType and affectedId go together")
	}
	if f.AffectedType != "" {
		if !oneOf(f.AffectedType, AffectedTargets) {
			return Result[Change]{}, invalid("affectedType must be one of %s", strings.Join(AffectedTargets, ", "))
		}
		if !uuidPattern.MatchString(f.AffectedID) {
			return Result[Change]{}, invalid("ids must be UUIDs")
		}
		// Filtering by a resource type the caller cannot see would confirm links to hidden records.
		if p.hides(f.AffectedType) {
			return Result[Change]{Items: []Change{}}, nil
		}
		f.AffectedID = strings.ToLower(f.AffectedID)
		// Current links and the links of terminal Changes (ended with their terminal reason).
		ids, _, err := s.graph.SourcesByTarget(ctx, s.store.Q(), RelationshipOwner, NodeChange, RelAffects,
			relationships.Node{Type: f.AffectedType, ID: f.AffectedID}, []string{ReasonChangeClosed, ReasonChangeCancelled, ReasonChangeRejected}, relationships.MaxSources)
		if err != nil {
			return Result[Change]{}, fmt.Errorf("list changes affecting the resource: %w", err)
		}
		f.AffectedChangeIDs = ids
	}
	f.OwnerID, f.RequesterID = strings.ToLower(f.OwnerID), strings.ToLower(f.RequesterID)
	if f.WindowFrom != nil && f.WindowTo != nil && f.WindowTo.Before(*f.WindowFrom) {
		return Result[Change]{}, invalid("windowTo must not be before windowFrom")
	}
	if !p.readsAll() {
		f.OnlyUserID = p.UserID
	}
	f.Page = f.Page.Normalize()
	return s.store.List(ctx, f)
}

// UserNames resolves display names for ids already returned to the caller.
func (s *Service) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	return s.dir.UserNames(ctx, ids)
}

// AffectedResource is one affected resource as the caller may see it. Fields the
// caller may not see stay nil: Hidden means the caller may not see records of
// the type, and ID (and RelationshipID) are opaque placeholders (hidden-1, ...)
// that are numbered per response. Missing means the lookup ran and found no
// such record.
type AffectedResource struct {
	RelationshipID string
	Type           string
	ID             string
	Name           *string
	Reference      *string
	Status         *string
	Confidence     string
	Since          time.Time
	Missing        bool
	Hidden         bool
}

// TaskSummary summarizes the execution Tasks of a Change.
type TaskSummary struct {
	Total int
	Open  int
	Items []TaskInfo
}

// Detail is a Change with everything the detail view shows.
type Detail struct {
	Change     Change
	Affected   []AffectedResource
	Approvals  []ApprovalInfo
	Tasks      TaskSummary
	AllowedOps []string
}

// Get returns a Change with its affected resources (redacted by the caller's
// visibility of each record type), approvals, execution Tasks and the
// operations available to the caller. The requester, the owner, approvers and
// holders of changes.view|manage|execute may read it; for anyone else it does not exist.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	c, err := s.readable(ctx, p, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Change: c, AllowedOps: AllowedOperations(c, p)}
	if d.Affected, err = s.affected(ctx, p, c); err != nil {
		return Detail{}, err
	}
	if d.Approvals, err = s.approvals.ForSubject(ctx, c.ID); err != nil {
		return Detail{}, fmt.Errorf("load approvals: %w", err)
	}
	ids, err := s.store.TaskIDs(ctx, c.ID)
	if err != nil {
		return Detail{}, err
	}
	if len(ids) > 0 {
		tasks, err := s.tasks.Tasks(ctx, ids)
		if err != nil {
			return Detail{}, fmt.Errorf("load tasks: %w", err)
		}
		d.Tasks = TaskSummary{Total: len(tasks), Items: tasks}
		for _, t := range tasks {
			if !taskFinished(t.Status) {
				d.Tasks.Open++
			}
		}
	}
	return d, nil
}

// masker replaces the ids of records the caller may not see with opaque
// placeholders, numbered in order of first use.
type masker struct{ ids map[string]string }

func (m *masker) id(real string) string {
	if m.ids == nil {
		m.ids = map[string]string{}
	}
	ph, ok := m.ids[real]
	if !ok {
		ph = fmt.Sprintf("hidden-%d", len(m.ids)+1)
		m.ids[real] = ph
	}
	return ph
}

// affected lists the affected resources of a Change (at most MaxAffected): the
// current links, or the links that ended when a terminal Change terminated.
func (s *Service) affected(ctx context.Context, p Principal, c Change) ([]AffectedResource, error) {
	links, err := affectedLinks(ctx, s.graph, s.store.Q(), c)
	if err != nil {
		return nil, fmt.Errorf("list affected resources: %w", err)
	}
	out := make([]AffectedResource, 0, len(links))
	byType := map[string][]string{}
	for _, r := range links {
		byType[r.Target.Type] = append(byType[r.Target.Type], r.Target.ID)
	}
	svcs, vms, assets, locs := map[string]ServiceInfo{}, map[string]VMInfo{}, map[string]AssetInfo{}, map[string]string{}
	if ids := byType[NodeService]; len(ids) > 0 && !p.hides(NodeService) {
		if svcs, err = s.services.Lookup(ctx, ids); err != nil {
			return nil, fmt.Errorf("load services: %w", err)
		}
	}
	if ids := byType[NodeVM]; len(ids) > 0 && !p.hides(NodeVM) {
		if vms, err = s.infra.VMs(ctx, ids); err != nil {
			return nil, fmt.Errorf("load virtual machines: %w", err)
		}
	}
	if ids := byType[NodeAsset]; len(ids) > 0 && !p.hides(NodeAsset) {
		if assets, err = s.assets.Assets(ctx, ids); err != nil {
			return nil, fmt.Errorf("load assets: %w", err)
		}
	}
	if ids := byType[NodeLocation]; len(ids) > 0 && !p.hides(NodeLocation) {
		if locs, err = s.dir.LocationNames(ctx, ids); err != nil {
			return nil, fmt.Errorf("load locations: %w", err)
		}
	}
	var m masker
	for _, r := range links {
		a := AffectedResource{RelationshipID: r.ID, Type: r.Target.Type, ID: r.Target.ID, Confidence: r.Confidence, Since: r.ValidFrom}
		if p.hides(a.Type) {
			a.Hidden, a.RelationshipID, a.ID = true, m.id(r.ID), m.id(r.Target.ID)
			out = append(out, a)
			continue
		}
		switch a.Type {
		case NodeService:
			if v, ok := svcs[a.ID]; ok {
				a.Name, a.Reference, a.Status = &v.Name, &v.Reference, &v.Status
			} else {
				a.Missing = true
			}
		case NodeVM:
			if v, ok := vms[a.ID]; ok {
				a.Name, a.Status = &v.Name, &v.State
			} else {
				a.Missing = true
			}
		case NodeAsset:
			if v, ok := assets[a.ID]; ok {
				a.Reference, a.Status = &v.Reference, &v.Status
			} else {
				a.Missing = true
			}
		case NodeLocation:
			if name, ok := locs[a.ID]; ok {
				a.Name = &name
			} else {
				a.Missing = true
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// Transitions lists a Change's state history, oldest first.
func (s *Service) Transitions(ctx context.Context, p Principal, id string, page Page) (Result[Transition], error) {
	c, err := s.readable(ctx, p, id)
	if err != nil {
		return Result[Transition]{}, err
	}
	return s.store.Transitions(ctx, c.ID, page.Normalize())
}

// ImpactResult is the downstream impact of every affected resource of a Change.
// Each start carries its own walk (placeholder ids of hidden records are
// numbered per walk). Truncated is true when any walk was cut or the overall
// cap (1000 records) stopped the listing; Skipped counts affected resources
// whose type the caller may not see or that no longer exist.
type ImpactResult struct {
	Starts    []Impact
	Skipped   int
	Truncated bool
}

// Impact answers "what is affected if the affected resources of this Change
// are down". It runs the Services impact traversal (depth 1-6, default 6; at
// most 500 records per start, 1000 in all) for each affected resource and
// applies the Services redaction rules, so it needs services.view in addition
// to read access to the Change. One walk per user runs at a time.
func (s *Service) Impact(ctx context.Context, p Principal, id string, depth int) (ImpactResult, error) {
	c, err := s.readable(ctx, p, id)
	if err != nil {
		return ImpactResult{}, err
	}
	if !p.ServicesView {
		return ImpactResult{}, ErrForbidden
	}
	if depth < 0 || depth > relationships.MaxDepth {
		return ImpactResult{}, invalid("depth must be between 1 and %d", relationships.MaxDepth)
	}
	if !s.acquireImpact(p.UserID) {
		return ImpactResult{}, ErrImpactBusy
	}
	defer s.releaseImpact(p.UserID)
	links, err := affectedLinks(ctx, s.graph, s.store.Q(), c)
	if err != nil {
		return ImpactResult{}, fmt.Errorf("list affected resources: %w", err)
	}
	res := ImpactResult{Starts: []Impact{}}
	budget := ImpactNodeCap
	for _, r := range links {
		if p.hides(r.Target.Type) {
			res.Skipped++
			continue
		}
		if budget <= 0 {
			res.Truncated = true
			continue
		}
		in, err := s.services.Impact(ctx, p, r.Target.Type, r.Target.ID, depth)
		if err != nil {
			if err == ErrNotFound {
				res.Skipped++
				continue
			}
			return ImpactResult{}, err
		}
		if len(in.Nodes) > budget {
			in.Nodes, in.Truncated, in.NodeLimited = in.Nodes[:budget], true, true
		}
		budget -= len(in.Nodes)
		res.Truncated = res.Truncated || in.Truncated
		res.Starts = append(res.Starts, in)
	}
	return res, nil
}

func (s *Service) acquireImpact(user string) bool {
	s.impactMu.Lock()
	defer s.impactMu.Unlock()
	if s.impactRunning[user] {
		return false
	}
	s.impactRunning[user] = true
	return true
}

func (s *Service) releaseImpact(user string) {
	s.impactMu.Lock()
	delete(s.impactRunning, user)
	s.impactMu.Unlock()
}
