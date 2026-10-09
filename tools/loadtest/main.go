// Command loadtest is a development-only open-loop load generator for the Turaco API.
//
// It signs simulated hospital users in (docs/development/simulation-hospital.md), drives a
// persona-weighted workload at a scheduled arrival rate, measures latency from the scheduled
// start, samples Postgres, verifies data invariants afterwards and can remove what it created.
// See docs/development/load-testing.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

const exitUsage = 2

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var code int
	switch cmd {
	case "run":
		code = runCmd(args)
	case "cleanup":
		code = cleanupCmd(args)
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		code = exitUsage
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `loadtest: development-only load generator for the Turaco API

  loadtest run [flags]       run a load test (default command; see -h)
  loadtest cleanup [flags]   delete the tickets and comments created by earlier runs (needs --pg-url)

Documentation: docs/development/load-testing.md
`)
}

func newTag() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		baseURL     = fs.String("base-url", "http://localhost:8080", "API origin (the Origin header of writes is derived from it)")
		profile     = fs.String("profile", "smoke", "stage profile: smoke, ramp, spike, soak")
		stagesSpec  = fs.String("stages", "", "explicit stages, e.g. ramp:50@20s,hold:50@30s,hold:2000@10s,hold:0@10s (overrides --profile)")
		rateScale   = fs.Float64("rate-scale", 1, "multiply every stage rate (for example 0.25 on a small laptop)")
		arrivals    = fs.String("arrivals", "poisson", "arrival process: poisson or uniform")
		seed        = fs.Int64("seed", time.Now().UnixNano(), "random seed (printed in the report)")
		users       = fs.Int("users", 0, "number of simulated users (0 = all 32; taken round-robin over the persona classes)")
		classes     = fs.String("classes", "", "only these persona classes: employee,firstlevel,technician,lead,viewer,admin,vendor")
		weightsSpec = fs.String("weights", "", "arrival share per class, e.g. employee=60,technician=20 (defaults: employee=50 firstlevel=14 technician=20 lead=8 viewer=3 admin=3 vendor=2)")
		simPass     = fs.String("password", simPassword, "password of the simulation logins")
		admPass     = fs.String("admin-password", adminPassword, "password of devadmin")
		maxInflight = fs.Int("max-inflight", 256, "worker goroutines: upper bound on concurrent operations")
		maxConns    = fs.Int("max-conns", 128, "upper bound on HTTP connections to the API (connection pool)")
		queueSize   = fs.Int("queue", 20000, "arrivals waiting for a free worker; beyond this they are dropped and counted")
		timeout     = fs.Duration("timeout", 15*time.Second, "per-request timeout")
		drain       = fs.Duration("drain", 30*time.Second, "how long to wait for unfinished work after the last arrival")
		seedTickets = fs.Int("seed-tickets", 60, "tickets raised before the run so that staff have work")
		maxTickets  = fs.Int("max-tickets", 20000, "upper bound on tickets created during the run")
		hotTickets  = fs.Int("hot-tickets", 50, "size of the hot set (newest tickets) that staff concentrate on")
		hotProb     = fs.Float64("hot-prob", 0.5, "probability that staff work a ticket of the hot set (contention)")
		maxRetries  = fs.Int("max-retries", 2, "retries of a write after a version conflict (read again, then write again)")
		tag         = fs.String("tag", "", "correlation tag embedded in everything created (default: random)")
		pgURL       = fs.String("pg-url", "", "Postgres URL: sample pg_stat_activity/pg_stat_database and verify invariants in SQL")
		pgInterval  = fs.Duration("pg-interval", 2*time.Second, "Postgres sampling interval")
		settle      = fs.Duration("settle", 16*time.Second, "wait before comparing queue counts (System View counts are cached for 15 s)")
		sloP99      = fs.Duration("slo-p99", time.Second, "p99 latency above which a stage counts as saturated")
		outDir      = fs.String("out", "tmp/loadtest", "directory for the JSON and text report")
		noInv       = fs.Bool("no-invariants", false, "skip the post-run invariant checks")
		cleanup     = fs.Bool("cleanup", false, "delete the tagged data after the run (needs --pg-url)")
		noFail      = fs.Bool("no-fail", false, "exit 0 even when the verdict is FAIL")
		iKnow       = fs.Bool("i-know", false, "override the development-only safety checks (never use against shared or production systems)")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: loadtest run [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *tag == "" {
		*tag = newTag()
	}
	if !validTag(*tag) {
		return usageErr("--tag may only contain lower-case letters, digits and dashes")
	}
	spec := *stagesSpec
	if spec == "" {
		p, err := Profile(*profile)
		if err != nil {
			return usageErr(err.Error())
		}
		spec = p
	}
	stages, err := ParseStages(spec)
	if err != nil {
		return usageErr(err.Error())
	}
	if *rateScale <= 0 {
		return usageErr("--rate-scale must be positive")
	}
	for i := range stages {
		stages[i].Rate *= *rateScale
	}
	plan := NewPlan(stages)
	weights, err := ParseWeights(*weightsSpec)
	if err != nil {
		return usageErr(err.Error())
	}
	classSet, err := ParseClasses(*classes)
	if err != nil {
		return usageErr(err.Error())
	}
	if *arrivals != "poisson" && *arrivals != "uniform" {
		return usageErr("--arrivals must be poisson or uniform")
	}
	if *maxInflight < 1 || *maxConns < 1 || *queueSize < 1 {
		return usageErr("--max-inflight, --max-conns and --queue must be at least 1")
	}
	if *cleanup && *pgURL == "" {
		return usageErr("--cleanup needs --pg-url")
	}
	if err := checkLocal(*baseURL, *pgURL, *iKnow); err != nil {
		fmt.Fprintln(os.Stderr, "refusing to run:", err)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rec := NewRecorder()
	client, err := NewClient(*baseURL, *maxConns, *timeout, rec)
	if err != nil {
		return usageErr(err.Error())
	}
	defer client.Close()

	var meta struct {
		Environment string `json:"environment"`
	}
	if err := client.RawGet(ctx, "/meta", &meta); err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the API:", err)
		return 1
	}
	if !devLike(meta.Environment) {
		if !*iKnow {
			fmt.Fprintf(os.Stderr, "refusing to run: the API reports environment %q, not a development one (--i-know overrides)\n", meta.Environment)
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "WARNING: running against environment %q because of --i-know\n", meta.Environment)
	}

	roster := SelectPersonas(Roster(*admPass), classSet, *users)
	for i := range roster {
		if roster[i].Login != adminLogin {
			roster[i].Password = *simPass
		}
	}
	if len(roster) == 0 {
		return usageErr("no personas selected")
	}
	env := &Env{
		Cfg:      Config{Tag: *tag, MaxRetries: *maxRetries, HotTickets: *hotTickets, HotProb: *hotProb, MaxTickets: *maxTickets, SeedTickets: *seedTickets},
		Client:   client,
		Pool:     NewPool(),
		Trk:      NewTracker(),
		Seen:     newIDRing(2000),
		Sessions: map[string][]*Session{},
	}
	var logins []string
	for _, p := range roster {
		env.Sessions[p.Class] = append(env.Sessions[p.Class], &Session{P: p})
		logins = append(logins, p.Login)
	}

	fmt.Fprintf(os.Stderr, "loadtest tag=%s base=%s env=%s users=%d seed=%d\nstages: %s\n", *tag, *baseURL, meta.Environment, len(roster), *seed, describeStages(plan))
	fmt.Fprintf(os.Stderr, "signing in %d users ...\n", len(roster))
	if err := loginAll(ctx, env); err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		return 1
	}

	var sampler *PGSampler
	if *pgURL != "" {
		sampler, err = StartPGSampler(ctx, *pgURL, *pgInterval)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Postgres sampling disabled:", err)
		}
	}

	fmt.Fprintf(os.Stderr, "seeding %d tickets ...\n", *seedTickets)
	if err := Seed(ctx, env, *seedTickets); err != nil {
		fmt.Fprintln(os.Stderr, "seeding interrupted:", err)
	}
	fmt.Fprintf(os.Stderr, "running %s (expected %.0f operations) ...\n", plan.Duration(), plan.Expected())
	res := Run(ctx, env, rec, RunOptions{Plan: plan, Poisson: *arrivals == "poisson", Seed: *seed, MaxInflight: *maxInflight, QueueSize: *queueSize, Drain: *drain, Weights: weights}, os.Stderr)

	var pg *PGSummary
	if sampler != nil {
		pg = sampler.Stop(context.Background())
	}

	// Invariants use a fresh context: an interrupted run is still checked.
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelCheck()
	var inv []InvariantResult
	if !*noInv {
		fmt.Fprintln(os.Stderr, "checking invariants ...")
		inv = CheckInvariants(checkCtx, InvariantInput{Env: env, Rec: rec, PGURL: *pgURL, Settle: *settle})
	}

	logs, relogs := client.LoginCounts()
	rep := BuildReport(ReportInput{
		Tag: *tag, BaseURL: *baseURL, ServerEnv: meta.Environment, Plan: plan, Users: logins, Rec: rec, Run: res, Env: env, PG: pg,
		Invariants: inv, SLOP99: *sloP99, Logins: logs, Relogins: relogs,
		Config: map[string]any{
			"stages": spec, "rateScale": *rateScale, "arrivals": *arrivals, "seed": *seed, "weights": weights, "maxInflight": *maxInflight,
			"maxConns": *maxConns, "queue": *queueSize, "timeoutMs": timeout.Milliseconds(), "seedTickets": *seedTickets, "maxTickets": *maxTickets,
			"hotTickets": *hotTickets, "hotProb": *hotProb, "maxRetries": *maxRetries, "users": len(roster), "classes": *classes,
		},
	})
	rep.WriteText(os.Stdout)

	if path, err := writeReport(rep, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write report:", err)
	} else {
		fmt.Fprintf(os.Stdout, "\nReports written: %s.json and %s.txt\n", path, path)
	}

	if *cleanup {
		cr, err := Cleanup(context.Background(), *pgURL, *tag, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cleanup failed:", err)
		} else {
			fmt.Fprintf(os.Stdout, "Cleanup: deleted %d tickets and %d comments tagged %s\n", cr.Tickets, cr.Comments, ticketPrefix(*tag))
		}
	} else if *pgURL != "" {
		fmt.Fprintf(os.Stdout, "Created data is tagged %s; remove it with: loadtest cleanup --pg-url ... --tag %s\n", ticketPrefix(*tag), *tag)
	} else {
		fmt.Fprintf(os.Stdout, "Created data is tagged %s (titles and comments); remove it with 'loadtest cleanup --pg-url ... --tag %s'.\n", ticketPrefix(*tag), *tag)
	}

	if !rep.Verdict.Pass && !*noFail {
		return 1
	}
	return 0
}

func cleanupCmd(args []string) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	pgURL := fs.String("pg-url", "", "Postgres URL of the development database (required)")
	tag := fs.String("tag", "", "tag of the run to delete")
	all := fs.Bool("all", false, "delete the data of every load-test tag")
	dry := fs.Bool("dry-run", false, "only count")
	iKnow := fs.Bool("i-know", false, "override the localhost check of --pg-url")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *pgURL == "" {
		return usageErr("cleanup needs --pg-url")
	}
	if (*tag == "") == !*all {
		return usageErr("give exactly one of --tag or --all")
	}
	if *tag != "" && !validTag(*tag) {
		return usageErr("invalid --tag")
	}
	if env := os.Getenv("APP_ENV"); env != "" && !devLike(env) && !*iKnow {
		fmt.Fprintf(os.Stderr, "refusing to run: APP_ENV=%q is not a development environment (--i-know overrides)\n", env)
		return exitUsage
	}
	host, err := PGHost(*pgURL)
	if err != nil {
		return usageErr(err.Error())
	}
	if !isLoopback(host) && !*iKnow {
		fmt.Fprintf(os.Stderr, "refusing to run: Postgres host %q is not local (--i-know overrides)\n", host)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := Cleanup(ctx, *pgURL, *tag, *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cleanup failed:", err)
		return 1
	}
	verb := "deleted"
	if res.DryRun {
		verb = "would delete"
	}
	fmt.Printf("%s %d tickets and %d comments\n", verb, res.Tickets, res.Comments)
	return 0
}

func usageErr(msg string) int {
	fmt.Fprintln(os.Stderr, "error:", msg)
	return exitUsage
}

func validTag(t string) bool {
	if t == "" || len(t) > 40 {
		return false
	}
	for _, c := range t {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func devLike(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "development", "dev", "local", "test", "testing":
		return true
	}
	return false
}

func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true // an empty host means the local Unix socket
	}
	if strings.HasPrefix(h, "/") {
		return true // Unix socket directory
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// checkLocal enforces the development-only rules before anything is sent.
func checkLocal(baseURL, pgURL string, iKnow bool) error {
	if env := os.Getenv("APP_ENV"); env != "" && !devLike(env) && !iKnow {
		return fmt.Errorf("APP_ENV=%q is not a development environment (--i-know overrides)", env)
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid --base-url %q", baseURL)
	}
	if !isLoopback(u.Hostname()) && !iKnow {
		return fmt.Errorf("--base-url host %q is not localhost; this tool creates thousands of tickets and logs in with a public password (--i-know overrides)", u.Hostname())
	}
	if pgURL != "" {
		host, err := PGHost(pgURL)
		if err != nil {
			return err
		}
		if !isLoopback(host) && !iKnow {
			return fmt.Errorf("--pg-url host %q is not local (--i-know overrides)", host)
		}
	}
	return nil
}

func loginAll(ctx context.Context, env *Env) error {
	var all []*Session
	for _, ss := range env.Sessions {
		all = append(all, ss...)
	}
	errs := make(chan error, len(all))
	for _, s := range all {
		go func(s *Session) { errs <- env.Client.Login(ctx, s, false) }(s)
	}
	var first error
	for range all {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

func describeStages(p *Plan) string {
	var parts []string
	for _, s := range p.Stages {
		parts = append(parts, fmt.Sprintf("%s:%.0f@%s", s.Kind, s.Rate, s.Duration))
	}
	return strings.Join(parts, ", ")
}

func writeReport(rep *Report, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, fmt.Sprintf("loadtest-%s-%s", rep.StartedAt.Format("20060102-150405"), rep.Tag))
	jf, err := os.Create(base + ".json")
	if err != nil {
		return "", err
	}
	defer jf.Close()
	if err := rep.WriteJSON(jf); err != nil {
		return "", err
	}
	tf, err := os.Create(base + ".txt")
	if err != nil {
		return "", err
	}
	defer tf.Close()
	rep.WriteText(tf)
	return base, nil
}
