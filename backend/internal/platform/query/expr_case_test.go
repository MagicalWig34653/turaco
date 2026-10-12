package query

import "testing"

func TestCaseBuildsAClosedSearchedExpression(t *testing.T) {
	due := Col("t", "due_at")
	e := Case([]When{
		{If: ColNotNull(Col("t", "breached_at")), Then: "breached"},
		{If: AnyOf(ColNotAfterNow(due), ColTrue(Col("t", "paused"))), Then: "at_risk"},
		{If: AllOf(ColIsNull(due), Not(ColTrue(Col("t", "paused"))), ColEquals(Col("t", "outcome"), "met")), Then: "met"},
	}, "none")
	want := "(CASE WHEN t.breached_at IS NOT NULL THEN 'breached' WHEN ((t.due_at <= now()) IS TRUE OR t.paused IS TRUE) THEN 'at_risk' " +
		"WHEN (t.due_at IS NULL AND (NOT t.paused IS TRUE) AND t.outcome = 'met') THEN 'met' ELSE 'none' END)"
	if e.err != nil || e.SQL() != want || e.alias != "t" || !e.derived {
		t.Fatalf("got %q (%v)", e.SQL(), e.err)
	}
	if f := Flag(AnyOf(ColNotNull(due), ColTrue(Col("t", "paused")))); f.err != nil || f.SQL() != "(t.due_at IS NOT NULL OR t.paused IS TRUE)" {
		t.Fatalf("flag: %q %v", f.SQL(), f.err)
	}
}

func TestCaseRejectsFreeTextAndMixedAliases(t *testing.T) {
	ok := ColNotNull(Col("t", "a"))
	for name, e := range map[string]Expr{
		"quote in then":     Case([]When{{If: ok, Then: "x'; drop"}}, "none"),
		"quote in fallback": Case([]When{{If: ok, Then: "x"}}, "no'ne"),
		"literal compare":   Case([]When{{If: ColEquals(Col("t", "a"), "a' OR '1"), Then: "x"}}, "none"),
		"mixed aliases":     Case([]When{{If: ok, Then: "x"}, {If: ColNotNull(Col("u", "a")), Then: "y"}}, "none"),
		"mixed in group":    {sql: AllOf(ok, ColNotNull(Col("u", "a"))).sql, err: AllOf(ok, ColNotNull(Col("u", "a"))).err},
		"no branches":       Case(nil, "none"),
		"bad column":        Case([]When{{If: ColNotNull(Col("t", "A;b")), Then: "x"}}, "none"),
	} {
		if e.err == nil {
			t.Errorf("%s: accepted %q", name, e.SQL())
		}
	}
}
