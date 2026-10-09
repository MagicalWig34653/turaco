package query

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	identRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	keyRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	literalRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
)

// Expr is a closed SQL expression. It can only be built by the typed
// constructors below, from identifiers that match a strict lower-case
// pattern; there is no constructor that accepts free SQL text. The catalog
// validation (and a database test) checks every referenced column against the
// module's schema.
type Expr struct {
	sql   string
	alias string
	cols  []string
	err   error
}

// Col references column of the Resource table alias.
func Col(alias, column string) Expr {
	if !identRe.MatchString(alias) || !identRe.MatchString(column) {
		return Expr{err: fmt.Errorf("invalid identifier %q.%q", alias, column)}
	}
	return Expr{sql: alias + "." + column, alias: alias, cols: []string{column}}
}

// Lower is lower(e); it makes text comparison and sorting case-insensitive.
func Lower(e Expr) Expr {
	if e.err != nil {
		return e
	}
	return Expr{sql: "lower(" + e.sql + ")", alias: e.alias, cols: e.cols}
}

// Ordinal maps the text column e to the position of its value in values (an
// unlisted value sorts last). It sorts enumerations by meaning, not by name.
func Ordinal(e Expr, values ...string) Expr {
	if e.err != nil {
		return e
	}
	if len(values) == 0 || len(values) > 16 {
		return Expr{err: fmt.Errorf("ordinal needs 1 to 16 values")}
	}
	var b strings.Builder
	b.WriteString("(CASE " + e.sql)
	for i, v := range values {
		if !literalRe.MatchString(v) {
			return Expr{err: fmt.Errorf("invalid ordinal value %q", v)}
		}
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", v, i)
	}
	fmt.Fprintf(&b, " ELSE %d END)", len(values))
	return Expr{sql: b.String(), alias: e.alias, cols: e.cols}
}

// SQL returns the expression text (for tests and diagnostics).
func (e Expr) SQL() string { return e.sql }

func (e Expr) zero() bool { return e.sql == "" && e.err == nil }
