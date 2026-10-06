package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Advisory feed synchronization (docs/integrations/advisory-feeds.md). The job reads the configured feeds
// through the advisories port and imports through the regular Import path as the system actor
// "advisory-feed", so idempotency, validation, re-analysis and audit rules are the import's. The CISA KEV
// feed only enriches existing advisories (known_exploited and dates); it never creates one.
const (
	AdvisorySyncJobType = "security.advisory_sync"
	// AdvisorySyncJobTimeout covers a run: NVD is rate limited to 5 requests per 30 s without a key.
	AdvisorySyncJobTimeout = 45 * time.Minute

	FeedNVD     = "nvd"
	FeedCISAKEV = "cisa_kev"

	feedActor = "advisory-feed"
	// feedLease is the lease of one source; it expires on its own when a worker dies.
	feedLease = AdvisorySyncJobTimeout + 5*time.Minute
	// DefaultFeedStart is how far back the first NVD run reads.
	DefaultFeedStart = 30 * 24 * time.Hour
	// DefaultKEVFetchPerRun bounds the NVD by-id requests that create advisories for KEV CVEs per run.
	DefaultKEVFetchPerRun = 50
	// FeedOverlap is how far before the cursor a run starts reading again; imports are idempotent, so the
	// overlap only protects against records NVD publishes late with an older modification time.
	FeedOverlap = 2 * time.Hour
	// KEVMissRetry is how long a KEV CVE that NVD could not provide is skipped by the by-id fetch, so
	// unfetchable ids cannot starve the others.
	KEVMissRetry = 7 * 24 * time.Hour
	// Plausibility bounds of the KEV catalog: more entries than KEVMaxEntries or than the last accepted
	// catalog plus KEVMaxGrowth, or more than KEVMaxNewlyFlagged newly flagged advisories in one run,
	// abort the enrichment (FeedErrKEVImplausible).
	KEVMaxEntries      = 5000
	KEVMaxGrowth       = 200
	KEVMaxNewlyFlagged = 500
	// FeedStaleAfter is the age of the last success after which the feed counts as stale in the briefing.
	FeedStaleAfter = 48 * time.Hour
)

// FeedSources are the configured feeds; a nil source is not configured.
type FeedSources struct {
	NVD advisories.Syncer
	KEV advisories.KEVSource
	// KEVFetchPerRun bounds the by-id fetches for KEV CVEs without an advisory (default DefaultKEVFetchPerRun).
	KEVFetchPerRun int
}

// FeedSourceKeys lists the feeds in run order (KEV after NVD so fresh CVEs can be enriched at once).
func (f FeedSources) FeedSourceKeys() []string {
	var out []string
	if f.NVD != nil {
		out = append(out, FeedNVD)
	}
	if f.KEV != nil {
		out = append(out, FeedCISAKEV)
	}
	return out
}

// WithFeeds configures the advisory feeds of the sync job and the admin command.
func (s *Service) WithFeeds(f FeedSources) *Service {
	if f.KEVFetchPerRun <= 0 {
		f.KEVFetchPerRun = DefaultKEVFetchPerRun
	}
	s.feeds = f
	return s
}

// FeedState is the persisted state of one feed source. LastError is a constant code, never an error text.
type FeedState struct {
	Source        string
	Cursor        string
	ETag          string
	LastSuccessAt *time.Time
	LastAttemptAt *time.Time
	LastError     string
	// LeaseToken identifies the run that claimed the source; only that run may finish it.
	LeaseToken string
	// KEVMiss maps KEV CVE ids NVD could not provide to the time of the failed fetch; KEVCount is the size
	// of the last accepted KEV catalog.
	KEVMiss  map[string]time.Time
	KEVCount int
}

// ErrFeedLeaseLost is returned by FinishFeed when another run took the source over (the lease of the
// finishing run had expired); the late run records nothing.
var ErrFeedLeaseLost = errors.New("security: feed lease lost")

// FeedFinish is the outcome a run records. A nil Cursor or ETag keeps the stored value; an empty ETag clears it.
type FeedFinish struct {
	Cursor    *string
	ETag      *string
	Success   bool
	ErrorCode string
	// KEVMiss and KEVCount replace the stored KEV state when set.
	KEVMiss  *map[string]time.Time
	KEVCount *int
}

// KEVEntry is one Known Exploited Vulnerabilities enrichment to apply.
type KEVEntry struct {
	CVEID   string
	AddedAt *time.Time
	DueDate *time.Time
}

type feedStore interface {
	ClaimFeed(ctx context.Context, source string, lease time.Duration) (FeedState, bool, error)
	FinishFeed(ctx context.Context, source, leaseToken string, f FeedFinish) error
	FeedStates(ctx context.Context) ([]FeedState, error)
	MissingExternalIDs(ctx context.Context, source string, ids []string) ([]string, error)
	ApplyKEVTx(ctx context.Context, tx pgx.Tx, entries []KEVEntry) ([]string, error)
}

// Error codes of FeedState.LastError.
const (
	FeedErrRateLimited = "rate_limited"
	FeedErrUnavailable = "unavailable"
	FeedErrInvalid     = "invalid_response"
	FeedErrTimeout     = "timeout"
	FeedErrImport      = "import_failed"
	FeedErrFailed      = "failed"
	// FeedErrKEVImplausible aborts a KEV enrichment whose catalog or effect is implausibly large.
	FeedErrKEVImplausible = "kev_catalog_implausible"
)

func feedErrorCode(err error) string {
	switch {
	case errors.Is(err, advisories.ErrRateLimited):
		return FeedErrRateLimited
	case errors.Is(err, advisories.ErrInvalidResponse):
		return FeedErrInvalid
	case errors.Is(err, context.DeadlineExceeded):
		return FeedErrTimeout
	case errors.Is(err, advisories.ErrUnavailable):
		return FeedErrUnavailable
	}
	return FeedErrFailed
}

// AdvisorySyncPayload is the payload of the sync job; empty means every configured source.
type AdvisorySyncPayload struct {
	Sources []string   `json:"sources,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
}

// FeedRunResult reports one source of a run.
type FeedRunResult struct {
	Source string
	// Skipped is set when another run holds the source.
	Skipped bool
	// Error is a constant error code ("" on success).
	Error    string
	Fetched  int
	Import   ImportResult
	Complete bool
	// KEVEnriched counts advisories newly marked known exploited; KEVMissing counts KEV CVEs that still
	// have no advisory after the bounded by-id fetch.
	KEVEnriched int
	KEVMissing  int
}

// FeedCaller is the system actor of the feed sync.
func FeedCaller(correlationID string) Caller {
	return Caller{Actor: audit.SystemActor(feedActor), CorrelationID: correlationID}
}

// HandleAdvisorySync is the job handler of AdvisorySyncJobType. Source failures are recorded in the feed
// state (and visible in the briefing health) and do not fail the job, so a throttled feed is not retried
// in a loop; only cancellation and database errors do.
func (s *Service) HandleAdvisorySync(ctx context.Context, job jobs.Job) error {
	var p AdvisorySyncPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return jobs.Permanent(fmt.Errorf("advisory sync payload: %w", err))
		}
	}
	results, err := s.SyncFeeds(ctx, FeedCaller("job:"+job.ID), p)
	for _, r := range results {
		slog.InfoContext(ctx, "advisory feed run", "source", r.Source, "skipped", r.Skipped, "error", r.Error, "fetched", r.Fetched,
			"created", r.Import.Created, "updated", r.Import.Updated, "unchanged", r.Import.Unchanged, "rejected", r.Import.Rejected,
			"complete", r.Complete, "kev_enriched", r.KEVEnriched, "kev_missing", r.KEVMissing)
	}
	return err
}

// SyncFeeds runs the requested (default: all configured) sources one after the other. A failing source is
// recorded and does not stop the others.
func (s *Service) SyncFeeds(ctx context.Context, c Caller, p AdvisorySyncPayload) ([]FeedRunResult, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	store, ok := s.store.(feedStore)
	if !ok {
		return nil, ErrNotFound
	}
	configured := s.feeds.FeedSourceKeys()
	sources := configured
	if len(p.Sources) > 0 {
		sources = nil
		for _, k := range configured {
			if slices.Contains(p.Sources, k) {
				sources = append(sources, k)
			}
		}
		for _, k := range p.Sources {
			if !slices.Contains(configured, k) {
				return nil, invalid("the feed source %q is not configured", k)
			}
		}
	}
	var results []FeedRunResult
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		res, err := s.syncSource(ctx, store, c, src, p)
		results = append(results, res)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (s *Service) syncSource(ctx context.Context, store feedStore, c Caller, src string, p AdvisorySyncPayload) (FeedRunResult, error) {
	res := FeedRunResult{Source: src}
	st, claimed, err := store.ClaimFeed(ctx, src, feedLease)
	if err != nil {
		return res, err
	}
	if !claimed {
		res.Skipped = true
		return res, nil
	}
	var fin FeedFinish
	var runErr error
	switch src {
	case FeedNVD:
		fin, runErr = s.runNVD(ctx, c, st, p, &res)
	case FeedCISAKEV:
		fin, runErr = s.runKEV(ctx, store, c, st, &res)
	}
	if runErr != nil && ctx.Err() != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		runErr = ctx.Err()
	}
	if fin.ErrorCode == "" && runErr != nil {
		fin.ErrorCode = feedErrorCode(runErr)
	}
	fin.Success = fin.ErrorCode == ""
	res.Error = fin.ErrorCode
	// The outcome is recorded even when the run was cancelled; the lease must be released.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := store.FinishFeed(rctx, src, st.LeaseToken, fin); err != nil {
		if errors.Is(err, ErrFeedLeaseLost) {
			slog.WarnContext(ctx, "advisory feed lease lost; the late run records nothing", "source", src)
			return res, nil
		}
		return res, err
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, errFeedInternal) {
		return res, runErr
	}
	return res, nil
}

// errFeedInternal marks failures of Turaco's own storage during a run (they fail the job, unlike failures of the source).
var errFeedInternal = errors.New("security: feed storage failure")

func (s *Service) runNVD(ctx context.Context, c Caller, st FeedState, p AdvisorySyncPayload, res *FeedRunResult) (FeedFinish, error) {
	now := s.now()
	since := now.Add(-DefaultFeedStart)
	var stored time.Time
	if st.Cursor != "" {
		if t, err := time.Parse(time.RFC3339, st.Cursor); err == nil {
			stored = t
			since = t.Add(-FeedOverlap)
		}
	}
	if p.Since != nil {
		since = p.Since.UTC()
	}
	sync, syncErr := s.feeds.NVD.Sync(ctx, since)
	// The records read before an error are imported and the cursor advances to the last completely read
	// window, so a failing run keeps its progress.
	res.Fetched, res.Complete = len(sync.Records), sync.Complete && syncErr == nil
	if len(sync.Records) > 0 {
		if err := s.importRecords(ctx, c, sync.Records, &res.Import); err != nil {
			return FeedFinish{ErrorCode: FeedErrImport}, fmt.Errorf("%w: %w", errFeedInternal, err)
		}
	}
	// An explicit earlier start (backfill) never moves the cursor backwards.
	fin := FeedFinish{}
	if through := sync.Through.UTC(); !through.IsZero() && through.After(stored) {
		cursor := through.Format(time.RFC3339)
		fin.Cursor = &cursor
	}
	return fin, syncErr
}

func (s *Service) runKEV(ctx context.Context, store feedStore, c Caller, st FeedState, res *FeedRunResult) (FeedFinish, error) {
	cat, err := s.feeds.KEV.Catalog(ctx, st.ETag)
	if err != nil {
		return FeedFinish{}, err
	}
	if cat.NotModified {
		return FeedFinish{}, nil
	}
	n := len(cat.Entries)
	res.Fetched = n
	if n > KEVMaxEntries || (st.KEVCount > 0 && n > st.KEVCount+KEVMaxGrowth) {
		return FeedFinish{ErrorCode: FeedErrKEVImplausible}, nil
	}
	ids := make([]string, 0, n)
	inCatalog := make(map[string]bool, n)
	entries := make([]KEVEntry, 0, n)
	for _, e := range cat.Entries {
		ids = append(ids, e.CVEID)
		inCatalog[e.CVEID] = true
		entries = append(entries, KEVEntry{CVEID: e.CVEID, AddedAt: e.DateAdded, DueDate: e.DueDate})
	}
	// Ids NVD could not provide are skipped for KEVMissRetry; expired entries and ids that left the catalog drop out.
	now := s.now()
	miss := map[string]time.Time{}
	for id, at := range st.KEVMiss {
		if inCatalog[id] && now.Sub(at) < KEVMissRetry {
			miss[id] = at
		}
	}
	var fetchErr error
	if s.feeds.NVD != nil {
		missing, err := store.MissingExternalIDs(ctx, FeedNVD, ids)
		if err != nil {
			return FeedFinish{}, fmt.Errorf("%w: %w", errFeedInternal, err)
		}
		var candidates []string
		for _, id := range missing {
			if _, skip := miss[id]; !skip && len(candidates) < s.feeds.KEVFetchPerRun {
				candidates = append(candidates, id)
			}
		}
		if len(candidates) > 0 {
			records, err := s.feeds.NVD.ByID(ctx, candidates)
			fetchErr = err
			if len(records) > 0 {
				var imp ImportResult
				if err := s.importRecords(ctx, c, records, &imp); err != nil {
					return FeedFinish{ErrorCode: FeedErrImport}, fmt.Errorf("%w: %w", errFeedInternal, err)
				}
				res.Import = imp
			}
			if fetchErr == nil {
				// Whatever is still missing was not found or was rejected: do not ask again for a while.
				still, err := store.MissingExternalIDs(ctx, FeedNVD, candidates)
				if err != nil {
					return FeedFinish{}, fmt.Errorf("%w: %w", errFeedInternal, err)
				}
				for _, id := range still {
					miss[id] = now
				}
			}
		}
	}
	changed, err := s.applyKEV(ctx, store, c, entries)
	if errors.Is(err, errKEVImplausible) {
		return FeedFinish{ErrorCode: FeedErrKEVImplausible}, nil
	}
	if err != nil {
		return FeedFinish{ErrorCode: FeedErrImport}, fmt.Errorf("%w: %w", errFeedInternal, err)
	}
	res.KEVEnriched = changed
	// KEVMissing counts KEV CVEs without an advisory of any source.
	missingAny, err := store.MissingExternalIDs(ctx, "", ids)
	if err != nil {
		return FeedFinish{}, fmt.Errorf("%w: %w", errFeedInternal, err)
	}
	res.KEVMissing, res.Complete = len(missingAny), len(missingAny) == 0
	// The ETag is kept only when NVD is configured (otherwise missing CVEs can never be fetched) and every
	// KEV CVE has an advisory; otherwise the next run re-reads the catalog.
	etag := ""
	if s.feeds.NVD != nil && len(missingAny) == 0 {
		etag = cat.ETag
	}
	fin := FeedFinish{ETag: &etag, KEVMiss: &miss, KEVCount: &n}
	if fetchErr != nil {
		fin.ErrorCode = feedErrorCode(fetchErr)
	}
	return fin, nil
}

// errKEVImplausible aborts (and rolls back) a KEV enrichment that would flag implausibly many advisories.
var errKEVImplausible = errors.New("security: implausible KEV enrichment")

// importRecords imports in batches that respect the import bounds.
func (s *Service) importRecords(ctx context.Context, c Caller, records []advisories.AdvisoryRecord, total *ImportResult) error {
	var batch []AdvisoryInput
	criteria := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		r, err := s.importBatch(ctx, c, Principal{Manage: true}, batch, true)
		if err != nil {
			return err
		}
		total.Created += r.Created
		total.Updated += r.Updated
		total.Unchanged += r.Unchanged
		total.Rejected += r.Rejected
		total.Reanalyze += r.Reanalyze
		batch, criteria = nil, 0
		return nil
	}
	for _, rec := range records {
		in := FromRecord(rec)
		if len(batch) == MaxImportRecords || criteria+len(in.Criteria) > MaxImportCriteria {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, in)
		criteria += len(in.Criteria)
	}
	return flush()
}

// applyKEV marks the advisories of the catalog as known exploited in one transaction, auditing every change
// with ids and dates only.
func (s *Service) applyKEV(ctx context.Context, store feedStore, c Caller, entries []KEVEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	changed := 0
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		ids, err := store.ApplyKEVTx(ctx, tx, entries)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := recordAudit(ctx, tx, c, "security.advisory.kev_enriched", "security_advisory", id, nil, nil, map[string]any{"via": FeedCISAKEV}); err != nil {
				return err
			}
		}
		if len(ids) > KEVMaxNewlyFlagged {
			return errKEVImplausible
		}
		changed = len(ids)
		return nil
	})
	return changed, err
}

// FeedStates returns the feed sources that have run, for the briefing health. It reads state and constant
// error codes only.
func (s *Service) FeedStates(ctx context.Context) ([]FeedState, error) {
	store, ok := s.store.(feedStore)
	if !ok {
		return nil, ErrNotFound
	}
	return store.FeedStates(ctx)
}
