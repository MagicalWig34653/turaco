package query

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidateSchema checks trusted catalog declarations against the migrated
// database before the API starts accepting queries.
func ValidateSchema(ctx context.Context, pool *pgxpool.Pool, catalogs ...*Catalog) error {
	families := map[Type][]string{
		TypeText: {"text", "character varying"}, TypeEnum: {"text", "character varying"},
		TypeNumber:  {"numeric", "integer", "bigint", "smallint", "double precision"},
		TypeBoolean: {"boolean"}, TypeDate: {"date"}, TypeDateTime: {"timestamp with time zone"},
		TypeReference: {"uuid"},
	}
	for _, cat := range catalogs {
		for _, ref := range cat.ColumnRefs() {
			var typ, nullable string
			err := pool.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
				WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
				ref.Schema, ref.Table, ref.Column).Scan(&typ, &nullable)
			if err != nil {
				return fmt.Errorf("query catalog %s field %s: missing column %s.%s.%s: %w", cat.Key(), ref.Field, ref.Schema, ref.Table, ref.Column, err)
			}
			if !ref.Own {
				continue
			}
			matched := false
			for _, accepted := range families[ref.Type] {
				matched = matched || typ == accepted
			}
			if !matched {
				return fmt.Errorf("query catalog %s field %s: column %s has type %s, expected %s", cat.Key(), ref.Field, ref.Column, typ, ref.Type)
			}
			if nullable == "YES" && !ref.Nullable {
				return fmt.Errorf("query catalog %s field %s: column %s is nullable", cat.Key(), ref.Field, ref.Column)
			}
		}
	}
	return nil
}
