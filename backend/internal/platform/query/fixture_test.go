package query_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

const (
	u1 = "11111111-1111-4111-8111-111111111111"
	u2 = "22222222-2222-4222-8222-222222222222"
	u3 = "33333333-3333-4333-8333-333333333333"
)

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// env is a throw-away table with a catalog over it.
type env struct {
	pool   *pgxpool.Pool
	table  string
	child  string
	cat    *query.Catalog
	engine *query.Engine
}

func itemID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

func ids(ns ...int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = itemID(n)
	}
	return out
}

var textOps = query.OperatorsOf(query.TypeText)

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	e := &env{pool: pool, table: fmt.Sprintf("qt_items_%d", time.Now().UnixNano()%1_000_000_000_000)}
	e.child = e.table + "_tags"
	for _, stmt := range []string{
		`CREATE SCHEMA IF NOT EXISTS querytest`,
		`CREATE TABLE querytest.` + e.table + ` (
			id uuid PRIMARY KEY, title text NOT NULL, note text, qty numeric, flag boolean, status text NOT NULL, prio text NOT NULL,
			owner uuid, born timestamptz, day date, secret text, cost numeric, body text)`,
		`CREATE TABLE querytest.` + e.child + ` (item_id uuid NOT NULL, tag text NOT NULL, state text NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS querytest.`+e.child)
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS querytest.`+e.table)
	})
	e.cat = query.MustCatalog(e.resource())
	e.engine = query.NewEngine([]byte("test-secret")).WithLimiter(query.NewLimiter(10000, 10000))
	return e
}

func (e *env) resource() query.Resource {
	col := func(c string) query.Expr { return query.Col("t", c) }
	return query.Resource{
		Key: "items", Module: "qtest", Schema: "querytest", Table: e.table, Alias: "t", IDColumn: "id",
		DefaultSort: []query.SortSpec{{Field: "title", Dir: "asc"}},
		Fields: []query.Field{
			{Key: "title", Type: query.TypeText, Column: col("title"), SortColumn: query.Lower(col("title")), Operators: textOps,
				Filterable: true, Sortable: true, Searchable: true, SortIndexed: true, Index: query.IndexTrigram},
			{Key: "note", Type: query.TypeText, Column: col("note"), Nullable: true, Operators: textOps, Filterable: true, Searchable: true,
				Index: query.IndexTrigram},
			{Key: "qty", Type: query.TypeNumber, Column: col("qty"), Nullable: true, Operators: query.OperatorsOf(query.TypeNumber),
				Filterable: true, Sortable: true, SortIndexed: true, Index: query.IndexBtree},
			{Key: "flag", Type: query.TypeBoolean, Column: col("flag"), Nullable: true, Operators: query.OperatorsOf(query.TypeBoolean), Filterable: true},
			{Key: "status", Type: query.TypeEnum, Column: col("status"), Operators: query.OperatorsOf(query.TypeEnum), Filterable: true,
				Sortable: true, SortIndexed: true, EnumValues: []query.EnumValue{{Value: "new"}, {Value: "open"}, {Value: "closed"}}},
			{Key: "prio", Type: query.TypeEnum, Column: col("prio"), Operators: query.OperatorsOf(query.TypeEnum), Filterable: true,
				Sortable: true, SortIndexed: true, SortByEnumOrder: true,
				EnumValues: []query.EnumValue{{Value: "urgent"}, {Value: "high"}, {Value: "normal"}, {Value: "low"}}},
			{Key: "owner", Type: query.TypeReference, Reference: "users", Column: col("owner"), Nullable: true, Filterable: true,
				Operators: query.OperatorsOf(query.TypeReference), Sortable: true, SortIndexed: true},
			{Key: "born", Type: query.TypeDateTime, Column: col("born"), Nullable: true, Operators: query.OperatorsOf(query.TypeDateTime),
				Filterable: true, Sortable: true, SortIndexed: true},
			{Key: "day", Type: query.TypeDate, Column: col("day"), Nullable: true, Operators: query.OperatorsOf(query.TypeDate), Filterable: true},
			// A masked value can be displayed masked but never be filtered, sorted or searched.
			{Key: "secret", Type: query.TypeText, Column: col("secret"), Nullable: true, Redaction: query.RedactMasked, Permission: "q.secret"},
			// Permission-gated field: absent for callers without q.cost.
			{Key: "cost", Type: query.TypeNumber, Column: col("cost"), Nullable: true, Operators: query.OperatorsOf(query.TypeNumber),
				Filterable: true, Sortable: true, SortIndexed: true, Permission: "q.cost", Redaction: query.RedactHidden},
			// Gate-restricted (row-specific disclosure) searchable text.
			{Key: "body", Type: query.TypeText, Column: col("body"), Nullable: true, Operators: textOps, Filterable: true, Searchable: true,
				Index: query.IndexTrigram, Gate: func(s query.Subject) bool { return s.Has("q.staff") }, Redaction: query.RedactHidden},
			{Key: "tag", Type: query.TypeTags, Operators: []query.Op{query.OpHasAny, query.OpHasAll, query.OpHasNone}, Filterable: true,
				EnumValues: []query.EnumValue{{Value: "red"}, {Value: "blue"}, {Value: "green"}, {Value: "vip", Permission: "q.vip"}},
				Sub: &query.Sub{Table: e.child, Alias: "g", LinkColumn: "item_id", ValueColumn: "tag",
					Fixed: []query.FixedCond{{Alias: "g", Column: "state", Equals: "live"}}}},
		},
	}
}

// seed inserts the small operator-matrix dataset.
func (e *env) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	rows := []string{
		fmt.Sprintf(`(%d, 'Alpha', 'x', 1, true, 'open', 'high', '%s', '2026-10-01T10:00:00Z', '2026-10-01', 's1', 10, 'b1')`, 1, u1),
		fmt.Sprintf(`(%d, 'alpha two', NULL, 2, false, 'open', 'low', '%s', '2026-10-02T10:00:00Z', '2026-10-02', 's2', 20, NULL)`, 2, u1),
		fmt.Sprintf(`(%d, 'Beta%%', '', 3, NULL, 'closed', 'low', '%s', NULL, NULL, 's3', NULL, 'body3')`, 3, u2),
		fmt.Sprintf(`(%d, 'Be_ta', 'yak', NULL, true, 'closed', 'urgent', '%s', '2026-09-01T00:00:00Z', '2026-09-01', NULL, 40, 'b4')`, 4, u2),
		fmt.Sprintf(`(%d, 'Gamma\x', NULL, 5, false, 'open', 'normal', NULL, '2026-10-08T00:30:00Z', '2026-10-08', 's5', 50, NULL)`, 5),
		fmt.Sprintf(`(%d, 'Delta', 'z', -4.5, NULL, 'new', 'normal', NULL, '2026-10-07T23:30:00Z', '2026-10-07', 's6', 60, 'b6')`, 6),
	}
	for _, r := range rows {
		n := strings.SplitN(strings.TrimPrefix(r, "("), ",", 2)
		var idn int
		fmt.Sscanf(n[0], "%d", &idn)
		sql := `INSERT INTO querytest.` + e.table + ` (id, title, note, qty, flag, status, prio, owner, born, day, secret, cost, body) VALUES ` +
			strings.Replace(r, n[0]+",", "'"+itemID(idn)+"',", 1)
		if _, err := e.pool.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	for _, tag := range []struct {
		item       int
		tag, state string
	}{{1, "red", "live"}, {1, "blue", "live"}, {2, "red", "live"}, {2, "green", "gone"}, {4, "blue", "live"}, {4, "vip", "live"}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO querytest.`+e.child+` VALUES ($1::uuid, $2, $3)`, itemID(tag.item), tag.tag, tag.state); err != nil {
			t.Fatalf("seed tags: %v", err)
		}
	}
}

func (e *env) subject(perms ...string) query.Subject {
	m := map[string]bool{}
	for _, p := range perms {
		m[p] = true
	}
	return query.Subject{UserID: u1, Permissions: m, Now: fixedNow}
}

func cond(field string, op query.Op, value any) query.Node { return query.Cond(field, op, value) }

func group(logic string, children ...query.Node) query.Node {
	return query.Node{Type: "group", Logic: logic, Children: children}
}

func filterOf(n query.Node) *query.Filter { return &query.Filter{V: 1, Root: &n} }

// idsOf runs the request with the given visibility and returns the ids in order.
func (e *env) idsOf(t *testing.T, subj query.Subject, req query.Request, vis query.Fragment) ([]string, query.Page[string], error) {
	t.Helper()
	plan, err := e.engine.Prepare(e.cat, subj, req, "all", query.Options{})
	if err != nil {
		return nil, query.Page[string]{}, err
	}
	page, err := query.Run(context.Background(), e.pool, plan, query.Select{Columns: "t.id::text", Visibility: vis},
		func(rows pgx.Rows, extra []any) (string, error) {
			var id string
			return id, rows.Scan(append([]any{&id}, extra...)...)
		})
	return page.Items, page, err
}

func (e *env) match(t *testing.T, subj query.Subject, n query.Node) []string {
	t.Helper()
	got, _, err := e.idsOf(t, subj, query.Request{Filter: filterOf(n), Limit: 100, Sort: []query.SortSpec{{Field: "title", Dir: "asc"}}}, query.Fragment{})
	if err != nil {
		t.Fatalf("query %v: %v", n, err)
	}
	return got
}
