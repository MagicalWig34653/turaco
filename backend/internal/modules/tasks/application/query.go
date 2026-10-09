package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Task list query (ADR-0033). The catalog declares which Task properties a
// caller may filter, sort or search; the visibility predicate below is ANDed
// outside the user's filter.

const (
	permTasksView   = "tasks.view"
	permTasksManage = "tasks.manage"
	permTasksWork   = "tasks.work"
)

func enumValues(values ...string) []query.EnumValue {
	out := make([]query.EnumValue, len(values))
	for i, v := range values {
		out[i] = query.EnumValue{Value: v}
	}
	return out
}

var (
	taskTextOps = []query.Op{query.OpEquals, query.OpNotEquals, query.OpContains, query.OpNotContains, query.OpStartsWith, query.OpIn, query.OpNotIn}
	taskEnumOps = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn}
	taskRefOps  = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty}
	taskTimeOps = query.OperatorsOf(query.TypeDateTime)
)

var taskCatalog = query.MustCatalog(query.Resource{
	Key: "tasks", Module: "tasks", Schema: "platform", Table: "tasks", Alias: "t", IDColumn: "id",
	// The shared task order: due date (none last), then priority (urgent first).
	DefaultSort: []query.SortSpec{{Field: "due_at", Dir: "asc"}, {Field: "priority", Dir: "asc"}},
	Fields: []query.Field{
		// Substring matches (contains, search) on the title are served by a trigram index (migration 000060). The
		// description has none: only emptiness can be filtered and search does not cover it.
		{Key: "title", Type: query.TypeText, Column: query.Col("t", "title"), Operators: taskTextOps, Filterable: true, Searchable: true,
			Index: query.IndexTrigram},
		{Key: "description", Type: query.TypeText, Column: query.Col("t", "description"), Nullable: true, Filterable: true,
			Operators: []query.Op{query.OpIsEmpty, query.OpIsNotEmpty}},
		{Key: "status", Type: query.TypeEnum, Column: query.Col("t", "status"), Operators: taskEnumOps, Filterable: true, EnumValues: enumValues(statuses...)},
		{Key: "priority", Type: query.TypeEnum, Column: query.Col("t", "priority"), Operators: taskEnumOps, Filterable: true,
			Sortable: true, SortIndexed: true, SortByEnumOrder: true, EnumValues: enumValues(PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow)},
		{Key: "assigned_user", Type: query.TypeReference, Reference: "users", Column: query.Col("t", "assigned_user_id"), Nullable: true,
			Operators: append(append([]query.Op{}, taskRefOps...), query.OpIsMe), Filterable: true, Index: query.IndexBtree},
		{Key: "assigned_team", Type: query.TypeReference, Reference: "teams", Column: query.Col("t", "assigned_team_id"), Nullable: true,
			Operators: append(append([]query.Op{}, taskRefOps...), query.OpIsMyTeams), Filterable: true, Index: query.IndexBtree},
		{Key: "created_by", Type: query.TypeReference, Reference: "users", Column: query.Col("t", "created_by_user_id"), Nullable: true,
			Operators: append(append([]query.Op{}, taskRefOps...), query.OpIsMe), Filterable: true},
		{Key: "context_type", Type: query.TypeText, Column: query.Col("t", "context_type"), Nullable: true, Filterable: true,
			Operators: []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty}},
		{Key: "due_at", Type: query.TypeDateTime, Column: query.Col("t", "due_at"), Nullable: true, Operators: taskTimeOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "completed_at", Type: query.TypeDateTime, Column: query.Col("t", "completed_at"), Nullable: true, Operators: taskTimeOps, Filterable: true},
		{Key: "created_at", Type: query.TypeDateTime, Column: query.Col("t", "created_at"), Operators: taskTimeOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "updated_at", Type: query.TypeDateTime, Column: query.Col("t", "updated_at"), Operators: taskTimeOps,
			Filterable: true, Sortable: true, SortIndexed: true},
	},
})

// TaskCatalog is the task Field Catalog (catalog tests and documentation).
func TaskCatalog() *query.Catalog { return taskCatalog }

// QueryStore is the persistence port of the query engine; the repository
// implements it. It is separate from Store so existing fakes stay valid.
type QueryStore interface {
	QueryTasks(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[Task], error)
}

var errNoQueryStore = errors.New("tasks: store does not support queries")

// WithQueryEngine sets the shared query engine (cursor key, rate limit).
func (s *Service) WithQueryEngine(e *query.Engine) *Service {
	s.engine = e
	return s
}

// visibility is the row-scope predicate of a caller: every task for tasks.view or tasks.manage (unless mine is set),
// otherwise the tasks assigned to the caller or one of the caller's Teams. scope names the variant for cursors.
func (a access) visibility(mine bool) (query.Fragment, string) {
	if mine || !(a.p.ViewAll || a.p.Manage) {
		m := a.mine()
		return query.Fragment{SQL: "t.assigned_user_id = ?::uuid OR t.assigned_team_id = ANY(?::text[]::uuid[])", Args: []any{m.UserID, m.TeamIDs}}, "mine"
	}
	return query.Fragment{}, "all"
}

func (a access) subject() query.Subject {
	teams := a.mine().TeamIDs
	return query.Subject{UserID: a.p.UserID, TeamIDs: teams, Permissions: map[string]bool{
		permTasksView: a.p.ViewAll, permTasksManage: a.p.Manage, permTasksWork: a.p.Work}}
}

// QueryFields returns the task catalog as the caller may use it.
func (s *Service) QueryFields(ctx context.Context, p Principal) (query.Info, error) {
	if p.UserID == "" || !(p.ViewAll || p.Manage || p.Work) {
		return query.Info{}, ErrForbidden
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return query.Info{}, err
	}
	return taskCatalog.Describe(a.subject()), nil
}

// CompatNodes maps the pre-engine list parameters (everything but Mine, which
// restricts visibility) onto Filter conditions.
func (s *Service) CompatNodes(f ListFilter) []query.Node {
	var out []query.Node
	if len(f.Statuses) > 0 {
		out = append(out, query.Cond("status", query.OpIn, f.Statuses))
	}
	if f.Priority != "" {
		out = append(out, query.Cond("priority", query.OpEquals, f.Priority))
	}
	if f.AssignedUserID != "" {
		out = append(out, query.Cond("assigned_user", query.OpEquals, f.AssignedUserID))
	}
	if f.AssignedTeamID != "" {
		out = append(out, query.Cond("assigned_team", query.OpEquals, f.AssignedTeamID))
	}
	if f.Overdue {
		out = append(out, query.Cond("due_at", query.OpBefore, s.now().UTC().Format(time.RFC3339Nano)),
			query.Cond("status", query.OpNotIn, []string{StatusCompleted, StatusCancelled}))
	}
	if f.TitlePrefix != "" {
		out = append(out, query.Cond("title", query.OpStartsWith, f.TitlePrefix))
	}
	return out
}

// Query runs a filter over the tasks the caller may see: every task for
// tasks.view or tasks.manage (unless mine is set), otherwise those assigned to
// the caller or one of the caller's Teams (tasks.work); nothing without any of
// them. The visibility predicate is built here and ANDed outside the filter.
func (s *Service) Query(ctx context.Context, p Principal, req query.Request, mine bool, compat []query.Node) (query.Page[TaskView], error) {
	if p.UserID == "" || !(p.ViewAll || p.Manage || p.Work) {
		return query.Page[TaskView]{}, ErrForbidden
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return query.Page[TaskView]{}, err
	}
	qs, ok := s.store.(QueryStore)
	if !ok {
		return query.Page[TaskView]{}, errNoQueryStore
	}
	vis, scope := a.visibility(mine)
	req.Filter = query.And(req.Filter, compat...)
	plan, err := s.engine.Prepare(taskCatalog, a.subject(), req, scope, query.Options{})
	if err != nil {
		return query.Page[TaskView]{}, err
	}
	page, err := qs.QueryTasks(ctx, plan, vis)
	if err != nil {
		return query.Page[TaskView]{}, fmt.Errorf("query tasks: %w", err)
	}
	views, err := s.view(ctx, page.Items)
	if err != nil {
		return query.Page[TaskView]{}, err
	}
	return query.Page[TaskView]{Items: views, NextCursor: page.NextCursor, Count: page.Count, CountCapped: page.CountCapped, Warnings: page.Warnings}, nil
}
