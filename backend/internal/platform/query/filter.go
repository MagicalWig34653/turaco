package query

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Filter is the versioned Filter AST (and the definition a Saved View stores).
type Filter struct {
	V      int        `json:"v"`
	Root   *Node      `json:"root,omitempty"`
	Search string     `json:"search,omitempty"`
	Sort   []SortSpec `json:"sort,omitempty"`
}

// Node is a group (logic and children) or a condition (field, op, value).
type Node struct {
	Type     string          `json:"type"`
	Logic    string          `json:"logic,omitempty"`
	Children []Node          `json:"children,omitempty"`
	Field    string          `json:"field,omitempty"`
	Op       string          `json:"op,omitempty"`
	Value    json.RawMessage `json:"value,omitempty"`
}

// DecodeFilter strictly decodes a Filter AST: unknown properties, trailing
// data, oversized documents and unsupported versions are rejected.
func DecodeFilter(raw []byte) (*Filter, error) {
	if len(raw) > MaxDefinitionBytes {
		return nil, tooComplex("", "The filter is too large.")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f Filter
	if err := dec.Decode(&f); err != nil {
		return nil, invalid("", "The filter is not a valid filter document.")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("", "The filter is not a valid filter document.")
	}
	if f.V != 1 {
		return nil, invalid("v", "Unsupported filter version.")
	}
	return &f, nil
}

// Cond builds a condition node (for compatibility mappings); a nil value
// means no value.
func Cond(field string, op Op, value any) Node {
	n := Node{Type: "condition", Field: field, Op: string(op)}
	if value != nil {
		n.Value, _ = json.Marshal(value)
	}
	return n
}

// And returns f with the extra conditions ANDed to its root. f may be nil.
func And(f *Filter, extra ...Node) *Filter {
	if len(extra) == 0 {
		return f
	}
	out := Filter{V: 1}
	if f != nil {
		out = *f
	}
	children := slices.Clone(extra)
	if out.Root != nil {
		children = append([]Node{*out.Root}, children...)
	}
	out.Root = &Node{Type: "group", Logic: "and", Children: children}
	return &out
}

// Warning reports a condition that could not be evaluated as written.
type Warning struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

type compiled struct {
	sql     string
	cost    int
	maxCost int
}

type compiler struct {
	cat      *Catalog
	subj     Subject
	lenient  bool
	now      time.Time
	loc      *time.Location
	args     []any
	conds    int
	cost     int
	used     map[string]bool
	warnings []Warning
}

func (c *compiler) arg(v any) string {
	c.args = append(c.args, v)
	return "?"
}

func (c *compiler) node(n Node, depth int, path string) (compiled, error) {
	switch n.Type {
	case "group":
		if n.Field != "" || n.Op != "" || len(n.Value) > 0 {
			return compiled{}, invalid(path, "A group cannot carry a condition.")
		}
		if depth > MaxDepth {
			return compiled{}, tooComplex(path, "Groups are nested too deeply.")
		}
		if n.Logic != "and" && n.Logic != "or" {
			return compiled{}, invalid(path, "A group needs logic \"and\" or \"or\".")
		}
		if len(n.Children) == 0 {
			return compiled{}, invalid(path, "A group cannot be empty.")
		}
		if len(n.Children) > MaxConditions {
			return compiled{}, tooComplex(path, "The group has too many children.")
		}
		parts := make([]string, 0, len(n.Children))
		out := compiled{}
		for i, ch := range n.Children {
			r, err := c.node(ch, depth+1, fmt.Sprintf("%s.children[%d]", path, i))
			if err != nil {
				return compiled{}, err
			}
			parts = append(parts, r.sql)
			out.cost += r.cost
			out.maxCost = max(out.maxCost, r.maxCost)
		}
		if n.Logic == "or" && len(parts) > 1 && out.maxCost >= slowCost {
			return compiled{}, tooComplex(path, "A slow condition cannot be combined with others by OR.")
		}
		out.sql = "(" + strings.Join(parts, " "+strings.ToUpper(n.Logic)+" ") + ")"
		return out, nil
	case "condition":
		if n.Logic != "" || len(n.Children) > 0 {
			return compiled{}, invalid(path, "A condition cannot carry children.")
		}
		return c.condition(n, path)
	}
	return compiled{}, invalid(path, "Unknown node type.")
}

func (c *compiler) condition(n Node, path string) (compiled, error) {
	c.conds++
	if c.conds > MaxConditions {
		return compiled{}, tooComplex(path, "The filter has too many conditions.")
	}
	f, ok := c.cat.get(c.subj, n.Field)
	if !ok || !f.Filterable {
		if c.lenient {
			c.warnings = append(c.warnings, Warning{Code: CodeFieldUnavailable, Path: path})
			return compiled{sql: "FALSE"}, nil
		}
		// Unknown, forbidden and non-filterable fields answer identically.
		return compiled{}, invalid(path, "Unknown or unavailable field.")
	}
	op := Op(n.Op)
	if !slices.Contains(f.Operators, op) {
		return compiled{}, invalid(path, "The operator is not supported for this field.")
	}
	sql, err := c.build(f, op, n.Value, path)
	if err != nil {
		return compiled{}, err
	}
	cost := opCost(f, op)
	c.cost += cost
	c.used[f.Key] = true
	return compiled{sql: sql, cost: cost, maxCost: cost}, nil
}

// ---- value decoding ----

func hasValue(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}

func decodeString(raw json.RawMessage, path string) (string, error) {
	var s string
	if !hasValue(raw) || json.Unmarshal(raw, &s) != nil {
		return "", invalid(path, "The value must be a string.")
	}
	return s, checkString(s, path)
}

func checkString(s, path string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return invalid(path, "The value is not valid text.")
	}
	if utf8.RuneCountInString(s) > MaxStringLength {
		return tooComplex(path, "The value is too long.")
	}
	return nil
}

func decodeStrings(raw json.RawMessage, path string) ([]string, error) {
	var in []string
	if !hasValue(raw) || json.Unmarshal(raw, &in) != nil {
		return nil, invalid(path, "The value must be a list of strings.")
	}
	if len(in) == 0 {
		return nil, invalid(path, "The list cannot be empty.")
	}
	if len(in) > MaxInValues {
		return nil, tooComplex(path, "The list has too many values.")
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if err := checkString(s, path); err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

var numberRe = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,6})?$`)

func decodeNumber(raw json.RawMessage, path string) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if !hasValue(raw) || dec.Decode(&v) != nil {
		return "", invalid(path, "The value must be a number.")
	}
	num, ok := v.(json.Number)
	if !ok || !numberRe.MatchString(num.String()) {
		return "", invalid(path, "The value must be a number.")
	}
	return num.String(), nil
}

func decodeNumbers(raw json.RawMessage, path string) ([]string, error) {
	var list []json.RawMessage
	if !hasValue(raw) || json.Unmarshal(raw, &list) != nil {
		return nil, invalid(path, "The value must be a list of numbers.")
	}
	if len(list) == 0 {
		return nil, invalid(path, "The list cannot be empty.")
	}
	if len(list) > MaxInValues {
		return nil, tooComplex(path, "The list has too many values.")
	}
	out := make([]string, 0, len(list))
	for _, r := range list {
		s, err := decodeNumber(r, path)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func decodePair(raw json.RawMessage, path string) ([2]json.RawMessage, error) {
	var list []json.RawMessage
	if !hasValue(raw) || json.Unmarshal(raw, &list) != nil || len(list) != 2 {
		return [2]json.RawMessage{}, invalid(path, "The value must be a list of two values.")
	}
	return [2]json.RawMessage{list[0], list[1]}, nil
}

func decodeDays(raw json.RawMessage, path string) (int, error) {
	s, err := decodeNumber(raw, path)
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(s)
	if convErr != nil || n < 1 || n > MaxNDays {
		return 0, invalid(path, "The number of days is out of range.")
	}
	return n, nil
}

func noValue(raw json.RawMessage, path string) error {
	if hasValue(raw) {
		return invalid(path, "This operator takes no value.")
	}
	return nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

func decodeUUID(raw json.RawMessage, path string) (string, error) {
	s, err := decodeString(raw, path)
	if err != nil {
		return "", err
	}
	s = strings.ToLower(s)
	if !isUUID(s) {
		return "", invalid(path, "The value must be an identifier.")
	}
	return s, nil
}

func decodeUUIDs(raw json.RawMessage, path string) ([]string, error) {
	in, err := decodeStrings(raw, path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(s)
		if !isUUID(s) {
			return nil, invalid(path, "The value must be a list of identifiers.")
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *compiler) decodeEnum(f *Field, raw json.RawMessage, path string) (string, error) {
	s, err := decodeString(raw, path)
	if err != nil {
		return "", err
	}
	if !f.allowedValues(c.subj)[s] {
		return "", invalid(path, "The value is not supported for this field.")
	}
	return s, nil
}

func (c *compiler) decodeEnums(f *Field, raw json.RawMessage, path string) ([]string, error) {
	in, err := decodeStrings(raw, path)
	if err != nil {
		return nil, err
	}
	allowed := f.allowedValues(c.subj)
	for _, s := range in {
		if allowed != nil && !allowed[s] {
			return nil, invalid(path, "The value is not supported for this field.")
		}
		if allowed == nil && !literalRe.MatchString(s) {
			return nil, invalid(path, "The value is not supported for this field.")
		}
	}
	return in, nil
}

// likeEscape escapes the LIKE metacharacters of a user value.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

const escapeClause = ` ESCAPE '\'`

// ---- SQL per type ----

func (c *compiler) build(f *Field, op Op, raw json.RawMessage, path string) (string, error) {
	col := f.Column.sql
	switch f.Type {
	case TypeText:
		return c.text(f, col, op, raw, path)
	case TypeNumber:
		return c.number(col, op, raw, path)
	case TypeBoolean:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		switch op {
		case OpIsTrue:
			return col + " IS TRUE", nil
		case OpIsFalse:
			return col + " IS FALSE", nil
		}
		return col + " IS NULL", nil
	case TypeEnum:
		return c.enum(f, col, op, raw, path)
	case TypeReference:
		return c.reference(col, op, raw, path)
	case TypeDate, TypeDateTime:
		return c.temporal(f, col, op, raw, path)
	case TypeTags:
		return c.tags(f, op, raw, path)
	}
	return "", invalid(path, "Unsupported field type.")
}

func (c *compiler) text(f *Field, col string, op Op, raw json.RawMessage, path string) (string, error) {
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if op == OpIsEmpty {
			return "(" + col + " IS NULL OR " + col + " = '')", nil
		}
		return "(" + col + " IS NOT NULL AND " + col + " <> '')", nil
	case OpIn, OpNotIn:
		vals, err := decodeStrings(raw, path)
		if err != nil {
			return "", err
		}
		match := "lower(" + col + ") = ANY(ARRAY(SELECT lower(x) FROM unnest(" + c.arg(vals) + "::text[]) AS x))"
		if op == OpIn {
			return match, nil
		}
		return "(" + col + " IS NULL OR NOT (" + match + "))", nil
	}
	v, err := decodeString(raw, path)
	if err != nil {
		return "", err
	}
	switch op {
	case OpEquals:
		return "lower(" + col + ") = lower(" + c.arg(v) + ")", nil
	case OpNotEquals:
		return "(" + col + " IS NULL OR lower(" + col + ") <> lower(" + c.arg(v) + "))", nil
	case OpContains:
		return col + " ILIKE " + c.arg("%"+likeEscape(v)+"%") + escapeClause, nil
	case OpNotContains:
		return "(" + col + " IS NULL OR " + col + " NOT ILIKE " + c.arg("%"+likeEscape(v)+"%") + escapeClause + ")", nil
	case OpStartsWith:
		return col + " ILIKE " + c.arg(likeEscape(v)+"%") + escapeClause, nil
	case OpEndsWith:
		return col + " ILIKE " + c.arg("%"+likeEscape(v)) + escapeClause, nil
	}
	return "", invalid(path, "The operator is not supported for this field.")
}

func (c *compiler) number(col string, op Op, raw json.RawMessage, path string) (string, error) {
	cmp := map[Op]string{OpEquals: "=", OpGreater: ">", OpGreaterOrEqual: ">=", OpLess: "<", OpLessOrEqual: "<="}
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if op == OpIsEmpty {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	case OpIn, OpNotIn:
		vals, err := decodeNumbers(raw, path)
		if err != nil {
			return "", err
		}
		a := c.arg(vals) + "::text[]::numeric[]"
		if op == OpIn {
			return col + " = ANY(" + a + ")", nil
		}
		return "(" + col + " IS NULL OR " + col + " <> ALL(" + a + "))", nil
	case OpBetween:
		pair, err := decodePair(raw, path)
		if err != nil {
			return "", err
		}
		lo, err := decodeNumber(pair[0], path)
		if err != nil {
			return "", err
		}
		hi, err := decodeNumber(pair[1], path)
		if err != nil {
			return "", err
		}
		return col + " BETWEEN " + c.arg(lo) + "::text::numeric AND " + c.arg(hi) + "::text::numeric", nil
	case OpNotEquals:
		v, err := decodeNumber(raw, path)
		if err != nil {
			return "", err
		}
		return col + " IS DISTINCT FROM " + c.arg(v) + "::text::numeric", nil
	}
	sym, ok := cmp[op]
	if !ok {
		return "", invalid(path, "The operator is not supported for this field.")
	}
	v, err := decodeNumber(raw, path)
	if err != nil {
		return "", err
	}
	return col + " " + sym + " " + c.arg(v) + "::text::numeric", nil
}

func (c *compiler) enum(f *Field, col string, op Op, raw json.RawMessage, path string) (string, error) {
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if op == OpIsEmpty {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	case OpIn, OpNotIn:
		vals, err := c.decodeEnums(f, raw, path)
		if err != nil {
			return "", err
		}
		a := c.arg(vals) + "::text[]"
		if op == OpIn {
			return col + " = ANY(" + a + ")", nil
		}
		return "(" + col + " IS NULL OR " + col + " <> ALL(" + a + "))", nil
	}
	v, err := c.decodeEnum(f, raw, path)
	if err != nil {
		return "", err
	}
	if op == OpNotEquals {
		return col + " IS DISTINCT FROM " + c.arg(v) + "::text", nil
	}
	return col + " = " + c.arg(v) + "::text", nil
}

func (c *compiler) reference(col string, op Op, raw json.RawMessage, path string) (string, error) {
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if op == OpIsEmpty {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	case OpIsMe:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if !isUUID(c.subj.UserID) {
			return "FALSE", nil
		}
		return col + " = " + c.arg(c.subj.UserID) + "::uuid", nil
	case OpIsMyTeams:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		teams := make([]string, 0, len(c.subj.TeamIDs))
		for _, t := range c.subj.TeamIDs {
			if isUUID(t) {
				teams = append(teams, t)
			}
		}
		if len(teams) == 0 {
			return "FALSE", nil
		}
		return col + " = ANY(" + c.arg(teams) + "::text[]::uuid[])", nil
	case OpIn, OpNotIn:
		vals, err := decodeUUIDs(raw, path)
		if err != nil {
			return "", err
		}
		a := c.arg(vals) + "::text[]::uuid[]"
		if op == OpIn {
			return col + " = ANY(" + a + ")", nil
		}
		return "(" + col + " IS NULL OR " + col + " <> ALL(" + a + "))", nil
	}
	v, err := decodeUUID(raw, path)
	if err != nil {
		return "", err
	}
	if op == OpNotEquals {
		return col + " IS DISTINCT FROM " + c.arg(v) + "::uuid", nil
	}
	return col + " = " + c.arg(v) + "::uuid", nil
}

func (c *compiler) tags(f *Field, op Op, raw json.RawMessage, path string) (string, error) {
	vals, err := c.decodeEnums(f, raw, path)
	if err != nil {
		return "", err
	}
	s := f.Sub
	from := c.cat.res.Schema + "." + s.Table + " " + s.Alias
	where := s.Alias + "." + s.LinkColumn + " = " + c.cat.res.Alias + "." + c.cat.res.IDColumn
	conds := slices.Clone(s.Fixed)
	if j := s.Join; j != nil {
		from += " JOIN " + c.cat.res.Schema + "." + j.Table + " " + j.Alias + " ON " + j.Alias + "." + j.JoinColumn + " = " + s.Alias + "." + j.ChildColumn
		conds = append(conds, j.Fixed...)
	}
	for _, fc := range conds {
		if fc.IsNull {
			where += " AND " + fc.Alias + "." + fc.Column + " IS NULL"
		} else {
			where += " AND " + fc.Alias + "." + fc.Column + " = '" + fc.Equals + "'"
		}
	}
	match := s.Alias + "." + s.ValueColumn + " = ANY(" + c.arg(vals) + "::text[])"
	switch op {
	case OpHasAny:
		return "EXISTS (SELECT 1 FROM " + from + " WHERE " + where + " AND " + match + ")", nil
	case OpHasNone:
		return "NOT EXISTS (SELECT 1 FROM " + from + " WHERE " + where + " AND " + match + ")", nil
	}
	return "(SELECT count(DISTINCT " + s.Alias + "." + s.ValueColumn + ") FROM " + from + " WHERE " + where + " AND " + match + ") = " + c.arg(len(vals)) + "::bigint", nil
}

// ---- temporal ----

type bound struct {
	t         time.Time
	inclusive bool
}

type window struct{ lo, hi *bound }

// instant parses a datetime or date value. dateOnly marks a plain day.
func (c *compiler) instant(f *Field, raw json.RawMessage, path string) (t time.Time, dateOnly bool, err error) {
	s, err := decodeString(raw, path)
	if err != nil {
		return time.Time{}, false, err
	}
	if d, perr := time.ParseInLocation("2006-01-02", s, c.loc); perr == nil {
		return d, true, nil
	}
	if f.Type == TypeDateTime {
		if ts, perr := time.Parse(time.RFC3339Nano, s); perr == nil {
			return ts, false, nil
		}
	}
	return time.Time{}, false, invalid(path, "The value must be a date.")
}

func (c *compiler) dayStart(t time.Time) time.Time {
	y, m, d := t.In(c.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, c.loc)
}

func (c *compiler) temporal(f *Field, col string, op Op, raw json.RawMessage, path string) (string, error) {
	var w window
	now := c.now.In(c.loc)
	switch op {
	case OpIsEmpty, OpIsNotEmpty:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		if op == OpIsEmpty {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	case OpEquals, OpBefore, OpAfter:
		t, dayOnly, err := c.instant(f, raw, path)
		if err != nil {
			return "", err
		}
		start := c.dayStart(t)
		switch op {
		case OpEquals:
			w = window{&bound{start, true}, &bound{start.AddDate(0, 0, 1), false}}
		case OpBefore:
			if dayOnly {
				w.hi = &bound{start, false}
			} else {
				w.hi = &bound{t, false}
			}
		default:
			if dayOnly {
				w.lo = &bound{start.AddDate(0, 0, 1), true}
			} else {
				w.lo = &bound{t, false}
			}
		}
	case OpBetween:
		pair, err := decodePair(raw, path)
		if err != nil {
			return "", err
		}
		a, aDay, err := c.instant(f, pair[0], path)
		if err != nil {
			return "", err
		}
		b, bDay, err := c.instant(f, pair[1], path)
		if err != nil {
			return "", err
		}
		w.lo = &bound{c.dayStart(a), true}
		if !aDay {
			w.lo = &bound{a, true}
		}
		if bDay {
			w.hi = &bound{c.dayStart(b).AddDate(0, 0, 1), false}
		} else {
			w.hi = &bound{b, true}
		}
	case OpToday:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		s := c.dayStart(now)
		w = window{&bound{s, true}, &bound{s.AddDate(0, 0, 1), false}}
	case OpThisWeek:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		s := c.dayStart(now).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
		w = window{&bound{s, true}, &bound{s.AddDate(0, 0, 7), false}}
	case OpThisMonth:
		if err := noValue(raw, path); err != nil {
			return "", err
		}
		s := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, c.loc)
		w = window{&bound{s, true}, &bound{s.AddDate(0, 1, 0), false}}
	case OpLastNDays, OpNextNDays, OpOlderThanNDays, OpWithinNDays:
		n, err := decodeDays(raw, path)
		if err != nil {
			return "", err
		}
		switch op {
		case OpLastNDays:
			w = window{&bound{now.AddDate(0, 0, -n), true}, &bound{now, true}}
		case OpNextNDays:
			w = window{&bound{now, true}, &bound{now.AddDate(0, 0, n), true}}
		case OpOlderThanNDays:
			w.hi = &bound{now.AddDate(0, 0, -n), false}
		default:
			w.hi = &bound{now.AddDate(0, 0, n), true}
		}
	default:
		return "", invalid(path, "The operator is not supported for this field.")
	}
	var parts []string
	emit := func(b *bound, lower bool) {
		if b == nil {
			return
		}
		sym := map[[2]bool]string{{true, true}: ">=", {true, false}: ">", {false, true}: "<=", {false, false}: "<"}[[2]bool{lower, b.inclusive}]
		if f.Type == TypeDate {
			parts = append(parts, col+" "+sym+" "+c.arg(b.t.In(c.loc).Format("2006-01-02"))+"::text::date")
		} else {
			parts = append(parts, col+" "+sym+" "+c.arg(b.t.UTC()))
		}
	}
	emit(w.lo, true)
	emit(w.hi, false)
	return "(" + strings.Join(parts, " AND ") + ")", nil
}

// ---- search ----

func (c *compiler) search(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if utf8.RuneCountInString(s) > MaxSearchLength || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", invalid("search", "The search text is invalid.")
	}
	var parts []string
	pat := "%" + likeEscape(s) + "%"
	for _, key := range c.cat.order {
		f := c.cat.fields[key]
		// Only fields the caller may filter and that are marked searchable.
		if !f.Searchable || !f.Filterable || !f.available(c.subj) {
			continue
		}
		parts = append(parts, f.Column.sql+" ILIKE "+c.arg(pat)+escapeClause)
		c.cost += opCost(f, OpContains)
	}
	if len(parts) == 0 {
		return "FALSE", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}
