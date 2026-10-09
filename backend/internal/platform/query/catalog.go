package query

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Type is the value type of a catalog field; it decides the operator set.
type Type string

const (
	TypeText      Type = "text"
	TypeNumber    Type = "number"
	TypeBoolean   Type = "boolean"
	TypeDate      Type = "date"
	TypeDateTime  Type = "datetime"
	TypeEnum      Type = "enum"
	TypeReference Type = "reference"
	TypeTags      Type = "tags"
)

// Op is a filter operator. Names are part of the API.
type Op string

const (
	OpEquals         Op = "equals"
	OpNotEquals      Op = "not_equals"
	OpContains       Op = "contains"
	OpNotContains    Op = "not_contains"
	OpStartsWith     Op = "starts_with"
	OpEndsWith       Op = "ends_with"
	OpIsEmpty        Op = "is_empty"
	OpIsNotEmpty     Op = "is_not_empty"
	OpIn             Op = "in"
	OpNotIn          Op = "not_in"
	OpGreater        Op = "greater"
	OpGreaterOrEqual Op = "greater_or_equal"
	OpLess           Op = "less"
	OpLessOrEqual    Op = "less_or_equal"
	OpBetween        Op = "between"
	OpIsTrue         Op = "is_true"
	OpIsFalse        Op = "is_false"
	OpBefore         Op = "before"
	OpAfter          Op = "after"
	OpLastNDays      Op = "last_n_days"
	OpNextNDays      Op = "next_n_days"
	OpToday          Op = "today"
	OpThisWeek       Op = "this_week"
	OpThisMonth      Op = "this_month"
	OpOlderThanNDays Op = "older_than_n_days"
	OpWithinNDays    Op = "within_n_days_from_now"
	OpIsMe           Op = "is_me"
	OpIsMyTeams      Op = "is_my_teams"
	OpHasAny         Op = "has_any"
	OpHasAll         Op = "has_all"
	OpHasNone        Op = "has_none"
)

var temporalOps = []Op{OpEquals, OpBefore, OpAfter, OpBetween, OpIsEmpty, OpIsNotEmpty,
	OpLastNDays, OpNextNDays, OpToday, OpThisWeek, OpThisMonth, OpOlderThanNDays, OpWithinNDays}

// typeOps is the closed operator set of each type (the table of the design).
var typeOps = map[Type][]Op{
	TypeText: {OpEquals, OpNotEquals, OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpIsEmpty, OpIsNotEmpty, OpIn, OpNotIn},
	TypeNumber: {OpEquals, OpNotEquals, OpGreater, OpGreaterOrEqual, OpLess, OpLessOrEqual, OpBetween,
		OpIsEmpty, OpIsNotEmpty, OpIn, OpNotIn},
	TypeBoolean:   {OpIsTrue, OpIsFalse, OpIsEmpty},
	TypeDate:      temporalOps,
	TypeDateTime:  temporalOps,
	TypeEnum:      {OpEquals, OpNotEquals, OpIn, OpNotIn, OpIsEmpty, OpIsNotEmpty},
	TypeReference: {OpEquals, OpNotEquals, OpIn, OpNotIn, OpIsEmpty, OpIsNotEmpty, OpIsMe, OpIsMyTeams},
	TypeTags:      {OpHasAny, OpHasAll, OpHasNone},
}

// OperatorsOf returns the operator set of a type (for catalog declarations).
func OperatorsOf(t Type) []Op { return slices.Clone(typeOps[t]) }

// Redaction says what a caller without access to a field sees.
type Redaction string

const (
	// RedactNone: the field is visible to everyone who may read the Resource.
	RedactNone Redaction = ""
	// RedactHidden: the field is absent (not filterable, sortable, searchable
	// or listed) for callers without Permission or failing Gate.
	RedactHidden Redaction = "hidden_when_not_permitted"
	// RedactMasked: the value is displayed masked, so no operator, sort or
	// search may exist on it (a masked value must not be revealed by bisection).
	RedactMasked Redaction = "masked"
)

// Index tells the cost model how a filter on the field is served.
type Index int

const (
	// IndexNone: no usable index; the condition is flagged slow.
	IndexNone Index = iota
	// IndexBtree: equality, range and null tests are served by an index.
	IndexBtree
	// IndexTrigram: substring matches are served by a trigram index.
	IndexTrigram
)

// EnumValue is one allowed value of an enum or tags field.
type EnumValue struct {
	Value string
	// Permission, when set, makes the value invisible and unusable for callers
	// without it (it is rejected like an unknown value).
	Permission string
}

// FixedCond is a fixed condition inside a Sub.
type FixedCond struct {
	Alias  string
	Column string
	IsNull bool
	Equals string
}

// SubJoin joins one more table of the module's schema to a Sub.
type SubJoin struct {
	Table       string
	Alias       string
	ChildColumn string
	JoinColumn  string
	Fixed       []FixedCond
}

// Sub describes the multi-valued side of a tags field: rows of a child table
// of the module's own schema linked to the Resource row by LinkColumn, whose
// ValueColumn holds the tag value. The graph is closed: one child table and at
// most one join, both in the Resource's schema.
type Sub struct {
	Table       string
	Alias       string
	LinkColumn  string
	ValueColumn string
	Fixed       []FixedCond
	Join        *SubJoin
}

// Field is one catalog entry. Projection, filter and sort are separate
// declarations: a field can be filterable without being sortable.
type Field struct {
	Key      string
	LabelKey string // i18n key, default "<resource>.field.<key>"
	Type     Type
	// Column is the SQL expression filters (and by default sorts) use.
	Column Expr
	// SortColumn overrides the sort expression (for example Lower(Col)).
	SortColumn Expr
	// SortByEnumOrder sorts an enum field by the position of its value in
	// EnumValues instead of by name (priority: urgent first).
	SortByEnumOrder bool

	Operators  []Op
	Filterable bool
	Sortable   bool
	Searchable bool
	// SortIndexed declares that an index serves the sort; an unindexed sort is
	// refused with query.unindexed_sort.
	SortIndexed bool
	Index       Index
	Nullable    bool

	// Permission and Gate restrict the field to some callers; Redaction says
	// how (see Redact*). Row-specific disclosure (a value only some rows may
	// show) is expressed by a Gate that is false for the callers who see
	// only some rows: the field is then neither filterable, sortable,
	// searchable nor countable for them.
	Permission string
	Gate       func(Subject) bool
	Redaction  Redaction

	EnumValues []EnumValue
	// Reference is the picker resource key of a reference field.
	Reference string
	Sub       *Sub
}

// SortSpec is one sort key of a request or of a Resource default.
type SortSpec struct {
	Field string `json:"field"`
	Dir   string `json:"dir"`
	// Nulls is "first" or "last"; empty uses the PostgreSQL default (nulls are
	// the largest value: last when ascending, first when descending).
	Nulls string `json:"nulls,omitempty"`
}

// Resource declares one queryable list.
type Resource struct {
	Key    string
	Module string
	// Schema, Table and Alias name the one table the Resource reads. Sub
	// tables must live in the same schema.
	Schema string
	Table  string
	Alias  string
	// IDColumn is the unique uuid column used as the keyset tiebreaker.
	IDColumn    string
	DefaultSort []SortSpec
	Fields      []Field
}

// Catalog is a validated Resource.
type Catalog struct {
	res    Resource
	fields map[string]*Field
	order  []string
}

var tableRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// MustCatalog validates res and panics when the declaration is unsafe. Call it
// while building the module (package variable or constructor) so an invalid
// catalog fails startup and every test of the module.
func MustCatalog(res Resource) *Catalog {
	c, err := NewCatalog(res)
	if err != nil {
		panic(fmt.Sprintf("query catalog %q: %v", res.Key, err))
	}
	return c
}

// NewCatalog validates a Resource declaration.
func NewCatalog(res Resource) (*Catalog, error) {
	if !keyRe.MatchString(res.Key) || !keyRe.MatchString(res.Module) {
		return nil, fmt.Errorf("invalid resource or module key")
	}
	for _, id := range []string{res.Schema, res.Table, res.Alias, res.IDColumn} {
		if !tableRe.MatchString(id) {
			return nil, fmt.Errorf("invalid identifier %q", id)
		}
	}
	if len(res.Fields) == 0 || len(res.Fields) > 80 {
		return nil, fmt.Errorf("a resource needs 1 to 80 fields")
	}
	res.Fields = slices.Clone(res.Fields)
	c := &Catalog{res: res, fields: make(map[string]*Field, len(res.Fields))}
	for i := range c.res.Fields {
		if c.res.Fields[i].LabelKey == "" {
			c.res.Fields[i].LabelKey = res.Key + ".field." + c.res.Fields[i].Key
		}
	}
	for i := range c.res.Fields {
		f := &c.res.Fields[i]
		if err := c.validateField(f); err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Key, err)
		}
		if _, dup := c.fields[f.Key]; dup {
			return nil, fmt.Errorf("duplicate field %q", f.Key)
		}
		c.fields[f.Key] = f
		c.order = append(c.order, f.Key)
	}
	if len(res.DefaultSort) == 0 || len(res.DefaultSort) > MaxSortKeys {
		return nil, fmt.Errorf("a resource needs 1 to %d default sort keys", MaxSortKeys)
	}
	for _, s := range res.DefaultSort {
		f, ok := c.fields[s.Field]
		if !ok || !f.Sortable || !f.SortIndexed {
			return nil, fmt.Errorf("default sort %q must be a sortable, indexed field", s.Field)
		}
		if s.Dir != "asc" && s.Dir != "desc" || (s.Nulls != "" && s.Nulls != "first" && s.Nulls != "last") {
			return nil, fmt.Errorf("default sort %q has an invalid direction", s.Field)
		}
		if f.Gate != nil || f.Permission != "" {
			return nil, fmt.Errorf("default sort %q must not be restricted", s.Field)
		}
	}
	return c, nil
}

func (c *Catalog) validateField(f *Field) error {
	if !keyRe.MatchString(f.Key) {
		return fmt.Errorf("invalid key")
	}
	ops, known := typeOps[f.Type]
	if !known {
		return fmt.Errorf("unknown type")
	}
	if f.Column.err != nil {
		return f.Column.err
	}
	if f.Type != TypeTags {
		if f.Column.zero() {
			return fmt.Errorf("column expression missing")
		}
		if f.Column.alias != c.res.Alias {
			return fmt.Errorf("expression must use the resource alias")
		}
	}
	if f.SortColumn.err != nil {
		return f.SortColumn.err
	}
	if !f.SortColumn.zero() && f.SortColumn.alias != c.res.Alias {
		return fmt.Errorf("sort expression must use the resource alias")
	}
	if !f.Filterable && len(f.Operators) > 0 {
		return fmt.Errorf("operators on a field that is not filterable")
	}
	if f.Filterable && len(f.Operators) == 0 {
		return fmt.Errorf("a filterable field needs operators")
	}
	for _, op := range f.Operators {
		if !slices.Contains(ops, op) {
			return fmt.Errorf("operator %q is not valid for type %s", op, f.Type)
		}
	}
	if f.Sortable && f.Type == TypeTags {
		return fmt.Errorf("a tags field cannot be sorted")
	}
	if f.SortIndexed && !f.Sortable {
		return fmt.Errorf("SortIndexed without Sortable")
	}
	if f.SortByEnumOrder && f.Type != TypeEnum {
		return fmt.Errorf("SortByEnumOrder needs an enum")
	}
	if f.Searchable && (f.Type != TypeText || !f.Filterable) {
		return fmt.Errorf("only filterable text fields are searchable")
	}
	switch f.Redaction {
	case RedactNone:
	case RedactHidden:
		if f.Permission == "" && f.Gate == nil {
			return fmt.Errorf("hidden redaction needs a Permission or Gate")
		}
	case RedactMasked:
		if f.Filterable || f.Sortable || f.Searchable {
			return fmt.Errorf("a masked field cannot be filtered, sorted or searched")
		}
	default:
		return fmt.Errorf("unknown redaction")
	}
	if (f.Permission != "" || f.Gate != nil) && f.Redaction == RedactNone {
		return fmt.Errorf("a restricted field must declare its redaction")
	}
	switch f.Type {
	case TypeEnum:
		if len(f.EnumValues) == 0 {
			return fmt.Errorf("enum values missing")
		}
	case TypeReference:
		if f.Reference == "" || !keyRe.MatchString(f.Reference) {
			return fmt.Errorf("reference picker key missing")
		}
	case TypeTags:
		if f.Sub == nil {
			return fmt.Errorf("tags field needs a Sub")
		}
		if err := c.validateSub(f.Sub); err != nil {
			return err
		}
	}
	if f.Type != TypeTags && f.Sub != nil {
		return fmt.Errorf("Sub on a field that is not tags")
	}
	for _, ev := range f.EnumValues {
		if !literalRe.MatchString(ev.Value) {
			return fmt.Errorf("invalid enum value")
		}
	}
	if slices.Contains(f.Operators, OpIsMe) || slices.Contains(f.Operators, OpIsMyTeams) {
		if f.Type != TypeReference {
			return fmt.Errorf("is_me needs a reference")
		}
	}
	return nil
}

func (c *Catalog) validateSub(s *Sub) error {
	for _, id := range []string{s.Table, s.Alias, s.LinkColumn, s.ValueColumn} {
		if !identRe.MatchString(id) {
			return fmt.Errorf("invalid sub identifier")
		}
	}
	if s.Alias == c.res.Alias {
		return fmt.Errorf("sub alias collides with the resource alias")
	}
	aliases := map[string]bool{s.Alias: true}
	if s.Join != nil {
		j := s.Join
		for _, id := range []string{j.Table, j.Alias, j.ChildColumn, j.JoinColumn} {
			if !identRe.MatchString(id) {
				return fmt.Errorf("invalid sub join identifier")
			}
		}
		if aliases[j.Alias] || j.Alias == c.res.Alias {
			return fmt.Errorf("sub join alias collides")
		}
		aliases[j.Alias] = true
	}
	conds := slices.Clone(s.Fixed)
	if s.Join != nil {
		conds = append(conds, s.Join.Fixed...)
	}
	for _, fc := range conds {
		if !aliases[fc.Alias] || !identRe.MatchString(fc.Column) {
			return fmt.Errorf("invalid fixed condition")
		}
		if !fc.IsNull && !literalRe.MatchString(fc.Equals) {
			return fmt.Errorf("invalid fixed condition value")
		}
	}
	return nil
}

// Resource returns the declaration (the fields carry defaults filled in).
func (c *Catalog) Resource() Resource { return c.res }

// Key is the Resource key.
func (c *Catalog) Key() string { return c.res.Key }

// Subject is who runs a query: the module's application layer builds it from
// the authenticated principal. It carries no database access.
type Subject struct {
	UserID      string
	Permissions map[string]bool
	TeamIDs     []string
	// Location is the time zone used for relative dates; nil means UTC.
	Location *time.Location
	// Now is the evaluation time; zero means time.Now().
	Now time.Time
}

// Has reports whether the subject holds the permission.
func (s Subject) Has(permission string) bool { return s.Permissions[permission] }

func (s Subject) loc() *time.Location {
	if s.Location == nil {
		return time.UTC
	}
	return s.Location
}

func (s Subject) now() time.Time {
	if s.Now.IsZero() {
		return time.Now()
	}
	return s.Now
}

// available reports whether the subject may use the field at all.
func (f *Field) available(s Subject) bool {
	if f.Permission != "" && !s.Has(f.Permission) {
		return false
	}
	return f.Gate == nil || f.Gate(s)
}

// allowedValues returns the enum/tag values the subject may use.
func (f *Field) allowedValues(s Subject) map[string]bool {
	if len(f.EnumValues) == 0 {
		return nil
	}
	out := make(map[string]bool, len(f.EnumValues))
	for _, v := range f.EnumValues {
		if v.Permission == "" || s.Has(v.Permission) {
			out[v.Value] = true
		}
	}
	return out
}

func (f *Field) enumOrder() []string {
	out := make([]string, 0, len(f.EnumValues))
	for _, v := range f.EnumValues {
		out = append(out, v.Value)
	}
	return out
}

// get returns the field when the subject may use it.
func (c *Catalog) get(s Subject, key string) (*Field, bool) {
	f, ok := c.fields[key]
	if !ok || !f.available(s) {
		return nil, false
	}
	return f, true
}

// FieldInfo is one field as the caller may use it.
type FieldInfo struct {
	Key        string   `json:"key"`
	LabelKey   string   `json:"labelKey"`
	Type       Type     `json:"type"`
	Operators  []Op     `json:"operators"`
	Filterable bool     `json:"filterable"`
	Sortable   bool     `json:"sortable"`
	Searchable bool     `json:"searchable"`
	Nullable   bool     `json:"nullable"`
	Slow       bool     `json:"slow"`
	EnumValues []string `json:"enumValues,omitempty"`
	Reference  string   `json:"reference,omitempty"`
}

// LimitsInfo mirrors the validation limits so the UI can enforce them early.
type LimitsInfo struct {
	MaxConditions   int `json:"maxConditions"`
	MaxDepth        int `json:"maxDepth"`
	MaxSortKeys     int `json:"maxSortKeys"`
	MaxInValues     int `json:"maxInValues"`
	MaxStringLength int `json:"maxStringLength"`
	MaxSearchLength int `json:"maxSearchLength"`
	DefaultPageSize int `json:"defaultPageSize"`
	MaxPageSize     int `json:"maxPageSize"`
	CountCap        int `json:"countCap"`
}

// Info is the catalog response of GET /<resource>/fields.
type Info struct {
	Resource    string      `json:"resource"`
	Fields      []FieldInfo `json:"fields"`
	DefaultSort []SortSpec  `json:"defaultSort"`
	Limits      LimitsInfo  `json:"limits"`
}

// Describe returns the catalog as the subject may use it. Fields the subject
// may not use are absent, as are restricted enum values.
func (c *Catalog) Describe(s Subject) Info {
	info := Info{Resource: c.res.Key, Fields: []FieldInfo{}, DefaultSort: slices.Clone(c.res.DefaultSort), Limits: Limits()}
	for _, key := range c.order {
		f := c.fields[key]
		if !f.available(s) {
			continue
		}
		fi := FieldInfo{Key: f.Key, LabelKey: f.LabelKey, Type: f.Type, Operators: []Op{}, Filterable: f.Filterable, Sortable: f.Sortable,
			Searchable: f.Searchable && f.Filterable, Nullable: f.Nullable, Reference: f.Reference}
		fi.Operators = append(fi.Operators, f.Operators...)
		allowed := f.allowedValues(s)
		for _, v := range f.EnumValues {
			if allowed[v.Value] {
				fi.EnumValues = append(fi.EnumValues, v.Value)
			}
		}
		for _, op := range f.Operators {
			if opCost(f, op) >= slowCost {
				fi.Slow = true
			}
		}
		info.Fields = append(info.Fields, fi)
	}
	return info
}

// Uses reports whether key names an existing field (for module code that adds
// compatibility conditions).
func (c *Catalog) Uses(key string) bool { _, ok := c.fields[key]; return ok }

// ColumnRef is a database column a catalog entry depends on. Catalog tests
// check every reference against the real schema (querytest.CheckSchema), the
// "closed expression set validated against the module's schema" of ADR-0033.
type ColumnRef struct {
	Field  string
	Schema string
	Table  string
	Column string
	// Type and Nullable are the declared field type and nullability for the
	// field's own column; Own is false for columns of Sub tables.
	Type     Type
	Nullable bool
	Own      bool
}

// ColumnRefs lists every column the catalog reads.
func (c *Catalog) ColumnRefs() []ColumnRef {
	r := c.res
	out := []ColumnRef{{Field: "(id)", Schema: r.Schema, Table: r.Table, Column: r.IDColumn, Type: TypeReference}}
	for _, key := range c.order {
		f := c.fields[key]
		for _, e := range []Expr{f.Column, f.SortColumn} {
			for _, col := range e.cols {
				out = append(out, ColumnRef{Field: f.Key, Schema: r.Schema, Table: r.Table, Column: col, Type: f.Type, Nullable: f.Nullable, Own: true})
			}
		}
		if s := f.Sub; s != nil {
			add := func(table, col string) {
				out = append(out, ColumnRef{Field: f.Key, Schema: r.Schema, Table: table, Column: col})
			}
			add(s.Table, s.LinkColumn)
			add(s.Table, s.ValueColumn)
			conds := slices.Clone(s.Fixed)
			if j := s.Join; j != nil {
				add(s.Table, j.ChildColumn)
				add(j.Table, j.JoinColumn)
				conds = append(conds, j.Fixed...)
			}
			for _, fc := range conds {
				table := s.Table
				if s.Join != nil && fc.Alias == s.Join.Alias {
					table = s.Join.Table
				}
				add(table, fc.Column)
			}
		}
	}
	return out
}
