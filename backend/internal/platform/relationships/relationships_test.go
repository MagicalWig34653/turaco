package relationships_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

func newGraph() *relationships.Graph {
	reg := relationships.NewRegistry()
	reg.Register(
		relationships.Triple{SourceType: "tnode", Type: "TEST_DEPENDS_ON", TargetType: "tnode"},
		relationships.Triple{SourceType: "tnode", Type: "TEST_RUNS_ON", TargetType: "tother"},
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
	r, _, err := e.g.Link(context.Background(), e.pool, relationships.LinkInput{Source: from, Type: "TEST_DEPENDS_ON", Target: to, Confidence: relationships.ConfidenceDeclared})
	if err != nil {
		e.t.Fatalf("link: %v", err)
	}
	return r
}

func TestRegistry(t *testing.T) {
	reg := relationships.NewRegistry()
	reg.Register(relationships.Triple{SourceType: "a", Type: "X_Y", TargetType: "b"})
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
	reg.Register(relationships.Triple{SourceType: "A", Type: "x", TargetType: "b"})
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
		{"unknown triple", relationships.LinkInput{Source: a, Type: "TEST_RUNS_ON", Target: b, Confidence: "declared"}, relationships.ErrNotAllowed},
		{"unregistered type", relationships.LinkInput{Source: a, Type: "TEST_NOPE", Target: b, Confidence: "declared"}, relationships.ErrNotAllowed},
		{"bad id", relationships.LinkInput{Source: relationships.Node{Type: "tnode", ID: "x'; DROP"}, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"}, relationships.ErrInvalid},
		{"bad confidence", relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "guess"}, relationships.ErrInvalid},
		{"self link", relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: a, Confidence: "declared"}, relationships.ErrInvalid},
		{"bad creator", relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared", CreatedBy: "nope"}, relationships.ErrInvalid},
	}
	for _, c := range cases {
		if _, _, err := e.g.Link(ctx, e.pool, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	if _, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: a, Type: "TEST_RUNS_ON", Target: other, Confidence: "derived", RecordedBy: "test"}); err != nil || !created {
		t.Fatalf("allowed triple: created=%v err=%v", created, err)
	}
}

func TestLinkIsIdempotentAndUnlinkKeepsHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.node("tnode"), e.node("tnode")
	first, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
	if err != nil || !created {
		t.Fatalf("first link: %v %v", created, err)
	}
	second, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "observed"})
	if err != nil || created || second.ID != first.ID || second.Confidence != "declared" {
		t.Fatalf("duplicate must return the current row unchanged: %+v created=%v err=%v", second, created, err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, first.ID, "Bad Reason", ""); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("reason code: %v", err)
	}
	ended, did, err := e.g.Unlink(ctx, e.pool, first.ID, "no_longer_needed", "")
	if err != nil || !did || ended.Current() || ended.EndReason == nil || *ended.EndReason != "no_longer_needed" {
		t.Fatalf("unlink: %+v %v %v", ended, did, err)
	}
	if again, did, err := e.g.Unlink(ctx, e.pool, first.ID, "other", ""); err != nil || did || *again.EndReason != "no_longer_needed" {
		t.Fatalf("second unlink must be a no-op: %+v %v %v", again, did, err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "00000000-0000-7000-8000-000000000001", "other", ""); !errors.Is(err, relationships.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, _, err := e.g.Unlink(ctx, e.pool, "not-a-uuid", "other", ""); !errors.Is(err, relationships.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}
	// After ending, the same triple can be linked again as a new row.
	third, created, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("relink: %+v %v %v", third, created, err)
	}
	if ok, err := e.g.UnlinkTriple(ctx, e.pool, a, "TEST_DEPENDS_ON", b, "replaced", ""); err != nil || !ok {
		t.Fatalf("unlink triple: %v %v", ok, err)
	}
	if ok, err := e.g.UnlinkTriple(ctx, e.pool, a, "TEST_DEPENDS_ON", b, "replaced", ""); err != nil || ok {
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
			_, c, err := e.g.Link(context.Background(), e.pool, relationships.LinkInput{Source: a, Type: "TEST_DEPENDS_ON", Target: b, Confidence: "declared"})
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
	out, trunc, err := e.g.Outgoing(ctx, e.pool, hub, nil, 3)
	if err != nil || len(out) != 3 || !trunc {
		t.Fatalf("outgoing: %d %v %v", len(out), trunc, err)
	}
	out, trunc, err = e.g.Outgoing(ctx, e.pool, hub, []string{"TEST_DEPENDS_ON"}, 10)
	if err != nil || len(out) != 5 || trunc {
		t.Fatalf("outgoing all: %d %v %v", len(out), trunc, err)
	}
	in, _, err := e.g.Incoming(ctx, e.pool, leaf, nil, 10)
	if err != nil || len(in) != 3 {
		t.Fatalf("incoming: %d %v", len(in), err)
	}
	if _, _, err := e.g.Outgoing(ctx, e.pool, hub, []string{"UNKNOWN"}, 10); !errors.Is(err, relationships.ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
	// Ended relationships are not current.
	if _, _, err := e.g.Unlink(ctx, e.pool, out[0].ID, "other", ""); err != nil {
		t.Fatal(err)
	}
	out, _, _ = e.g.Outgoing(ctx, e.pool, hub, nil, 10)
	if len(out) != 4 {
		t.Fatalf("ended link must disappear: %d", len(out))
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
	if _, _, err := e.g.Link(ctx, e.pool, relationships.LinkInput{Source: hub, Type: "TEST_RUNS_ON", Target: host, Confidence: "derived"}); err != nil {
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
	ok, err := e.g.Reaches(ctx, e.pool, nodes[0], nodes[11], relationships.Forward, nil)
	if err != nil || !ok {
		t.Fatalf("deep reach: %v %v", ok, err)
	}
	ok, err = e.g.Reaches(ctx, e.pool, nodes[11], nodes[0], relationships.Forward, nil)
	if err != nil || ok {
		t.Fatalf("no reverse reach: %v %v", ok, err)
	}
	rel, _, _ := e.g.Outgoing(ctx, e.pool, nodes[5], nil, 5)
	if _, _, err := e.g.Unlink(ctx, e.pool, rel[0].ID, "other", ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.g.Reaches(ctx, e.pool, nodes[0], nodes[11], relationships.Forward, nil); ok {
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
	n, err := e.g.UnlinkAll(ctx, e.pool, a, relationships.Forward, "service_retired", "")
	if err != nil || n != 3 {
		t.Fatalf("unlink all: %d %v", n, err)
	}
	in, _, _ := e.g.Incoming(ctx, e.pool, a, nil, 10)
	if len(in) != 1 {
		t.Fatal("incoming links stay")
	}
}
