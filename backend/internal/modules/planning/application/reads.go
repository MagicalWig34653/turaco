package application

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// Operations an Initiative's status offers the caller (the UI shows them;
// every operation is authorized again by the backend).
const (
	OpUpdate        = "update"
	OpStartPlanning = "start_planning"
	OpPropose       = "propose"
	OpActivate      = "activate"
	OpHold          = "hold"
	OpResume        = "resume"
	OpComplete      = "complete"
	OpCancel        = "cancel"
	OpEditItems     = "edit_items"
	OpMilestones    = "edit_milestones"
)

// AllowedOperations lists the operations the status and the caller's authority allow.
func AllowedOperations(i Initiative, p Principal) []string {
	ops := []string{}
	if !p.Manage {
		return ops
	}
	if oneOf(i.Status, editableStatuses) {
		ops = append(ops, OpUpdate, OpEditItems, OpMilestones)
	}
	switch i.Status {
	case StatusIdea:
		ops = append(ops, OpStartPlanning)
	case StatusPlanning:
		ops = append(ops, OpPropose)
	case StatusApproved:
		ops = append(ops, OpActivate)
	case StatusActive:
		ops = append(ops, OpHold, OpComplete)
	case StatusOnHold:
		ops = append(ops, OpResume)
	}
	if i.Status != StatusCompleted && i.Status != StatusCancelled {
		ops = append(ops, OpCancel)
	}
	return ops
}

// canRead reports whether the caller may read the Initiative: planning.view or
// planning.manage, its owner, or somebody who is or was its approver. Anyone
// else gets ErrNotFound from the callers, never a hint that it exists.
func (s *Service) canRead(ctx context.Context, p Principal, i Initiative) (bool, error) {
	if p.readsAll() {
		return true, nil
	}
	if p.UserID == "" {
		return false, nil
	}
	if p.UserID == i.OwnerID {
		return true, nil
	}
	ok, err := s.approvals.CanView(ctx, i.ID, p.UserID)
	if err != nil {
		return false, fmt.Errorf("check approver: %w", err)
	}
	return ok, nil
}

func (s *Service) readable(ctx context.Context, p Principal, id string) (Initiative, error) {
	if !uuidPattern.MatchString(id) {
		return Initiative{}, ErrNotFound
	}
	i, err := s.store.Get(ctx, strings.ToLower(id))
	if err != nil {
		return Initiative{}, err
	}
	ok, err := s.canRead(ctx, p, i)
	if err != nil {
		return Initiative{}, err
	}
	if !ok {
		return Initiative{}, ErrNotFound
	}
	return i, nil
}

// List lists Initiatives, newest first. Callers with planning.view or
// planning.manage see all Initiatives; everybody else only the ones they own.
func (s *Service) List(ctx context.Context, p Principal, f Filter) (Result[Initiative], error) {
	if p.UserID == "" {
		return Result[Initiative]{}, ErrForbidden
	}
	if f.Status != "" && !oneOf(f.Status, Statuses) {
		return Result[Initiative]{}, invalid("unknown status")
	}
	if f.OwnerID != "" {
		if !uuidPattern.MatchString(f.OwnerID) {
			return Result[Initiative]{}, invalid("ids must be UUIDs")
		}
		f.OwnerID = strings.ToLower(f.OwnerID)
	}
	f.Query = strings.TrimSpace(f.Query)
	if utf8.RuneCountInString(f.Query) > maxQuery || !utf8.ValidString(f.Query) {
		return Result[Initiative]{}, invalid("q must be at most %d characters", maxQuery)
	}
	f.TargetFrom, f.TargetTo = dayPtr(f.TargetFrom), dayPtr(f.TargetTo)
	if f.TargetFrom != nil && f.TargetTo != nil && f.TargetTo.Before(*f.TargetFrom) {
		return Result[Initiative]{}, invalid("targetTo must not be before targetFrom")
	}
	if !p.readsAll() {
		f.OnlyOwnerID = p.UserID
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

// Item is one included record as the caller may see it. Fields the caller may
// not see stay nil: Hidden means the caller may not see records of the type,
// and ID (and RelationshipID) are opaque placeholders (hidden-1, ...) numbered
// per response. Missing means the lookup ran and found no such record.
type Item struct {
	RelationshipID string
	Type           string
	ID             string
	Reference      *string
	Title          *string
	Status         *string
	Since          time.Time
	Missing        bool
	Hidden         bool
}

// ItemPage is one page of included records.
type ItemPage struct {
	Items      []Item
	NextCursor string
}

// Progress summarizes an Initiative. Counts by status are nil when the caller
// may not see records of the type (the number of such records is still in
// Items, which counts every current link).
type Progress struct {
	Items              int
	ChangesByStatus    map[string]int
	TasksByStatus      map[string]int
	RequestsByStatus   map[string]int
	MilestonesDone     int
	MilestonesTotal    int
	MilestonesOverdue  int
	ItemsLimitExceeded bool
}

// Detail is an Initiative with everything the detail view shows.
type Detail struct {
	Initiative Initiative
	Items      ItemPage
	Milestones []Milestone
	Approvals  []ApprovalInfo
	Progress   Progress
	AllowedOps []string
}

// Get returns an Initiative with the first page of included records (redacted
// by the caller's visibility of each record type), its Milestones, approvals,
// progress summary and the operations available to the caller.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	i, err := s.readable(ctx, p, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Initiative: i, AllowedOps: AllowedOperations(i, p)}
	if d.Items, err = s.items(ctx, p, i.ID, Page{Limit: DefaultItemLimit}); err != nil {
		return Detail{}, err
	}
	if d.Milestones, err = s.store.Milestones(ctx, i.ID); err != nil {
		return Detail{}, err
	}
	if d.Approvals, err = s.approvals.ForSubject(ctx, i.ID); err != nil {
		return Detail{}, fmt.Errorf("load approvals: %w", err)
	}
	if d.Progress, err = s.progress(ctx, p, i.ID, d.Milestones); err != nil {
		return Detail{}, err
	}
	return d, nil
}

// Items lists the records an Initiative includes, oldest link first, keyset
// paged by relationship id (at most MaxItemLimit per page).
func (s *Service) Items(ctx context.Context, p Principal, id string, page Page) (ItemPage, error) {
	i, err := s.readable(ctx, p, id)
	if err != nil {
		return ItemPage{}, err
	}
	return s.items(ctx, p, i.ID, page)
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

func (s *Service) items(ctx context.Context, p Principal, id string, page Page) (ItemPage, error) {
	if page.Limit <= 0 {
		page.Limit = DefaultItemLimit
	}
	if page.Limit > MaxItemLimit {
		page.Limit = MaxItemLimit
	}
	if page.Cursor != "" && !uuidPattern.MatchString(page.Cursor) {
		return ItemPage{}, ErrInvalidCursor
	}
	links, err := s.graph.Outgoing(ctx, s.store.Q(), relationships.Node{Type: NodeInitiative, ID: id}, []string{RelIncludes}, page.Cursor, page.Limit)
	if err != nil {
		return ItemPage{}, fmt.Errorf("list included records: %w", err)
	}
	info, err := s.lookupItems(ctx, p, links.Items)
	if err != nil {
		return ItemPage{}, err
	}
	out := ItemPage{Items: make([]Item, 0, len(links.Items)), NextCursor: links.NextCursor}
	var m masker
	for _, r := range links.Items {
		it := Item{RelationshipID: r.ID, Type: r.Target.Type, ID: r.Target.ID, Since: r.ValidFrom}
		if p.hides(it.Type) {
			it.Hidden, it.RelationshipID, it.ID = true, m.id(r.ID), m.id(r.Target.ID)
			out.Items = append(out.Items, it)
			continue
		}
		if v, ok := info[r.Target]; ok {
			it.Reference, it.Title, it.Status = v.reference, v.title, &v.status
		} else {
			it.Missing = true
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

type itemInfo struct {
	reference *string
	title     *string
	status    string
}

// lookupItems loads the visible included records through the owning modules' contracts.
func (s *Service) lookupItems(ctx context.Context, p Principal, links []relationships.Relationship) (map[relationships.Node]itemInfo, error) {
	byType := map[string][]string{}
	for _, r := range links {
		if !p.hides(r.Target.Type) {
			byType[r.Target.Type] = append(byType[r.Target.Type], r.Target.ID)
		}
	}
	out := map[relationships.Node]itemInfo{}
	if ids := byType[NodeChange]; len(ids) > 0 {
		found, err := s.changes.Lookup(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load changes: %w", err)
		}
		for id, c := range found {
			out[relationships.Node{Type: NodeChange, ID: id}] = itemInfo{reference: &c.Reference, title: &c.Title, status: c.Status}
		}
	}
	if ids := byType[NodeTask]; len(ids) > 0 {
		found, err := s.tasks.Tasks(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load tasks: %w", err)
		}
		for _, t := range found {
			out[relationships.Node{Type: NodeTask, ID: t.ID}] = itemInfo{title: &t.Title, status: t.Status}
		}
	}
	if ids := byType[NodeProcurementRequest]; len(ids) > 0 {
		found, err := s.procurement.Requests(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load procurement requests: %w", err)
		}
		for id, r := range found {
			out[relationships.Node{Type: NodeProcurementRequest, ID: id}] = itemInfo{reference: &r.Reference, status: r.Status}
		}
	}
	if ids := byType[NodeService]; len(ids) > 0 {
		found, err := s.services.Lookup(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load services: %w", err)
		}
		for id, v := range found {
			out[relationships.Node{Type: NodeService, ID: id}] = itemInfo{reference: &v.Reference, title: &v.Name, status: v.Status}
		}
	}
	return out, nil
}

// progress counts the included records by status (per visible type) and the Milestones.
func (s *Service) progress(ctx context.Context, p Principal, id string, milestones []Milestone) (Progress, error) {
	links, err := s.graph.Outgoing(ctx, s.store.Q(), relationships.Node{Type: NodeInitiative, ID: id}, []string{RelIncludes}, "", MaxItems)
	if err != nil {
		return Progress{}, fmt.Errorf("list included records: %w", err)
	}
	info, err := s.lookupItems(ctx, p, links.Items)
	if err != nil {
		return Progress{}, err
	}
	pr := Progress{Items: len(links.Items), ItemsLimitExceeded: links.NextCursor != ""}
	counts := func(t string) map[string]int {
		if p.hides(t) {
			return nil
		}
		return map[string]int{}
	}
	pr.ChangesByStatus, pr.TasksByStatus, pr.RequestsByStatus = counts(NodeChange), counts(NodeTask), counts(NodeProcurementRequest)
	for _, r := range links.Items {
		v, ok := info[r.Target]
		if !ok {
			continue
		}
		switch r.Target.Type {
		case NodeChange:
			pr.ChangesByStatus[v.status]++
		case NodeTask:
			pr.TasksByStatus[v.status]++
		case NodeProcurementRequest:
			pr.RequestsByStatus[v.status]++
		}
	}
	today := day(s.now())
	for _, m := range milestones {
		pr.MilestonesTotal++
		switch {
		case m.DoneAt != nil:
			pr.MilestonesDone++
		case m.DueDate.Before(today):
			pr.MilestonesOverdue++
		}
	}
	return pr, nil
}

// Transitions lists an Initiative's state history, oldest first.
func (s *Service) Transitions(ctx context.Context, p Principal, id string, page Page) (Result[Transition], error) {
	i, err := s.readable(ctx, p, id)
	if err != nil {
		return Result[Transition]{}, err
	}
	return s.store.Transitions(ctx, i.ID, page.Normalize())
}
