package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// RunOptions configure the open-loop run.
type RunOptions struct {
	Plan        *Plan
	Poisson     bool
	Seed        int64
	MaxInflight int           // worker goroutines = bound on concurrent operations
	QueueSize   int           // bound on scheduled-but-not-started operations; beyond it arrivals are dropped
	Drain       time.Duration // how long to wait for the queue and in-flight work after the last arrival
	Weights     map[string]float64
}

// TimelinePoint is one second of the run.
type TimelinePoint struct {
	T          float64 `json:"t"`
	Stage      string  `json:"stage"`
	Target     float64 `json:"targetRate"`
	Offered    int64   `json:"offered"`
	Started    int64   `json:"started"`
	Finished   int64   `json:"finished"`
	Dropped    int64   `json:"dropped"`
	InFlight   int64   `json:"inFlight"`
	QueueDepth int     `json:"queueDepth"`
	Requests   uint64  `json:"requests"`
	Errors     uint64  `json:"errors"`
	Throttled  uint64  `json:"throttled"`
	P50Ms      float64 `json:"p50Ms"`
	P99Ms      float64 `json:"p99Ms"`
}

// StageCounters are the arrival counters of one stage.
type StageCounters struct {
	Offered  int64 `json:"offered"`
	Dropped  int64 `json:"dropped"`
	Finished int64 `json:"finished"`
}

// RunResult is what the open-loop run produced besides the recorder.
type RunResult struct {
	Start        time.Time
	End          time.Time
	Timeline     []TimelinePoint
	Stages       []StageCounters
	Offered      int64
	Dropped      int64
	Finished     int64
	Abandoned    int64
	MaxInFlight  int64
	MaxQueue     int
	LagP99       time.Duration
	LagMax       time.Duration
	Interrupted  bool
	MaxGoroutine int
}

type job struct {
	class string
	op    int
	stage int
	sched time.Time
}

// Run executes the schedule: arrivals are generated at their scheduled times
// regardless of how fast the server answers (open loop), handed to a bounded
// worker pool through a bounded queue, and timed from the scheduled start.
func Run(ctx context.Context, env *Env, rec *Recorder, opts RunOptions, progress io.Writer) *RunResult {
	ops := Ops()
	classes := map[string]float64{}
	opChoosers := map[string]*weighted{}
	opByClass := map[string][]OpDef{}
	for class, sessions := range env.Sessions {
		if len(sessions) == 0 {
			continue
		}
		if w := opts.Weights[class]; w > 0 {
			classes[class] = w
		}
		defs := ops[class]
		opByClass[class] = defs
		m := map[string]float64{}
		for i, d := range defs {
			m[fmt.Sprintf("%03d", i)] = d.Weight
		}
		opChoosers[class] = newWeighted(m)
	}
	classPick := newWeighted(classes)
	res := &RunResult{Stages: make([]StageCounters, len(opts.Plan.Stages))}
	if classPick.empty() {
		return res
	}

	jobs := make(chan job, opts.QueueSize)
	var offered, dropped, started, finished atomic.Int64
	stageOffered := make([]atomic.Int64, len(opts.Plan.Stages))
	stageDropped := make([]atomic.Int64, len(opts.Plan.Stages))
	stageFinished := make([]atomic.Int64, len(opts.Plan.Stages))

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	var wg sync.WaitGroup
	for w := 0; w < opts.MaxInflight; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(opts.Seed + int64(id)*7919 + 1))
			for j := range jobs {
				started.Add(1)
				sessions := env.Sessions[j.class]
				s := sessions[rng.Intn(len(sessions))]
				if cookie, at := s.snapshot(); cookie == "" || time.Since(at) > env.Client.maxAge {
					if err := env.Client.Login(runCtx, s, false); err != nil && runCtx.Err() != nil {
						finished.Add(1)
						stageFinished[j.stage].Add(1)
						continue
					}
				}
				jc := &JobCtx{Stage: j.stage, Sched: j.sched}
				opByClass[j.class][j.op].Fn(runCtx, env, jc, s, rng)
				finished.Add(1)
				stageFinished[j.stage].Add(1)
			}
		}(w)
	}

	start := time.Now()
	res.Start = start

	// Timeline and progress.
	stopTL := make(chan struct{})
	var tlDone sync.WaitGroup
	tlDone.Add(1)
	go func() {
		defer tlDone.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var lastOff, lastStart, lastFin, lastDrop int64
		for n := 1; ; n++ {
			select {
			case <-stopTL:
				return
			case <-tick.C:
			}
			off, st, fin, dr := offered.Load(), started.Load(), finished.Load(), dropped.Load()
			win := rec.TakeWindow()
			t := time.Since(start)
			p := TimelinePoint{
				T: t.Seconds(), Stage: opts.Plan.Stages[opts.Plan.StageAt(t)].Name, Target: opts.Plan.RateAt(t),
				Offered: off - lastOff, Started: st - lastStart, Finished: fin - lastFin, Dropped: dr - lastDrop,
				InFlight: st - fin, QueueDepth: len(jobs), Requests: win.Count, Errors: win.Errors, Throttled: win.Throttled,
				P50Ms: ms(win.P50), P99Ms: ms(win.P99),
			}
			lastOff, lastStart, lastFin, lastDrop = off, st, fin, dr
			res.Timeline = append(res.Timeline, p)
			if p.InFlight > res.MaxInFlight {
				res.MaxInFlight = p.InFlight
			}
			if p.QueueDepth > res.MaxQueue {
				res.MaxQueue = p.QueueDepth
			}
			if g := runtime.NumGoroutine(); g > res.MaxGoroutine {
				res.MaxGoroutine = g
			}
			if progress != nil && n%5 == 0 {
				fmt.Fprintf(progress, "[%5.0fs] %-14s target %6.0f/s  offered %5d  done %5d  inflight %4d  queue %5d  dropped %4d  p99 %7.1f ms  err %d  429 %d\n",
					p.T, p.Stage, p.Target, p.Offered, p.Finished, p.InFlight, p.QueueDepth, p.Dropped, p.P99Ms, p.Errors, p.Throttled)
			}
		}
	}()

	// Dispatcher: schedule arrivals.
	arr := NewArrivals(opts.Plan, opts.Poisson, opts.Seed)
	rng := rand.New(rand.NewSource(opts.Seed ^ 0x5eed))
	lag := NewHist()
dispatch:
	for {
		off, ok := arr.Next()
		if !ok {
			break
		}
		due := start.Add(off)
		if d := time.Until(due); d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				res.Interrupted = true
				break dispatch
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			res.Interrupted = true
			break
		}
		now := time.Now()
		lag.Add(now.Sub(due))
		stage := opts.Plan.StageAt(off)
		class := classPick.pick(rng.Float64())
		op, _ := atoiSafe(opChoosers[class].pick(rng.Float64()))
		offered.Add(1)
		stageOffered[stage].Add(1)
		select {
		case jobs <- job{class: class, op: op, stage: stage, sched: due}:
		default:
			dropped.Add(1)
			stageDropped[stage].Add(1)
		}
	}
	close(jobs)

	// Drain: wait for queued and in-flight work.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	drain := time.NewTimer(opts.Drain)
	select {
	case <-done:
	case <-drain.C:
		cancelRun()
		<-done
	case <-ctx.Done():
		res.Interrupted = true
		cancelRun()
		<-done
	}
	drain.Stop()
	res.End = time.Now()
	close(stopTL)
	tlDone.Wait()

	res.Offered, res.Dropped, res.Finished = offered.Load(), dropped.Load(), finished.Load()
	res.Abandoned = res.Offered - res.Dropped - res.Finished
	res.LagP99, res.LagMax = lag.Quantile(0.99), lag.Max()
	for i := range res.Stages {
		res.Stages[i] = StageCounters{Offered: stageOffered[i].Load(), Dropped: stageDropped[i].Load(), Finished: stageFinished[i].Load()}
	}
	return res
}

func atoiSafe(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Seed creates the initial tickets (employees raise them) and loads the Queue list.
func Seed(ctx context.Context, env *Env, n int) error {
	// Queues, as seen by a staff or any session.
	var probe *Session
	for _, class := range []string{ClassFirstLevel, ClassTechnician, ClassLead, ClassAdmin, ClassEmployee, ClassViewer, ClassVendor} {
		if ss := env.Sessions[class]; len(ss) > 0 {
			probe = ss[0]
			break
		}
	}
	if probe != nil {
		var out struct {
			Items []QueueInfo `json:"items"`
		}
		if r := env.Client.Call(ctx, nil, probe, "queues.list", "GET", "/service-desk/queues", nil, &out); r.OK() {
			env.Queues = out.Items
		}
	}
	emps := env.Sessions[ClassEmployee]
	if n <= 0 || len(emps) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			rng := rand.New(rand.NewSource(int64(i) + 99))
			env.createTicket(ctx, &JobCtx{Stage: -1}, emps[i%len(emps)], rng, "tickets.seed")
		}(i)
	}
	wg.Wait()
	return nil
}
