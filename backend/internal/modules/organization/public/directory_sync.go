package public

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// ErrInvalidSnapshot is returned (permanently) when a source delivered a
// snapshot that cannot be applied, for example with duplicate IDs. The run is
// recorded as failed and no data changes.
var ErrInvalidSnapshot = errors.New("organization: invalid directory snapshot")

// Run outcomes as stored in organization.directory_sync_runs.outcome.
const (
	SyncOutcomeSucceeded        = "succeeded"
	SyncOutcomeFailed           = "failed"
	SyncOutcomeAbortedSafeguard = "aborted_safeguard"

	maxStoredErrorLen          = 500
	defaultRunTimeout          = 15 * time.Minute
	maxDeactivationPercentCeil = 100
)

// DirectorySyncConfig configures a DirectorySync.
type DirectorySyncConfig struct {
	// MaxDeactivationPercent is the safeguard threshold (0-100): a run that
	// would deactivate more than this share of the provider's active directory
	// identities, and more than 5 of them, is aborted without changes.
	MaxDeactivationPercent int
	// RunTimeout bounds the fetch; a run still "running" after this long is
	// treated as abandoned by the next run. Zero means 15 minutes.
	RunTimeout time.Duration
}

// SyncRunResult summarizes one run. Counts and ConflictCount are only filled
// for succeeded runs.
type SyncRunResult struct {
	RunID         string
	Outcome       string
	Counts        map[string]int
	ConflictCount int
}

// SyncUser is a normalized snapshot user together with its attributes hash.
type SyncUser struct {
	DirectoryUser
	AttributesHash string
}

// SyncApplyInput is everything the store needs to apply one validated snapshot
// in a single transaction and finish the run.
type SyncApplyInput struct {
	RunID                  string
	ProviderKey            string
	ObservedAt             time.Time
	FinishedAt             time.Time
	MaxDeactivationPercent int
	Users                  []SyncUser
	Groups                 []DirectoryGroup
}

// SyncApplyOutput is the result of a committed apply.
type SyncApplyOutput struct {
	Counts        map[string]int
	ConflictCount int
}

// SyncRunFinish records a run that did not succeed. Outcome is
// SyncOutcomeFailed or SyncOutcomeAbortedSafeguard. For aborted runs the store
// also writes the audit event in the same transaction.
type SyncRunFinish struct {
	RunID       string
	ProviderKey string
	Outcome     string
	ObservedAt  *time.Time
	FinishedAt  time.Time
	Error       string // sanitized, at most 500 characters
	Safeguard   *SafeguardError
}

// SafeguardError reports an aborted run. It matches ErrSyncSafeguard with
// errors.Is.
type SafeguardError struct {
	ActiveBefore  int
	Deactivations int
	MaxPercent    int
}

func (e *SafeguardError) Error() string {
	return fmt.Sprintf("%s: would deactivate %d of %d active identities (limit %d%%)",
		ErrSyncSafeguard.Error(), e.Deactivations, e.ActiveBefore, e.MaxPercent)
}

func (e *SafeguardError) Is(target error) bool { return target == ErrSyncSafeguard }

// DirectorySyncStore is the persistence port of DirectorySync, implemented by
// the Organization repository.
type DirectorySyncStore interface {
	// StartRun marks the provider's running runs that started before
	// abandonedBefore as failed ("abandoned") and inserts a running run. It
	// returns ErrSyncAlreadyRunning when another run is in progress.
	StartRun(ctx context.Context, providerKey string, trigger SyncTrigger, startedAt, abandonedBefore time.Time) (runID string, err error)
	// ApplySnapshot applies the snapshot and finishes the run as succeeded in
	// one transaction. It returns a *SafeguardError (and changes nothing) when
	// the deactivation safeguard trips.
	ApplySnapshot(ctx context.Context, in SyncApplyInput) (SyncApplyOutput, error)
	// FinishRun finishes a running run as failed or aborted.
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
	if cfg.MaxDeactivationPercent < 0 {
		cfg.MaxDeactivationPercent = 0
	}
	if cfg.MaxDeactivationPercent > maxDeactivationPercentCeil {
		cfg.MaxDeactivationPercent = maxDeactivationPercentCeil
	}
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
//
// Errors: ErrSyncAlreadyRunning (no run was created); a fetch or database
// error (retryable; the run is recorded as failed); a jobs.Permanent error
// wrapping ErrInvalidSnapshot or ErrSyncSafeguard (not retryable). The
// returned SyncRunResult carries RunID and Outcome whenever a run was started.
func (s *DirectorySync) Run(ctx context.Context, src DirectorySource, trigger SyncTrigger) (SyncRunResult, error) {
	providerKey := src.ProviderKey()
	if providerKey == "" {
		return SyncRunResult{}, jobs.Permanent(errors.New("organization: directory source has no provider key"))
	}
	if trigger != SyncTriggerScheduled && trigger != SyncTriggerManual {
		return SyncRunResult{}, jobs.Permanent(fmt.Errorf("organization: unknown sync trigger %q", trigger))
	}

	startedAt := s.clock()
	runID, err := s.store.StartRun(ctx, providerKey, trigger, startedAt, startedAt.Add(-s.cfg.RunTimeout))
	if err != nil {
		return SyncRunResult{}, err
	}
	res := SyncRunResult{RunID: runID, Outcome: SyncOutcomeFailed}
	log := s.logger.With("runId", runID, "providerKey", providerKey, "trigger", string(trigger))

	fetchCtx, cancel := context.WithTimeout(ctx, s.cfg.RunTimeout)
	snapshot, err := src.Fetch(fetchCtx)
	cancel()
	if err != nil {
		s.finishFailed(ctx, log, runID, providerKey, nil, "fetch failed: "+sanitizeError(err))
		return res, fmt.Errorf("fetch directory snapshot: %w", err)
	}

	observedAt := s.clock()
	users, reason := prepareSnapshot(snapshot)
	if reason != "" {
		s.finishFailed(ctx, log, runID, providerKey, &observedAt, "invalid snapshot: "+reason)
		return res, jobs.Permanent(fmt.Errorf("%w: %s", ErrInvalidSnapshot, reason))
	}

	out, err := s.store.ApplySnapshot(ctx, SyncApplyInput{
		RunID: runID, ProviderKey: providerKey, ObservedAt: observedAt, FinishedAt: s.clock(),
		MaxDeactivationPercent: s.cfg.MaxDeactivationPercent, Users: users, Groups: snapshot.Groups,
	})
	var guard *SafeguardError
	switch {
	case errors.As(err, &guard):
		res.Outcome = SyncOutcomeAbortedSafeguard
		ferr := s.finish(ctx, SyncRunFinish{
			RunID: runID, ProviderKey: providerKey, Outcome: SyncOutcomeAbortedSafeguard,
			ObservedAt: &observedAt, FinishedAt: s.clock(), Error: guard.Error(), Safeguard: guard,
		})
		if ferr != nil {
			log.Error("record aborted directory sync run", "error", ferr)
		}
		log.Warn("directory sync aborted by deactivation safeguard", "activeBefore", guard.ActiveBefore, "deactivations", guard.Deactivations)
		return res, jobs.Permanent(err)
	case err != nil:
		s.finishFailed(ctx, log, runID, providerKey, &observedAt, "apply failed: "+sanitizeError(err))
		return res, fmt.Errorf("apply directory snapshot: %w", err)
	}

	res.Outcome = SyncOutcomeSucceeded
	res.Counts = out.Counts
	res.ConflictCount = out.ConflictCount
	log.Info("directory sync succeeded", "counts", out.Counts, "conflicts", out.ConflictCount)
	return res, nil
}

func (s *DirectorySync) finishFailed(ctx context.Context, log *slog.Logger, runID, providerKey string, observedAt *time.Time, msg string) {
	err := s.finish(ctx, SyncRunFinish{
		RunID: runID, ProviderKey: providerKey, Outcome: SyncOutcomeFailed,
		ObservedAt: observedAt, FinishedAt: s.clock(), Error: truncate(msg, maxStoredErrorLen),
	})
	if err != nil {
		log.Error("record failed directory sync run", "error", err)
	}
	log.Warn("directory sync failed", "error", msg)
}

// finish records the end of a run even when the caller's context is already
// cancelled (for example during shutdown).
func (s *DirectorySync) finish(ctx context.Context, f SyncRunFinish) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.store.FinishRun(finishCtx, f)
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

// prepareSnapshot validates the snapshot, normalizes optional strings (trim,
// empty means absent) and computes attributes hashes. A non-empty reason means
// the snapshot is invalid; reasons carry indexes, never attribute values.
func prepareSnapshot(snap DirectorySnapshot) ([]SyncUser, string) {
	if len(snap.Users) == 0 {
		return nil, "snapshot contains no users"
	}
	ids := make(map[string]struct{}, len(snap.Users)+len(snap.Groups))
	users := make([]SyncUser, 0, len(snap.Users))
	for i, u := range snap.Users {
		if u.ExternalID == "" {
			return nil, "users[" + strconv.Itoa(i) + ": " + "empty external id"
		}
		if _, dup := ids[u.ExternalID]; dup {
			return nil, "users[" + strconv.Itoa(i) + ": " + "external id used twice"
		}
		ids[u.ExternalID] = struct{}{}
		u.DisplayName = strings.TrimSpace(u.DisplayName)
		if u.DisplayName == "" {
			return nil, "users[" + strconv.Itoa(i) + ": " + "empty display name"
		}
		u.Username = strings.TrimSpace(u.Username)
		u.GivenName = normalizeOptional(u.GivenName)
		u.FamilyName = normalizeOptional(u.FamilyName)
		u.Email = normalizeOptional(u.Email)
		u.EmployeeNumber = normalizeOptional(u.EmployeeNumber)
		u.ManagerExternalID = normalizeOptional(u.ManagerExternalID)
		users = append(users, SyncUser{DirectoryUser: u, AttributesHash: attributesHash(u)})
	}
	for i, g := range snap.Groups {
		if g.ExternalID == "" {
			return nil, "groups[" + strconv.Itoa(i) + ": " + "empty external id"
		}
		if _, dup := ids[g.ExternalID]; dup {
			return nil, "groups[" + strconv.Itoa(i) + ": " + "external id used twice"
		}
		ids[g.ExternalID] = struct{}{}
		if strings.TrimSpace(g.DisplayName) == "" {
			return nil, "groups[" + strconv.Itoa(i) + ": " + "empty display name"
		}
	}
	return users, ""
}

func normalizeOptional(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

// attributesHash is SHA-256 over a length-prefixed canonical encoding of every
// directory-owned field except the manager reference, which is resolved
// separately on every run.
func attributesHash(u DirectoryUser) string {
	h := sha256.New()
	str := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	opt := func(p *string) {
		if p == nil {
			h.Write([]byte("-"))
			return
		}
		h.Write([]byte("+"))
		str(*p)
	}
	str(u.ExternalID)
	str(u.Username)
	str(u.DistinguishedName)
	str(u.DisplayName)
	opt(u.GivenName)
	opt(u.FamilyName)
	opt(u.Email)
	opt(u.EmployeeNumber)
	if u.Enabled {
		h.Write([]byte("E"))
	} else {
		h.Write([]byte("D"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
