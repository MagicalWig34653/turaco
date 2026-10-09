package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Device list query (ADR-0033). Observed (provider) values and Turaco-owned
// values stay separate fields; the management fields exist only for callers
// who may see management data.

const (
	permEndpointsView   = "endpoints.view"
	permEndpointsManage = "endpoints.manage"
	// permManagementView is held by endpoint.management.view or endpoints.manage (canViewManagement).
	permManagementView = "endpoint.management.view"
)

func enumValues(values ...string) []query.EnumValue {
	out := make([]query.EnumValue, len(values))
	for i, v := range values {
		out[i] = query.EnumValue{Value: v}
	}
	return out
}

func findingValues() []query.EnumValue {
	out := make([]query.EnumValue, len(FindingKinds))
	for i, k := range FindingKinds {
		out[i] = query.EnumValue{Value: k}
		for _, m := range ManagementFindingKinds {
			if k == m {
				out[i].Permission = permManagementView
			}
		}
	}
	return out
}

var (
	deviceTextOps  = []query.Op{query.OpEquals, query.OpNotEquals, query.OpContains, query.OpNotContains, query.OpStartsWith, query.OpIn, query.OpNotIn}
	deviceNullText = append(append([]query.Op{}, deviceTextOps...), query.OpIsEmpty, query.OpIsNotEmpty)
	deviceEnumOps  = []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn}
	deviceTimeOps  = query.OperatorsOf(query.TypeDateTime)
	tagOps         = []query.Op{query.OpHasAny, query.OpHasAll, query.OpHasNone}
)

var deviceCatalog = query.MustCatalog(query.Resource{
	Key: "devices", Module: "endpoints", Schema: "endpoints", Table: "devices", Alias: "d", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "name", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "name", Type: query.TypeText, Column: query.Col("d", "name"), SortColumn: query.Lower(query.Col("d", "name")), Operators: deviceTextOps,
			Filterable: true, Sortable: true, Searchable: true, SortIndexed: true, Index: query.IndexTrigram},
		{Key: "serial_number", Type: query.TypeText, Column: query.Col("d", "serial_number"), Nullable: true, Operators: deviceNullText,
			Filterable: true, Searchable: true, Index: query.IndexTrigram},
		{Key: "provider", Type: query.TypeText, Column: query.Col("d", "provider"), Index: query.IndexBtree, Filterable: true,
			Operators: []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn}},
		{Key: "platform", Type: query.TypeEnum, Column: query.Col("d", "os_platform"), Operators: deviceEnumOps, Filterable: true,
			EnumValues: enumValues("windows", "macos", "ios", "android", "linux", "other")},
		{Key: "os_version", Type: query.TypeText, Column: query.Col("d", "os_version"), Nullable: true, Operators: deviceNullText, Filterable: true},
		{Key: "manufacturer", Type: query.TypeText, Column: query.Col("d", "manufacturer"), Nullable: true, Operators: deviceNullText, Filterable: true},
		{Key: "model", Type: query.TypeText, Column: query.Col("d", "model"), Nullable: true, Operators: deviceNullText, Filterable: true},
		{Key: "ownership", Type: query.TypeEnum, Column: query.Col("d", "ownership"), Operators: deviceEnumOps, Filterable: true,
			EnumValues: enumValues("corporate", "personal", "unknown")},
		{Key: "compliance", Type: query.TypeEnum, Column: query.Col("d", "compliance_state"), Operators: deviceEnumOps, Filterable: true,
			EnumValues: enumValues("compliant", "noncompliant", "in_grace_period", "unknown")},
		// An Asset link is a Turaco-owned assignment; "linked" is is_not_empty.
		{Key: "asset", Type: query.TypeReference, Reference: "assets", Column: query.Col("d", "asset_id"), Nullable: true, Filterable: true,
			Operators: []query.Op{query.OpEquals, query.OpNotEquals, query.OpIn, query.OpNotIn, query.OpIsEmpty, query.OpIsNotEmpty}},
		{Key: "asset_link_source", Type: query.TypeEnum, Column: query.Col("d", "asset_link_source"), Nullable: true, Filterable: true,
			Operators: append(append([]query.Op{}, deviceEnumOps...), query.OpIsEmpty, query.OpIsNotEmpty), EnumValues: enumValues("serial", "manual")},
		{Key: "auto_link_blocked", Type: query.TypeBoolean, Column: query.Col("d", "auto_link_blocked"), Filterable: true,
			Operators: []query.Op{query.OpIsTrue, query.OpIsFalse}},
		// Source and freshness of the observed values.
		{Key: "source", Type: query.TypeEnum, Column: query.Col("d", "source"), Operators: deviceEnumOps, Filterable: true, EnumValues: enumValues("sync", "import")},
		{Key: "last_checkin_at", Type: query.TypeDateTime, Column: query.Col("d", "last_checkin_at"), Nullable: true, Operators: deviceTimeOps,
			Filterable: true, Sortable: true, SortIndexed: true},
		{Key: "observed_at", Type: query.TypeDateTime, Column: query.Col("d", "observed_at"), Operators: deviceTimeOps, Filterable: true},
		{Key: "last_synced_at", Type: query.TypeDateTime, Column: query.Col("d", "last_synced_at"), Operators: deviceTimeOps, Filterable: true},
		// Devices that a complete snapshot no longer contained are tombstoned; they are hidden unless a
		// condition addresses this field.
		{Key: "deleted_observed_at", Type: query.TypeDateTime, Column: query.Col("d", "deleted_observed_at"), Nullable: true, Operators: deviceTimeOps, Filterable: true},
		{Key: "open_finding", Type: query.TypeTags, Operators: tagOps, Filterable: true, EnumValues: findingValues(),
			Sub: &query.Sub{Table: "findings", Alias: "f", LinkColumn: "device_id", ValueColumn: "kind",
				Fixed: []query.FixedCond{{Alias: "f", Column: "status", Equals: "open"}}}},
		// The observed state of management artifacts is management data.
		{Key: "management_state", Type: query.TypeTags, Operators: tagOps, Filterable: true, EnumValues: enumValues(ManagementStateFilters...),
			Permission: permManagementView, Redaction: query.RedactHidden,
			Sub: &query.Sub{Table: "management_observations", Alias: "o", LinkColumn: "device_id", ValueColumn: "normalized_state",
				Fixed: []query.FixedCond{{Alias: "o", Column: "retired_at", IsNull: true}},
				Join: &query.SubJoin{Table: "management_artifacts", Alias: "a", ChildColumn: "artifact_id", JoinColumn: "id",
					Fixed: []query.FixedCond{{Alias: "a", Column: "deleted_observed_at", IsNull: true}}}}},
	},
})

// DeviceCatalog is the device Field Catalog (catalog tests and documentation).
func DeviceCatalog() *query.Catalog { return deviceCatalog }

// QueryStore is the persistence port of the query engine; the repository
// implements it. It is separate from Store so existing fakes stay valid.
type QueryStore interface {
	QueryDevices(ctx context.Context, plan *query.Plan, visibility query.Fragment) (query.Page[Device], error)
}

var errNoQueryStore = errors.New("endpoints: store does not support queries")

// WithQueryEngine sets the shared query engine (cursor key, rate limit).
func (s *Service) WithQueryEngine(e *query.Engine) *Service {
	s.engine = e
	return s
}

func (p Principal) querySubject() query.Subject {
	return query.Subject{UserID: p.UserID, Permissions: map[string]bool{
		permEndpointsView: p.View, permEndpointsManage: p.Manage, permManagementView: p.canViewManagement()}}
}

// QueryFields returns the device catalog as the caller may use it.
func (s *Service) QueryFields(p Principal) (query.Info, error) {
	if !p.canView() {
		return query.Info{}, ErrForbidden
	}
	return deviceCatalog.Describe(p.querySubject()), nil
}

// deviceCompat maps the pre-engine list parameters onto Filter conditions.
func deviceCompat(f DeviceFilter) []query.Node {
	var out []query.Node
	if f.Platform != "" {
		out = append(out, query.Cond("platform", query.OpEquals, f.Platform))
	}
	if f.Compliance != "" {
		out = append(out, query.Cond("compliance", query.OpEquals, f.Compliance))
	}
	if f.Linked != nil {
		op := query.OpIsEmpty
		if *f.Linked {
			op = query.OpIsNotEmpty
		}
		out = append(out, query.Cond("asset", op, nil))
	}
	if f.ManagementState != "" {
		out = append(out, query.Cond("management_state", query.OpHasAny, []string{f.ManagementState}))
	}
	if f.HasFinding != "" {
		out = append(out, query.Cond("open_finding", query.OpHasAny, []string{f.HasFinding}))
	}
	if f.OSVersionPrefix != "" {
		out = append(out, query.Cond("os_version", query.OpStartsWith, f.OSVersionPrefix))
	}
	if f.LastCheckinOlderThanDays > 0 {
		out = append(out, query.Cond("last_checkin_at", query.OpOlderThanNDays, f.LastCheckinOlderThanDays))
	}
	if f.Query != "" {
		out = append(out, query.Node{Type: "group", Logic: "or", Children: []query.Node{
			query.Cond("name", query.OpStartsWith, f.Query), query.Cond("serial_number", query.OpStartsWith, f.Query)}})
	}
	return out
}

// QueryDevices runs a filter over the live Devices (tombstoned devices only
// when includeDeleted is set or a condition addresses deleted_observed_at).
// Callers without endpoints.view or endpoints.manage see nothing.
func (s *Service) QueryDevices(ctx context.Context, p Principal, req query.Request, f DeviceFilter) (query.Page[Device], error) {
	if !p.canView() {
		return query.Page[Device]{}, ErrForbidden
	}
	qs, ok := s.store.(QueryStore)
	if !ok {
		return query.Page[Device]{}, errNoQueryStore
	}
	req.Filter = query.And(req.Filter, deviceCompat(f)...)
	scope := "live"
	if f.IncludeDeleted {
		scope = "deleted"
	}
	plan, err := s.engine.Prepare(deviceCatalog, p.querySubject(), req, scope, query.Options{})
	if err != nil {
		return query.Page[Device]{}, err
	}
	vis := query.Fragment{}
	if !f.IncludeDeleted && !plan.Uses("deleted_observed_at") {
		vis.SQL = "d.deleted_observed_at IS NULL"
	}
	page, err := qs.QueryDevices(ctx, plan, vis)
	if err != nil {
		return query.Page[Device]{}, fmt.Errorf("query devices: %w", err)
	}
	return page, nil
}
