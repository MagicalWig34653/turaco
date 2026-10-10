// Package health is the platform health contract: named checks that read stored facts (database rows, job tables,
// configuration, module state) and report one of a fixed vocabulary of states. It never probes the outside world;
// connectivity probes are a separate, explicit action. Results never contain secrets or configuration values, only
// configuration key names in NextStep.
package health

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status is the state of one check.
type Status string

const (
	StatusOK            Status = "ok"             // configured, real, recent success
	StatusStale         Status = "stale"          // configured, last success older than three intervals
	StatusFake          Status = "fake"           // a fake or in-memory adapter is wired
	StatusNotConfigured Status = "not_configured" // required configuration is missing
	StatusDisabled      Status = "disabled"       // switched off on purpose
	StatusFailing       Status = "failing"        // the last attempt failed
	StatusUnknown       Status = "unknown"        // no observation yet
)

// Valid reports whether s is part of the vocabulary.
func (s Status) Valid() bool {
	switch s {
	case StatusOK, StatusStale, StatusFake, StatusNotConfigured, StatusDisabled, StatusFailing, StatusUnknown:
		return true
	}
	return false
}

// Attention reports whether the state needs an administrator's attention.
func (s Status) Attention() bool {
	return s == StatusFailing || s == StatusStale || s == StatusNotConfigured
}

// Mode says what kind of adapter is behind an integration.
type Mode string

const (
	ModeReal          Mode = "real"
	ModeFake          Mode = "fake"
	ModeNotConfigured Mode = "not_configured"
)

// NextStep tells the administrator what to do: open a screen, set configuration keys (names only) or read a guide.
type NextStep struct {
	Kind       string   `json:"kind"` // route | config | docs
	Route      string   `json:"route,omitempty"`
	ConfigKeys []string `json:"configKeys,omitempty"`
	DocsPath   string   `json:"docsPath,omitempty"`
}

// Result is what a check reports. ErrorCode is a short machine code, never raw error text.
type Result struct {
	Status        Status         `json:"status"`
	Mode          Mode           `json:"mode,omitempty"`
	ObservedAt    time.Time      `json:"observedAt"`
	LastSuccessAt *time.Time     `json:"lastSuccessAt,omitempty"`
	LastAttemptAt *time.Time     `json:"lastAttemptAt,omitempty"`
	ErrorCode     string         `json:"errorCode,omitempty"`
	Counts        map[string]int `json:"counts,omitempty"`
	Detail        map[string]any `json:"detail,omitempty"`
	NextStep      *NextStep      `json:"nextStep,omitempty"`
}

// Category groups checks for the pages that show them.
type Category string

const (
	CategorySystem      Category = "system"
	CategoryIntegration Category = "integration"
	CategoryModule      Category = "module"
)

// Check is one named health check. Run must read stored facts only and honour ctx.
type Check struct {
	Key      string
	Category Category
	// Intervals is the usual period of the thing observed; StaleAfter helpers use it (3 x interval).
	Run func(ctx context.Context) Result
}

// Entry is a check with its result.
type Entry struct {
	Key      string   `json:"key"`
	Category Category `json:"category"`
	Result
}

// Registry holds the checks and caches their results for a few seconds.
type Registry struct {
	mu      sync.Mutex
	checks  []Check
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time
	cached  []Entry
	cachedA time.Time
}

// NewRegistry returns a registry with a 5 second cache and a 3 second timeout per check.
func NewRegistry() *Registry {
	return &Registry{ttl: 5 * time.Second, timeout: 3 * time.Second, now: time.Now}
}

// Register adds a check; keys must be unique.
func (r *Registry) Register(c Check) error {
	if c.Key == "" || c.Run == nil {
		return fmt.Errorf("health: a check needs a key and a Run function")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.checks {
		if e.Key == c.Key {
			return fmt.Errorf("health: duplicate check %q", c.Key)
		}
	}
	r.checks = append(r.checks, c)
	r.cached = nil
	return nil
}

// All runs every check (or serves the cached run) and returns the entries sorted by category and key.
func (r *Registry) All(ctx context.Context) []Entry {
	r.mu.Lock()
	if r.cached != nil && r.now().Sub(r.cachedA) < r.ttl {
		out := append([]Entry(nil), r.cached...)
		r.mu.Unlock()
		return out
	}
	checks := append([]Check(nil), r.checks...)
	r.mu.Unlock()

	entries := make([]Entry, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			entries[i] = Entry{Key: c.Key, Category: c.Category, Result: r.run(ctx, c)}
		}(i, c)
	}
	wg.Wait()
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Category != entries[j].Category {
			return entries[i].Category < entries[j].Category
		}
		return entries[i].Key < entries[j].Key
	})
	r.mu.Lock()
	r.cached, r.cachedA = entries, r.now()
	r.mu.Unlock()
	return append([]Entry(nil), entries...)
}

// Get returns one entry by key.
func (r *Registry) Get(ctx context.Context, key string) (Entry, bool) {
	for _, e := range r.All(ctx) {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// run executes one check with a timeout; a panic or an invalid status becomes "failing".
func (r *Registry) run(ctx context.Context, c Check) (res Result) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			res = Result{Status: StatusFailing, ErrorCode: "check_panic", ObservedAt: r.now()}
		}
	}()
	res = c.Run(cctx)
	if !res.Status.Valid() {
		res = Result{Status: StatusFailing, ErrorCode: "invalid_status"}
	}
	if cctx.Err() != nil && res.Status == StatusOK {
		res.Status, res.ErrorCode = StatusFailing, "check_timeout"
	}
	res.ObservedAt = r.now().UTC()
	return res
}

// FreshStatus classifies a recurring success: ok when younger than three intervals, stale when older, unknown
// when there is none. A last failure newer than the last success wins as failing.
func FreshStatus(now time.Time, interval time.Duration, lastSuccess, lastFailure *time.Time) Status {
	if lastFailure != nil && (lastSuccess == nil || lastFailure.After(*lastSuccess)) {
		return StatusFailing
	}
	if lastSuccess == nil {
		return StatusUnknown
	}
	if now.Sub(*lastSuccess) > 3*interval {
		return StatusStale
	}
	return StatusOK
}
