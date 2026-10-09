package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Ticket list query (ADR-0033). The catalog below is the only place that
// decides which Ticket properties a caller may filter, sort or search.

const (
	permTicketsView   = "tickets.view"
	permTicketsManage = "tickets.manage"
	// permQueueScope is a synthetic subject permission: the caller views Tickets of at least one Queue through a grant.
	permQueueScope = "servicedesk.queue_scope"
)

// isStaff is the row-disclosure gate: employees see only their own tickets and
// routing (queue, assignee) is not visible to them, so those fields are not
// filterable, sortable or searchable for them either.
func isStaff(s query.Subject) bool {
	return s.Has(permTicketsView) || s.Has(permTicketsManage) || s.Has(permQueueScope)
}

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
		// Substring matches (contains, search) are served by trigram indexes (migration 000060) on reference and
		// title. The description has no trigram index (multi-kilobyte text): only emptiness can be filtered, so a
		// request can never ask for an unindexed scan of it, and search does not cover it.
		{Key: "reference", Type: query.TypeText, Column: query.Col("t", "reference"), Operators: textOps,
			Filterable: true, Sortable: true, Searchable: true, SortIndexed: true, Index: query.IndexTrigram},
		{Key: "title", Type: query.TypeText, Column: query.Col("t", "title"), Operators: textOps, Filterable: true, Searchable: true,
			Index: query.IndexTrigram},
		{Key: "description", Type: query.TypeText, Column: query.Col("t", "description"),
			Operators: []query.Op{query.OpIsEmpty, query.OpIsNotEmpty}, Filterable: true, Nullable: true},
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
		// queue is the desk (Ticket Queue). It is never empty. It is row-specific for callers who see only some
		// Queues (their own Tickets in other Queues stay undisclosed), so the engine narrows such callers to the
		// Queues they view as soon as a request addresses it (see Query).
		{Key: "queue", Type: query.TypeReference, Reference: "queues", Column: query.Col("t", "queue_id"),
			Operators:  []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn},
			Filterable: true, Index: query.IndexBtree, Gate: isStaff, Redaction: query.RedactHidden},
		// routing_team is the Team a Ticket is routed to inside its desk (a hint, not access).
		{Key: "routing_team", Type: query.TypeReference, Reference: "teams", Column: query.Col("t", "queue_team_id"), Nullable: true,
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

func (a access) subject(p Principal) query.Subject {
	return query.Subject{UserID: p.UserID, Permissions: map[string]bool{permTicketsView: p.View, permTicketsManage: p.Manage, permQueueScope: a.anyView()}}
}

// QueryFields returns the ticket catalog as the caller may use it.
func (s *Service) QueryFields(ctx context.Context, p Principal) (query.Info, error) {
	if p.UserID == "" {
		return query.Info{}, ErrForbidden
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return query.Info{}, err
	}
	return ticketCatalog.Describe(a.subject(p)), nil
}

// CompatNodes maps the pre-engine list parameters onto Filter conditions, so
// they combine with filter/sort/search. Routing filters are ignored for
// employees exactly as the plain list does. queueId and assigneeId keep their
// meaning: the routing Team and the assignee.
func (s *Service) CompatNodes(ctx context.Context, p Principal, f Filter) ([]query.Node, error) {
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	var out []query.Node
	if f.Status != "" {
		out = append(out, query.Cond("status", query.OpEquals, f.Status))
	}
	if a.anyView() {
		if f.AssigneeID != "" {
			out = append(out, query.Cond("assignee", query.OpEquals, f.AssigneeID))
		}
		if f.QueueID != "" {
			out = append(out, query.Cond("routing_team", query.OpEquals, f.QueueID))
		}
	}
	if f.OpenOnly {
		out = append(out, query.Cond("status", query.OpIn, []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting}))
	}
	return out, nil
}

// QueryScope selects which Tickets a query may cover.
type QueryScope int

const (
	// ScopeMine: the Tickets the caller reported or is affected by.
	ScopeMine QueryScope = iota
	// ScopeAll: everything the caller may see; ErrForbidden for callers who see only their own.
	ScopeAll
	// ScopeAuto: ScopeAll when the caller may, otherwise ScopeMine.
	ScopeAuto
)

// Query runs a filter over the tickets the caller may see: all tickets for
// staff with all=true, otherwise those the caller reported or is affected by.
func (s *Service) Query(ctx context.Context, p Principal, req query.Request, all bool, compat []query.Node) (query.Page[Ticket], error) {
	scope := ScopeMine
	if all {
		scope = ScopeAll
	}
	return s.QueryScoped(ctx, p, req, scope, compat, "")
}

var routingFields = []string{"queue", "routing_team", "assignee"}

// QueryScoped is Query with a scope selector and an optional Queue restriction. The visibility predicate is built
// here and ANDed outside the user's filter:
//
//   - tickets.view or tickets.manage: every Ticket (ScopeAll);
//   - view grants: the caller's own Tickets plus the Tickets of the Queues they view;
//   - everybody else: their own Tickets.
//
// Fields that differ per row for a caller who sees only some Queues (queue, routing Team, assignee) narrow such a
// caller to the Tickets of the Queues they view as soon as a request uses them, so a filter cannot reveal the
// routing of a Ticket in a Queue the caller does not know. inQueue narrows further to one Queue the caller views;
// a Queue they do not view yields an empty page, the same as an empty Queue.
func (s *Service) QueryScoped(ctx context.Context, p Principal, req query.Request, scope QueryScope, compat []query.Node, inQueue string) (query.Page[Ticket], error) {
	if p.UserID == "" {
		return query.Page[Ticket]{}, ErrForbidden
	}
	qs, ok := s.store.(QueryStore)
	if !ok {
		return query.Page[Ticket]{}, errNoQueryStore
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return query.Page[Ticket]{}, err
	}
	if scope == ScopeAuto {
		scope = ScopeMine
		if a.anyView() {
			scope = ScopeAll
		}
	}
	if scope == ScopeAll && !a.anyView() {
		return query.Page[Ticket]{}, ErrForbidden
	}
	name := "mine"
	if scope == ScopeAll {
		name = "queues"
		if a.global() {
			name = "all"
		}
	}
	if inQueue != "" {
		name += ":" + strings.ToLower(inQueue)
	}
	req.Filter = query.And(req.Filter, compat...)
	plan, err := s.engine.Prepare(ticketCatalog, a.subject(p), req, name, query.Options{})
	if err != nil {
		return query.Page[Ticket]{}, err
	}
	viewable := a.viewIDs()
	own := query.Fragment{SQL: "t.reporter_user_id = ?::uuid OR t.affected_user_id = ?::uuid", Args: []any{p.UserID, p.UserID}}
	var vis query.Fragment
	switch {
	case scope == ScopeAll && a.global():
	case scope == ScopeAll:
		vis = query.Fragment{SQL: "(" + own.SQL + ") OR t.queue_id = ANY(?::text[]::uuid[])", Args: append(append([]any{}, own.Args...), viewable)}
	default:
		vis = own
	}
	narrow := inQueue != ""
	for _, f := range routingFields {
		narrow = narrow || (!a.global() && plan.Uses(f))
	}
	if narrow {
		ids := viewable
		if inQueue != "" {
			ids = nil
			if a.canView(strings.ToLower(inQueue)) {
				ids = []string{strings.ToLower(inQueue)}
			}
		}
		n := query.Fragment{SQL: "t.queue_id = ANY(?::text[]::uuid[])", Args: []any{nonNil(ids)}}
		if vis.SQL == "" {
			vis = n
		} else {
			vis = query.Fragment{SQL: "(" + vis.SQL + ") AND " + n.SQL, Args: append(append([]any{}, vis.Args...), n.Args...)}
		}
	}
	page, err := qs.QueryTickets(ctx, plan, vis)
	if err != nil {
		return query.Page[Ticket]{}, fmt.Errorf("query tickets: %w", err)
	}
	if err := s.shape(ctx, a, ptrs(page.Items)...); err != nil {
		return query.Page[Ticket]{}, err
	}
	return page, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
