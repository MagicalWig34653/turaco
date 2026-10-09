package main

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Stage is one part of the arrival-rate schedule. A "hold" stage keeps Rate
// constant; a "ramp" stage moves linearly from the previous stage's end rate
// (the first ramp starts at its own rate) to Rate. Rate 0 is a pause
// (cooldown): no arrivals, the run only waits for in-flight work.
type Stage struct {
	Name     string
	Kind     string // "hold" or "ramp"
	Rate     float64
	Duration time.Duration
}

// ParseStages parses "ramp:50@20s,hold:50@30s,ramp:500@20s,hold:500@30s,hold:0@10s".
// The kind prefix is optional and defaults to hold. An optional name can be
// given as "name=kind:rate@duration".
func ParseStages(spec string) ([]Stage, error) {
	var out []Stage
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		st := Stage{Kind: "hold"}
		if eq := strings.Index(part, "="); eq > 0 {
			st.Name = strings.TrimSpace(part[:eq])
			part = strings.TrimSpace(part[eq+1:])
		}
		if colon := strings.Index(part, ":"); colon > 0 {
			st.Kind = strings.ToLower(strings.TrimSpace(part[:colon]))
			part = strings.TrimSpace(part[colon+1:])
		}
		if st.Kind != "hold" && st.Kind != "ramp" {
			return nil, fmt.Errorf("stage %d: unknown kind %q (use hold or ramp)", i+1, st.Kind)
		}
		at := strings.Index(part, "@")
		if at < 0 {
			return nil, fmt.Errorf("stage %d: expected RATE@DURATION, got %q", i+1, part)
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(part[:at]), 64)
		if err != nil || rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return nil, fmt.Errorf("stage %d: invalid rate %q", i+1, part[:at])
		}
		d, err := time.ParseDuration(strings.TrimSpace(part[at+1:]))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("stage %d: invalid duration %q", i+1, part[at+1:])
		}
		st.Rate, st.Duration = rate, d
		if st.Name == "" {
			st.Name = fmt.Sprintf("%d-%s-%g", len(out)+1, st.Kind, rate)
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no stages given")
	}
	return out, nil
}

// Profile returns a named stage specification.
func Profile(name string) (string, error) {
	switch name {
	case "smoke":
		return "hold:20@5s", nil
	case "ramp":
		return "ramp:50@10s,hold:50@20s,ramp:500@20s,hold:500@30s,ramp:2000@20s,hold:2000@30s,ramp:4000@20s,hold:4000@30s,hold:0@10s", nil
	case "spike":
		return "hold:100@20s,hold:2000@10s,hold:100@20s,hold:0@10s", nil
	case "soak":
		return "ramp:100@30s,hold:100@10m,hold:0@10s", nil
	}
	return "", fmt.Errorf("unknown profile %q (smoke, ramp, spike, soak)", name)
}

type segment struct {
	stage    int
	start    time.Duration
	dur      time.Duration
	r0, r1   float64
	cumStart float64
	cumTotal float64
}

// Plan is the piecewise-linear arrival-rate function of a run.
type Plan struct {
	Stages []Stage
	segs   []segment
	total  float64
	dur    time.Duration
}

// NewPlan builds the plan of the stages.
func NewPlan(stages []Stage) *Plan {
	p := &Plan{Stages: stages}
	prev := 0.0
	var t time.Duration
	for i, st := range stages {
		r0 := st.Rate
		if st.Kind == "ramp" {
			r0 = prev
			if i == 0 {
				r0 = st.Rate
			}
		}
		sec := st.Duration.Seconds()
		seg := segment{stage: i, start: t, dur: st.Duration, r0: r0, r1: st.Rate, cumStart: p.total, cumTotal: (r0 + st.Rate) / 2 * sec}
		p.segs = append(p.segs, seg)
		p.total += seg.cumTotal
		t += st.Duration
		prev = st.Rate
	}
	p.dur = t
	return p
}

// Duration is the length of the schedule.
func (p *Plan) Duration() time.Duration { return p.dur }

// Expected is the expected number of arrivals of the whole schedule.
func (p *Plan) Expected() float64 { return p.total }

// StageAt returns the stage index active at offset t (the last stage after the end).
func (p *Plan) StageAt(t time.Duration) int {
	for _, s := range p.segs {
		if t < s.start+s.dur {
			return s.stage
		}
	}
	return len(p.segs) - 1
}

// RateAt returns the target arrival rate at offset t.
func (p *Plan) RateAt(t time.Duration) float64 {
	for _, s := range p.segs {
		if t < s.start+s.dur {
			frac := float64(t-s.start) / float64(s.dur)
			return s.r0 + (s.r1-s.r0)*frac
		}
	}
	return 0
}

// TimeOf returns the offset at which the cumulative expected number of
// arrivals reaches cum. ok is false when cum lies beyond the schedule.
func (p *Plan) TimeOf(cum float64) (time.Duration, bool) {
	if cum < 0 {
		cum = 0
	}
	if cum > p.total+1e-9 {
		return 0, false
	}
	for _, s := range p.segs {
		if s.cumTotal <= 0 || cum > s.cumStart+s.cumTotal+1e-9 {
			continue
		}
		c := cum - s.cumStart
		if c < 0 {
			c = 0
		}
		sec := s.dur.Seconds()
		k := (s.r1 - s.r0) / sec
		var x float64
		if math.Abs(k) < 1e-12 {
			x = c / s.r0
		} else {
			disc := s.r0*s.r0 + 2*k*c
			if disc < 0 {
				disc = 0
			}
			// The "+ sqrt" root is the smaller non-negative one for ramps up and down.
			x = (-s.r0 + math.Sqrt(disc)) / k
		}
		if x < 0 {
			x = 0
		}
		if x > sec {
			x = sec
		}
		return s.start + time.Duration(x*float64(time.Second)), true
	}
	return 0, false
}

// Arrivals generates scheduled start offsets of an open-loop arrival process.
type Arrivals struct {
	plan    *Plan
	poisson bool
	rng     *rand.Rand
	cum     float64
	n       int
}

// NewArrivals creates the arrival process: evenly spaced (uniform) or a
// non-homogeneous Poisson process following the plan's rate.
func NewArrivals(p *Plan, poisson bool, seed int64) *Arrivals {
	return &Arrivals{plan: p, poisson: poisson, rng: rand.New(rand.NewSource(seed))}
}

// Next returns the scheduled offset of the next arrival; ok is false at the end of the schedule.
func (a *Arrivals) Next() (time.Duration, bool) {
	if a.poisson {
		a.cum += a.rng.ExpFloat64()
	} else if a.n == 0 {
		a.cum = 0.5
	} else {
		a.cum++
	}
	a.n++
	return a.plan.TimeOf(a.cum)
}
