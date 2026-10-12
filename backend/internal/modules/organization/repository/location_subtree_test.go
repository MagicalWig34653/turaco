package repository

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// area inserts an area below parent. Cleanups run in reverse order, so children go before their parents.
func (f *fixture) area(name, parent string) string {
	return f.insert(`INSERT INTO organization.locations(name, kind, parent_location_id) VALUES ($1, 'area', $2::uuid) RETURNING id::text`,
		`DELETE FROM organization.locations WHERE id = $1`, name, parent)
}

func sortedIDs(ids ...string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

func TestLocationSubtreeIDs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	siteA := f.location(f.pfx + " Site A")
	a1 := f.area(f.pfx+" A1", siteA)
	a1a := f.area(f.pfx+" A1a", a1)
	a1a1 := f.area(f.pfx+" A1a1", a1a) // fourth level
	siteB := f.location(f.pfx + " Site B")
	b1 := f.area(f.pfx+" B1", siteB)

	got, err := f.repo.LocationSubtreeIDs(ctx, []string{siteA})
	if err != nil {
		t.Fatal(err)
	}
	if want := sortedIDs(siteA, a1, a1a, a1a1); !reflect.DeepEqual(got, want) {
		t.Fatalf("subtree(A) = %v, want %v", got, want)
	}
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{a1a}); !reflect.DeepEqual(got, sortedIDs(a1a, a1a1)) {
		t.Fatalf("subtree(A1a) = %v", got)
	}
	// Overlapping roots are de-duplicated; unknown ids contribute nothing.
	unknown := "01960000-0000-7000-8000-00000000dead"
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{a1, siteA, b1, unknown}); !reflect.DeepEqual(got, sortedIDs(siteA, a1, a1a, a1a1, b1)) {
		t.Fatalf("subtree(A1, A, B1, unknown) = %v", got)
	}
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{unknown}); len(got) != 0 {
		t.Fatalf("subtree(unknown) = %v", got)
	}
	// A deactivated Location stays in the tree and in the expanded set (F16 L2, open question 14 default).
	if _, err := f.pool.Exec(ctx, `UPDATE organization.locations SET active = false WHERE id = $1`, a1a); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{siteA}); !reflect.DeepEqual(got, sortedIDs(siteA, a1, a1a, a1a1)) {
		t.Fatalf("subtree(A) with inactive A1a = %v", got)
	}
	// A move changes the next answer.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.locations SET parent_location_id = $2 WHERE id = $1`, b1, a1); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{siteB}); !reflect.DeepEqual(got, []string{siteB}) {
		t.Fatalf("subtree(B) after moving B1 away = %v", got)
	}
	if got, _ := f.repo.LocationSubtreeIDs(ctx, []string{siteA}); !reflect.DeepEqual(got, sortedIDs(siteA, a1, a1a, a1a1, b1)) {
		t.Fatalf("subtree(A) after moving B1 below A1 = %v", got)
	}
}

// Corrupt data with a cycle (impossible through the tree trigger) must not hang the evaluator: the walk is depth
// bounded and returns.
func TestLocationSubtreeIDsTerminatesOnCycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	site := f.location(f.pfx + " Cycle site")
	x := f.area(f.pfx+" X", site)
	y := f.area(f.pfx+" Y", x)
	if _, err := f.pool.Exec(ctx, `ALTER TABLE organization.locations DISABLE TRIGGER locations_guard_tree`); err != nil {
		t.Fatal(err)
	}
	restore := func() {
		_, _ = f.pool.Exec(ctx, `UPDATE organization.locations SET parent_location_id = $2 WHERE id = $1`, x, site)
		_, _ = f.pool.Exec(ctx, `ALTER TABLE organization.locations ENABLE TRIGGER locations_guard_tree`)
	}
	t.Cleanup(restore)
	if _, err := f.pool.Exec(ctx, `UPDATE organization.locations SET parent_location_id = $2 WHERE id = $1`, x, y); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	got, err := f.repo.LocationSubtreeIDs(cctx, []string{x})
	slog.SetDefault(prev)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sortedIDs(x, y)) {
		t.Fatalf("subtree on a cycle = %v", got)
	}
	// Reaching the depth bound means corrupt data and is logged (L-A security review item 6).
	if !strings.Contains(logs.String(), "depth bound") {
		t.Fatalf("depth bound not logged: %q", logs.String())
	}
	// Paths terminate on the cycle as well.
	if _, err := f.repo.LocationPaths(cctx, []string{x}); err != nil {
		t.Fatal(err)
	}
	restore()
	logs.Reset()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	_, _ = f.repo.LocationSubtreeIDs(ctx, []string{site})
	slog.SetDefault(prev)
	if logs.Len() != 0 {
		t.Fatalf("valid tree logged: %q", logs.String())
	}
}

func TestLocationPaths(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	site := f.location(f.pfx + " Site P")
	a1 := f.area(f.pfx+" P1", site)
	a1a := f.area(f.pfx+" P1a", a1)
	if _, err := f.pool.Exec(ctx, `UPDATE organization.locations SET active = false WHERE id = $1`, a1); err != nil {
		t.Fatal(err)
	}
	unknown := "01960000-0000-7000-8000-00000000dead"
	got, err := f.repo.LocationPaths(ctx, []string{a1a, site, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[unknown]; ok || len(got) != 2 {
		t.Fatalf("paths = %+v", got)
	}
	p := got[a1a]
	if len(p) != 3 || p[0].ID != site || p[1].ID != a1 || p[2].ID != a1a || p[1].Active || !p[2].Active || p[2].Name != f.pfx+" P1a" {
		t.Fatalf("path of P1a = %+v", p)
	}
	if s := got[site]; len(s) != 1 || s[0].ID != site {
		t.Fatalf("path of the Site = %+v", s)
	}
}

// Performance bound (F16 L13): three Sites with 200 areas in total expand in one bounded query.
func TestLocationSubtreeIDsPerformanceBound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var sites []string
	total := 0
	for s := 0; s < 3; s++ {
		site := f.location(fmt.Sprintf("%s Site %d", f.pfx, s))
		sites = append(sites, site)
		total++
		for a := 0; a < 10; a++ {
			area := f.area(fmt.Sprintf("%s S%d A%d", f.pfx, s, a), site)
			total++
			for b := 0; b < 6 && total < 3+200; b++ {
				f.area(fmt.Sprintf("%s S%d A%d B%d", f.pfx, s, a, b), area)
				total++
			}
		}
	}
	start := time.Now()
	got, err := f.repo.LocationSubtreeIDs(ctx, sites)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("expanded %d ids, want %d", len(got), total)
	}
	if elapsed > time.Second {
		t.Fatalf("subtree expansion of %d Locations took %v", total, elapsed)
	}
}
