package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// The Problem, Change and Request text searches (migration 000088) are only unbounded in practice when a trigram
// expression index serves the very predicate their repositories emit. The planner is forced off sequential and plain
// index scans so that "an index CAN serve the statement" is asserted, independent of the test table size.
func TestFoldedSearchUsesTrigramExpressionIndexes(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	cases := []struct{ table, column, index string }{
		{"servicedesk.problems", "reference", "problems_reference_fold_trgm_idx"},
		{"servicedesk.problems", "title", "problems_title_fold_trgm_idx"},
		{"changes.changes", "reference", "changes_reference_fold_trgm_idx"},
		{"changes.changes", "title", "changes_title_fold_trgm_idx"},
		{"requests.service_requests", "reference", "service_requests_reference_fold_trgm_idx"},
		{"requests.service_requests", "catalog_item_title", "service_requests_title_fold_trgm_idx"},
	}
	for _, c := range cases {
		sql := "EXPLAIN SELECT id FROM " + c.table + " WHERE " + search.FoldSQL + "(" + c.column + ") ILIKE $1 ESCAPE '\\'"
		var plan []string
		err := query.ReadTx(ctx, pool, 5000, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL enable_indexscan = off`); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, sql, "%drucker%")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("%s.%s: %v", c.table, c.column, err)
		}
		if text := strings.Join(plan, "\n"); !strings.Contains(text, c.index) {
			t.Errorf("%s.%s: the plan does not use %s:\n%s", c.table, c.column, c.index, text)
		}
	}
}
