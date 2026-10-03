// Package relationships implements the generic Relationship of the core data
// model (docs/domain/core-data-model.md): a typed, time-bounded link between
// two records of any module, stored in platform.relationships.
//
// Modules register the (source type, relationship type, target type) triples
// they own in a Registry at startup; Link refuses any other triple and
// Link/Unlink refuse a module that does not own the triple. Records
// are referenced by id only (no foreign keys), so the module that links is
// responsible for checking that both records exist and may be linked. Domain
// invariants and history (assignments, placements) stay in module tables;
// relationship rows are informational links used for impact views.
//
// There is at most one current row per (source, type, target). Ending a
// relationship keeps the row with a reason code (valid_until, end_reason).
// Traverse is breadth-first, cycle-safe and bounded in depth and size; it
// reports truncation instead of cutting silently.
package relationships

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Confidence says where a relationship comes from.
const (
	ConfidenceDeclared = "declared" // stated by a person
	ConfidenceDerived  = "derived"  // derived by Turaco from its own data
	ConfidenceObserved = "observed" // reported by an integration
)

// Traversal bounds. MaxDepth and MaxNodes are the ceilings for Traverse.
const (
	MaxDepth     = 6
	MaxNodes     = 500
	DefaultLimit = 100
	MaxLimit     = 500

	// Reaches (cycle checks) searches deeper than Traverse because a missed
	// cycle is a data error, not a display limit.
	reachDepth = 50
	reachNodes = 5000
	// edgeScanLimit bounds the edges read for one breadth-first level.
	edgeScanLimit = 5000
)

// Direction selects which way Traverse follows relationships.
type Direction int

const (
	// Forward follows relationships from source to target.
	Forward Direction = iota
	// Reverse follows relationships from target to source.
	Reverse
)

var (
	// ErrNotAllowed means the (source type, type, target type) triple is not
	// registered, or not owned by the module that tries to change it.
	ErrNotAllowed = errors.New("relationships: the relationship is not allowed between these record types")
	// ErrInvalid means an argument is malformed (bad id, type, confidence or reason).
	ErrInvalid = errors.New("relationships: invalid argument")
	// ErrNotFound means there is no such relationship.
	ErrNotFound = errors.New("relationships: not found")
	// ErrTooLarge means a cycle check hit its search limit without an answer.
	ErrTooLarge = errors.New("relationships: the graph is too large to check")
)

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
	relPattern    = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)
	reasonPattern = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
	uuidPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Node identifies a record of any module.
type Node struct {
	Type string
	ID   string
}

// Triple is an allowed relationship shape. Owner names the module that may
// create and end relationships of this shape (e.g. "services"); a module that
// later writes the same shape from elsewhere must go through the owner.
type Triple struct {
	SourceType string
	Type       string
	TargetType string
	Owner      string
}

type shape struct{ source, typ, target string }

// Registry holds the allowed triples and their owners. It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	allowed map[shape]string // shape -> owner
}

func NewRegistry() *Registry { return &Registry{allowed: map[shape]string{}} }

// Register adds allowed triples; registering one twice is harmless. It
// panics on malformed names, a missing owner or a second owner for the same
// shape: triples are compile-time decisions of modules.
func (r *Registry) Register(triples ...Triple) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range triples {
		if !typePattern.MatchString(t.SourceType) || !typePattern.MatchString(t.TargetType) || !relPattern.MatchString(t.Type) || !typePattern.MatchString(t.Owner) {
			panic(fmt.Sprintf("relationships: malformed triple %+v", t))
		}
		k := shape{t.SourceType, t.Type, t.TargetType}
		if o, ok := r.allowed[k]; ok && o != t.Owner {
			panic(fmt.Sprintf("relationships: triple %+v is already owned by %q", t, o))
		}
		r.allowed[k] = t.Owner
	}
}

// Allowed reports whether the triple is registered.
func (r *Registry) Allowed(sourceType, typ, targetType string) bool {
	_, ok := r.Owner(sourceType, typ, targetType)
	return ok
}

// Owner returns the module that owns the triple.
func (r *Registry) Owner(sourceType, typ, targetType string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.allowed[shape{sourceType, typ, targetType}]
	return o, ok
}

// ownedBy returns the shapes of an owner as parallel arrays for SQL.
func (r *Registry) ownedBy(owner string) (sources, types, targets []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sources, types, targets = []string{}, []string{}, []string{}
	for k, o := range r.allowed {
		if o == owner {
			sources, types, targets = append(sources, k.source), append(types, k.typ), append(targets, k.target)
		}
	}
	return
}

// Types lists the registered relationship types, sorted.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	for t := range r.allowed {
		seen[t.typ] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Relationship is one stored link.
type Relationship struct {
	ID         string
	Source     Node
	Type       string
	Target     Node
	Confidence string
	ValidFrom  time.Time
	ValidUntil *time.Time
	EndReason  *string
	CreatedBy  *string
	EndedBy    *string
	// RecordedBy is the free "source" column: a module name or integration key.
	RecordedBy *string
}

// Current reports whether the relationship has not ended.
func (r Relationship) Current() bool { return r.ValidUntil == nil }

// LinkInput describes a relationship to create. Owner must be the module that
// owns the triple.
type LinkInput struct {
	Owner      string
	Source     Node
	Type       string
	Target     Node
	Confidence string
	// CreatedBy is a User id; empty for system-derived links.
	CreatedBy string
	// RecordedBy names the module or integration that records the link (optional, at most 50 characters).
	RecordedBy string
}

// Graph links, unlinks, reads and traverses relationships against a Registry.
type Graph struct{ reg *Registry }

func New(reg *Registry) *Graph { return &Graph{reg: reg} }

// Registry returns the registry the graph validates against.
func (g *Graph) Registry() *Registry { return g.reg }

const cols = `id::text, source_type, source_id::text, type, target_type, target_id::text, confidence, valid_from, valid_until, end_reason, created_by::text, ended_by::text, source`

func scan(row pgx.Row) (Relationship, error) {
	var r Relationship
	err := row.Scan(&r.ID, &r.Source.Type, &r.Source.ID, &r.Type, &r.Target.Type, &r.Target.ID, &r.Confidence, &r.ValidFrom, &r.ValidUntil, &r.EndReason, &r.CreatedBy, &r.EndedBy, &r.RecordedBy)
	return r, err
}

// checkNode validates a node and returns it with a lower-case id, the form
// PostgreSQL returns, so comparisons and visited sets agree.
func checkNode(n Node) (Node, error) {
	if !typePattern.MatchString(n.Type) || !uuidPattern.MatchString(n.ID) {
		return Node{}, fmt.Errorf("%w: node must have a type and a UUID", ErrInvalid)
	}
	return Node{Type: n.Type, ID: strings.ToLower(n.ID)}, nil
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Link creates the relationship, or returns the current one when it exists
// already (created is false then and nothing changes, even the confidence).
// Duplicate links, also concurrent ones, therefore end with one row. Pass a
// transaction to link atomically with the change that causes the link.
func (g *Graph) Link(ctx context.Context, q Querier, in LinkInput) (rel Relationship, created bool, err error) {
	src, err := checkNode(in.Source)
	if err != nil {
		return Relationship{}, false, err
	}
	dst, err := checkNode(in.Target)
	if err != nil {
		return Relationship{}, false, err
	}
	owner, ok := g.reg.Owner(src.Type, in.Type, dst.Type)
	if !ok || owner != in.Owner {
		return Relationship{}, false, ErrNotAllowed
	}
	if in.Confidence != ConfidenceDeclared && in.Confidence != ConfidenceDerived && in.Confidence != ConfidenceObserved {
		return Relationship{}, false, fmt.Errorf("%w: unknown confidence", ErrInvalid)
	}
	if src == dst {
		return Relationship{}, false, fmt.Errorf("%w: a record cannot be linked to itself", ErrInvalid)
	}
	var createdBy *string
	if in.CreatedBy != "" {
		if !uuidPattern.MatchString(in.CreatedBy) {
			return Relationship{}, false, fmt.Errorf("%w: created by must be a UUID", ErrInvalid)
		}
		c := strings.ToLower(in.CreatedBy)
		createdBy = &c
	}
	var recorded *string
	if in.RecordedBy != "" {
		if in.RecordedBy != strings.TrimSpace(in.RecordedBy) || len(in.RecordedBy) > 50 {
			return Relationship{}, false, fmt.Errorf("%w: recorded by must be at most 50 characters", ErrInvalid)
		}
		recorded = &in.RecordedBy
	}
	// A conflicting current row may end between the insert and the read-back
	// (another transaction unlinks it); then the insert is simply retried once.
	for attempt := 0; attempt < 2; attempt++ {
		rel, err = scan(q.QueryRow(ctx, `
			INSERT INTO platform.relationships (source_type, source_id, type, target_type, target_id, confidence, created_by, source)
			VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6, $7::uuid, $8)
			ON CONFLICT (source_type, source_id, type, target_type, target_id) WHERE valid_until IS NULL DO NOTHING
			RETURNING `+cols, src.Type, src.ID, in.Type, dst.Type, dst.ID, in.Confidence, createdBy, recorded))
		if err == nil {
			return rel, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Relationship{}, false, fmt.Errorf("link relationship: %w", err)
		}
		// A current row exists (possibly committed by a concurrent transaction
		// that ON CONFLICT waited for).
		rel, err = scan(q.QueryRow(ctx, `
			SELECT `+cols+` FROM platform.relationships
			WHERE source_type = $1 AND source_id = $2::uuid AND type = $3 AND target_type = $4 AND target_id = $5::uuid AND valid_until IS NULL`,
			src.Type, src.ID, in.Type, dst.Type, dst.ID))
		if err == nil {
			return rel, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Relationship{}, false, fmt.Errorf("read existing relationship: %w", err)
		}
	}
	return Relationship{}, false, errors.New("link relationship: the current relationship kept changing")
}

func checkReason(reason string) error {
	if !reasonPattern.MatchString(reason) {
		return fmt.Errorf("%w: reason must be a lower-case code", ErrInvalid)
	}
	return nil
}

func actorArg(endedBy string) (*string, error) {
	if endedBy == "" {
		return nil, nil
	}
	if !uuidPattern.MatchString(endedBy) {
		return nil, fmt.Errorf("%w: ended by must be a UUID", ErrInvalid)
	}
	b := strings.ToLower(endedBy)
	return &b, nil
}

// ownedFilter restricts an UPDATE to the triples the owner owns.
const ownedFilter = `(source_type, type, target_type) IN (SELECT * FROM unnest(%s::text[], %s::text[], %s::text[]))`

// The end timestamp is clock_timestamp() (never earlier than valid_from): now()
// is the transaction start and can precede a valid_from committed by another
// transaction while this one waited for a lock.
const endSet = `valid_until = greatest(clock_timestamp(), valid_from)`

// Unlink ends the relationship with a reason code; owner must own its triple
// (else ErrNotAllowed). Ending an ended relationship returns it unchanged
// (ended is false), so retries are safe. ErrNotFound when no such
// relationship exists.
func (g *Graph) Unlink(ctx context.Context, q Querier, owner, id, reason, endedBy string) (rel Relationship, ended bool, err error) {
	if !uuidPattern.MatchString(id) {
		return Relationship{}, false, ErrNotFound
	}
	if err := checkReason(reason); err != nil {
		return Relationship{}, false, err
	}
	by, err := actorArg(endedBy)
	if err != nil {
		return Relationship{}, false, err
	}
	srcs, typs, tgts := g.reg.ownedBy(owner)
	rel, err = scan(q.QueryRow(ctx, `
		UPDATE platform.relationships SET `+endSet+`, end_reason = $2, ended_by = $3::uuid
		WHERE id = $1::uuid AND valid_until IS NULL AND `+fmt.Sprintf(ownedFilter, "$4", "$5", "$6")+` RETURNING `+cols,
		strings.ToLower(id), reason, by, srcs, typs, tgts))
	if err == nil {
		return rel, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Relationship{}, false, fmt.Errorf("unlink relationship: %w", err)
	}
	rel, err = g.Get(ctx, q, id)
	if err != nil {
		return Relationship{}, false, err
	}
	if o, ok := g.reg.Owner(rel.Source.Type, rel.Type, rel.Target.Type); !ok || o != owner {
		return Relationship{}, false, ErrNotAllowed
	}
	return rel, false, nil
}

// UnlinkTriple ends the current relationship of a triple; ended is false when
// there is none.
func (g *Graph) UnlinkTriple(ctx context.Context, q Querier, owner string, source Node, typ string, target Node, reason, endedBy string) (ended bool, err error) {
	source, err = checkNode(source)
	if err != nil {
		return false, err
	}
	target, err = checkNode(target)
	if err != nil {
		return false, err
	}
	if err := checkReason(reason); err != nil {
		return false, err
	}
	if o, ok := g.reg.Owner(source.Type, typ, target.Type); !ok || o != owner {
		return false, ErrNotAllowed
	}
	by, err := actorArg(endedBy)
	if err != nil {
		return false, err
	}
	var id string
	err = q.QueryRow(ctx, `
		UPDATE platform.relationships SET `+endSet+`, end_reason = $6, ended_by = $7::uuid
		WHERE source_type = $1 AND source_id = $2::uuid AND type = $3 AND target_type = $4 AND target_id = $5::uuid AND valid_until IS NULL
		RETURNING id::text`, source.Type, source.ID, typ, target.Type, target.ID, reason, by).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("unlink relationship: %w", err)
	}
	return true, nil
}

// UnlinkAll ends every current relationship of the owner's triples at a node
// in the given direction (Forward: node is the source) and returns how many
// ended.
func (g *Graph) UnlinkAll(ctx context.Context, q Querier, owner string, node Node, dir Direction, reason, endedBy string) (int, error) {
	return g.unlinkAll(ctx, q, owner, node, dir, nil, reason, endedBy)
}

// UnlinkAllExcept is UnlinkAll but keeps the relationship whose other end is keep.
func (g *Graph) UnlinkAllExcept(ctx context.Context, q Querier, owner string, node Node, dir Direction, keep Node, reason, endedBy string) (int, error) {
	k, err := checkNode(keep)
	if err != nil {
		return 0, err
	}
	return g.unlinkAll(ctx, q, owner, node, dir, &k, reason, endedBy)
}

func (g *Graph) unlinkAll(ctx context.Context, q Querier, owner string, node Node, dir Direction, keep *Node, reason, endedBy string) (int, error) {
	node, err := checkNode(node)
	if err != nil {
		return 0, err
	}
	if err := checkReason(reason); err != nil {
		return 0, err
	}
	by, err := actorArg(endedBy)
	if err != nil {
		return 0, err
	}
	near, far := "source", "target"
	if dir == Reverse {
		near, far = "target", "source"
	}
	srcs, typs, tgts := g.reg.ownedBy(owner)
	args := []any{node.Type, node.ID, reason, by, srcs, typs, tgts}
	keepCond := ""
	if keep != nil {
		args = append(args, keep.Type, keep.ID)
		keepCond = ` AND NOT (` + far + `_type = $8 AND ` + far + `_id = $9::uuid)`
	}
	rows, err := q.Query(ctx, `
		UPDATE platform.relationships SET `+endSet+`, end_reason = $3, ended_by = $4::uuid
		WHERE `+near+`_type = $1 AND `+near+`_id = $2::uuid AND valid_until IS NULL AND `+fmt.Sprintf(ownedFilter, "$5", "$6", "$7")+keepCond+`
		RETURNING id::text`, args...)
	if err != nil {
		return 0, fmt.Errorf("unlink relationships: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// Get returns one relationship, current or ended.
func (g *Graph) Get(ctx context.Context, q Querier, id string) (Relationship, error) {
	if !uuidPattern.MatchString(id) {
		return Relationship{}, ErrNotFound
	}
	r, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM platform.relationships WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Relationship{}, ErrNotFound
	}
	if err != nil {
		return Relationship{}, fmt.Errorf("get relationship: %w", err)
	}
	return r, nil
}

func (g *Graph) checkTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return g.reg.Types(), nil
	}
	known := map[string]bool{}
	for _, t := range g.reg.Types() {
		known[t] = true
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		if !relPattern.MatchString(t) || !known[t] {
			return nil, fmt.Errorf("%w: unknown relationship type", ErrInvalid)
		}
		out = append(out, t)
	}
	return out, nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

// Page is one keyset page of relationships, ordered by id. NextCursor is
// empty on the last page; pass it as afterID for the next one.
type Page struct {
	Items      []Relationship
	NextCursor string
}

// Outgoing lists the current relationships whose source is the node, oldest
// id first, after afterID (empty: from the start), at most limit (default 100,
// maximum 500). Empty types means every registered type.
func (g *Graph) Outgoing(ctx context.Context, q Querier, node Node, types []string, afterID string, limit int) (Page, error) {
	return g.adjacent(ctx, q, node, types, afterID, limit, "source")
}

// Incoming lists the current relationships whose target is the node.
func (g *Graph) Incoming(ctx context.Context, q Querier, node Node, types []string, afterID string, limit int) (Page, error) {
	return g.adjacent(ctx, q, node, types, afterID, limit, "target")
}

func (g *Graph) adjacent(ctx context.Context, q Querier, node Node, types []string, afterID string, limit int, side string) (Page, error) {
	node, err := checkNode(node)
	if err != nil {
		return Page{}, err
	}
	types, err = g.checkTypes(types)
	if err != nil {
		return Page{}, err
	}
	var after *string
	if afterID != "" {
		if !uuidPattern.MatchString(afterID) {
			return Page{}, fmt.Errorf("%w: cursor must be a UUID", ErrInvalid)
		}
		a := strings.ToLower(afterID)
		after = &a
	}
	limit = clampLimit(limit)
	rows, err := q.Query(ctx, `
		SELECT `+cols+` FROM platform.relationships
		WHERE `+side+`_type = $1 AND `+side+`_id = $2::uuid AND type = ANY($3::text[]) AND valid_until IS NULL
		  AND ($5::uuid IS NULL OR id > $5::uuid)
		ORDER BY id LIMIT $4`, node.Type, node.ID, types, limit+1, after)
	if err != nil {
		return Page{}, fmt.Errorf("list relationships: %w", err)
	}
	defer rows.Close()
	var out []Relationship
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return Page{}, fmt.Errorf("list relationships: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("list relationships: %w", err)
	}
	if len(out) > limit {
		out = out[:limit]
		return Page{Items: out, NextCursor: out[limit-1].ID}, nil
	}
	return Page{Items: out}, nil
}

// Edge is one relationship on a traversal path, in the direction it was
// walked: From is the node the walk came from, To the node it reached.
type Edge struct {
	RelationshipID string
	From           Node
	To             Node
	Type           string
	Confidence     string
}

// Reached is a node found by Traverse, with the shortest path to it.
type Reached struct {
	Node  Node
	Depth int
	// Path lists the edges from the start node to this node (Depth entries).
	Path []Edge
}

// TraverseInput bounds a traversal. MaxDepth and MaxNodes default to the
// ceilings and are clamped to them.
type TraverseInput struct {
	Start     Node
	Direction Direction
	MaxDepth  int
	MaxNodes  int
	// Types restricts the relationship types followed; empty means all registered types.
	Types []string
	// NodeTypes restricts the record types the walk reaches (and continues
	// from); empty means all. Relationships to other types are not read at all.
	NodeTypes []string
}

// TraverseResult lists the reached nodes (without the start) in breadth-first
// order. DepthLimited reports that nodes exist beyond MaxDepth, NodeLimited
// that MaxNodes (or the per-level edge scan) cut the result; Truncated is true
// when either happened.
type TraverseResult struct {
	Nodes        []Reached
	Truncated    bool
	DepthLimited bool
	NodeLimited  bool
}

// Traverse walks current relationships breadth-first from the start node.
// Every node appears once, at its shortest depth, so cycles and diamonds are
// safe. Depth is at most 6 and the result at most 500 nodes.
func (g *Graph) Traverse(ctx context.Context, q Querier, in TraverseInput) (TraverseResult, error) {
	if in.MaxDepth <= 0 || in.MaxDepth > MaxDepth {
		in.MaxDepth = MaxDepth
	}
	if in.MaxNodes <= 0 || in.MaxNodes > MaxNodes {
		in.MaxNodes = MaxNodes
	}
	res, _, err := g.walk(ctx, q, in, nil)
	return res, err
}

// Reaches reports whether target is reachable from start in the direction,
// searching deeper than Traverse and only through records of nodeTypes (empty:
// all). ErrTooLarge when the search limit was hit without finding it.
func (g *Graph) Reaches(ctx context.Context, q Querier, start, target Node, dir Direction, types, nodeTypes []string) (bool, error) {
	target, err := checkNode(target)
	if err != nil {
		return false, err
	}
	res, found, err := g.walk(ctx, q, TraverseInput{Start: start, Direction: dir, MaxDepth: reachDepth, MaxNodes: reachNodes, Types: types, NodeTypes: nodeTypes}, &target)
	if err != nil {
		return false, err
	}
	if found {
		return true, nil
	}
	if res.Truncated {
		return false, ErrTooLarge
	}
	return false, nil
}

func (g *Graph) walk(ctx context.Context, q Querier, in TraverseInput, find *Node) (TraverseResult, bool, error) {
	start, err := checkNode(in.Start)
	if err != nil {
		return TraverseResult{}, false, err
	}
	in.Start = start
	types, err := g.checkTypes(in.Types)
	if err != nil {
		return TraverseResult{}, false, err
	}
	for _, t := range in.NodeTypes {
		if !typePattern.MatchString(t) {
			return TraverseResult{}, false, fmt.Errorf("%w: unknown node type", ErrInvalid)
		}
	}
	nodeTypes := in.NodeTypes
	if nodeTypes == nil {
		nodeTypes = []string{}
	}
	near, far := "source", "target" // Forward: the frontier is the source side
	if in.Direction == Reverse {
		near, far = "target", "source"
	}
	visited := map[Node]bool{in.Start: true}
	paths := map[Node][]Edge{in.Start: nil}
	frontier := []Node{in.Start}
	var res TraverseResult
	for depth := 1; depth <= in.MaxDepth+1 && len(frontier) > 0; depth++ {
		fTypes, fIDs := make([]string, len(frontier)), make([]string, len(frontier))
		for i, n := range frontier {
			fTypes[i], fIDs[i] = n.Type, n.ID
		}
		rows, err := q.Query(ctx, `
			SELECT `+cols+` FROM platform.relationships
			WHERE (`+near+`_type, `+near+`_id) IN (SELECT * FROM unnest($1::text[], $2::uuid[]))
			  AND type = ANY($3::text[]) AND valid_until IS NULL
			  AND (cardinality($5::text[]) = 0 OR `+far+`_type = ANY($5::text[]))
			ORDER BY id LIMIT $4`, fTypes, fIDs, types, edgeScanLimit+1, nodeTypes)
		if err != nil {
			return TraverseResult{}, false, fmt.Errorf("traverse relationships: %w", err)
		}
		var edges []Relationship
		for rows.Next() {
			r, err := scan(rows)
			if err != nil {
				rows.Close()
				return TraverseResult{}, false, fmt.Errorf("traverse relationships: %w", err)
			}
			edges = append(edges, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return TraverseResult{}, false, fmt.Errorf("traverse relationships: %w", err)
		}
		if len(edges) > edgeScanLimit {
			edges = edges[:edgeScanLimit]
			res.NodeLimited = true
		}
		var next []Node
		for _, e := range edges {
			from, to := e.Source, e.Target
			if in.Direction == Reverse {
				from, to = e.Target, e.Source
			}
			if visited[to] {
				continue
			}
			if depth > in.MaxDepth {
				// Something exists beyond the depth limit.
				res.DepthLimited = true
				break
			}
			if len(res.Nodes) >= in.MaxNodes {
				res.NodeLimited = true
				break
			}
			visited[to] = true
			path := append(append([]Edge(nil), paths[from]...), Edge{RelationshipID: e.ID, From: from, To: to, Type: e.Type, Confidence: e.Confidence})
			paths[to] = path
			res.Nodes = append(res.Nodes, Reached{Node: to, Depth: depth, Path: path})
			next = append(next, to)
			if find != nil && to == *find {
				res.Truncated = res.DepthLimited || res.NodeLimited
				return res, true, nil
			}
		}
		frontier = next
	}
	res.Truncated = res.DepthLimited || res.NodeLimited
	return res, false, nil
}
