package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Field Catalogs of Users, Teams, Locations and Departments (ADR-0033, F14 A5). HR-adjacent fields (manager,
// department, primary location, employee number, access expiry) exist only for callers with
// organization.users.view_details: without it they cannot be filtered, sorted or searched either, so a value
// cannot be probed by bisection.

const (
	permView        = "organization.view"
	permViewDetails = "organization.users.view_details"
)

func enumValues(values ...string) []query.EnumValue {
	out := make([]query.EnumValue, len(values))
	for i, v := range values {
		out[i] = query.EnumValue{Value: v}
	}
	return out
}

var (
	orgTextOps = []query.Op{query.OpEquals, query.OpNotEquals, query.OpContains, query.OpNotContains, query.OpStartsWith, query.OpIn, query.OpNotIn}
	orgNullOps = append(append([]query.Op{}, orgTextOps...), query.OpIsEmpty, query.OpIsNotEmpty)
	// Fields without a trigram index offer no substring match (a scan of the table): equality and prefix only.
	orgPlainOps     = []query.Op{query.OpEquals, query.OpNotEquals, query.OpStartsWith, query.OpIn, query.OpNotIn}
	orgPlainNullOps = append(append([]query.Op{}, orgPlainOps...), query.OpIsEmpty, query.OpIsNotEmpty)
	orgEnumOps      = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn}
	orgRefOps       = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty}
	orgTimeOps      = query.OperatorsOf(query.TypeDateTime)
	orgBoolOps      = []query.Op{query.OpIsTrue, query.OpIsFalse}
)

var userCatalog = query.MustCatalog(query.Resource{
	Key: "users", Module: "organization", Schema: "organization", Table: "users", Alias: "u", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "display_name", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "display_name", Type: query.TypeText, Column: query.Col("u", "display_name"), SortColumn: query.Lower(query.Col("u", "display_name")),
			Operators: orgTextOps, Filterable: true, Sortable: true, SortIndexed: true, Searchable: true, Index: query.IndexTrigram},
		{Key: "primary_email", Type: query.TypeText, Column: query.Col("u", "primary_email"), Nullable: true, Operators: orgNullOps,
			Filterable: true, Searchable: true, Index: query.IndexTrigram},
		{Key: "status", Type: query.TypeEnum, Column: query.Col("u", "status"), Operators: orgEnumOps, Filterable: true, Sortable: true, SortIndexed: true,
			EnumValues: enumValues("active", "inactive", "departed", "external", "unknown")},
		{Key: "account_kind", Type: query.TypeEnum, Column: query.Col("u", "account_kind"), Operators: orgEnumOps, Filterable: true,
			EnumValues: enumValues(AccountKindEmployee, AccountKindExternal)},
		// "source" says where the account comes from: the directory, Turaco itself or the emergency account.
		{Key: "source", Type: query.TypeEnum, Column: query.Col("u", "origin"), Operators: orgEnumOps, Filterable: true,
			EnumValues: enumValues(OriginDirectory, OriginLocal, OriginEmergency)},
		{Key: "department", Type: query.TypeReference, Reference: "departments", Column: query.Col("u", "department_id"), Nullable: true,
			Operators: orgRefOps, Filterable: true, Permission: permViewDetails, Redaction: query.RedactHidden},
		{Key: "location", Type: query.TypeReference, Reference: "locations", Column: query.Col("u", "primary_location_id"), Nullable: true,
			Operators: orgRefOps, Filterable: true, Permission: permViewDetails, Redaction: query.RedactHidden},
		{Key: "manager", Type: query.TypeReference, Reference: "users", Column: query.Col("u", "manager_user_id"), Nullable: true,
			Operators: orgRefOps, Filterable: true, Permission: permViewDetails, Redaction: query.RedactHidden},
		{Key: "employee_number", Type: query.TypeText, Column: query.Col("u", "employee_number"), Nullable: true,
			Operators:  []query.Op{query.OpEquals, query.OpNotEquals, query.OpStartsWith, query.OpIn, query.OpIsEmpty, query.OpIsNotEmpty},
			Filterable: true, Permission: permViewDetails, Redaction: query.RedactHidden},
		{Key: "access_expires_at", Type: query.TypeDateTime, Column: query.Col("u", "access_expires_at"), Nullable: true, Operators: orgTimeOps,
			Filterable: true, Permission: permViewDetails, Redaction: query.RedactHidden},
		{Key: "created_at", Type: query.TypeDateTime, Column: query.Col("u", "created_at"), Operators: orgTimeOps, Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "updated_at", Type: query.TypeDateTime, Column: query.Col("u", "updated_at"), Operators: orgTimeOps, Filterable: true, Sortable: true, SortIndexed: true},
	},
})

var teamCatalog = query.MustCatalog(query.Resource{
	Key: "teams", Module: "organization", Schema: "organization", Table: "teams", Alias: "t", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "name", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "name", Type: query.TypeText, Column: query.Col("t", "name"), SortColumn: query.Lower(query.Col("t", "name")), Operators: orgPlainOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "active", Type: query.TypeBoolean, Column: query.Col("t", "active"), Operators: orgBoolOps, Filterable: true},
	},
})

var locationCatalog = query.MustCatalog(query.Resource{
	Key: "locations", Module: "organization", Schema: "organization", Table: "locations", Alias: "l", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "name", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "name", Type: query.TypeText, Column: query.Col("l", "name"), SortColumn: query.Lower(query.Col("l", "name")), Operators: orgPlainOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "code", Type: query.TypeText, Column: query.Col("l", "code"), Nullable: true, Operators: orgPlainNullOps, Filterable: true},
		{Key: "kind", Type: query.TypeEnum, Column: query.Col("l", "kind"), Operators: orgEnumOps, Filterable: true, EnumValues: enumValues(LocationSite, LocationArea)},
		{Key: "parent", Type: query.TypeReference, Reference: "locations", Column: query.Col("l", "parent_location_id"), Nullable: true, Operators: orgRefOps, Filterable: true},
		{Key: "active", Type: query.TypeBoolean, Column: query.Col("l", "active"), Operators: orgBoolOps, Filterable: true},
	},
})

var departmentCatalog = query.MustCatalog(query.Resource{
	Key: "departments", Module: "organization", Schema: "organization", Table: "departments", Alias: "d", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "name", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "name", Type: query.TypeText, Column: query.Col("d", "name"), SortColumn: query.Lower(query.Col("d", "name")), Operators: orgPlainOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "code", Type: query.TypeText, Column: query.Col("d", "code"), Nullable: true, Operators: orgPlainNullOps, Filterable: true},
		{Key: "parent", Type: query.TypeReference, Reference: "departments", Column: query.Col("d", "parent_department_id"), Nullable: true, Operators: orgRefOps, Filterable: true},
		{Key: "active", Type: query.TypeBoolean, Column: query.Col("d", "active"), Operators: orgBoolOps, Filterable: true},
	},
})

// Catalogs returns the Organization catalogs (schema validation at startup and catalog tests).
func Catalogs() []*query.Catalog {
	return []*query.Catalog{userCatalog, teamCatalog, locationCatalog, departmentCatalog}
}

// QueryStore is the persistence port of the Organization lists.
type QueryStore interface {
	QueryUsers(ctx context.Context, plan *query.Plan) (query.Page[User], error)
	QueryTeams(ctx context.Context, plan *query.Plan) (query.Page[Team], error)
	QueryLocations(ctx context.Context, plan *query.Plan) (query.Page[Location], error)
	QueryDepartments(ctx context.Context, plan *query.Plan) (query.Page[Department], error)
}

// QueryPrincipal is the caller of a list query; the transport fills it from the authenticated principal.
type QueryPrincipal struct {
	UserID      string
	ViewDetails bool
}

// ErrForbidden: the caller has no organization.view.
var ErrForbidden = errors.New("organization: forbidden")

// Queries runs the Organization lists through the shared query engine.
type Queries struct {
	store  QueryStore
	engine *query.Engine
}

func NewQueries(store QueryStore, engine *query.Engine) *Queries {
	if engine == nil {
		engine = query.NewEphemeralEngine()
	}
	return &Queries{store: store, engine: engine}
}

func (p QueryPrincipal) subject() query.Subject {
	return query.Subject{UserID: p.UserID, Permissions: map[string]bool{permView: true, permViewDetails: p.ViewDetails}}
}

// Fields returns the catalog of resource ("users", "teams", "locations", "departments") as the caller may use it.
func (q *Queries) Fields(p QueryPrincipal, resource string) (query.Info, error) {
	cat, ok := catalogOf(resource)
	if !ok {
		return query.Info{}, ErrNotFound
	}
	return cat.Describe(p.subject()), nil
}

func catalogOf(resource string) (*query.Catalog, bool) {
	switch resource {
	case "users":
		return userCatalog, true
	case "teams":
		return teamCatalog, true
	case "locations":
		return locationCatalog, true
	case "departments":
		return departmentCatalog, true
	}
	return nil, false
}

func (q *Queries) plan(p QueryPrincipal, cat *query.Catalog, req query.Request) (*query.Plan, error) {
	return q.engine.Prepare(cat, p.subject(), req, "all", query.Options{})
}

func (q *Queries) Users(ctx context.Context, p QueryPrincipal, req query.Request) (query.Page[User], error) {
	plan, err := q.plan(p, userCatalog, req)
	if err != nil {
		return query.Page[User]{}, err
	}
	page, err := q.store.QueryUsers(ctx, plan)
	if err != nil {
		return query.Page[User]{}, fmt.Errorf("query users: %w", err)
	}
	return page, nil
}

func (q *Queries) Teams(ctx context.Context, p QueryPrincipal, req query.Request) (query.Page[Team], error) {
	plan, err := q.plan(p, teamCatalog, req)
	if err != nil {
		return query.Page[Team]{}, err
	}
	page, err := q.store.QueryTeams(ctx, plan)
	if err != nil {
		return query.Page[Team]{}, fmt.Errorf("query teams: %w", err)
	}
	return page, nil
}

func (q *Queries) Locations(ctx context.Context, p QueryPrincipal, req query.Request) (query.Page[Location], error) {
	plan, err := q.plan(p, locationCatalog, req)
	if err != nil {
		return query.Page[Location]{}, err
	}
	page, err := q.store.QueryLocations(ctx, plan)
	if err != nil {
		return query.Page[Location]{}, fmt.Errorf("query locations: %w", err)
	}
	return page, nil
}

func (q *Queries) Departments(ctx context.Context, p QueryPrincipal, req query.Request) (query.Page[Department], error) {
	plan, err := q.plan(p, departmentCatalog, req)
	if err != nil {
		return query.Page[Department]{}, err
	}
	page, err := q.store.QueryDepartments(ctx, plan)
	if err != nil {
		return query.Page[Department]{}, fmt.Errorf("query departments: %w", err)
	}
	return page, nil
}
