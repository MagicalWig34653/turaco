package relationships_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

func newGraph() *relationships.Graph {
	reg := relationships.NewRegistry()
	reg.Register(
		relationships.Triple{SourceType: "tnode", Type: "TEST_DEPENDS_ON", TargetType: "tnode", Owner: "tests"},
		relationships.Triple{SourceType: "tnode", Type: "TEST_RUNS_ON", TargetType: "tother", Owner: "tests"},
	)
	return relationships.New(reg)
}

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	g    *relationships.Graph
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, pool: dbtest.Pool(t), g: newGraph()}
	clean := func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM platform.relationships WHERE type LIKE 'TEST\_%'`)
	}
	clean()
	t.Cleanup(clean)
	return e
}

func (e *env) node(typ string) relationships.Node {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return relationships.Node{Type: typ, ID: id}
}

func (e *env) link(from, to relationships.Node) relationships.Relationship {
	e.t.Helper()
	r, _, err := e.g.Link(context.Background(), e.pool, relationships.LinkInput{Owner: "tests", Source: from, Type: "TEST_DEPENDS_ON", Target: to, Confidence: relationships.ConfidenceDeclared})
	if err != nil {
		e.t.Fatalf("link: %v", err)
	}
	return r
}

func TestRegistry(t *testing.T) {
	reg := relationships.NewRegistry()
	reg.Register(relationships.Triple{SourceType: "a", Type: "X_Y", TargetType: "b", Owner: "tests"})
	if !reg.Allowed("a", "X_Y", "b") || reg.Allowed("b", "X_Y", "a") || reg.Allowed("a", "OTHER", "b") {
		t.Fatal("registry allows exactly the registered triple")
	}
	if got := reg.Types(); len(got) != 1 || got[0] != "X_Y" {
		t.Fatalf("types: %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a malformed triple must panic")
		}
	}()
	reg.Register(relationships.Triple{SourceType: "A", Type: "x", TargetType: "b", Owner: "tests"})
}

func TestLinkValidationAndUnknownTriple(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b, other := e.node("tnode"), e.node("tnode"), e.node("tother")
	cases := []struct {
		name string
		in   relationships.LinkInput
		want error
	}{
		{"unknown triple", relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_RUNS_ON", Target: b, Confidence: "declared"}, relationships.ErrNotAllowed},
		{"unregistered type", relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_NOPE", Target: b, Confidence: "declared"}, relationships.ErrNotAllowed},
		{"bad id", relationships.LinkInput{Owner: "tests", Source: relationships.Node{Type: "tnode", ID: "x'; DROP"}, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"}, relationships.ErrInvalid},
		{"bad confidence", relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "guess"}, relationships.ErrInvalid},
		{"self link", relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: a, Confidence: "declared"}, relationships.ErrInvalid},
		{"bad creator", relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared", CreatedBy: "nope"}, relationships.ErrInvalid},
	}
	for _, c := range cases {
		if _, _, err := e.g.Link(ctx, e.pool, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	if _, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_RUNS_ON", Target: other, Confidence: "derived", RecordedBy: "test"}); err != nil || !created {
		t.Fatalf("allowed triple: created=%v err=%v", created, err)
	}
}

func TestLinkIsIdempotentAndUnlinkKeepsHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.node("tnode"), e.node("tnode")
	first, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
	if err != nil || !created {
		t.Fatalf("first link: %v %v", created, err)
	}
	second, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "observed"})
	if err != nil || created || second.ID != first.ID || second.Confidence != "declared" {
		t.Fatalf("duplicate must return the current row unchanged: %+v created=%v err=%v", second, created, err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "tests", first.ID, "Bad Reason", ""); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("reason code: %v", err)
	}
	ended, did, err := e.g.Unlink(ctx, e.pool, "tests", first.ID, "no_longer_needed", "")
	if err != nil || !did || ended.Current() || ended.EndReason == nil || *ended.EndReason != "no_longer_needed" {
		t.Fatalf("unlink: %+v %v %v", ended, did, err)
	}
	if again, did, err := e.g.Unlink(ctx, e.pool, "tests", first.ID, "other", ""); err != nil || did || *again.EndReason != "no_longer_needed" {
		t.Fatalf("second unlink must be a no-op: %+v %v %v", again, did, err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "tests", "00000000-0000-7000-8000-000000000001", "other", ""); !errors.Is(err, relationships.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "tests", "not-a-uuid", "other", ""); !errors.Is(err, relationships.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}
	// After ending, the same triple can be linked again as a new row.
	third, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("relink: %+v %v %v", third, created, err)
	}
	if ok, err := e.g.UnlinkTriple(ctx, e.pool, "tests", a, "TEST_DEPENDS_ON", b, "replaced", ""); err != nil || !ok {
		t.Fatalf("unlink triple: %v %v", ok, err)
	}
	if ok, err := e.g.UnlinkTriple(ctx, e.pool, "tests", a, "TEST_DEPENDS_ON", b, "replaced", ""); err != nil || ok {
		t.Fatalf("second unlink triple: %v %v", ok, err)
	}
}

func TestConcurrentDuplicateLinksEndWithOneRow(t *testing.T) {
	e := newEnv(t)
	a, b := e.node("tnode"), e.node("tnode")
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := e.g.Link(context.Background(), e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
			if err != nil {
				t.Errorf("link: %v", err)
				return
			}
			if c {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.relationships WHERE source_id = $1::uuid AND valid_until IS NULL`, a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || created.Load() != 1 {
		t.Fatalf("rows=%d created=%d, want 1 and 1", n, created.Load())
	}
}

func TestOutgoingIncomingLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hub := e.node("tnode")
	for i := 0; i < 5; i++ {
		e.link(hub, e.node("tnode"))
	}
	leaf := e.node("tnode")
	for i := 0; i < 3; i++ {
		e.link(e.node("tnode"), leaf)
	}
	page, err := e.g.Outgoing(ctx, e.pool, hub, nil, "", 3)
	if err != nil || len(page.Items) != 3 || page.NextCursor == "" {
		t.Fatalf("outgoing: %+v %v", page, err)
	}
	// Keyset paging: the cursor continues after the last id, with no overlap.
	rest, err := e.g.Outgoing(ctx, e.pool, hub, nil, page.NextCursor, 3)
	if err != nil || len(rest.Items) != 2 || rest.NextCursor != "" || rest.Items[0].ID <= page.Items[2].ID {
		t.Fatalf("outgoing page 2: %+v %v", rest, err)
	}
	if _, err := e.g.Outgoing(ctx, e.pool, hub, nil, "nope", 3); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
	all, err := e.g.Outgoing(ctx, e.pool, hub, []string{"TEST_DEPENDS_ON"}, "", 10)
	if err != nil || len(all.Items) != 5 || all.NextCursor != "" {
		t.Fatalf("outgoing all: %+v %v", all, err)
	}
	in, err := e.g.Incoming(ctx, e.pool, leaf, nil, "", 10)
	if err != nil || len(in.Items) != 3 {
		t.Fatalf("incoming: %d %v", len(in.Items), err)
	}
	if _, err := e.g.Outgoing(ctx, e.pool, hub, []string{"UNKNOWN"}, "", 10); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
	// Ended relationships are not current.
	if _, _, err := e.g.Unlink(ctx, e.pool, "tests", all.Items[0].ID, "other", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := e.g.Outgoing(ctx, e.pool, hub, nil, "", 10)
	if len(after.Items) != 4 {
		t.Fatalf("ended link must disappear: %d", len(after.Items))
	}
}

func chain(e *env, n int) []relationships.Node {
	nodes := make([]relationships.Node, n)
	for i := range nodes {
		nodes[i] = e.node("tnode")
		if i > 0 {
			e.link(nodes[i-1], nodes[i])
		}
	}
	return nodes
}

func TestTraverseDepthAndPath(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nodes := chain(e, 10) // n0 -> n1 -> ... -> n9
	res, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: nodes[0], MaxDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 3 || !res.DepthLimited || !res.Truncated || res.NodeLimited {
		t.Fatalf("depth 3: %d nodes %+v", len(res.Nodes), res)
	}
	last := res.Nodes[2]
	if last.Node != nodes[3] || last.Depth != 3 || len(last.Path) != 3 || last.Path[0].From != nodes[0] || last.Path[2].To != nodes[3] {
		t.Fatalf("path: %+v", last)
	}
	// The ceiling is 6 even when more is asked.
	res, _ = e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: nodes[0], MaxDepth: 50})
	if len(res.Nodes) != relationships.MaxDepth || !res.DepthLimited {
		t.Fatalf("depth ceiling: %d %+v", len(res.Nodes), res.DepthLimited)
	}
	// A chain that ends exactly at the depth limit is not truncated.
	res, _ = e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: nodes[4], MaxDepth: 5})
	if len(res.Nodes) != 5 || res.Truncated {
		t.Fatalf("complete chain flagged: %d %+v", len(res.Nodes), res)
	}
	// Reverse walks dependents.
	res, _ = e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: nodes[9], Direction: relationships.Reverse, MaxDepth: 2})
	if len(res.Nodes) != 2 || res.Nodes[0].Node != nodes[8] || res.Nodes[1].Path[1].To != nodes[7] {
		t.Fatalf("reverse: %+v", res.Nodes)
	}
}

func TestTraverseCyclesAndDiamonds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// a -> b -> c -> a (cycle) and a -> c (shortcut).
	a, b, c := e.node("tnode"), e.node("tnode"), e.node("tnode")
	e.link(a, b)
	e.link(b, c)
	e.link(c, a)
	e.link(a, c)
	res, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: a})
	if err != nil || len(res.Nodes) != 2 || res.Truncated {
		t.Fatalf("cycle: %+v %v", res, err)
	}
	for _, n := range res.Nodes {
		if n.Node == c && n.Depth != 1 {
			t.Fatalf("shortest depth wanted, got %d", n.Depth)
		}
		if n.Node == a {
			t.Fatal("the start node must not be reported")
		}
	}
}

func TestTraverseNodeCapAndTypes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hub := e.node("tnode")
	for i := 0; i < 8; i++ {
		e.link(hub, e.node("tnode"))
	}
	host := e.node("tother")
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: hub, Type: "TEST_RUNS_ON", Target: host, Confidence: "derived"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: hub, MaxNodes: 5})
	if err != nil || len(res.Nodes) != 5 || !res.NodeLimited || !res.Truncated || res.DepthLimited {
		t.Fatalf("cap: %d %+v %v", len(res.Nodes), res, err)
	}
	res, _ = e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: hub, Types: []string{"TEST_RUNS_ON"}})
	if len(res.Nodes) != 1 || res.Nodes[0].Node != host || res.Nodes[0].Path[0].Confidence != "derived" {
		t.Fatalf("type filter: %+v", res.Nodes)
	}
	if _, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: relationships.Node{Type: "tnode", ID: "bad"}}); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("bad start: %v", err)
	}
	if _, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: hub, Types: []string{"NOPE"}}); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("bad types: %v", err)
	}
	// The hard ceiling is 500 nodes.
	big := e.node("tnode")
	for i := 0; i < relationships.MaxNodes+20; i++ {
		e.link(big, e.node("tnode"))
	}
	res, _ = e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: big, MaxNodes: 100000})
	if len(res.Nodes) != relationships.MaxNodes || !res.NodeLimited {
		t.Fatalf("node ceiling: %d %+v", len(res.Nodes), res.NodeLimited)
	}
}

func TestReachesFindsDeepCyclesAndEndedLinksDoNotCount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nodes := chain(e, 12) // deeper than the Traverse ceiling
	ok, err := e.g.Reaches(ctx, e.pool, nodes[0], nodes[11], relationships.Forward, nil, nil)
	if err != nil || !ok {
		t.Fatalf("deep reach: %v %v", ok, err)
	}
	ok, err = e.g.Reaches(ctx, e.pool, nodes[11], nodes[0], relationships.Forward, nil, nil)
	if err != nil || ok {
		t.Fatalf("no reverse reach: %v %v", ok, err)
	}
	rel, _ := e.g.Outgoing(ctx, e.pool, nodes[5], nil, "", 5)
	if _, _, err := e.g.Unlink(ctx, e.pool, "tests", rel.Items[0].ID, "other", ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.g.Reaches(ctx, e.pool, nodes[0], nodes[11], relationships.Forward, nil, nil); ok {
		t.Fatal("an ended link must not connect")
	}
}

func TestUnlinkAll(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.node("tnode")
	for i := 0; i < 3; i++ {
		e.link(a, e.node("tnode"))
	}
	e.link(e.node("tnode"), a)
	n, err := e.g.UnlinkAll(ctx, e.pool, "tests", a, relationships.Forward, "service_retired", "")
	if err != nil || n != 3 {
		t.Fatalf("unlink all: %d %v", n, err)
	}
	in, _ := e.g.Incoming(ctx, e.pool, a, nil, "", 10)
	if len(in.Items) != 1 {
		t.Fatal("incoming links stay")
	}
}

func TestOwnershipIsEnforced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.node("tnode"), e.node("tnode")
	rel := e.link(a, b)
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "intruder", Source: a, Type: "TEST_DEPENDS_ON", Target: e.node("tnode"), Confidence: "declared"}); !errors.Is(err, relationships.ErrNotAllowed) {
		t.Fatalf("foreign link: %v", err)
	}
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: e.node("tnode"), Confidence: "declared"}); !errors.Is(err, relationships.ErrNotAllowed) {
		t.Fatalf("link without owner: %v", err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "intruder", rel.ID, "other", ""); !errors.Is(err, relationships.ErrNotAllowed) {
		t.Fatalf("foreign unlink: %v", err)
	}
	if ok, err := e.g.UnlinkTriple(ctx, e.pool, "intruder", a, "TEST_DEPENDS_ON", b, "other", ""); !errors.Is(err, relationships.ErrNotAllowed) || ok {
		t.Fatalf("foreign unlink triple: %v %v", ok, err)
	}
	if n, err := e.g.UnlinkAll(ctx, e.pool, "intruder", a, relationships.Forward, "other", ""); err != nil || n != 0 {
		t.Fatalf("foreign unlink all must end nothing: %d %v", n, err)
	}
	if cur, _ := e.g.Get(ctx, e.pool, rel.ID); !cur.Current() {
		t.Fatal("a foreign module must not end the relationship")
	}
	reg := relationships.NewRegistry()
	reg.Register(relationships.Triple{SourceType: "a", Type: "X_Y", TargetType: "b", Owner: "one"})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("a second owner for the same shape must panic")
			}
		}()
		reg.Register(relationships.Triple{SourceType: "a", Type: "X_Y", TargetType: "b", Owner: "two"})
	}()
}

func TestIDsAreNormalizedToLowerCase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.node("tnode"), e.node("tnode")
	up := func(n relationships.Node) relationships.Node { n.ID = strings.ToUpper(n.ID); return n }
	first := e.link(a, b)
	again, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: up(a), Type: "TEST_DEPENDS_ON", Target: up(b), Confidence: "declared"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("upper-case link must find the same row: %v %v", created, err)
	}
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: up(a), Type: "TEST_DEPENDS_ON", Target: up(a), Confidence: "declared"}); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("self link in mixed case: %v", err)
	}
	if ok, err := e.g.Reaches(ctx, e.pool, up(a), up(b), relationships.Forward, nil, nil); err != nil || !ok {
		t.Fatalf("reaches with upper-case ids: %v %v", ok, err)
	}
	res, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: up(a)})
	if err != nil || len(res.Nodes) != 1 || res.Nodes[0].Node != b {
		t.Fatalf("traverse with upper-case start: %+v %v", res, err)
	}
	if _, did, err := e.g.Unlink(ctx, e.pool, "tests", strings.ToUpper(first.ID), "other", strings.ToUpper(e.node("x").ID)); err != nil || !did {
		t.Fatalf("unlink with upper-case ids: %v %v", did, err)
	}
}

func TestReachesNodeTypeFilterAndHardCap(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// a -> b (tnode) -> c (tnode); a -> o (tother) is never walked when filtering to tnode.
	a, b, c, o := e.node("tnode"), e.node("tnode"), e.node("tnode"), e.node("tother")
	e.link(a, b)
	e.link(b, c)
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Owner: "tests", Source: a, Type: "TEST_RUNS_ON", Target: o, Confidence: "derived"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.g.Reaches(ctx, e.pool, a, c, relationships.Forward, nil, []string{"tnode"}); err != nil || !ok {
		t.Fatalf("filtered reach: %v %v", ok, err)
	}
	if ok, err := e.g.Reaches(ctx, e.pool, a, o, relationships.Forward, nil, []string{"tnode"}); err != nil || ok {
		t.Fatalf("a filtered-out type must not be reached: %v %v", ok, err)
	}
	res, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: a, NodeTypes: []string{"tnode"}})
	if err != nil || len(res.Nodes) != 2 {
		t.Fatalf("filtered traverse: %+v %v", res, err)
	}
	if _, err := e.g.Traverse(ctx, e.pool, relationships.TraverseInput{Start: a, NodeTypes: []string{"Bad Type"}}); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("bad node type: %v", err)
	}
}

// A relationship committed by another transaction after this one began has a
// valid_from later than this transaction's now(); ending it after the lock wait
// must not violate valid_until >= valid_from.
func TestUnlinkAfterLockWaitKeepsTimestampsOrdered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.node("tnode"), e.node("tnode")
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var started time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&started); err != nil { // fixes this transaction's now()
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	rel := e.link(a, b) // committed by another connection, after tx began
	if !rel.ValidFrom.After(started) {
		t.Fatalf("valid_from %v must be taken at insert time, after %v", rel.ValidFrom, started)
	}
	ended, did, err := e.g.Unlink(ctx, tx, "tests", rel.ID, "other", "")
	if err != nil || !did {
		t.Fatalf("unlink: %v %v", did, err)
	}
	if ended.ValidUntil == nil || ended.ValidUntil.Before(ended.ValidFrom) {
		t.Fatalf("valid_until %v before valid_from %v", ended.ValidUntil, ended.ValidFrom)
	}
	// The triple and bulk variants share the rule.
	rel2 := e.link(a, e.node("tnode"))
	if ok, err := e.g.UnlinkTriple(ctx, tx, "tests", rel2.Source, "TEST_DEPENDS_ON", rel2.Target, "other", ""); err != nil || !ok {
		t.Fatalf("unlink triple: %v %v", ok, err)
	}
	rel3 := e.link(a, e.node("tnode"))
	if n, err := e.g.UnlinkAll(ctx, tx, "tests", rel3.Source, relationships.Forward, "other", ""); err != nil || n != 1 {
		t.Fatalf("unlink all: %d %v", n, err)
	}
}

func TestEndedByRequiresEnd(t *testing.T) {
	e := newEnv(t)
	rel := e.link(e.node("tnode"), e.node("tnode"))
	_, err := e.pool.Exec(context.Background(), `UPDATE platform.relationships SET ended_by = uuidv7() WHERE id = $1::uuid`, rel.ID)
	if err == nil {
		t.Fatal("ended_by on a current relationship must violate the check")
	}
}

func TestUnlinkAllExceptKeepsOneTarget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, keep := e.node("tnode"), e.node("tnode")
	e.link(a, keep)
	e.link(a, e.node("tnode"))
	e.link(a, e.node("tnode"))
	n, err := e.g.UnlinkAllExcept(ctx, e.pool, "tests", a, relationships.Forward, keep, "other", "")
	if err != nil || n != 2 {
		t.Fatalf("unlink except: %d %v", n, err)
	}
	left, _ := e.g.Outgoing(ctx, e.pool, a, nil, "", 10)
	if len(left.Items) != 1 || left.Items[0].Target != keep {
		t.Fatalf("kept: %+v", left.Items)
	}
}
