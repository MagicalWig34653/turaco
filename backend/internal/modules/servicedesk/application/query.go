package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Ticket list query (ADR-0033). The catalog below is the only place that
// decides which Ticket properties a caller may filter, sort or search.

const (
	permTicketsView   = "tickets.view"
	permTicketsManage = "tickets.manage"
)

// isStaff is the row-disclosure gate: employees see only their own tickets and
// routing (queue, assignee) is not visible to them, so those fields are not
// filterable, sortable or searchable for them either.
func isStaff(s query.Subject) bool { return s.Has(permTicketsView) || s.Has(permTicketsManage) }

func enumValues(values ...string) []query.EnumValue {
	out := make([]query.EnumValue, len(values))
	for i, v := range values {
		out[i] = query.EnumValue{Value: v}
	}
	return out
}

var textOps = []query.Op{query.OpEquals, query.OpNotEquals, query.OpContains, query.OpNotContains, query.OpStartsWith, query.OpIn, query.OpNotIn}
var nullableTextOps = append(append([]query.Op{}, textOps...), query.OpIsEmpty, query.OpIsNotEmpty)
var enumOps = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn}
var timeOps = query.OperatorsOf(query.TypeDateTime)

var ticketCatalog = query.MustCatalog(query.Resource{
	Key: "tickets", Module: "servicedesk", Schema: "servicedesk", Table: "tickets", Alias: "t", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "created_at", Dir: "desc"}},
	Fields: []query.Field{
		{Key: "reference", Type: query.TypeText, Column: query.Col("t", "reference"), Operators: textOps,
			Filterable: true, Sortable: true, Searchable: true, SortIndexed: true, Index: query.IndexBtree},
		{Key: "title", Type: query.TypeText, Column: query.Col("t", "title"), Operators: textOps, Filterable: true, Searchable: true},
		{Key: "description", Type: query.TypeText, Column: query.Col("t", "description"), Operators: nullableTextOps,
			Filterable: true, Searchable: true, Nullable: true},
		{Key: "status", Type: query.TypeEnum, Column: query.Col("t", "status"), Operators: enumOps, Filterable: true,
			EnumValues: enumValues(Statuses...)},
		{Key: "waiting_reason", Type: query.TypeEnum, Column: query.Col("t", "waiting_reason"), Filterable: true, Nullable: true,
			Operators: append(append([]query.Op{}, enumOps...), query.OpIsEmpty, query.OpIsNotEmpty), EnumValues: enumValues(WaitingReasons...)},
		{Key: "priority", Type: query.TypeEnum, Column: query.Col("t", "priority"), Operators: enumOps, Filterable: true,
			Sortable: true, SortIndexed: true, SortByEnumOrder: true, EnumValues: enumValues("urgent", "high", "normal", "low")},
		{Key: "reporter", Type: query.TypeReference, Reference: "users", Column: query.Col("t", "reporter_user_id"),
			Operators: []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsMe}, Filterable: true, Index: query.IndexBtree},
		{Key: "affected_user", Type: query.TypeReference, Reference: "users", Column: query.Col("t", "affected_user_id"),
			Operators: []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsMe}, Filterable: true, Index: query.IndexBtree},
		{Key: "queue", Type: query.TypeReference, Reference: "teams", Column: query.Col("t", "queue_team_id"), Nullable: true,
			Operators:  []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty},
			Filterable: true, Index: query.IndexBtree, Gate: isStaff, Redaction: query.RedactHidden},
		{Key: "assignee", Type: query.TypeReference, Reference: "users", Column: query.Col("t", "assignee_user_id"), Nullable: true,
			Operators:  []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty, query.OpIsMe},
			Filterable: true, Index: query.IndexBtree, Gate: isStaff, Redaction: query.RedactHidden},
		{Key: "asset", Type: query.TypeReference, Reference: "assets", Column: query.Col("t", "asset_id"), Nullable: true,
			Operators:  []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty},
			Filterable: true, Index: query.IndexBtree},
		{Key: "created_at", Type: query.TypeDateTime, Column: query.Col("t", "created_at"), Operators: timeOps,
			Filterable: true, Sortable: true, SortIndexed: true, Index: query.IndexBtree},
		{Key: "updated_at", Type: query.TypeDateTime, Column: query.Col("t", "updated_at"), Operators: timeOps,
			Filterable: true, Sortable: true, SortIndexed: true, Index: query.IndexBtree},
		{Key: "resolved_at", Type: query.TypeDateTime, Column: query.Col("t", "resolved_at"), Operators: timeOps,
			Filterable: true, Nullable: true},
		{Key: "closed_at", Type: query.TypeDateTime, Column: query.Col("t", "closed_at"), Operators: timeOps,
			Filterable: true, Nullable: true},
	},
})

// TicketCatalog is the ticket Field Catalog (catalog tests and documentation).
func TicketCatalog() *query.Catalog { return ticketCatalog }

// QueryStore is the persistence port of the query engine; the repository
// implements it. It is separate from Store so existing fakes stay valid.
type QueryStore interface {
	QueryTickets(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[Ticket], error)
}

var errNoQueryStore = errors.New("servicedesk: store does not support queries")

// WithQueryEngine sets the shared query engine (cursor key, rate limit).
func (s *Service) WithQueryEngine(e *query.Engine) *Service {
	s.engine = e
	return s
}

func (p Principal) subject() query.Subject {
	return query.Subject{UserID: p.UserID, Permissions: map[string]bool{permTicketsView: p.View, permTicketsManage: p.Manage}}
}

// QueryFields returns the ticket catalog as the caller may use it.
func (s *Service) QueryFields(p Principal) (query.Info, error) {
	if p.UserID == "" {
		return query.Info{}, ErrForbidden
	}
	return ticketCatalog.Describe(p.subject()), nil
}

// CompatNodes maps the pre-engine list parameters onto Filter conditions, so
// they combine with filter/sort/search. Routing filters are ignored for
// employees exactly as the plain list does.
func CompatNodes(p Principal, f Filter) []query.Node {
	var out []query.Node
	if f.Status != "" {
		out = append(out, query.Cond("status", query.OpEquals, f.Status))
	}
	if p.staff() {
		if f.AssigneeID != "" {
			out = append(out, query.Cond("assignee", query.OpEquals, f.AssigneeID))
		}
		if f.QueueID != "" {
			out = append(out, query.Cond("queue", query.OpEquals, f.QueueID))
		}
	}
	if f.OpenOnly {
		out = append(out, query.Cond("status", query.OpIn, []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting}))
	}
	return out
}

// Query runs a filter over the tickets the caller may see: all tickets for
// staff with all=true, otherwise those the caller reported or is affected by.
// The visibility predicate is built here and ANDed outside the user's filter.
func (s *Service) Query(ctx context.Context, p Principal, req query.Request, all bool, compat []query.Node) (query.Page[Ticket], error) {
	if p.UserID == "" || (all && !p.staff()) {
		return query.Page[Ticket]{}, ErrForbidden
	}
	qs, ok := s.store.(QueryStore)
	if !ok {
		return query.Page[Ticket]{}, errNoQueryStore
	}
	vis := query.Fragment{}
	scope := "all"
	if !all {
		scope = "mine"
		vis = query.Fragment{SQL: "t.reporter_user_id = ?::uuid OR t.affected_user_id = ?::uuid", Args: []any{p.UserID, p.UserID}}
	}
	req.Filter = query.And(req.Filter, compat...)
	plan, err := s.engine.Prepare(ticketCatalog, p.subject(), req, scope, query.Options{})
	if err != nil {
		return query.Page[Ticket]{}, err
	}
	page, err := qs.QueryTickets(ctx, plan, vis)
	if err != nil {
		return query.Page[Ticket]{}, fmt.Errorf("query tickets: %w", err)
	}
	if !p.staff() {
		for i := range page.Items {
			page.Items[i].QueueTeamID = nil
		}
	}
	return page, nil
}
