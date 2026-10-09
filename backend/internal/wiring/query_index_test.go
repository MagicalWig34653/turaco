package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Substring matches and search are only offered cheaply where a trigram index serves the very SQL the compiler
// emits (ILIKE with an ESCAPE clause). The planner is forced off sequential and plain index scans so that "an index CAN serve the
// statement" is what is asserted, independent of the table size of the test database.
func TestSubstringAndSearchUseTrigramIndexes(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	engine := QueryEngine(pool)
	staff := query.Subject{UserID: "00000000-0000-7000-8000-0000000009a1", Permissions: map[string]bool{
		"tickets.view": true, "tasks.view": true, "endpoints.view": true}}
	cases := []struct {
		name  string
		cat   *query.Catalog
		req   query.Request
		index []string
	}{
		{"ticket title contains", servicedeskapp.TicketCatalog(), contains("title", "printer"), []string{"tickets_title_trgm_idx"}},
		{"ticket reference contains", servicedeskapp.TicketCatalog(), contains("reference", "0042"), []string{"tickets_reference_trgm_idx"}},
		{"ticket search", servicedeskapp.TicketCatalog(), query.Request{Search: "printer"}, []string{"tickets_reference_trgm_idx", "tickets_title_trgm_idx"}},
		{"task title contains", tasksapp.TaskCatalog(), contains("title", "printer"), []string{"tasks_title_trgm_idx"}},
		{"task search", tasksapp.TaskCatalog(), query.Request{Search: "printer"}, []string{"tasks_title_trgm_idx"}},
		{"device name contains", endpointsapp.DeviceCatalog(), contains("name", "laptop"), []string{"devices_name_trgm_idx"}},
		{"device search", endpointsapp.DeviceCatalog(), query.Request{Search: "laptop"}, []string{"devices_name_trgm_idx"}},
	}
	for _, c := range cases {
		plan, err := engine.Prepare(c.cat, staff, c.req, "all", query.Options{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		sql, args := plan.Statement(query.Select{Columns: c.cat.Resource().Alias + ".id"})
		var explain []string
		err = query.ReadTx(ctx, pool, 5000, func(tx pgx.Tx) error {
			// Without sequential scans and plain index scans (the ordered scan of a sort index would otherwise win on
			// an empty table) only a bitmap scan over an index that serves the ILIKE qual remains.
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL enable_indexscan = off`); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				explain = append(explain, line)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("%s: explain: %v\n%s", c.name, err, sql)
		}
		text := strings.Join(explain, "\n")
		for _, idx := range c.index {
			if !strings.Contains(text, idx) {
				t.Errorf("%s: the plan does not use %s:\n%s", c.name, idx, text)
			}
		}
	}
}

// The planner cannot be forced onto one of two equivalent indexes on an empty test table (a partial btree on the
// serial number also matches), so the serial index is asserted by its definition.
func TestTrigramIndexDefinitions(t *testing.T) {
	pool := dbtest.Pool(t)
	want := map[string]string{
		"tickets_reference_trgm_idx": "USING gin (reference gin_trgm_ops)",
		"tickets_title_trgm_idx":     "USING gin (title gin_trgm_ops)",
		"tasks_title_trgm_idx":       "USING gin (title gin_trgm_ops)",
		"devices_name_trgm_idx":      "USING gin (name gin_trgm_ops)",
		"devices_serial_trgm_idx":    "USING gin (serial_number gin_trgm_ops)",
	}
	for name, def := range want {
		var got string
		if err := pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name).Scan(&got); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(got, def) {
			t.Errorf("%s = %s, want %s", name, got, def)
		}
	}
}

func contains(field, value string) query.Request {
	return query.Request{Filter: &query.Filter{V: 1, Root: &query.Node{Type: "condition", Field: field, Op: string(query.OpContains), Value: []byte(`"` + value + `"`)}}}
}

// The catalogs must not offer an unindexed substring scan: every searchable field has a trigram index, and
// descriptions can only be tested for emptiness.
func TestCatalogsOfferNoUnindexedSubstringScan(t *testing.T) {
	subj := query.Subject{UserID: "u", Permissions: map[string]bool{"tickets.view": true, "tasks.view": true, "endpoints.view": true, "endpoint.management.view": true}}
	for _, cat := range []*query.Catalog{servicedeskapp.TicketCatalog(), tasksapp.TaskCatalog(), endpointsapp.DeviceCatalog()} {
		for _, f := range cat.Describe(subj).Fields {
			if f.Key == "description" {
				for _, op := range f.Operators {
					if op != query.OpIsEmpty && op != query.OpIsNotEmpty {
						t.Errorf("%s.description offers %s, an unindexed scan", cat.Key(), op)
					}
				}
				if f.Searchable {
					t.Errorf("%s.description is searchable", cat.Key())
				}
			}
		}
	}
}
