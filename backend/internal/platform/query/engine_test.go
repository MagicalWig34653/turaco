package query_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: got %v, want %v", what, ids2n(g), ids2n(w))
	}
}

func ids2n(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s[len(s)-2:]
	}
	return out
}

func TestOperatorMatrixAndNullSemantics(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	s := e.subject()
	for _, c := range []struct {
		name string
		n    query.Node
		want []string
	}{
		{"text equals is case-insensitive", cond("title", query.OpEquals, "ALPHA"), ids(1)},
		{"text not_equals", cond("title", query.OpNotEquals, "alpha"), ids(2, 3, 4, 5, 6)},
		{"text contains", cond("title", query.OpContains, "ALPHA"), ids(1, 2)},
		{"contains percent is literal", cond("title", query.OpContains, "%"), ids(3)},
		{"contains underscore is literal", cond("title", query.OpContains, "_"), ids(4)},
		{"contains backslash is literal", cond("title", query.OpContains, `\`), ids(5)},
		{"starts_with", cond("title", query.OpStartsWith, "be"), ids(3, 4)},
		{"ends_with", cond("title", query.OpEndsWith, "TWO"), ids(2)},
		{"in", cond("title", query.OpIn, []string{"ALPHA", "delta"}), ids(1, 6)},
		{"not_in includes nothing null here", cond("title", query.OpNotIn, []string{"ALPHA", "delta"}), ids(2, 3, 4, 5)},
		{"is_empty covers null and empty text", cond("note", query.OpIsEmpty, nil), ids(2, 3, 5)},
		{"is_not_empty", cond("note", query.OpIsNotEmpty, nil), ids(1, 4, 6)},
		{"not_contains keeps NULL rows", cond("note", query.OpNotContains, "x"), ids(2, 3, 4, 5, 6)},
		{"not_equals keeps NULL rows", cond("note", query.OpNotEquals, "x"), ids(2, 3, 4, 5, 6)},
		{"number equals", cond("qty", query.OpEquals, 2), ids(2)},
		{"number not_equals keeps NULL rows", cond("qty", query.OpNotEquals, 2), ids(1, 3, 4, 5, 6)},
		{"number greater", cond("qty", query.OpGreater, 2), ids(3, 5)},
		{"number less_or_equal", cond("qty", query.OpLessOrEqual, 1), ids(1, 6)},
		{"number between", cond("qty", query.OpBetween, []int{1, 3}), ids(1, 2, 3)},
		{"number in", cond("qty", query.OpIn, []int{1, 5}), ids(1, 5)},
		{"number not_in keeps NULL rows", cond("qty", query.OpNotIn, []int{1, 5}), ids(2, 3, 4, 6)},
		{"number is_empty", cond("qty", query.OpIsEmpty, nil), ids(4)},
		{"decimal", cond("qty", query.OpEquals, -4.5), ids(6)},
		{"boolean is_true", cond("flag", query.OpIsTrue, nil), ids(1, 4)},
		{"boolean is_false", cond("flag", query.OpIsFalse, nil), ids(2, 5)},
		{"boolean is_empty", cond("flag", query.OpIsEmpty, nil), ids(3, 6)},
		{"enum in", cond("status", query.OpIn, []string{"open", "new"}), ids(1, 2, 5, 6)},
		{"enum not_in", cond("status", query.OpNotIn, []string{"closed"}), ids(1, 2, 5, 6)},
		{"enum not_equals", cond("prio", query.OpNotEquals, "low"), ids(1, 4, 5, 6)},
		{"reference equals", cond("owner", query.OpEquals, u1), ids(1, 2)},
		{"reference not_equals keeps NULL rows", cond("owner", query.OpNotEquals, u1), ids(3, 4, 5, 6)},
		{"reference uppercase uuid", cond("owner", query.OpEquals, strings.ToUpper(u1)), ids(1, 2)},
		{"reference in", cond("owner", query.OpIn, []string{u1, u2}), ids(1, 2, 3, 4)},
		{"reference not_in keeps NULL rows", cond("owner", query.OpNotIn, []string{u1, u2}), ids(5, 6)},
		{"reference is_empty", cond("owner", query.OpIsEmpty, nil), ids(5, 6)},
		{"reference is_me", cond("owner", query.OpIsMe, nil), ids(1, 2)},
		{"datetime before date-only is start of that day", cond("born", query.OpBefore, "2026-10-02"), ids(1, 4)},
		{"datetime after date-only is the next day on", cond("born", query.OpAfter, "2026-10-02"), ids(5, 6)},
		{"datetime equals is the day", cond("born", query.OpEquals, "2026-10-02"), ids(2)},
		{"datetime between days is inclusive", cond("born", query.OpBetween, []string{"2026-10-01", "2026-10-02"}), ids(1, 2)},
		{"datetime between instants", cond("born", query.OpBetween, []string{"2026-10-01T10:00:00Z", "2026-10-02T09:59:59Z"}), ids(1)},
		{"datetime after instant is exclusive", cond("born", query.OpAfter, "2026-10-01T10:00:00Z"), ids(2, 5, 6)},
		{"today", cond("born", query.OpToday, nil), ids(5)},
		{"this_week", cond("born", query.OpThisWeek, nil), ids(5, 6)},
		{"this_month", cond("born", query.OpThisMonth, nil), ids(1, 2, 5, 6)},
		{"last_n_days", cond("born", query.OpLastNDays, 2), ids(5, 6)},
		{"older_than_n_days", cond("born", query.OpOlderThanNDays, 30), ids(4)},
		{"next_n_days", cond("born", query.OpNextNDays, 3), nil},
		{"within_n_days_from_now includes the past", cond("born", query.OpWithinNDays, 1), ids(1, 2, 4, 5, 6)},
		{"datetime is_empty", cond("born", query.OpIsEmpty, nil), ids(3)},
		{"date equals", cond("day", query.OpEquals, "2026-10-02"), ids(2)},
		{"date before", cond("day", query.OpBefore, "2026-10-02"), ids(1, 4)},
		{"date after", cond("day", query.OpAfter, "2026-10-02"), ids(5, 6)},
		{"date this_month", cond("day", query.OpThisMonth, nil), ids(1, 2, 5, 6)},
		{"date last_n_days", cond("day", query.OpLastNDays, 7), ids(1, 2, 5, 6)},
		{"date is_empty", cond("day", query.OpIsEmpty, nil), ids(3)},
		{"tags has_any", cond("tag", query.OpHasAny, []string{"red"}), ids(1, 2)},
		{"tags has_all", cond("tag", query.OpHasAll, []string{"red", "blue"}), ids(1)},
		{"tags has_none", cond("tag", query.OpHasNone, []string{"red"}), ids(3, 4, 5, 6)},
		{"tags fixed condition hides gone tags", cond("tag", query.OpHasAny, []string{"green"}), nil},
		{"and group", group("and", cond("status", query.OpEquals, "open"), cond("prio", query.OpEquals, "low")), ids(2)},
		{"or group", group("or", cond("prio", query.OpEquals, "urgent"), cond("owner", query.OpIsMe, nil)), ids(1, 2, 4)},
		{"nested", group("and", cond("status", query.OpIn, []string{"open", "new"}), group("or", cond("qty", query.OpGreater, 4), cond("flag", query.OpIsTrue, nil))), ids(1, 5)},
	} {
		sameSet(t, c.name, e.match(t, s, c.n), c.want)
	}
}

func TestRelativeDatesUseThePrincipalTimeZone(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	s := e.subject()
	s.Location = berlin
	// 12:00 UTC is 14:00 in Berlin: "today" there starts at 22:00 UTC the day before.
	sameSet(t, "today in Berlin", e.match(t, s, cond("born", query.OpToday, nil)), ids(5, 6))
	sameSet(t, "today in UTC", e.match(t, e.subject(), cond("born", query.OpToday, nil)), ids(5))
}

func TestMixedTeamAndMeOperators(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	s := e.subject()
	s.UserID = u2
	s.TeamIDs = []string{u1, "not-a-uuid"}
	sameSet(t, "is_me", e.match(t, s, cond("owner", query.OpIsMe, nil)), ids(3, 4))
	sameSet(t, "is_my_teams ignores invalid ids", e.match(t, s, cond("owner", query.OpIsMyTeams, nil)), ids(1, 2))
	s.TeamIDs = nil
	sameSet(t, "no teams matches nothing", e.match(t, s, cond("owner", query.OpIsMyTeams, nil)), nil)
}

func TestSearchCoversOnlyPermittedSearchableFields(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	run := func(s query.Subject, text string) []string {
		got, _, err := e.idsOf(t, s, query.Request{Search: text, Limit: 100}, query.Fragment{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	sameSet(t, "search title", run(e.subject(), "ALPHA"), ids(1, 2))
	sameSet(t, "search note", run(e.subject(), "yak"), ids(4))
	// "body3" occurs only in the gated body field: invisible to the search of a caller without the gate.
	sameSet(t, "gated field is not searched without the gate", run(e.subject(), "body3"), nil)
	sameSet(t, "gated field is searched with the gate", run(e.subject("q.staff"), "body3"), ids(3))
	// LIKE metacharacters are literal.
	sameSet(t, "percent", run(e.subject(), "ta%"), ids(3))
	sameSet(t, "underscore", run(e.subject(), "e_t"), ids(4))
	// A text shorter than a trigram would scan the whole index and is refused.
	_, _, err := e.idsOf(t, e.subject(), query.Request{Search: "ab", Limit: 10}, query.Fragment{})
	if qe, ok := query.AsError(err); !ok || qe.Code != query.CodeQueryTooShort || qe.MinLength != query.MinSearchLength {
		t.Errorf("a two-character search must be refused with the structured too-short error, got %v", err)
	}
	rec := httptest.NewRecorder()
	if !query.WriteError(rec, err) || rec.Code != 400 || !strings.Contains(rec.Body.String(), `"minLength":3`) || !strings.Contains(rec.Body.String(), "query.query_too_short") {
		t.Errorf("too-short response = %d %s", rec.Code, rec.Body.String())
	}
	if got := run(e.subject(), "   "); len(got) != 6 {
		t.Errorf("blank search must not filter, got %d rows", len(got))
	}
}

func TestVisibilityPredicateCannotBeWidened(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	vis := query.Fragment{SQL: "t.owner = ?::uuid", Args: []any{u1}}
	s := e.subject()
	for name, n := range map[string]query.Node{
		"or true":       group("or", cond("owner", query.OpEquals, u2), cond("owner", query.OpIsEmpty, nil), cond("status", query.OpIn, []string{"open", "closed", "new"})),
		"not_in":        cond("owner", query.OpNotIn, []string{u1}),
		"nested or":     group("and", group("or", cond("qty", query.OpIsEmpty, nil), cond("qty", query.OpIsNotEmpty, nil))),
		"or with other": group("or", cond("owner", query.OpNotEquals, u1), cond("status", query.OpNotIn, []string{"x"})),
	} {
		got, _, err := e.idsOf(t, s, query.Request{Filter: filterOf(n), Limit: 100}, vis)
		if err != nil {
			if name == "or with other" {
				continue // the unknown enum value is rejected, which is also safe
			}
			t.Fatalf("%s: %v", name, err)
		}
		for _, id := range got {
			if !slices.Contains(ids(1, 2), id) {
				t.Errorf("%s: row %s escaped the visibility predicate", name, id)
			}
		}
	}
	// Search and counts are scoped too.
	_, page, err := e.idsOf(t, s, query.Request{Search: "alp", Count: true, Limit: 100}, vis)
	if err != nil || page.Count == nil || *page.Count > 2 {
		t.Errorf("count escaped visibility: %v %v", page.Count, err)
	}
}

func TestInjectionCorpus(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	payloads := []string{
		`'; DROP TABLE querytest.` + e.table + `; --`, `" OR 1=1 --`, `') OR ('1'='1`, `\`, `%`, `_`, `%_\`, `$1`, `?`, `??`, `$$`,
		"a\u0000b", "'; SELECT pg_sleep(5); --", `ä'ö"ü`, strings.Repeat("%", 150), "‮;--", "1; DELETE FROM x",
	}
	textOpsToTry := []query.Op{query.OpEquals, query.OpNotEquals, query.OpContains, query.OpNotContains, query.OpStartsWith, query.OpEndsWith}
	for _, p := range payloads {
		for _, op := range textOpsToTry {
			plan, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Filter: filterOf(cond("title", op, p))}, "all", query.Options{})
			if err != nil {
				if qe, ok := query.AsError(err); !ok || (qe.Code != query.CodeInvalidFilter && qe.Code != query.CodeQueryTooShort) {
					t.Errorf("payload %q op %s: unexpected error %v", p, op, err)
				}
				continue
			}
			sql, args := plan.Statement(query.Select{Columns: "t.id::text"})
			if strings.Contains(sql, "DROP") || strings.Contains(sql, "pg_sleep") || strings.Contains(sql, "DELETE") {
				t.Errorf("payload text reached the SQL: %s", sql)
			}
			if len(args) == 0 {
				t.Errorf("value was not bound")
			}
			if _, _, err := e.idsOf(t, e.subject(), query.Request{Filter: filterOf(cond("title", op, p)), Limit: 100}, query.Fragment{}); err != nil {
				t.Errorf("payload %q op %s failed to execute: %v", p, op, err)
			}
		}
		// Search carries the payload as a bind value too.
		if _, _, err := e.idsOf(t, e.subject(), query.Request{Search: p, Limit: 100}, query.Fragment{}); err != nil {
			if qe, ok := query.AsError(err); !ok || (qe.Code != query.CodeInvalidFilter && qe.Code != query.CodeQueryTooShort) {
				t.Errorf("search payload %q: %v", p, err)
			}
		}
	}
	// Field keys, operators, sort and enum/reference values never reach SQL text.
	bad := []query.Node{
		cond(`title; DROP TABLE x`, query.OpEquals, "a"), cond(`t.id`, query.OpEquals, "a"), cond(`"title"`, query.OpEquals, "a"),
		cond(`TITLE`, query.OpEquals, "a"), cond(``, query.OpEquals, "a"), cond(`title`, `equals OR 1=1`, "a"), cond(`title`, `=`, "a"),
		cond(`title`, ``, "a"), cond(`status`, query.OpEquals, `open' OR '1'='1`), cond(`owner`, query.OpEquals, `x' OR '1'='1`),
		cond(`owner`, query.OpIn, []string{u1, `x`}), cond(`tag`, query.OpHasAny, []string{`red'); DROP TABLE x; --`}),
		cond(`qty`, query.OpEquals, `1; DROP TABLE x`), cond(`qty`, query.OpEquals, "1"), cond(`born`, query.OpBefore, `2026-01-01' OR true`),
		cond(`qty`, query.OpBetween, []any{1}), cond(`title`, query.OpEquals, 5), cond(`title`, query.OpIn, "a"), cond(`title`, query.OpIn, []string{}),
		cond(`title`, query.OpIsEmpty, "unexpected"), cond(`born`, query.OpLastNDays, 0), cond(`born`, query.OpLastNDays, -1),
		cond(`born`, query.OpLastNDays, 99999), cond(`born`, query.OpLastNDays, 1.5),
		{Type: "condition", Children: []query.Node{cond("title", query.OpEquals, "a")}}, {Type: "group", Logic: "xor", Children: []query.Node{cond("title", query.OpEquals, "a")}},
		{Type: "SELECT"}, {Type: "group", Logic: "and", Field: "title", Children: []query.Node{cond("title", query.OpEquals, "a")}},
	}
	for i, n := range bad {
		_, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Filter: filterOf(n)}, "all", query.Options{})
		if err == nil {
			t.Errorf("bad node %d was accepted: %+v", i, n)
		}
	}
	for _, sort := range [][]query.SortSpec{
		{{Field: "title; DROP TABLE x", Dir: "asc"}}, {{Field: "title", Dir: "asc; DROP TABLE x"}}, {{Field: "title", Dir: "asc", Nulls: "last; --"}},
		{{Field: "t.id", Dir: "asc"}}, {{Field: "note", Dir: "asc"}}, {{Field: "title", Dir: ""}}, {{Field: "title", Dir: "ASC"}},
	} {
		if _, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Sort: sort}, "all", query.Options{}); err == nil {
			t.Errorf("bad sort %+v was accepted", sort)
		}
	}
	// The table and its data are intact.
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM querytest.`+e.table).Scan(&n); err != nil || n != 6 {
		t.Fatalf("table damaged: %v %d", err, n)
	}
}

func TestStrictDecodingOfTheAST(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown property": `{"v":1,"root":{"type":"condition","field":"title","op":"equals","value":"a","sql":"1=1"}}`,
		"trailing data":    `{"v":1} {"v":1}`,
		"wrong version":    `{"v":2}`,
		"no version":       `{}`,
		"not an object":    `[]`,
		"garbage":          `{"v":1,`,
		"unknown top":      `{"v":1,"order":"id"}`,
	} {
		if _, err := query.DecodeFilter([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := query.DecodeFilter([]byte(`{"v":1,"root":{"type":"group","logic":"and","children":[{"type":"condition","field":"title","op":"equals","value":"a"}]},"search":"x","sort":[{"field":"title","dir":"asc"}]}`)); err != nil {
		t.Errorf("valid filter rejected: %v", err)
	}
	big := `{"v":1,"search":"` + strings.Repeat("a", query.MaxDefinitionBytes) + `"}`
	if _, err := query.DecodeFilter([]byte(big)); err == nil {
		t.Error("oversized filter accepted")
	}
	// A deeply nested document is refused by the depth limit, not by crashing.
	deep := strings.Repeat(`{"type":"group","logic":"and","children":[`, 3000) + `{"type":"condition","field":"title","op":"equals","value":"a"}` + strings.Repeat(`]}`, 3000)
	if f, err := query.DecodeFilter([]byte(`{"v":1,"root":` + deep + `}`)); err == nil {
		e := newEnv(t)
		if _, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Filter: f}, "all", query.Options{}); err == nil {
			t.Error("deep nesting accepted")
		}
	}
}

func prepareErr(e *env, s query.Subject, req query.Request) *query.Error {
	_, err := e.engine.Prepare(e.cat, s, req, "all", query.Options{})
	qe, _ := query.AsError(err)
	return qe
}

func TestValidationLimits(t *testing.T) {
	e := newEnv(t)
	s := e.subject()
	many := func(n int) []query.Node {
		out := make([]query.Node, n)
		for i := range out {
			out[i] = cond("status", query.OpEquals, "open")
		}
		return out
	}
	nest := func(depth int) query.Node {
		n := cond("status", query.OpEquals, "open")
		for i := 0; i < depth; i++ {
			n = group("and", n)
		}
		return n
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("and", many(query.MaxConditions)...))}); err != nil {
		t.Errorf("exactly the maximum number of conditions must pass: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("and", many(query.MaxConditions+1)...))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("26 conditions: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("and", group("and", many(20)...), group("and", many(6)...)))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("26 conditions across groups: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(nest(query.MaxDepth))}); err != nil {
		t.Errorf("depth %d must pass: %v", query.MaxDepth, err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(nest(query.MaxDepth + 1))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("depth %d: %v", query.MaxDepth+1, err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("and"))}); err == nil || err.Code != query.CodeInvalidFilter {
		t.Errorf("empty group: %v", err)
	}
	long := make([]string, query.MaxInValues+1)
	for i := range long {
		long[i] = fmt.Sprintf("v%d", i)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(cond("title", query.OpIn, long[:query.MaxInValues]))}); err != nil {
		t.Errorf("100 values must pass: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(cond("title", query.OpIn, long))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("101 values: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(cond("title", query.OpEquals, strings.Repeat("ä", query.MaxStringLength)))}); err != nil {
		t.Errorf("200 characters must pass: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(cond("title", query.OpEquals, strings.Repeat("a", query.MaxStringLength+1)))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("201 characters: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Search: strings.Repeat("a", query.MaxSearchLength+1)}); err == nil || err.Code != query.CodeInvalidFilter {
		t.Errorf("long search: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Sort: []query.SortSpec{{Field: "title", Dir: "asc"}, {Field: "qty", Dir: "asc"}, {Field: "born", Dir: "asc"}, {Field: "prio", Dir: "asc"}}}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("4 sort keys: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Sort: []query.SortSpec{{Field: "title", Dir: "asc"}, {Field: "title", Dir: "desc"}}}); err == nil {
		t.Error("duplicate sort field accepted")
	}
	if err := prepareErr(e, s, query.Request{Search: "a", Filter: &query.Filter{V: 1, Search: "b"}}); err == nil {
		t.Error("search given twice accepted")
	}
	if err := prepareErr(e, s, query.Request{Filter: &query.Filter{V: 3}}); err == nil {
		t.Error("unknown version accepted")
	}
	// Oversized pages are refused; omission uses the documented default.
	if err := prepareErr(e, s, query.Request{Limit: 5000}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("oversized limit: %v", err)
	}
	plan, _ := e.engine.Prepare(e.cat, s, query.Request{}, "all", query.Options{})
	if plan.Limit() != query.DefaultPageSize {
		t.Errorf("default limit %d", plan.Limit())
	}
}

// A trigram index serves a substring of three characters or more: contains is cheap enough for an OR group, a
// shorter text and every not_contains are as slow as an unindexed match.
func TestTrigramCostIsValueAware(t *testing.T) {
	e := newEnv(t)
	s := e.subject()
	or := func(n query.Node) *query.Error {
		return prepareErr(e, s, query.Request{Filter: filterOf(group("or", n, cond("status", query.OpEquals, "open")))})
	}
	if err := or(cond("title", query.OpContains, "alp")); err != nil {
		t.Errorf("indexed contains in an OR group: %v", err)
	}
	if err := or(cond("title", query.OpContains, "al")); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("a two-character contains must count as slow: %v", err)
	}
	if err := or(cond("title", query.OpNotContains, "alpha")); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("not_contains cannot use a trigram index: %v", err)
	}
	if err := or(cond("title", query.OpEndsWith, "pha")); err != nil {
		t.Errorf("indexed ends_with: %v", err)
	}
}

func TestCostLimits(t *testing.T) {
	e := newEnv(t)
	s := e.subject()
	// A slow (unindexed substring) condition cannot be combined with siblings by OR.
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("or", cond("note", query.OpContains, "a"), cond("status", query.OpEquals, "open")))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("slow OR: %v", err)
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("or", cond("note", query.OpContains, "a")))}); err != nil {
		t.Errorf("single-child OR is not a broad OR: %v", err)
	}
	// The same slow condition nested deeper is still found.
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("or", group("and", cond("note", query.OpEndsWith, "a")), cond("status", query.OpEquals, "open")))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("nested slow OR: %v", err)
	}
	// Several slow conditions exhaust the budget.
	var slow []query.Node
	for i := 0; i < 6; i++ {
		slow = append(slow, cond("note", query.OpContains, "a"))
	}
	if err := prepareErr(e, s, query.Request{Filter: filterOf(group("and", slow...))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("budget: %v", err)
	}
	// Search over several unindexed fields costs too and shares the budget.
	if err := prepareErr(e, s, query.Request{Search: "abc", Filter: filterOf(group("and", slow[:5]...))}); err == nil || err.Code != query.CodeTooComplex {
		t.Errorf("search plus conditions: %v", err)
	}
	// Sorting needs an index.
	if err := prepareErr(e, s, query.Request{Sort: []query.SortSpec{{Field: "title", Dir: "asc"}}}); err != nil {
		t.Errorf("indexed sort refused: %v", err)
	}
	noIdx := e.resource()
	for i := range noIdx.Fields {
		if noIdx.Fields[i].Key == "qty" {
			noIdx.Fields[i].SortIndexed = false
		}
	}
	// (the default sort must stay indexed; qty is not the default)
	cat := query.MustCatalog(noIdx)
	_, err := e.engine.Prepare(cat, s, query.Request{Sort: []query.SortSpec{{Field: "qty", Dir: "asc"}}}, "all", query.Options{})
	if qe, _ := query.AsError(err); qe == nil || qe.Code != query.CodeUnindexedSort {
		t.Errorf("unindexed sort: %v", err)
	}
}

func TestCatalogValidationRejectsUnsafeDeclarations(t *testing.T) {
	base := func() query.Resource {
		return query.Resource{Key: "items", Module: "qtest", Schema: "querytest", Table: "x", Alias: "t", IDColumn: "id",
			DefaultSort: []query.SortSpec{{Field: "title", Dir: "asc"}},
			Fields: []query.Field{{Key: "title", Type: query.TypeText, Column: query.Col("t", "title"), Operators: textOps,
				Filterable: true, Sortable: true, SortIndexed: true}}}
	}
	if _, err := query.NewCatalog(base()); err != nil {
		t.Fatalf("base catalog invalid: %v", err)
	}
	mut := func(f func(r *query.Resource)) error {
		r := base()
		f(&r)
		_, err := query.NewCatalog(r)
		return err
	}
	field := func(r *query.Resource) *query.Field { return &r.Fields[0] }
	cases := map[string]func(r *query.Resource){
		"identifier with SQL in the column":  func(r *query.Resource) { field(r).Column = query.Col("t", "title; DROP TABLE x") },
		"uppercase identifier":               func(r *query.Resource) { field(r).Column = query.Col("t", "Title") },
		"quoted identifier":                  func(r *query.Resource) { field(r).Column = query.Col("t", `"title"`) },
		"wrong alias":                        func(r *query.Resource) { field(r).Column = query.Col("other", "title") },
		"zero expression":                    func(r *query.Resource) { field(r).Column = query.Expr{} },
		"injection in the table":             func(r *query.Resource) { r.Table = "x; DROP TABLE y" },
		"injection in the schema":            func(r *query.Resource) { r.Schema = "a.b" },
		"bad field key":                      func(r *query.Resource) { field(r).Key = "Title" },
		"operator of another type":           func(r *query.Resource) { field(r).Operators = []query.Op{query.OpBetween} },
		"unknown operator":                   func(r *query.Resource) { field(r).Operators = []query.Op{"equals; --"} },
		"filterable without operators":       func(r *query.Resource) { field(r).Operators = nil },
		"operators without filterable":       func(r *query.Resource) { field(r).Filterable = false },
		"unknown type":                       func(r *query.Resource) { field(r).Type = "money" },
		"masked but filterable":              func(r *query.Resource) { field(r).Redaction = query.RedactMasked },
		"restricted without redaction":       func(r *query.Resource) { field(r).Permission = "p" },
		"hidden without permission":          func(r *query.Resource) { field(r).Redaction = query.RedactHidden },
		"searchable without a trigram index": func(r *query.Resource) { field(r).Searchable = true },
		"searchable number": func(r *query.Resource) {
			field(r).Type = query.TypeNumber
			field(r).Operators = []query.Op{query.OpEquals}
			field(r).Searchable = true
		},
		"enum without values": func(r *query.Resource) {
			field(r).Type = query.TypeEnum
			field(r).Operators = []query.Op{query.OpEquals}
		},
		"enum value with quote": func(r *query.Resource) {
			field(r).Type = query.TypeEnum
			field(r).Operators = []query.Op{query.OpEquals}
			field(r).EnumValues = []query.EnumValue{{Value: "a'b"}}
		},
		"reference without picker": func(r *query.Resource) {
			field(r).Type = query.TypeReference
			field(r).Operators = []query.Op{query.OpEquals}
		},
		"is_me on text":                 func(r *query.Resource) { field(r).Operators = []query.Op{query.OpIsMe} },
		"sortindexed without sortable":  func(r *query.Resource) { field(r).Sortable = false },
		"sort by enum order on text":    func(r *query.Resource) { field(r).SortByEnumOrder = true },
		"duplicate key":                 func(r *query.Resource) { r.Fields = append(r.Fields, r.Fields[0]) },
		"default sort on unknown field": func(r *query.Resource) { r.DefaultSort = []query.SortSpec{{Field: "nope", Dir: "asc"}} },
		"default sort unindexed":        func(r *query.Resource) { field(r).SortIndexed = false },
		"default sort bad direction":    func(r *query.Resource) { r.DefaultSort = []query.SortSpec{{Field: "title", Dir: "up"}} },
		"no fields":                     func(r *query.Resource) { r.Fields = nil },
		"ordinal with quote":            func(r *query.Resource) { field(r).SortColumn = query.Ordinal(query.Col("t", "title"), "a'b") },
		"tags without sub": func(r *query.Resource) {
			field(r).Type = query.TypeTags
			field(r).Operators = []query.Op{query.OpHasAny}
		},
		"tags sub with bad identifier": func(r *query.Resource) {
			field(r).Type = query.TypeTags
			field(r).Operators = []query.Op{query.OpHasAny}
			field(r).Sortable, field(r).SortIndexed = false, false
			field(r).Column = query.Expr{}
			field(r).Sub = &query.Sub{Table: "tags; --", Alias: "g", LinkColumn: "item_id", ValueColumn: "tag"}
			r.DefaultSort = nil
		},
	}
	for name, f := range cases {
		if err := mut(f); err == nil {
			t.Errorf("%s: invalid catalog accepted", name)
		}
	}
	if !didPanic(func() { query.MustCatalog(query.Resource{}) }) {
		t.Error("MustCatalog must panic on an invalid declaration")
	}
}

func didPanic(f func()) (p bool) {
	defer func() { p = recover() != nil }()
	f()
	return false
}

// sameError reports whether two errors are indistinguishable to the client.
func sameError(a, b *query.Error) bool {
	return a != nil && b != nil && a.Code == b.Code && a.Message == b.Message
}

func TestRedactedFieldsGiveNoOracle(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	plain := e.subject()
	unknownFilter := prepareErr(e, plain, query.Request{Filter: filterOf(cond("does_not_exist", query.OpEquals, "1"))})
	for name, req := range map[string]query.Request{
		"permission field in a filter": {Filter: filterOf(cond("cost", query.OpGreater, 10))},
		"gated field in a filter":      {Filter: filterOf(cond("body", query.OpContains, "b"))},
		"masked field in a filter":     {Filter: filterOf(cond("secret", query.OpStartsWith, "s"))},
	} {
		if err := prepareErr(e, plain, req); !sameError(err, unknownFilter) {
			t.Errorf("%s: %+v differs from the unknown-field answer %+v", name, err, unknownFilter)
		}
	}
	unknownSort := prepareErr(e, plain, query.Request{Sort: []query.SortSpec{{Field: "does_not_exist", Dir: "asc"}}})
	for _, f := range []string{"cost", "body", "secret"} {
		if err := prepareErr(e, plain, query.Request{Sort: []query.SortSpec{{Field: f, Dir: "asc"}}}); !sameError(err, unknownSort) {
			t.Errorf("sort by %s: %+v differs from unknown %+v", f, err, unknownSort)
		}
	}
	// Enum values that need a permission answer like unknown values.
	unknownVal := prepareErr(e, plain, query.Request{Filter: filterOf(cond("tag", query.OpHasAny, []string{"nonexistent"}))})
	if err := prepareErr(e, plain, query.Request{Filter: filterOf(cond("tag", query.OpHasAny, []string{"vip"}))}); !sameError(err, unknownVal) {
		t.Errorf("restricted enum value %+v differs from unknown %+v", err, unknownVal)
	}
	sameSet(t, "vip with permission", e.match(t, e.subject("q.vip"), cond("tag", query.OpHasAny, []string{"vip"})), ids(4))
	// The catalog response omits restricted fields and values.
	keys := func(s query.Subject) []string {
		var out []string
		for _, f := range e.cat.Describe(s).Fields {
			out = append(out, f.Key)
		}
		return out
	}
	for _, k := range []string{"cost", "body", "secret"} {
		if slices.Contains(keys(plain), k) {
			t.Errorf("catalog lists %s to a caller without access", k)
		}
	}
	if !slices.Contains(keys(e.subject("q.cost", "q.staff")), "cost") || !slices.Contains(keys(e.subject("q.cost", "q.staff")), "body") {
		t.Error("catalog hides fields from a caller with access")
	}
	for _, f := range e.cat.Describe(e.subject("q.secret")).Fields {
		if f.Key == "secret" && (f.Filterable || f.Sortable || f.Searchable || len(f.Operators) > 0) {
			t.Error("a masked field must offer no operator, sort or search")
		}
	}
	for _, f := range e.cat.Describe(plain).Fields {
		if f.Key == "tag" && slices.Contains(f.EnumValues, "vip") {
			t.Error("restricted enum value listed")
		}
	}
	// Bisection through ordering: a hidden field cannot steer the sort or the page boundaries either.
	_, page, err := e.idsOf(t, plain, query.Request{Limit: 2}, query.Fragment{})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page: %v", err)
	}
	if strings.Contains(page.NextCursor, "cost") {
		t.Error("cursor mentions a hidden field")
	}
}

func TestLenientModeReplacesUnavailableConditionsWithNoRows(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	n := group("and", cond("status", query.OpEquals, "open"), cond("cost", query.OpGreater, 0))
	plan, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Filter: filterOf(n), Limit: 100}, "all", query.Options{Lenient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != query.CodeFieldUnavailable || plan.Warnings[0].Path != "root.children[1]" {
		t.Fatalf("warnings %+v", plan.Warnings)
	}
	page, err := query.Run(context.Background(), e.pool, plan, query.Select{Columns: "t.id::text"},
		func(rows pgx.Rows, extra []any) (string, error) {
			var id string
			return id, rows.Scan(append([]any{&id}, extra...)...)
		})
	if err != nil || len(page.Items) != 0 || len(page.Warnings) != 1 {
		t.Fatalf("an unavailable condition inside AND must yield no rows (never be dropped): %v %v", page.Items, err)
	}
	// Inside OR the broken condition is just false: the other branch still matches.
	n = group("or", cond("status", query.OpEquals, "new"), cond("cost", query.OpGreater, 0))
	got, _, err := e.idsOf(t, e.subject("q.cost"), query.Request{Filter: filterOf(n), Limit: 100}, query.Fragment{})
	if err != nil || len(got) == 0 {
		t.Fatalf("with the permission the condition works: %v", err)
	}
}

func TestKeysetPaginationTiesNullsMixedDirectionsAndUpdates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// 60 rows with heavy ties and NULLs in every sortable column.
	if _, err := e.pool.Exec(ctx, `INSERT INTO querytest.`+e.table+` (id, title, qty, status, prio, owner, born)
		SELECT ('00000000-0000-4000-8000-' || lpad(to_hex(i), 12, '0'))::uuid,
			(ARRAY['Alpha','beta','Gamma','alpha','Beta'])[1 + i % 5],
			CASE WHEN i % 4 = 0 THEN NULL ELSE (i % 6)::numeric END,
			(ARRAY['new','open','closed'])[1 + i % 3],
			(ARRAY['urgent','high','normal','low'])[1 + i % 4],
			CASE WHEN i % 7 = 0 THEN NULL WHEN i % 2 = 0 THEN '`+u1+`'::uuid ELSE '`+u2+`'::uuid END,
			CASE WHEN i % 5 = 0 THEN NULL ELSE timestamptz '2026-01-01' + (i % 9) * interval '1 hour 0.123456 second' END
		FROM generate_series(1, 60) AS i`); err != nil {
		t.Fatal(err)
	}
	s := e.subject()
	specs := map[string][]query.SortSpec{
		"qty asc (nulls last)":             {{Field: "qty", Dir: "asc"}},
		"qty desc (nulls first)":           {{Field: "qty", Dir: "desc"}},
		"qty desc nulls last":              {{Field: "qty", Dir: "desc", Nulls: "last"}},
		"qty asc nulls first":              {{Field: "qty", Dir: "asc", Nulls: "first"}},
		"prio, qty desc, born nulls first": {{Field: "prio", Dir: "asc"}, {Field: "qty", Dir: "desc"}, {Field: "born", Dir: "asc", Nulls: "first"}},
		"title desc, born asc":             {{Field: "title", Dir: "desc"}, {Field: "born", Dir: "asc"}},
		"born desc nulls last, prio desc":  {{Field: "born", Dir: "desc", Nulls: "last"}, {Field: "prio", Dir: "desc"}},
		"owner asc, status desc, qty asc":  {{Field: "owner", Dir: "asc"}, {Field: "status", Dir: "desc"}, {Field: "qty", Dir: "asc", Nulls: "first"}},
		"status only (massive ties)":       {{Field: "status", Dir: "asc"}},
	}
	for name, sort := range specs {
		full, _, err := e.idsOf(t, s, query.Request{Sort: sort, Limit: 100}, query.Fragment{})
		if err != nil || len(full) != 60 {
			t.Fatalf("%s: full %d %v", name, len(full), err)
		}
		for _, size := range []int{1, 7, 13, 59} {
			var got []string
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > 100 {
					t.Fatalf("%s: runaway pagination", name)
				}
				items, page, err := e.idsOf(t, s, query.Request{Sort: sort, Limit: size, Cursor: cursor}, query.Fragment{})
				if err != nil {
					t.Fatalf("%s size %d: %v", name, size, err)
				}
				got = append(got, items...)
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if !reflect.DeepEqual(got, full) {
				t.Errorf("%s size %d: pages differ from the full result\n got %v\nwant %v", name, size, ids2n(got), ids2n(full))
			}
		}
	}

	// Rows changed between pages may move or repeat once, but unchanged rows are never skipped or repeated.
	sort := specs["prio, qty desc, born nulls first"]
	first, page, err := e.idsOf(t, s, query.Request{Sort: sort, Limit: 10}, query.Fragment{})
	if err != nil || page.NextCursor == "" {
		t.Fatal(err)
	}
	full, _, _ := e.idsOf(t, s, query.Request{Sort: sort, Limit: 100}, query.Fragment{})
	moved, jumped := first[2], full[40]
	for _, u := range []struct{ id, set string }{{moved, "qty = 99, prio = 'low'"}, {jumped, "qty = -99, prio = 'urgent'"}} {
		if _, err := e.pool.Exec(ctx, `UPDATE querytest.`+e.table+` SET `+u.set+` WHERE id = $1::uuid`, u.id); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	for _, id := range first {
		seen[id]++
	}
	cursor := page.NextCursor
	for pages := 0; cursor != "" && pages < 100; pages++ {
		items, p, err := e.idsOf(t, s, query.Request{Sort: sort, Limit: 10, Cursor: cursor}, query.Fragment{})
		if err != nil {
			t.Fatalf("cursor after update: %v", err)
		}
		for _, id := range items {
			seen[id]++
		}
		cursor = p.NextCursor
	}
	for _, id := range full {
		if id == moved || id == jumped {
			continue
		}
		if seen[id] != 1 {
			t.Errorf("unchanged row %s returned %d times", id[len(id)-2:], seen[id])
		}
	}
}

func TestCursorIsSignedAndBoundToTheRequest(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	s := e.subject()
	req := query.Request{Sort: []query.SortSpec{{Field: "qty", Dir: "asc"}}, Limit: 2, Filter: filterOf(cond("status", query.OpIn, []string{"open", "closed", "new"}))}
	_, page, err := e.idsOf(t, s, req, query.Fragment{})
	if err != nil || page.NextCursor == "" {
		t.Fatal(err)
	}
	use := func(subj query.Subject, r query.Request, cursor string) error {
		r.Cursor = cursor
		_, _, err := e.idsOf(t, subj, r, query.Fragment{})
		return err
	}
	if err := use(s, req, page.NextCursor); err != nil {
		t.Fatalf("own cursor rejected: %v", err)
	}
	body, sig, _ := strings.Cut(page.NextCursor, ".")
	flip := func(x string) string {
		b := []byte(x)
		if b[2] == 'A' {
			b[2] = 'B'
		} else {
			b[2] = 'A'
		}
		return string(b)
	}
	other := s
	other.UserID = u3
	otherFilter := req
	otherFilter.Filter = filterOf(cond("status", query.OpIn, []string{"open"}))
	otherSort := req
	otherSort.Sort = []query.SortSpec{{Field: "qty", Dir: "desc"}}
	for name, c := range map[string]struct {
		subj   query.Subject
		req    query.Request
		cursor string
	}{
		"tampered body":      {s, req, flip(body) + "." + sig},
		"tampered signature": {s, req, body + "." + flip(sig)},
		"no signature":       {s, req, body},
		"empty signature":    {s, req, body + "."},
		"garbage":            {s, req, "not a cursor"},
		"another user":       {other, req, page.NextCursor},
		"another filter":     {s, otherFilter, page.NextCursor},
		"another sort":       {s, otherSort, page.NextCursor},
		"oversized":          {s, req, strings.Repeat("a", 5000)},
		"unsigned forgery":   {s, req, "eyJoIjoieCIsImsiOltudWxsXX0.AAAA"},
	} {
		err := use(c.subj, c.req, c.cursor)
		if qe, ok := query.AsError(err); !ok || qe.Code != query.CodeInvalidCursor {
			t.Errorf("%s: want invalid_cursor, got %v", name, err)
		}
	}
	// A different engine secret cannot be replayed.
	other2 := query.NewEngine([]byte("another-secret")).WithLimiter(query.NewLimiter(1000, 1000))
	req.Cursor = page.NextCursor
	if _, err := other2.Prepare(e.cat, s, req, "all", query.Options{}); err == nil {
		t.Error("cursor of another key accepted")
	}
	// A different scope (module visibility variant) cannot be replayed.
	if _, err := e.engine.Prepare(e.cat, s, req, "mine", query.Options{}); err == nil {
		t.Error("cursor of another scope accepted")
	}
}

func TestCappedCount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO querytest.`+e.table+` (id, title, status, prio)
		SELECT ('10000000-0000-4000-8000-' || lpad(to_hex(i), 12, '0'))::uuid, 'row ' || i, 'open', 'low' FROM generate_series(1, 1200) AS i`); err != nil {
		t.Fatal(err)
	}
	_, page, err := e.idsOf(t, e.subject(), query.Request{Count: true, Limit: 5}, query.Fragment{})
	if err != nil || page.Count == nil || *page.Count != query.CountCap || !page.CountCapped {
		t.Fatalf("1200 rows: want capped %d, got %v capped=%v err=%v", query.CountCap, page.Count, page.CountCapped, err)
	}
	_, page, err = e.idsOf(t, e.subject(), query.Request{Count: true, Limit: 5, Filter: filterOf(cond("title", query.OpEquals, "row 7"))}, query.Fragment{})
	if err != nil || page.Count == nil || *page.Count != 1 || page.CountCapped {
		t.Fatalf("exact count: %v %v", page.Count, err)
	}
	_, page, _ = e.idsOf(t, e.subject(), query.Request{Limit: 5}, query.Fragment{})
	if page.Count != nil {
		t.Error("count returned although not requested")
	}
}

func TestStatementTimeoutAndReadOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := query.ReadTx(ctx, e.pool, 50, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT pg_sleep(2)`)
		return err
	}); !errors.Is(err, query.ErrTimeout) {
		t.Errorf("want ErrTimeout, got %v", err)
	}
	if err := query.ReadTx(ctx, e.pool, 1000, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO querytest.`+e.table+` (id, title, status, prio) VALUES (gen_random_uuid(), 'x', 'open', 'low')`)
		return err
	}); err == nil {
		t.Error("the query transaction must be read-only")
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO querytest.`+e.table+` (id, title, note, status, prio)
		SELECT gen_random_uuid(), 'row ' || i, repeat('abcdefghij', 100), 'open', 'low' FROM generate_series(1, 20000) AS i`); err != nil {
		t.Fatal(err)
	}
	slow := newEnv(t)
	slow.engine = query.NewEngine([]byte("k")).WithLimiter(query.NewLimiter(1000, 1000)).WithTimeout(time.Millisecond)
	slow.cat = e.cat
	slow.pool = e.pool
	_, _, err := slow.idsOf(t, slow.subject(), query.Request{Filter: filterOf(cond("note", query.OpContains, "zzzz")), Limit: 5}, query.Fragment{})
	if qe, ok := query.AsError(err); !ok || qe.Code != query.CodeTimeout {
		t.Errorf("statement_timeout must cancel the query: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	e.engine = query.NewEngine([]byte("k")).WithLimiter(query.NewLimiter(0.001, 2))
	s := e.subject()
	for i := 0; i < 2; i++ {
		if _, err := e.engine.Prepare(e.cat, s, query.Request{}, "all", query.Options{}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	_, err := e.engine.Prepare(e.cat, s, query.Request{}, "all", query.Options{})
	if qe, ok := query.AsError(err); !ok || qe.Code != query.CodeRateLimited || qe.Status() != 429 {
		t.Fatalf("third request: %v", err)
	}
	other := s
	other.UserID = u2
	if _, err := e.engine.Prepare(e.cat, other, query.Request{}, "all", query.Options{}); err != nil {
		t.Errorf("another principal is limited separately: %v", err)
	}
}

func TestLimiterRefills(t *testing.T) {
	l := query.NewLimiter(10, 1)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("burst of one")
	}
	time.Sleep(150 * time.Millisecond)
	if !l.Allow("a") {
		t.Error("token did not refill")
	}
}

func TestUsesAndDescribe(t *testing.T) {
	e := newEnv(t)
	plan, err := e.engine.Prepare(e.cat, e.subject(), query.Request{Filter: filterOf(group("and", cond("status", query.OpEquals, "open"), cond("note", query.OpIsEmpty, nil)))}, "all", query.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Uses("status") || !plan.Uses("note") || plan.Uses("born") {
		t.Error("Uses does not report the addressed fields")
	}
	info := e.cat.Describe(e.subject())
	if info.Resource != "items" || info.Limits.MaxConditions != query.MaxConditions || len(info.DefaultSort) != 1 {
		t.Errorf("info %+v", info)
	}
	for _, f := range info.Fields {
		if f.Key == "note" && !f.Slow {
			t.Error("a field offering an unindexed substring operator must be flagged slow")
		}
		if f.Key == "qty" && f.Slow {
			t.Error("qty is not slow")
		}
		if f.LabelKey != "items.field."+f.Key {
			t.Errorf("label key %q", f.LabelKey)
		}
	}
	raw, _ := json.Marshal(info)
	if strings.Contains(string(raw), "t.title") || strings.Contains(string(raw), "querytest") {
		t.Error("the catalog response leaks SQL expressions")
	}
}

func TestParseParams(t *testing.T) {
	v := url.Values{"filter": {`{"v":1,"root":{"type":"condition","field":"title","op":"equals","value":"a"}}`}, "sort": {"qty:desc:last,title:asc"}, "search": {"x"}, "count": {"true"}}
	if !query.HasParams(v) || query.HasParams(url.Values{"status": {"open"}, "limit": {"3"}}) {
		t.Fatal("HasParams")
	}
	req, err := query.ParseParams(v, "cur", 7)
	if err != nil || req.Cursor != "cur" || req.Limit != 7 || req.Search != "x" || !req.Count || len(req.Sort) != 2 || req.Sort[0].Nulls != "last" || req.Filter == nil {
		t.Fatalf("%+v %v", req, err)
	}
	for name, v := range map[string]url.Values{
		"bad filter": {"filter": {"{"}}, "bad sort": {"sort": {"qty"}}, "sort extra": {"sort": {"a:asc:last:x"}},
		"too many sort keys": {"sort": {"a:asc,b:asc,c:asc,d:asc"}}, "bad count": {"count": {"yes"}},
	} {
		if _, err := query.ParseParams(v, "", 10); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAndHelper(t *testing.T) {
	n := cond("a", query.OpEquals, "1")
	if query.And(nil) != nil {
		t.Error("And without extras keeps nil")
	}
	f := query.And(nil, n)
	if f == nil || f.Root == nil || len(f.Root.Children) != 1 {
		t.Fatalf("%+v", f)
	}
	g := query.And(f, n)
	if len(g.Root.Children) != 2 || len(f.Root.Children) != 1 {
		t.Error("And must not mutate its input")
	}
}

func TestUsesCoversSearchAndSort(t *testing.T) {
	e := newEnv(t)
	prep := func(req query.Request) *query.Plan {
		t.Helper()
		plan, err := e.engine.Prepare(e.cat, e.subject(), req, "all", query.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	if p := prep(query.Request{Search: "alpha"}); !p.Uses("title") || !p.Uses("note") || p.Uses("qty") {
		t.Error("search must report the searched fields")
	}
	if p := prep(query.Request{Sort: []query.SortSpec{{Field: "qty", Dir: "asc"}}}); !p.Uses("qty") || p.Uses("note") {
		t.Error("sort must report the sorted fields")
	}
}

// A date-time without an offset (a datetime-local input) is local time in the request's time zone, and the request
// zone also decides plain days and "today"; an unknown zone is refused.
func TestRequestTimeZoneAndZoneLessDateTimes(t *testing.T) {
	e := newEnv(t)
	e.seed(t)
	if _, err := time.LoadLocation("Europe/Berlin"); err != nil {
		t.Skip("no tzdata")
	}
	run := func(tz string, n query.Node) []string {
		t.Helper()
		got, _, err := e.idsOf(t, e.subject(), query.Request{Filter: filterOf(n), Limit: 100, TimeZone: tz}, query.Fragment{})
		if err != nil {
			t.Fatalf("%s: %v", tz, err)
		}
		return got
	}
	after := cond("born", query.OpAfter, "2026-10-01T11:00")
	// 11:00 in Berlin is 09:00Z: the ticket born at 10:00Z is after it; read as UTC it is not.
	sameSet(t, "zone-less in Berlin", run("Europe/Berlin", after), ids(1, 2, 5, 6))
	sameSet(t, "zone-less in UTC", run("", after), ids(2, 5, 6))
	sameSet(t, "explicit offset ignores the zone", run("Europe/Berlin", cond("born", query.OpAfter, "2026-10-01T11:00:00Z")), ids(2, 5, 6))
	sameSet(t, "today in Berlin by request zone", run("Europe/Berlin", cond("born", query.OpToday, nil)), ids(5, 6))
	for _, bad := range []string{"Mars/Base", "Local", strings.Repeat("a", 80)} {
		if _, _, err := e.idsOf(t, e.subject(), query.Request{TimeZone: bad, Limit: 10}, query.Fragment{}); err == nil {
			t.Errorf("time zone %q must be refused", bad)
		}
	}
}
