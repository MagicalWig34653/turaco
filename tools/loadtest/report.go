package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Latency is a latency summary in milliseconds.
type Latency struct {
	P50  float64 `json:"p50Ms"`
	P95  float64 `json:"p95Ms"`
	P99  float64 `json:"p99Ms"`
	Max  float64 `json:"maxMs"`
	Mean float64 `json:"meanMs"`
}

func latencyOf(h *Hist) Latency {
	return Latency{P50: ms(h.Quantile(0.5)), P95: ms(h.Quantile(0.95)), P99: ms(h.Quantile(0.99)), Max: ms(h.Max()), Mean: ms(h.Mean())}
}

// OpReport is the result of one operation (HTTP request kind).
type OpReport struct {
	Name         string            `json:"name"`
	Requests     uint64            `json:"requests"`
	PerSecond    float64           `json:"perSecond"`
	Response     Latency           `json:"response"`
	Service      Latency           `json:"service"`
	Classes      map[string]uint64 `json:"classes"`
	Statuses     map[string]uint64 `json:"statuses"`
	ExpectDenied bool              `json:"expectDenied,omitempty"`
	Warning      string            `json:"warning,omitempty"`
}

// StageReport is the result of one schedule stage.
type StageReport struct {
	Index         int               `json:"index"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	TargetRate    float64           `json:"targetRate"`
	DurationSec   float64           `json:"durationSec"`
	Offered       int64             `json:"offeredOps"`
	Dropped       int64             `json:"droppedOps"`
	Finished      int64             `json:"finishedOps"`
	OfferedPerSec float64           `json:"offeredOpsPerSec"`
	DonePerSec    float64           `json:"finishedOpsPerSec"`
	Requests      uint64            `json:"requests"`
	RequestsPerS  float64           `json:"requestsPerSec"`
	Response      Latency           `json:"response"`
	Classes       map[string]uint64 `json:"classes"`
	Saturated     bool              `json:"saturated"`
	Signals       []string          `json:"saturationSignals,omitempty"`
}

// Report is the complete result of a run (JSON).
type Report struct {
	Tool        string            `json:"tool"`
	Tag         string            `json:"tag"`
	BaseURL     string            `json:"baseUrl"`
	Environment string            `json:"serverEnvironment"`
	StartedAt   time.Time         `json:"startedAt"`
	EndedAt     time.Time         `json:"endedAt"`
	DurationS   float64           `json:"durationSec"`
	Config      map[string]any    `json:"config"`
	Users       []string          `json:"users"`
	Totals      Totals            `json:"totals"`
	Stages      []StageReport     `json:"stages"`
	Ops         []OpReport        `json:"operations"`
	Writes      WriteReport       `json:"writes"`
	Client      ClientReport      `json:"generator"`
	Postgres    *PGSummary        `json:"postgres,omitempty"`
	Invariants  []InvariantResult `json:"invariants"`
	Violations  []Violation       `json:"violations,omitempty"`
	Timeline    []TimelinePoint   `json:"timeline"`
	Verdict     Verdict           `json:"verdict"`
}

// Totals are the run-wide numbers.
type Totals struct {
	OfferedOps   int64             `json:"offeredOps"`
	FinishedOps  int64             `json:"finishedOps"`
	DroppedOps   int64             `json:"droppedOps"`
	AbandonedOps int64             `json:"abandonedOps"`
	Requests     uint64            `json:"requests"`
	RequestsPerS float64           `json:"requestsPerSec"`
	Response     Latency           `json:"response"`
	Classes      map[string]uint64 `json:"classes"`
	Statuses     map[string]uint64 `json:"statuses"`
	Bytes        uint64            `json:"responseBytes"`
}

// WriteReport describes version-conflict handling.
type WriteReport struct {
	TicketsCreated   int64  `json:"ticketsCreated"`
	SuccessfulWrites int    `json:"successfulConditionalWrites"`
	Conflicts409     uint64 `json:"versionConflicts409"`
	Retries          int64  `json:"conflictRetries"`
	GaveUp           int64  `json:"gaveUpAfterRetries"`
	StaleSkips       int64  `json:"staleSkips"`
	Probes           int64  `json:"deniedAccessProbes"`
	Leaks            int64  `json:"authorizationLeaks"`
}

// ClientReport shows the health of the load generator itself.
type ClientReport struct {
	DispatchLagP99Ms float64 `json:"dispatchLagP99Ms"`
	DispatchLagMaxMs float64 `json:"dispatchLagMaxMs"`
	MaxInFlight      int64   `json:"maxInFlight"`
	MaxQueueDepth    int     `json:"maxQueueDepth"`
	MaxGoroutines    int     `json:"maxGoroutines"`
	Logins           uint64  `json:"logins"`
	Relogins         uint64  `json:"relogins"`
	Interrupted      bool    `json:"interrupted"`
}

// Verdict is the pass/fail decision.
type Verdict struct {
	Pass     bool     `json:"pass"`
	Failures []string `json:"failures,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// ReportInput collects everything that BuildReport needs.
type ReportInput struct {
	Tag        string
	BaseURL    string
	ServerEnv  string
	Plan       *Plan
	Config     map[string]any
	Users      []string
	Rec        *Recorder
	Run        *RunResult
	Env        *Env
	PG         *PGSummary
	Invariants []InvariantResult
	SLOP99     time.Duration
	Logins     uint64
	Relogins   uint64
}

func classMap(c *Cell) map[string]uint64 {
	m := map[string]uint64{}
	for i, n := range c.Classes {
		if n > 0 {
			m[Class(i).String()] = n
		}
	}
	return m
}

func statusMap(c *Cell) map[string]uint64 {
	m := map[string]uint64{}
	for s, n := range c.Statuses {
		key := fmt.Sprint(s)
		if s == 0 {
			key = "none"
		}
		m[key] = n
	}
	return m
}

// BuildReport assembles the report and the verdict.
func BuildReport(in ReportInput) *Report {
	run := in.Run
	dur := run.End.Sub(run.Start).Seconds()
	if dur <= 0 {
		dur = 1
	}
	total := in.Rec.Total()
	runTotal := in.Rec.TotalRun()
	rep := &Report{
		Tool: "turaco loadtest", Tag: in.Tag, BaseURL: in.BaseURL, Environment: in.ServerEnv,
		StartedAt: run.Start, EndedAt: run.End, DurationS: dur, Config: in.Config, Users: in.Users,
		Timeline: run.Timeline, Postgres: in.PG, Invariants: in.Invariants,
	}
	rep.Totals = Totals{
		OfferedOps: run.Offered, FinishedOps: run.Finished, DroppedOps: run.Dropped, AbandonedOps: run.Abandoned,
		Requests: runTotal.Total(), RequestsPerS: float64(runTotal.Total()) / dur, Response: latencyOf(runTotal.Response),
		Classes: classMap(total), Statuses: statusMap(total), Bytes: total.Bytes,
	}

	names, cells := in.Rec.ByOp()
	for _, n := range names {
		c := cells[n]
		op := OpReport{Name: n, Requests: c.Total(), PerSecond: float64(c.Total()) / dur, Response: latencyOf(c.Response), Service: latencyOf(c.Service),
			Classes: classMap(c), Statuses: statusMap(c), ExpectDenied: ExpectsDenied[n]}
		if c.Missing > 0 {
			op.Warning = fmt.Sprintf("%d answers were 404 without an error body: the route does not exist in this API build (restart the API from the current source)", c.Missing)
		} else if c.Classes[ClassDenied] > 0 && !op.ExpectDenied {
			op.Warning = "403/404 answers: the persona may lack the permission for this operation"
		}
		if c.Classes[ClassInvalid] > 0 {
			op.Warning = "400/422 answers: request rejected as invalid (generator or API contract problem)"
		}
		rep.Ops = append(rep.Ops, op)
	}

	byStage := in.Rec.ByStage()
	for i, st := range in.Plan.Stages {
		c := byStage[i]
		if c == nil {
			c = newCell()
		}
		sc := run.Stages[i]
		sec := st.Duration.Seconds()
		sr := StageReport{
			Index: i, Name: st.Name, Kind: st.Kind, TargetRate: st.Rate, DurationSec: sec,
			Offered: sc.Offered, Dropped: sc.Dropped, Finished: sc.Finished,
			OfferedPerSec: float64(sc.Offered) / sec, DonePerSec: float64(sc.Finished) / sec,
			Requests: c.Total(), RequestsPerS: float64(c.Total()) / sec, Response: latencyOf(c.Response), Classes: classMap(c),
		}
		if st.Rate > 0 {
			sr.Signals = saturationSignals(sr, c, in.SLOP99)
			sr.Saturated = len(sr.Signals) > 0
		}
		rep.Stages = append(rep.Stages, sr)
	}

	var writes int
	rep.Violations, _, writes = in.Env.Trk.Snapshot()
	rep.Writes = WriteReport{
		TicketsCreated: in.Env.created.Load(), SuccessfulWrites: writes, Conflicts409: total.Classes[ClassConflict],
		Retries: in.Env.conflictRetries.Load(), GaveUp: in.Env.conflictGiveups.Load(), StaleSkips: in.Env.staleSkips.Load(),
		Probes: in.Env.probes.Load(), Leaks: in.Env.leaks.Load(),
	}
	rep.Client = ClientReport{
		DispatchLagP99Ms: ms(run.LagP99), DispatchLagMaxMs: ms(run.LagMax), MaxInFlight: run.MaxInFlight, MaxQueueDepth: run.MaxQueue,
		MaxGoroutines: run.MaxGoroutine, Logins: in.Logins, Relogins: in.Relogins, Interrupted: run.Interrupted,
	}

	v := Verdict{Pass: true}
	for _, inv := range in.Invariants {
		if inv.Status == "fail" {
			v.Pass = false
			v.Failures = append(v.Failures, "invariant "+inv.Name+": "+inv.Detail)
		}
	}
	if n := total.Classes[ClassServerError]; n > 0 {
		v.Pass = false
		v.Failures = append(v.Failures, fmt.Sprintf("%d responses with status 5xx", n))
	}
	if n := total.Classes[ClassNetwork]; n > 0 {
		v.Pass = false
		v.Failures = append(v.Failures, fmt.Sprintf("%d timeouts or connection errors", n))
	}
	if n := total.Classes[ClassInvalid]; n > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d responses with 4xx other than 401/403/404/409/429 (invalid requests)", n))
	}
	if run.Dropped > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d arrivals were dropped because the generator queue was full: latency numbers understate the overload (raise --queue/--max-inflight or lower the rate)", run.Dropped))
	}
	if run.Abandoned > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d operations were still unfinished when the drain time ended", run.Abandoned))
	}
	if run.LagP99 > 50*time.Millisecond {
		v.Warnings = append(v.Warnings, fmt.Sprintf("dispatch lag p99 %.0f ms: the load generator itself was too slow for the schedule", ms(run.LagP99)))
	}
	if n := total.Classes[ClassThrottled]; n > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d responses were rate limited (429), expected for per-principal limiters", n))
	}
	rep.Verdict = v
	return rep
}

func saturationSignals(sr StageReport, c *Cell, slo time.Duration) []string {
	var s []string
	if sr.Dropped > 0 {
		s = append(s, fmt.Sprintf("%d arrivals dropped (queue full)", sr.Dropped))
	}
	if sr.Offered > 0 && float64(sr.Finished) < 0.95*float64(sr.Offered-sr.Dropped) {
		s = append(s, fmt.Sprintf("only %d of %d operations finished inside the stage (backlog)", sr.Finished, sr.Offered-sr.Dropped))
	}
	if slo > 0 && c.Response.Count() > 0 && c.Response.Quantile(0.99) > slo {
		s = append(s, fmt.Sprintf("p99 %.0f ms above %.0f ms", ms(c.Response.Quantile(0.99)), ms(slo)))
	}
	if n := c.Classes[ClassServerError] + c.Classes[ClassNetwork]; n > 0 {
		s = append(s, fmt.Sprintf("%d server errors or timeouts", n))
	}
	if t := c.Total(); t > 0 && float64(c.Classes[ClassThrottled])/float64(t) > 0.05 {
		s = append(s, fmt.Sprintf("%.0f%% of requests rate limited (429)", 100*float64(c.Classes[ClassThrottled])/float64(t)))
	}
	return s
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes the human readable report.
func (r *Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "\nTuraco load test  tag=%s  target=%s (server environment: %s)\n", r.Tag, r.BaseURL, r.Environment)
	fmt.Fprintf(w, "Run %s .. %s (%.0f s), %d simulated users\n\n", r.StartedAt.Format("15:04:05"), r.EndedAt.Format("15:04:05"), r.DurationS, len(r.Users))

	t := r.Totals
	fmt.Fprintf(w, "TOTALS  operations offered %d, finished %d, dropped %d, abandoned %d | HTTP requests %d (%.0f/s) | %.1f MiB received\n",
		t.OfferedOps, t.FinishedOps, t.DroppedOps, t.AbandonedOps, t.Requests, t.RequestsPerS, float64(t.Bytes)/(1<<20))
	fmt.Fprintf(w, "        latency from scheduled start: p50 %.1f  p95 %.1f  p99 %.1f  max %.1f ms\n", t.Response.P50, t.Response.P95, t.Response.P99, t.Response.Max)
	fmt.Fprintf(w, "        outcome classes: %s\n\n", fmtClasses(t.Classes))

	fmt.Fprintln(w, "STAGES (ops = scheduled operations; one operation makes 1-3 HTTP requests)")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "stage\ttarget/s\toffered/s\tdone/s\tdropped\treq/s\tp50 ms\tp95 ms\tp99 ms\tmax ms\t5xx+net\t429\t409\tsaturation")
	for _, s := range r.Stages {
		c := s.Classes
		sat := "-"
		if s.Saturated {
			sat = strings.Join(s.Signals, "; ")
		}
		fmt.Fprintf(tw, "%s\t%.0f\t%.0f\t%.0f\t%d\t%.0f\t%.1f\t%.1f\t%.1f\t%.1f\t%d\t%d\t%d\t%s\n", s.Name, s.TargetRate, s.OfferedPerSec, s.DonePerSec, s.Dropped, s.RequestsPerS,
			s.Response.P50, s.Response.P95, s.Response.P99, s.Response.Max, c["server_error"]+c["network"], c["throttled"], c["conflict"], sat)
	}
	tw.Flush()

	fmt.Fprintln(w, "\nOPERATIONS (latency from the scheduled start; svc p99 = from the moment the request was sent)")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "operation\trequests\treq/s\tp50 ms\tp95 ms\tp99 ms\tmax ms\tsvc p99\tok\t409\t429\t403/404\t401\t4xx\t5xx\tnet")
	for _, o := range r.Ops {
		c := o.Classes
		fmt.Fprintf(tw, "%s\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", o.Name, o.Requests, o.PerSecond,
			o.Response.P50, o.Response.P95, o.Response.P99, o.Response.Max, o.Service.P99,
			c["ok"], c["conflict"], c["throttled"], c["denied"], c["reauth"], c["invalid"], c["server_error"], c["network"])
	}
	tw.Flush()
	for _, o := range r.Ops {
		if o.Warning != "" {
			fmt.Fprintf(w, "  note: %s: %s\n", o.Name, o.Warning)
		}
	}

	wr := r.Writes
	fmt.Fprintf(w, "\nWRITES  tickets created %d, successful conditional writes %d, version conflicts (409) %d, retries after conflict %d, gave up %d, stale skips %d\n",
		wr.TicketsCreated, wr.SuccessfulWrites, wr.Conflicts409, wr.Retries, wr.GaveUp, wr.StaleSkips)
	fmt.Fprintf(w, "        denied-access probes %d, authorization leaks %d\n", wr.Probes, wr.Leaks)

	c := r.Client
	fmt.Fprintf(w, "\nGENERATOR  dispatch lag p99 %.1f ms (max %.1f), peak in-flight %d, peak queue %d, goroutines %d, logins %d, re-logins %d\n",
		c.DispatchLagP99Ms, c.DispatchLagMaxMs, c.MaxInFlight, c.MaxQueueDepth, c.MaxGoroutines, c.Logins, c.Relogins)

	if p := r.Postgres; p != nil {
		fmt.Fprintf(w, "\nPOSTGRES  %d samples", p.Samples)
		if p.Error != "" {
			fmt.Fprintf(w, " (sampling error: %s)", p.Error)
		}
		fmt.Fprintf(w, "\n  connections peak %d of %d (%.0f%%), active peak %d, idle-in-transaction peak %d, lock waiters peak %d, ungranted locks peak %d\n",
			p.PeakConnections, p.MaxConnections, p.ConnectionsPercent, p.PeakActive, p.PeakIdleInTx, p.PeakLockWaiters, p.PeakNotGranted)
		fmt.Fprintf(w, "  longest active query %.1f s, longest transaction %.1f s", p.LongestQuerySec, p.LongestXactSec)
		if p.LongestQueryText != "" {
			fmt.Fprintf(w, "  [%s]", p.LongestQueryText)
		}
		fmt.Fprintf(w, "\n  commits %d (avg %.0f/s, peak %.0f/s), rollbacks %d, deadlocks %d, temp files %d (%d bytes), min cache hit ratio %.3f\n",
			p.Commits, p.CommitsPerSecAvg, p.CommitsPerSecPeak, p.Rollbacks, p.Deadlocks, p.TempFiles, p.TempBytes, p.CacheHitRatioMin)
		fmt.Fprintf(w, "  tuples inserted %d, updated %d, deleted %d\n", p.TuplesInserted, p.TuplesUpdated, p.TuplesDeleted)
	}

	fmt.Fprintln(w, "\nINVARIANTS")
	for _, inv := range r.Invariants {
		fmt.Fprintf(w, "  [%-7s] %s", strings.ToUpper(inv.Status), inv.Name)
		if inv.Detail != "" {
			fmt.Fprintf(w, ": %s", inv.Detail)
		}
		fmt.Fprintln(w)
	}
	for _, v := range r.Violations {
		fmt.Fprintf(w, "  violation %s: %s\n", v.Kind, v.Detail)
	}

	fmt.Fprintln(w)
	if r.Verdict.Pass {
		fmt.Fprintln(w, "VERDICT  PASS (no 5xx, no timeouts, all invariants hold)")
	} else {
		fmt.Fprintln(w, "VERDICT  FAIL")
		for _, f := range r.Verdict.Failures {
			fmt.Fprintf(w, "  - %s\n", f)
		}
	}
	for _, wn := range r.Verdict.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", wn)
	}
	var sat []string
	for _, s := range r.Stages {
		if s.Saturated {
			sat = append(sat, s.Name)
		}
	}
	if len(sat) > 0 {
		fmt.Fprintf(w, "  saturation first seen in stage %s\n", sat[0])
	}
}

func fmtClasses(m map[string]uint64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
