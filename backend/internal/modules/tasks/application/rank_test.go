package application

import (
	"math/rand"
	"sort"
	"testing"
)

func TestRankBetweenOrdersAndStaysValid(t *testing.T) {
	cases := []struct{ a, b string }{
		{"", ""}, {"", "1"}, {"V", "W"}, {"1V", "2"}, {"1V", "1k"}, {"1", "1V"}, {"1", "10V"}, {"", "05"}, {"z", ""}, {"zzz", ""}, {"A", "B"},
	}
	for _, c := range cases {
		r, err := rankBetween(c.a, c.b)
		if err != nil {
			t.Fatalf("rankBetween(%q, %q): %v", c.a, c.b, err)
		}
		if !validRank(r) || r <= c.a || (c.b != "" && r >= c.b) {
			t.Errorf("rankBetween(%q, %q) = %q is not strictly between", c.a, c.b, r)
		}
	}
}

func TestRankBetweenRejectsBadBounds(t *testing.T) {
	for _, c := range []struct{ a, b string }{{"b", "a"}, {"a", "a"}, {"10", "2"}, {"a0", ""}, {"", "x0"}, {"a-", ""}} {
		if _, err := rankBetween(c.a, c.b); err == nil {
			t.Errorf("rankBetween(%q, %q) accepted bad bounds", c.a, c.b)
		}
	}
}

// Random insertions at random positions keep a strict total order, every rank is valid, and ranks stay short.
func TestRankBetweenRandomInsertions(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var ranks []string
	longest := 0
	for i := 0; i < 3000; i++ {
		pos := rng.Intn(len(ranks) + 1)
		if i%3 == 0 {
			pos = len(ranks) // append-heavy, like adding cards at the bottom
		}
		lo, hi := "", ""
		if pos > 0 {
			lo = ranks[pos-1]
		}
		if pos < len(ranks) {
			hi = ranks[pos]
		}
		r, err := rankBetween(lo, hi)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		ranks = append(ranks, "")
		copy(ranks[pos+1:], ranks[pos:])
		ranks[pos] = r
		longest = max(longest, len(r))
		if len(r) > maxRankLength { // the store rebalances here
			for k := range ranks {
				ranks[k] = spreadRank(k, len(ranks))
			}
		}
	}
	if !sort.StringsAreSorted(ranks) {
		t.Fatal("ranks are not in order")
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i-1] == ranks[i] {
			t.Fatalf("duplicate rank %q", ranks[i])
		}
	}
	t.Logf("3000 random insertions, longest rank %d", longest)
}

// Repeated insertion at the same spot grows ranks linearly; the board rebalances before the database limit.
func TestRankGrowthIsBoundedByTheRebalanceThreshold(t *testing.T) {
	lo, hi := "", "1"
	steps := 0
	for {
		r, err := rankBetween(lo, hi)
		if err != nil {
			t.Fatal(err)
		}
		if len(r) > maxRankLength {
			break
		}
		hi = r // always insert before the previous one
		steps++
		if steps > 10000 {
			t.Fatal("ranks never grew")
		}
	}
	if steps < 30 {
		t.Errorf("rebalance would trigger after only %d same-spot insertions", steps)
	}
}

func TestSpreadRankIsOrderedAndValid(t *testing.T) {
	for _, n := range []int{1, 2, 61, 62, 63, 500, MaxRanksPerBoard} {
		prev := ""
		for i := 0; i < n; i++ {
			r := spreadRank(i, n)
			if !validRank(r) || r <= prev {
				t.Fatalf("n=%d i=%d rank %q after %q", n, i, r, prev)
			}
			prev = r
		}
	}
}

func TestMoveOperationMapsStatusPairs(t *testing.T) {
	cases := []struct {
		from, to string
		op       Operation
		ok       bool
	}{
		{StatusOpen, StatusInProgress, OpStart, true},
		{StatusBlocked, StatusInProgress, OpStart, true},
		{StatusInProgress, StatusCompleted, OpComplete, true},
		{StatusOpen, StatusCompleted, OpComplete, true},
		{StatusOpen, StatusBlocked, OpBlock, true},
		{StatusInProgress, StatusBlocked, OpBlock, true},
		{StatusBlocked, StatusOpen, OpUnblock, true},
		{StatusCompleted, StatusOpen, OpReopen, true},
		{StatusCancelled, StatusOpen, OpReopen, true},
		{StatusOpen, StatusCancelled, OpCancel, true},
		{StatusBlocked, StatusCancelled, OpCancel, true},
		// Forbidden by the state machine: no operation exists.
		{StatusCompleted, StatusInProgress, "", false},
		{StatusCancelled, StatusInProgress, "", false},
		{StatusInProgress, StatusOpen, "", false},
		{StatusBlocked, StatusCompleted, "", false},
		{StatusCompleted, StatusBlocked, "", false},
		{StatusCompleted, StatusCancelled, "", false},
		{StatusCancelled, StatusCompleted, "", false},
	}
	for _, c := range cases {
		op, ok := moveOperation(c.from, c.to)
		if ok != c.ok || op != c.op {
			t.Errorf("%s -> %s = %q, %v; want %q, %v", c.from, c.to, op, ok, c.op, c.ok)
		}
	}
}

func TestRanksAfterAreAscendingValidAndAfterLo(t *testing.T) {
	for _, lo := range []string{"", "V", "zz", "1k"} {
		for _, n := range []int{1, 2, 61, 62, 200, 4000} {
			run := ranksAfter(lo, n)
			if len(run) != n {
				t.Fatalf("lo=%q n=%d: got %d ranks", lo, n, len(run))
			}
			prev := lo
			for _, r := range run {
				if !validRank(r) || r <= prev {
					t.Fatalf("lo=%q n=%d: rank %q after %q", lo, n, r, prev)
				}
				prev = r
			}
			if len(run[n-1]) > len(lo)+3 {
				t.Errorf("lo=%q n=%d: suffix too long: %q", lo, n, run[n-1])
			}
		}
	}
}
