package main

import (
	"math"
	"testing"
	"time"
)

func TestParseStages(t *testing.T) {
	st, err := ParseStages("ramp:50@20s, hold:50@30s,spike=hold:2000@5s,100@1m")
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 4 {
		t.Fatalf("got %d stages", len(st))
	}
	if st[0].Kind != "ramp" || st[0].Rate != 50 || st[0].Duration != 20*time.Second {
		t.Errorf("stage 0: %+v", st[0])
	}
	if st[2].Name != "spike" || st[2].Rate != 2000 {
		t.Errorf("stage 2: %+v", st[2])
	}
	if st[3].Kind != "hold" || st[3].Duration != time.Minute {
		t.Errorf("stage 3: %+v", st[3])
	}
	for _, bad := range []string{"", "50", "hold:x@5s", "hold:5@0s", "jump:5@5s", "hold:-1@5s", "hold:5@abc"} {
		if _, err := ParseStages(bad); err == nil {
			t.Errorf("ParseStages(%q) should fail", bad)
		}
	}
}

func TestProfilesParse(t *testing.T) {
	for _, name := range []string{"smoke", "ramp", "spike", "soak"} {
		spec, err := Profile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseStages(spec); err != nil {
			t.Errorf("profile %s: %v", name, err)
		}
	}
	if _, err := Profile("nope"); err == nil {
		t.Error("unknown profile should fail")
	}
}

func TestPlanHoldArrivals(t *testing.T) {
	p := NewPlan([]Stage{{Name: "a", Kind: "hold", Rate: 100, Duration: 10 * time.Second}})
	if got := p.Expected(); math.Abs(got-1000) > 1e-9 {
		t.Fatalf("expected 1000 arrivals, got %v", got)
	}
	a := NewArrivals(p, false, 1)
	var prev time.Duration
	n := 0
	for {
		off, ok := a.Next()
		if !ok {
			break
		}
		if n > 0 {
			if gap := off - prev; gap < 9*time.Millisecond || gap > 11*time.Millisecond {
				t.Fatalf("gap %v at arrival %d, want about 10ms", gap, n)
			}
		}
		prev = off
		n++
	}
	if n != 1000 {
		t.Fatalf("got %d arrivals, want 1000", n)
	}
	if prev > 10*time.Second {
		t.Fatalf("last arrival %v beyond the schedule", prev)
	}
}

func TestPlanRampIncreasesRate(t *testing.T) {
	// ramp from 0 would be a hold at the start rate for the first stage, so use a hold then a ramp.
	p := NewPlan([]Stage{
		{Name: "base", Kind: "hold", Rate: 10, Duration: 10 * time.Second},
		{Name: "up", Kind: "ramp", Rate: 110, Duration: 10 * time.Second},
	})
	want := 10*10 + (10+110)/2*10
	if math.Abs(p.Expected()-float64(want)) > 1e-9 {
		t.Fatalf("expected %d, got %v", want, p.Expected())
	}
	a := NewArrivals(p, false, 1)
	first, second := 0, 0 // arrivals in the first and the second half of the ramp
	for {
		off, ok := a.Next()
		if !ok {
			break
		}
		switch {
		case off >= 10*time.Second && off < 15*time.Second:
			first++
		case off >= 15*time.Second:
			second++
		}
	}
	// integral of the ramp: first half 10..60 -> 175, second half 60..110 -> 425
	if first < 173 || first > 177 || second < 423 || second > 427 {
		t.Fatalf("ramp halves: %d and %d arrivals, want about 175 and 425", first, second)
	}
}

func TestPlanRampDownAndPause(t *testing.T) {
	p := NewPlan([]Stage{
		{Kind: "hold", Rate: 100, Duration: 2 * time.Second},
		{Kind: "ramp", Rate: 0, Duration: 2 * time.Second},
		{Kind: "hold", Rate: 0, Duration: 5 * time.Second},
	})
	if got := p.Expected(); math.Abs(got-300) > 1e-9 {
		t.Fatalf("expected 300, got %v", got)
	}
	a := NewArrivals(p, false, 1)
	n := 0
	var last time.Duration
	for {
		off, ok := a.Next()
		if !ok {
			break
		}
		if off < last {
			t.Fatalf("arrivals are not monotonic: %v after %v", off, last)
		}
		last = off
		n++
	}
	if n != 300 {
		t.Fatalf("got %d arrivals, want 300", n)
	}
	if last > 4*time.Second+time.Millisecond {
		t.Fatalf("arrival %v inside the pause", last)
	}
	if st := p.StageAt(3 * time.Second); st != 1 {
		t.Errorf("StageAt(3s) = %d", st)
	}
	if st := p.StageAt(6 * time.Second); st != 2 {
		t.Errorf("StageAt(6s) = %d", st)
	}
	if r := p.RateAt(3 * time.Second); math.Abs(r-50) > 1e-6 {
		t.Errorf("RateAt(3s) = %v, want 50", r)
	}
}

func TestPoissonArrivalsMatchExpectation(t *testing.T) {
	p := NewPlan([]Stage{{Kind: "hold", Rate: 500, Duration: 20 * time.Second}})
	a := NewArrivals(p, true, 42)
	n := 0
	for {
		if _, ok := a.Next(); !ok {
			break
		}
		n++
	}
	// 10000 expected; the standard deviation is 100
	if n < 9600 || n > 10400 {
		t.Fatalf("poisson produced %d arrivals, want about 10000", n)
	}
}

func TestSelectPersonas(t *testing.T) {
	r := Roster("x")
	if len(r) != 32 {
		t.Fatalf("roster has %d personas, want 32 (31 simulation logins + devadmin)", len(r))
	}
	all := SelectPersonas(r, nil, 0)
	if len(all) != 32 {
		t.Fatalf("all: %d", len(all))
	}
	few := SelectPersonas(r, nil, 7)
	seen := map[string]bool{}
	for _, p := range few {
		seen[p.Class] = true
	}
	if len(few) != 7 || len(seen) != 7 {
		t.Fatalf("7 users should cover 7 classes: %d users, %d classes", len(few), len(seen))
	}
	onlyStaff := SelectPersonas(r, map[string]bool{ClassTechnician: true, ClassLead: true}, 0)
	if len(onlyStaff) != 9 {
		t.Fatalf("technician+lead: %d", len(onlyStaff))
	}
	if _, err := ParseWeights("employee=x"); err == nil {
		t.Error("bad weight should fail")
	}
	if _, err := ParseClasses("nobody"); err == nil {
		t.Error("unknown class should fail")
	}
	w, err := ParseWeights("employee=1,technician=0")
	if err != nil || w[ClassEmployee] != 1 || w[ClassTechnician] != 0 || w[ClassLead] != defaultWeights[ClassLead] {
		t.Errorf("weights: %v %v", w, err)
	}
}

func TestWeightedPick(t *testing.T) {
	w := newWeighted(map[string]float64{"a": 1, "b": 3, "c": 0})
	counts := map[string]int{}
	for i := 0; i < 4000; i++ {
		counts[w.pick(float64(i)/4000)]++
	}
	if counts["c"] != 0 || counts["a"] < 990 || counts["a"] > 1010 || counts["b"] < 2990 || counts["b"] > 3010 {
		t.Fatalf("distribution %v", counts)
	}
}
