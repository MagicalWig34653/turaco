package query

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Request is the query body of POST /<resource>/query and, in a different
// encoding, the filter/sort/search/count parameters of the list endpoints.
type Request struct {
	Filter *Filter    `json:"filter"`
	Search string     `json:"search"`
	Sort   []SortSpec `json:"sort"`
	Cursor string     `json:"cursor"`
	Limit  int        `json:"limit"`
	// Count asks for the capped total (at most CountCap, then "capped").
	Count bool `json:"count"`
	// TimeZone is the IANA zone of the person asking (for example "Europe/Berlin"), sent with the request and never
	// stored in a saved filter: plain days, "today", "this week", "last N days" and date-times without an offset are
	// read in it. Empty means UTC.
	TimeZone string `json:"timeZone"`
}

// Options tune Prepare.
type Options struct {
	// Lenient replaces a condition on a field the caller cannot use with "no
	// rows" and reports a warning, as Saved Views need (never dropped, so a
	// permission loss cannot widen the result). Requests are strict.
	Lenient bool
	// Lead is an optional module-defined leading sort key (see LeadKey).
	Lead *LeadKey
	// RateLimited is set by a caller that already took the request's token with Engine.Take. One request that
	// runs several statements for one screen (a Board loads one page per column) takes a single token instead of
	// one per statement.
	RateLimited bool
	// SearchExtra is a trusted, module-written predicate ORed into the search text's matches when a search text is
	// present (for example "the reporter is one of these Users", resolved from the text through another module's
	// public contract). SQL uses ? placeholders bound to Args.
	SearchExtra *SearchExtra
}

// SearchExtra is one extra disjunct of the search.
type SearchExtra struct {
	SQL  string
	Args []any
	// Cost is added to the filter cost; Uses lists catalog fields the predicate reads (for row-disclosure narrowing).
	Cost int
	Uses []string
}

// LeadKey is a trusted, module-written leading ORDER BY key for orderings the
// catalog cannot express, such as a per-board card rank joined in through
// Select.Join. It sorts ascending with NULLs last and is part of the keyset
// cursor. Expr must yield text (use a "C" collated column for bytewise order)
// and must not contain a question mark; it is never built from user input.
type LeadKey struct {
	Expr string
}

// Fragment is trusted SQL written by a module (its visibility predicate or
// compatibility conditions) with ? placeholders and their arguments. It must
// not contain a literal question mark.
type Fragment struct {
	SQL  string
	Args []any
}

// Select is the module's part of a statement.
type Select struct {
	// Columns is the trusted select list (without the sort keys).
	Columns string
	// Visibility is the module's mandatory row-scope predicate, ANDed outside
	// the user filter. An empty SQL means TRUE (everything is visible).
	Visibility Fragment
	// Join is optional trusted SQL appended to the FROM clause (for example a
	// LEFT JOIN that supplies a LeadKey); its placeholders come first.
	Join Fragment
}

// Engine holds the process-wide pieces: cursor signing, rate limit, timeout.
type Engine struct {
	codec   *CursorCodec
	limiter *Limiter
	timeout time.Duration
}

// NewEngine builds an engine. secret signs cursors; the same secret across
// API instances makes cursors portable between them.
func NewEngine(secret []byte) *Engine {
	return &Engine{codec: NewCursorCodec(secret), limiter: NewLimiter(5, 30), timeout: 5 * time.Second}
}

// NewEphemeralEngine uses a random per-process secret (cursors do not survive
// a restart). It is the default of services that were not wired with a
// shared engine.
func NewEphemeralEngine() *Engine {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("query: no randomness: " + err.Error())
	}
	return NewEngine(secret)
}

// WithLimiter replaces the rate limiter (tests).
func (e *Engine) WithLimiter(l *Limiter) *Engine { e.limiter = l; return e }

// WithTimeout sets the statement timeout (default 5 s).
func (e *Engine) WithTimeout(d time.Duration) *Engine { e.timeout = d; return e }

// Plan is a validated, compiled query ready to be run by the module.
type Plan struct {
	cat        *Catalog
	engine     *Engine
	limit      int
	filterSQL  string
	filterArgs []any
	cursorSQL  string
	cursorArgs []any
	keys       []sortKey
	used       map[string]bool
	hash       string
	wantCount  bool
	// Warnings lists conditions replaced by "no rows" (Lenient only).
	Warnings []Warning
}

// Limit is the page size.
func (p *Plan) Limit() int { return p.limit }

// WantCount reports whether the request asked for a capped count.
func (p *Plan) WantCount() bool { return p.wantCount }

// Uses reports whether a filter condition, the search text or the sort names the field (a module may lift an
// implicit default scope or narrow the rows when the request addresses it explicitly).
func (p *Plan) Uses(field string) bool { return p.used[field] }

// Take consumes one token of the subject's per-principal rate limit and returns ErrRateLimited when none is
// left. Prepare takes one itself unless Options.RateLimited says the request already did.
func (e *Engine) Take(subj Subject) error {
	if subj.UserID == "" {
		return invalid("", "A signed-in user is required.")
	}
	if !e.limiter.Allow(subj.UserID) {
		return ErrRateLimited
	}
	return nil
}

// Prepare validates req against the catalog the subject may use and compiles
// it. scope names the module-level visibility variant (for example "all" or
// "mine") and is bound into cursors. It enforces the per-principal rate limit.
func (e *Engine) Prepare(cat *Catalog, subj Subject, req Request, scope string, opts Options) (*Plan, error) {
	if subj.UserID == "" {
		return nil, invalid("", "A signed-in user is required.")
	}
	if req.Limit < 0 || req.Limit > MaxPageSize {
		return nil, tooComplex("limit", "The page size is out of range.")
	}
	if !opts.RateLimited {
		if err := e.Take(subj); err != nil {
			return nil, err
		}
	}
	if req.TimeZone != "" {
		loc, err := time.LoadLocation(req.TimeZone)
		if err != nil || req.TimeZone == "Local" || len(req.TimeZone) > 64 {
			return nil, invalid("timeZone", "The time zone must be an IANA name such as Europe/Berlin.")
		}
		subj.Location = loc
		// Results depend on the zone, so a cursor is valid only for the zone it was issued for.
		scope += "|tz=" + loc.String()
	}
	f := Filter{V: 1}
	if req.Filter != nil {
		f = *req.Filter
	}
	if f.V != 1 {
		return nil, invalid("v", "Unsupported filter version.")
	}
	if req.Search != "" {
		if f.Search != "" {
			return nil, invalid("search", "The search text was given twice.")
		}
		f.Search = req.Search
	}
	if len(req.Sort) > 0 {
		if len(f.Sort) > 0 {
			return nil, invalid("sort", "The sort was given twice.")
		}
		f.Sort = req.Sort
	}
	c := &compiler{cat: cat, subj: subj, extra: opts.SearchExtra, lenient: opts.Lenient, now: subj.now(), loc: subj.loc(), used: map[string]bool{}}
	var where []string
	if f.Root != nil {
		r, err := c.node(*f.Root, 1, "root")
		if err != nil {
			return nil, err
		}
		where = append(where, r.sql)
	}
	if s, err := c.search(f.Search); err != nil {
		return nil, err
	} else if s != "" {
		where = append(where, s)
	}
	if c.cost > MaxCost {
		return nil, tooComplex("", "The filter is too expensive; use fewer or more selective conditions.")
	}
	keys, norm, err := cat.resolveSort(subj, f.Sort)
	if err != nil {
		return nil, err
	}
	for _, sp := range norm {
		c.used[sp.Field] = true
	}
	p := &Plan{cat: cat, engine: e, keys: keys, used: c.used, wantCount: req.Count, Warnings: c.warnings,
		filterSQL: strings.Join(where, " AND "), filterArgs: c.args, limit: req.Limit,
		hash: requestHash(cat.res.Key, scope, subj.UserID, &f, norm)}
	if p.limit <= 0 {
		p.limit = DefaultPageSize
	}
	p.limit = min(p.limit, MaxPageSize)
	if opts.Lead != nil {
		if opts.Lead.Expr == "" || strings.Contains(opts.Lead.Expr, "?") {
			return nil, invalid("", "The leading sort key is invalid.")
		}
		p.keys = append([]sortKey{{expr: opts.Lead.Expr, cast: "text", nullable: true}}, p.keys...)
		keys = p.keys
		p.hash = requestHash(cat.res.Key+"|"+opts.Lead.Expr, scope, subj.UserID, &f, norm)
	}
	if req.Cursor != "" {
		vals, err := e.codec.decode(req.Cursor, p.hash, keys)
		if err != nil {
			return nil, err
		}
		p.cursorSQL = keysetPredicate(keys, vals, func(v any) string { p.cursorArgs = append(p.cursorArgs, v); return "?" })
	}
	return p, nil
}

func (p *Plan) from() string {
	r := p.cat.res
	return r.Schema + "." + r.Table + " " + r.Alias
}

func (p *Plan) where(vis Fragment, withCursor bool) (string, []any) {
	var parts []string
	var args []any
	if vis.SQL != "" {
		parts = append(parts, "("+vis.SQL+")")
		args = append(args, vis.Args...)
	}
	if p.filterSQL != "" {
		parts = append(parts, "("+p.filterSQL+")")
		args = append(args, p.filterArgs...)
	}
	if withCursor && p.cursorSQL != "" {
		parts = append(parts, p.cursorSQL)
		args = append(args, p.cursorArgs...)
	}
	if len(parts) == 0 {
		return "TRUE", args
	}
	return strings.Join(parts, " AND "), args
}

// Statement builds the page statement: the module's columns, then one text
// column per sort key (the last is the id) for the next cursor, the visibility
// predicate, the compiled filter, the keyset predicate, ORDER BY and LIMIT
// (page size + 1, to detect a next page).
func (p *Plan) Statement(sel Select) (string, []any) {
	var sb strings.Builder
	sb.WriteString("SELECT " + sel.Columns)
	for _, k := range p.keys {
		sb.WriteString(", (" + k.expr + ")::text")
	}
	where, args := p.where(sel.Visibility, true)
	sb.WriteString(" FROM " + p.from())
	if sel.Join.SQL != "" {
		sb.WriteString(" " + sel.Join.SQL)
		args = append(append([]any{}, sel.Join.Args...), args...)
	}
	sb.WriteString(" WHERE " + where + " ORDER BY ")
	for i, k := range p.keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k.orderSQL())
	}
	sb.WriteString(" LIMIT ?")
	args = append(args, p.limit+1)
	return numberPlaceholders(sb.String()), args
}

// CountStatement builds the capped count of the whole filtered set (no
// cursor): at most CountCap+1 rows are read.
func (p *Plan) CountStatement(sel Select) (string, []any) {
	where, args := p.where(sel.Visibility, false)
	from := p.from()
	if sel.Join.SQL != "" {
		from += " " + sel.Join.SQL
		args = append(append([]any{}, sel.Join.Args...), args...)
	}
	args = append(args, CountCap+1)
	return numberPlaceholders("SELECT count(*) FROM (SELECT 1 FROM " + from + " WHERE " + where + " LIMIT ?) c"), args
}

func numberPlaceholders(sql string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(sql); i++ {
		if sql[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%s", strconv.Itoa(n))
			continue
		}
		b.WriteByte(sql[i])
	}
	return b.String()
}
