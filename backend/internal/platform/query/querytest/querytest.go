// Package querytest checks query catalogs against the real database schema.
// Import it only from _test.go files.
package querytest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var typeFamilies = map[query.Type][]string{
	query.TypeText:      {"text", "character varying"},
	query.TypeEnum:      {"text", "character varying"},
	query.TypeNumber:    {"numeric", "integer", "bigint", "smallint", "double precision"},
	query.TypeBoolean:   {"boolean"},
	query.TypeDate:      {"date"},
	query.TypeDateTime:  {"timestamp with time zone"},
	query.TypeReference: {"uuid"},
}

// CheckSchema fails the test for every catalog column that does not exist,
// has a data type that does not match the field type, or is nullable in the
// database while the field is not declared Nullable (a NULL would break the
// keyset predicate).
func CheckSchema(t *testing.T, pool *pgxpool.Pool, cat *query.Catalog) {
	t.Helper()
	for _, ref := range cat.ColumnRefs() {
		var dataType, nullable string
		err := pool.QueryRow(context.Background(),
			`SELECT data_type, is_nullable FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			ref.Schema, ref.Table, ref.Column).Scan(&dataType, &nullable)
		if err != nil {
			t.Errorf("catalog %s field %s: column %s.%s.%s does not exist", cat.Key(), ref.Field, ref.Schema, ref.Table, ref.Column)
			continue
		}
		if !ref.Own {
			continue
		}
		families, ok := typeFamilies[ref.Type]
		if !ok {
			continue
		}
		matched := false
		for _, f := range families {
			matched = matched || f == dataType
		}
		if !matched && ref.Type != query.TypeTags {
			t.Errorf("catalog %s field %s: column %s is %s, which does not fit type %s", cat.Key(), ref.Field, ref.Column, dataType, ref.Type)
		}
		if nullable == "YES" && !ref.Nullable {
			t.Errorf("catalog %s field %s: column %s is nullable but the field is not declared Nullable", cat.Key(), ref.Field, ref.Column)
		}
	}
}

// ExplainUsesIndex runs EXPLAIN on a page statement with sequential scans
// disabled and fails unless the plan reads an index and needs no explicit
// sort node (the ordering is served by the index).
func ExplainUsesIndex(t *testing.T, pool *pgxpool.Pool, sql string, args []any) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, sql)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan += line + "\n"
	}
	if !contains(plan, "Index") {
		t.Errorf("the plan reads no index:\n%s", plan)
	}
	if contains(plan, "Sort") && !contains(plan, "Incremental Sort") {
		t.Errorf("the plan sorts explicitly instead of using an index:\n%s", plan)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
