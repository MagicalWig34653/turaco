package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
)

// ErrSyncAlreadyRunning is returned when another run for the provider is in
// progress. It is retryable.
var ErrSyncAlreadyRunning = errors.New("organization: directory sync already running")

// ErrInvalidSnapshot is returned when a source delivered a snapshot that
// cannot be applied as a whole, for example with duplicate IDs. The run is
// recorded as failed and no data changes. Callers treat it as permanent.
var ErrInvalidSnapshot = errors.New("organization: invalid directory snapshot")

// ErrProviderKeyChanged is returned when the configured provider key has no
// external identities but another directory provider's do: re-keying a
// directory is not supported because it would orphan every identity. The run
// is recorded as failed. Callers treat it as permanent.
var ErrProviderKeyChanged = errors.New("organization: directory provider key changed; changing LDAP_PROVIDER_KEY is not supported")

// ErrInvalidRequest is returned for a source without provider key or an
// unknown trigger. No run is started. Callers treat it as permanent.
var ErrInvalidRequest = errors.New("organization: invalid directory sync request")

// Run outcomes as stored in organization.directory_sync_runs.outcome.
const (
	SyncOutcomeSucceeded     = "succeeded"
	SyncOutcomeFailed        = "failed"
	SyncOutcomeSweepWithheld = "sweep_withheld"
)

// Run conflict kinds.
const (
	ConflictEmailInUse        = "email_in_use"
	ConflictManagerUnresolved = "manager_unresolved"
	ConflictInvalidAttributes = "invalid_attributes"
)

const (
	maxStoredErrorLen         = 500
	defaultRunTimeout         = 15 * time.Minute
	maxMissingPercentCeil     = 100
	errorStorageFinishTimeout = 10 * time.Second
)

// DirectorySyncConfig configures a DirectorySync.
type DirectorySyncConfig struct {
	// MaxMissingPercent is the sweep safeguard threshold (0-100): when more
	// than 5 previously observed identities (or groups) and more than this
	// share of them are missing from the snapshot, the not-observed sweep of
	// that kind is withheld. See SweepWithheld.
	MaxMissingPercent int
	// RunTimeout bounds the fetch; a run still "running" after this long is
	// treated as abandoned by the next run. Zero means 15 minutes.
	RunTimeout time.Duration
}

// SyncRunResult summarizes one run. Counts and ConflictCount are only filled
// for runs that applied a snapshot.
type SyncRunResult struct {
	RunID         string
	Outcome       string
	Counts        map[string]int
	ConflictCount int
}

// SyncApplyInput is everything the store needs to apply one validated snapshot
// in a single transaction and finish the run.
type SyncApplyInput struct {
	RunID       string
	ProviderKey string
	// FetchedAt is when the fetch completed. The store clamps the observation
	// time to the provider's previous successful observation so a clock that
	// stepped back never moves freshness backwards.
	FetchedAt         time.Time
	MaxMissingPercent int
	Users             []SyncUser
	Groups            []SnapshotGroup
	// Now supplies the finish time, read after the data was applied.
	Now func() time.Time
}

// SyncApplyOutput is the result of a committed apply. Outcome is
// SyncOutcomeSucceeded or SyncOutcomeSweepWithheld.
type SyncApplyOutput struct {
	Outcome       string
	Counts        map[string]int
	ConflictCount int
}

// SyncRunFinish records a run that failed.
type SyncRunFinish struct {
	RunID      string
	Outcome    string // SyncOutcomeFailed
	ObservedAt *time.Time
	FinishedAt time.Time
	Error      string // sanitized, at most 500 characters
}

// DirectorySyncStore is the persistence port of DirectorySync, implemented by
// the Organization repository.
type DirectorySyncStore interface {
	// StartRun marks the provider's running runs that started before
	// abandonedBefore as failed ("abandoned") and inserts a running run linked
	// to jobID (empty when not run by a job). It returns ErrSyncAlreadyRunning
	// when another run is in progress.
	StartRun(ctx context.Context, providerKey string, trigger SyncTrigger, jobID string, startedAt, abandonedBefore time.Time) (runID string, err error)
	// ProviderKeyChanged reports whether providerKey has no external identity
	// while another directory provider's identities are still observed.
	ProviderKeyChanged(ctx context.Context, providerKey string) (bool, error)
	// ApplySnapshot applies the snapshot and finishes the run as succeeded or
	// sweep_withheld in one transaction.
	ApplySnapshot(ctx context.Context, in SyncApplyInput) (SyncApplyOutput, error)
	// FinishRun finishes a running run as failed.
	FinishRun(ctx context.Context, f SyncRunFinish) error
}

// DirectorySync is the directory synchronization use case.
type DirectorySync struct {
	store  DirectorySyncStore
	cfg    DirectorySyncConfig
	now    func() time.Time
	logger *slog.Logger
}

// NewDirectorySync creates the use case. now and logger may be nil.
func NewDirectorySync(store DirectorySyncStore, cfg DirectorySyncConfig, now func() time.Time, logger *slog.Logger) *DirectorySync {
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = defaultRunTimeout
	}
	cfg.MaxMissingPercent = min(max(cfg.MaxMissingPercent, 0), maxMissingPercentCeil)
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DirectorySync{store: store, cfg: cfg, now: now, logger: logger}
}

func (s *DirectorySync) clock() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// Run executes one synchronization of src: start run, fetch, apply, finish.
// jobID links the run to the platform job that executes it and may be empty.
//
// Errors are plain; the caller decides retry semantics. ErrInvalidRequest (no
// run was started), ErrInvalidSnapshot and ErrProviderKeyChanged (the run was
// recorded as failed) are permanent conditions; ErrSyncAlreadyRunning (no run
// was started), fetch and database errors (the run was recorded as failed)
// are retryable. A withheld sweep is not an error: the run succeeded with
// outcome sweep_withheld and the next run re-evaluates. The returned
// SyncRunResult carries RunID and Outcome whenever a run was started.
func (s *DirectorySync) Run(ctx context.Context, src DirectorySource, trigger SyncTrigger, jobID string) (SyncRunResult, error) {
	providerKey := src.ProviderKey()
	if providerKey == "" {
		return SyncRunResult{}, fmt.Errorf("%w: source has no provider key", ErrInvalidRequest)
	}
	if trigger != SyncTriggerScheduled && trigger != SyncTriggerManual {
		return SyncRunResult{}, fmt.Errorf("%w: unknown trigger %q", ErrInvalidRequest, trigger)
	}

	startedAt := s.clock()
	runID, err := s.store.StartRun(ctx, providerKey, trigger, jobID, startedAt, startedAt.Add(-s.cfg.RunTimeout))
	if err != nil {
		return SyncRunResult{}, err
	}
	res := SyncRunResult{RunID: runID, Outcome: SyncOutcomeFailed}
	log := s.logger.With("runId", runID, "providerKey", providerKey, "trigger", string(trigger))

	changed, err := s.store.ProviderKeyChanged(ctx, providerKey)
	if err != nil {
		s.finishFailed(ctx, log, runID, nil, "provider key check failed: "+sanitizeError(err))
		return res, fmt.Errorf("check provider key: %w", err)
	}
	if changed {
		s.finishFailed(ctx, log, runID, nil, ErrProviderKeyChanged.Error())
		return res, ErrProviderKeyChanged
	}

	fetchCtx, cancel := context.WithTimeout(ctx, s.cfg.RunTimeout)
	snapshot, err := src.Fetch(fetchCtx)
	cancel()
	if err != nil {
		s.finishFailed(ctx, log, runID, nil, "fetch failed: "+sanitizeError(err))
		return res, fmt.Errorf("fetch directory snapshot: %w", err)
	}

	fetchedAt := s.clock()
	users, groups, reason := prepareSnapshot(snapshot)
	if reason != "" {
		s.finishFailed(ctx, log, runID, &fetchedAt, "invalid snapshot: "+reason)
		return res, fmt.Errorf("%w: %s", ErrInvalidSnapshot, reason)
	}

	out, err := s.store.ApplySnapshot(ctx, SyncApplyInput{
		RunID: runID, ProviderKey: providerKey, FetchedAt: fetchedAt,
		MaxMissingPercent: s.cfg.MaxMissingPercent, Users: users, Groups: groups, Now: s.clock,
	})
	if err != nil {
		s.finishFailed(ctx, log, runID, &fetchedAt, "apply failed: "+sanitizeError(err))
		return res, fmt.Errorf("apply directory snapshot: %w", err)
	}

	res.Outcome = out.Outcome
	res.Counts = out.Counts
	res.ConflictCount = out.ConflictCount
	if out.Outcome == SyncOutcomeSweepWithheld {
		// Operators must look at this: the directory lost a large share of its
		// objects, or the source is misconfigured.
		log.Error("directory sync withheld not-observed sweep",
			"usersSweepWithheld", out.Counts["usersSweepWithheld"], "groupsSweepWithheld", out.Counts["groupsSweepWithheld"],
			"counts", out.Counts, "conflicts", out.ConflictCount)
		return res, nil
	}
	log.Info("directory sync succeeded", "counts", out.Counts, "conflicts", out.ConflictCount)
	return res, nil
}

func (s *DirectorySync) finishFailed(ctx context.Context, log *slog.Logger, runID string, observedAt *time.Time, msg string) {
	// The run must be recorded even when the caller's context is already
	// cancelled (for example during shutdown).
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), errorStorageFinishTimeout)
	defer cancel()
	err := s.store.FinishRun(finishCtx, SyncRunFinish{
		RunID: runID, Outcome: SyncOutcomeFailed,
		ObservedAt: observedAt, FinishedAt: s.clock(), Error: truncate(msg, maxStoredErrorLen),
	})
	if err != nil {
		log.Error("record failed directory sync run", "error", err)
	}
	log.Warn("directory sync failed", "error", msg)
}

// sanitizeError renders err for storage: control characters removed and
// truncated. Sources must not put attribute values into errors; the database
// driver's detail text is never included for server errors.
func sanitizeError(err error) string {
	var coder interface{ SQLState() string }
	if errors.As(err, &coder) {
		return "database error (SQLSTATE " + coder.SQLState() + ")"
	}
	msg := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, err.Error())
	return truncate(msg, maxStoredErrorLen)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut]
}
