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
	// derived marks a computed expression: its columns are checked for existence but not against the field type.
	derived bool
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

// Pred is a closed boolean SQL condition over columns of one Resource alias. Like Expr it can only be built by the
// constructors below.
type Pred struct {
	sql   string
	alias string
	cols  []string
	err   error
}

func predOf(e Expr, format string) Pred {
	if e.err != nil {
		return Pred{err: e.err}
	}
	if e.zero() {
		return Pred{err: fmt.Errorf("empty expression")}
	}
	return Pred{sql: fmt.Sprintf(format, e.sql), alias: e.alias, cols: e.cols}
}

// ColIsNull is "e IS NULL".
func ColIsNull(e Expr) Pred { return predOf(e, "%s IS NULL") }

// ColNotNull is "e IS NOT NULL".
func ColNotNull(e Expr) Pred { return predOf(e, "%s IS NOT NULL") }

// ColTrue is "e IS TRUE" for a boolean column.
func ColTrue(e Expr) Pred { return predOf(e, "%s IS TRUE") }

// ColNotAfterNow is "(e <= now()) IS TRUE" for a timestamp column (false for NULL).
func ColNotAfterNow(e Expr) Pred { return predOf(e, "(%s <= now()) IS TRUE") }

// ColEquals is "e = 'literal'" for a text column; the literal is a short lower-case token.
func ColEquals(e Expr, literal string) Pred {
	if !literalRe.MatchString(literal) {
		return Pred{err: fmt.Errorf("invalid literal %q", literal)}
	}
	return predOf(e, "%s = '"+literal+"'")
}

func combine(op string, ps []Pred) Pred {
	if len(ps) == 0 {
		return Pred{err: fmt.Errorf("%s needs at least one condition", op)}
	}
	out := Pred{}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.err != nil {
			return p
		}
		if out.alias == "" {
			out.alias = p.alias
		} else if p.alias != out.alias {
			return Pred{err: fmt.Errorf("conditions must use one alias")}
		}
		out.cols = append(out.cols, p.cols...)
		parts = append(parts, p.sql)
	}
	out.sql = "(" + strings.Join(parts, " "+op+" ") + ")"
	return out
}

// AllOf is the conjunction of its conditions.
func AllOf(ps ...Pred) Pred { return combine("AND", ps) }

// AnyOf is the disjunction of its conditions.
func AnyOf(ps ...Pred) Pred { return combine("OR", ps) }

// Not negates a condition. Every constructor above yields true or false, never NULL, so the negation is safe.
func Not(p Pred) Pred {
	if p.err != nil {
		return p
	}
	return Pred{sql: "(NOT " + p.sql + ")", alias: p.alias, cols: p.cols}
}

// Flag is the boolean value of a condition, for a boolean field (is_true / is_false filters).
func Flag(p Pred) Expr {
	if p.err != nil {
		return Expr{err: p.err}
	}
	return Expr{sql: p.sql, alias: p.alias, cols: p.cols, derived: true}
}

// When is one branch of Case.
type When struct {
	If   Pred
	Then string
}

// Case is a searched CASE over text literals: the first branch whose condition holds yields its literal, otherwise
// the fallback. It lets a field derive an enumeration from columns (the SLA state of a Ticket) without free SQL.
func Case(whens []When, otherwise string) Expr {
	if len(whens) == 0 || len(whens) > 16 || !literalRe.MatchString(otherwise) {
		return Expr{err: fmt.Errorf("case needs 1 to 16 branches and a literal fallback")}
	}
	var b strings.Builder
	b.WriteString("(CASE")
	e := Expr{derived: true}
	for _, w := range whens {
		if w.If.err != nil {
			return Expr{err: w.If.err}
		}
		if !literalRe.MatchString(w.Then) {
			return Expr{err: fmt.Errorf("invalid case value %q", w.Then)}
		}
		if e.alias == "" {
			e.alias = w.If.alias
		} else if w.If.alias != e.alias {
			return Expr{err: fmt.Errorf("conditions must use one alias")}
		}
		e.cols = append(e.cols, w.If.cols...)
		fmt.Fprintf(&b, " WHEN %s THEN '%s'", w.If.sql, w.Then)
	}
	fmt.Fprintf(&b, " ELSE '%s' END)", otherwise)
	e.sql = b.String()
	return e
}
