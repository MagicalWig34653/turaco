package application

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var assetCatalog = query.MustCatalog(query.Resource{
	Key: "assets", Module: "assets", Schema: "assets", Table: "assets", Alias: "a", IDColumn: "id",
	DefaultSort: []query.SortSpec{{Field: "reference", Dir: "asc"}},
	Fields: []query.Field{
		{Key: "reference", Type: query.TypeText, Column: query.Col("a", "reference"), Filterable: true, Searchable: true, Sortable: true, SortIndexed: true, Index: query.IndexTrigram, Operators: []query.Op{query.OpEquals, query.OpStartsWith}},
		{Key: "status", Type: query.TypeEnum, Column: query.Col("a", "status"), Filterable: true, Operators: []query.Op{query.OpEquals, query.OpIn, query.OpNotIn}, EnumValues: assetEnums(Statuses...)},
		{Key: "firmware_version", Type: query.TypeText, Column: query.Col("a", "firmware_version"), Nullable: true, Filterable: true, Operators: []query.Op{query.OpEquals, query.OpIsEmpty, query.OpIsNotEmpty}},
		{Key: "firmware_source", Type: query.TypeText, Column: query.Col("a", "firmware_source"), Nullable: true, Filterable: true, Operators: []query.Op{query.OpEquals, query.OpIsEmpty, query.OpIsNotEmpty}},
		{Key: "firmware_observed_at", Type: query.TypeDateTime, Column: query.Col("a", "firmware_observed_at"), Nullable: true, Filterable: true, Operators: query.OperatorsOf(query.TypeDateTime)},
		{Key: "last_calibrated_at", Type: query.TypeDate, Column: query.Col("a", "last_calibrated_at"), Nullable: true, Filterable: true, Operators: query.OperatorsOf(query.TypeDate)},
		{Key: "next_calibration_due", Type: query.TypeDate, Column: query.Col("a", "next_calibration_due"), Nullable: true, Filterable: true, Sortable: true, SortIndexed: true, Index: query.IndexBtree, Operators: query.OperatorsOf(query.TypeDate)},
		{Key: "next_maintenance_due", Type: query.TypeDate, Column: query.Col("a", "next_maintenance_due"), Nullable: true, Filterable: true, Sortable: true, SortIndexed: true, Index: query.IndexBtree, Operators: query.OperatorsOf(query.TypeDate)},
	},
})

func assetEnums(values ...string) []query.EnumValue {
	out := make([]query.EnumValue, len(values))
	for i, v := range values {
		out[i] = query.EnumValue{Value: v}
	}
	return out
}

func AssetCatalog() *query.Catalog { return assetCatalog }

type QueryStore interface {
	QueryAssets(context.Context, *query.Plan) (query.Page[Asset], error)
}

func (s *Service) WithQueryEngine(e *query.Engine) *Service { s.engine = e; return s }

func (s *Service) QueryFields(p Principal) (query.Info, error) {
	if !p.canView() {
		return query.Info{}, ErrForbidden
	}
	return assetCatalog.Describe(query.Subject{UserID: p.UserID}), nil
}

func (s *Service) Query(ctx context.Context, p Principal, req query.Request) (query.Page[Asset], error) {
	if !p.canView() {
		return query.Page[Asset]{}, ErrForbidden
	}
	qs, ok := s.store.(QueryStore)
	if !ok {
		return query.Page[Asset]{}, errors.New("assets: query store unavailable")
	}
	plan, err := s.engine.Prepare(assetCatalog, query.Subject{UserID: p.UserID}, req, "all", query.Options{})
	if err != nil {
		return query.Page[Asset]{}, err
	}
	return qs.QueryAssets(ctx, plan)
}
