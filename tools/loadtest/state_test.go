package main

import (
	"math/rand"
	"testing"
)

func TestTrackerDetectsLostUpdate(t *testing.T) {
	tr := NewTracker()
	tr.Created("t1", "TKT-000001", 1)
	tr.Write("t1", 1, 2, "tickets.start")
	tr.Write("t1", 2, 3, "tickets.assign")
	list, counts, ok := tr.Snapshot()
	if len(list) != 0 || ok != 2 {
		t.Fatalf("clean history flagged: %v, writes %d", list, ok)
	}
	// Two writers both read version 3 and both "succeed": one update was lost.
	tr.Write("t1", 3, 4, "tickets.priority")
	tr.Write("t1", 3, 4, "tickets.assign")
	list, counts, _ = tr.Snapshot()
	if counts["lost_update"] != 1 || len(list) != 1 {
		t.Fatalf("lost update not detected: %v %v", counts, list)
	}
	tr.Write("t1", 4, 3, "tickets.resolve")
	if _, counts, _ = tr.Snapshot(); counts["version_regressed"] != 1 {
		t.Fatalf("regression not detected: %v", counts)
	}
	if v, ref := tr.MaxSeen("t1"); v != 4 || ref != "TKT-000001" {
		t.Errorf("MaxSeen = %d %s", v, ref)
	}
	tr.Observe("t1", 9)
	if v, _ := tr.MaxSeen("t1"); v != 9 {
		t.Errorf("Observe did not raise max: %d", v)
	}
}

func TestTrackerNoopWriteIsNotAViolation(t *testing.T) {
	tr := NewTracker()
	tr.Created("t1", "TKT-000001", 1)
	tr.Write("t1", 1, 1, "tickets.assign")
	tr.Write("t1", 1, 1, "tickets.assign")
	if _, counts, _ := tr.Snapshot(); len(counts) != 0 {
		t.Fatalf("no-op writes flagged: %v", counts)
	}
}

func TestTrackerDuplicateReference(t *testing.T) {
	tr := NewTracker()
	tr.Created("a", "TKT-000007", 1)
	tr.Created("b", "TKT-000007", 1)
	if _, counts, _ := tr.Snapshot(); counts["duplicate_reference"] != 1 {
		t.Fatalf("duplicate reference not detected: %v", counts)
	}
}

func TestTrackerViolationDetailsAreBounded(t *testing.T) {
	tr := NewTracker()
	for i := 0; i < 500; i++ {
		tr.Violate("authorization_leak", "leak %d", i)
	}
	list, counts, _ := tr.Snapshot()
	if len(list) != maxViolationDetails || counts["authorization_leak"] != 500 {
		t.Fatalf("details %d, count %d", len(list), counts["authorization_leak"])
	}
}

func TestPoolPickingAndRemoval(t *testing.T) {
	p := NewPool()
	rng := rand.New(rand.NewSource(1))
	if p.Pick(rng, 5, 0.5) != nil || p.PickOwn(rng, "x") != nil || p.PickForeign(rng, "x") != nil {
		t.Fatal("empty pool must return nil")
	}
	for i := 0; i < 100; i++ {
		login := "anna"
		if i%2 == 1 {
			login = "bob"
		}
		p.Add(&Entry{ID: string(rune('A' + i)), Ref: "R", Reporter: login})
	}
	if p.Len() != 100 || p.Created() != 100 {
		t.Fatalf("len %d created %d", p.Len(), p.Created())
	}
	hot := 0
	for i := 0; i < 2000; i++ {
		if e := p.Pick(rng, 5, 1); e != nil {
			// hot set = the 5 newest entries
			if e.ID >= string(rune('A'+95)) {
				hot++
			}
		}
	}
	if hot != 2000 {
		t.Errorf("hotProb=1 should always pick from the hot set, got %d of 2000", hot)
	}
	for i := 0; i < 200; i++ {
		if e := p.PickOwn(rng, "anna"); e == nil || e.Reporter != "anna" {
			t.Fatalf("PickOwn returned %v", e)
		}
		if e := p.PickForeign(rng, "anna"); e == nil || e.Reporter == "anna" {
			t.Fatalf("PickForeign returned %v", e)
		}
	}
	for i := 0; i < 100; i++ {
		p.Remove(string(rune('A' + i)))
	}
	if p.Len() != 0 || p.Pick(rng, 5, 0.5) != nil || p.PickOwn(rng, "anna") != nil {
		t.Fatal("pool should be empty after removing everything")
	}
	p.Remove("unknown") // no panic
}

func TestIDRingOverwrites(t *testing.T) {
	r := newIDRing(3)
	rng := rand.New(rand.NewSource(1))
	if r.pick(rng) != "" {
		t.Fatal("empty ring")
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		r.add(id)
	}
	for i := 0; i < 50; i++ {
		if id := r.pick(rng); id == "a" || id == "b" {
			t.Fatalf("overwritten id %s still returned", id)
		}
	}
}

func TestSafetyChecks(t *testing.T) {
	cases := []struct {
		base, pg string
		iKnow    bool
		ok       bool
	}{
		{"http://localhost:8080", "", false, true},
		{"http://127.0.0.1:8080", "postgres://u:p@localhost:5432/turaco", false, true},
		{"http://[::1]:8080", "", false, true},
		{"https://turaco.example.com", "", false, false},
		{"http://10.1.2.3:8080", "", false, false},
		{"https://turaco.example.com", "", true, true},
		{"http://localhost:8080", "postgres://u:p@db.prod.internal:5432/turaco", false, false},
		{"not a url", "", false, false},
	}
	t.Setenv("APP_ENV", "")
	for _, c := range cases {
		err := checkLocal(c.base, c.pg, c.iKnow)
		if (err == nil) != c.ok {
			t.Errorf("checkLocal(%q, %q, %v) = %v, want ok=%v", c.base, c.pg, c.iKnow, err, c.ok)
		}
	}
	t.Setenv("APP_ENV", "production")
	if err := checkLocal("http://localhost:8080", "", false); err == nil {
		t.Error("APP_ENV=production must be refused")
	}
	if err := checkLocal("http://localhost:8080", "", true); err != nil {
		t.Errorf("--i-know should override: %v", err)
	}
	if !devLike("Development") || devLike("production") || devLike("staging") {
		t.Error("devLike")
	}
	if !validTag("ab12-x") || validTag("A") || validTag("a b") || validTag("") || validTag("a]") {
		t.Error("validTag")
	}
}
