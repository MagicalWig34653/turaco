package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	securityrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/security/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

type fakeNVD struct {
	records []advisories.AdvisoryRecord
	byID    map[string]advisories.AdvisoryRecord
	through time.Time
	err     error
	since   time.Time
	asked   []string
}

func (f *fakeNVD) Sync(_ context.Context, since time.Time) (advisories.SyncResult, error) {
	f.since = since
	if f.err != nil {
		// Progress made before the error (records read, windows completed) is reported with it.
		return advisories.SyncResult{Records: f.records, Through: f.through}, f.err
	}
	return advisories.SyncResult{Records: f.records, Through: f.through, Complete: true}, nil
}

func (f *fakeNVD) ByID(_ context.Context, ids []string) ([]advisories.AdvisoryRecord, error) {
	f.asked = append(f.asked, ids...)
	var out []advisories.AdvisoryRecord
	for _, id := range ids {
		if r, ok := f.byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

type fakeKEV struct {
	cat   advisories.KEVCatalog
	err   error
	asked []string
}

func (f *fakeKEV) Catalog(_ context.Context, etag string) (advisories.KEVCatalog, error) {
	f.asked = append(f.asked, etag)
	if f.err != nil {
		return advisories.KEVCatalog{}, f.err
	}
	if etag != "" && etag == f.cat.ETag {
		return advisories.KEVCatalog{ETag: etag, NotModified: true}, nil
	}
	return f.cat, nil
}

func feedRecord(id string, modified time.Time) advisories.AdvisoryRecord {
	return advisories.AdvisoryRecord{Source: "nvd", ExternalID: id, Title: id + ": test advisory", Severity: "high", ModifiedAt: &modified,
		SourceURL: "https://nvd.nist.gov/vuln/detail/" + id,
		Criteria:  []advisories.Criteria{{ProductName: "feed test product " + id, Rules: []advisories.VersionRule{{Kind: "lt", Version: "2.0"}}}}}
}

func cleanFeedTest(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	ctx := context.Background()
	wipe := func() {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		sub := `(SELECT id FROM security.advisories WHERE external_id LIKE $1)`
		_, _ = conn.Exec(ctx, `DELETE FROM platform.jobs WHERE dedupe_key IN (SELECT 'security.match:' || id::text FROM `+sub+` s)`, prefix+"%")
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_transitions WHERE advisory_id IN `+sub, prefix+"%")
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria_rules WHERE criteria_id IN (SELECT id FROM security.advisory_criteria WHERE advisory_id IN `+sub+`)`, prefix+"%")
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria WHERE advisory_id IN `+sub, prefix+"%")
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisories WHERE external_id LIKE $1`, prefix+"%")
		_, _ = conn.Exec(ctx, `DELETE FROM security.feed_state`)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	}
	wipe()
	t.Cleanup(wipe)
}

func feedTestPrefix(t *testing.T) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "CVE-2099-" + strings.ToUpper(hex.EncodeToString(b)) + "-"
}

func runSyncJob(t *testing.T, svc *securityapp.Service, payload string) {
	t.Helper()
	if err := svc.HandleAdvisorySync(context.Background(), jobs.Job{ID: "feed-test", Type: securityapp.AdvisorySyncJobType, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
}

type kevRow struct {
	exploited bool
	added     *time.Time
	due       *time.Time
	version   int
}

func readKEV(t *testing.T, pool *pgxpool.Pool, id string) (r kevRow, found bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT known_exploited, known_exploited_added_at, kev_due_date, version FROM security.advisories WHERE source = 'nvd' AND external_id = $1`, id).
		Scan(&r.exploited, &r.added, &r.due, &r.version)
	return r, err == nil
}

func TestAdvisorySyncJobImportsIdempotentlyAndEnrichesKEV(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	a, b, c, missing := prefix+"A", prefix+"B", prefix+"C", prefix+"D"
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	through := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	nvd := &fakeNVD{records: []advisories.AdvisoryRecord{feedRecord(a, mod), feedRecord(b, mod)}, through: through,
		byID: map[string]advisories.AdvisoryRecord{c: feedRecord(c, mod)}}
	added, due := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	kev := &fakeKEV{cat: advisories.KEVCatalog{ETag: `"v1"`, Entries: []advisories.KEVEntry{
		{CVEID: a, DateAdded: &added, DueDate: &due}, {CVEID: c, DateAdded: &added, DueDate: &due}, {CVEID: missing, DateAdded: &added}}}}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd, KEV: kev, KEVFetchPerRun: 10})

	runSyncJob(t, svc, "")
	for _, id := range []string{a, b, c} {
		if _, ok := readKEV(t, pool, id); !ok {
			t.Fatalf("%s was not imported", id)
		}
	}
	if _, ok := readKEV(t, pool, missing); ok {
		t.Fatal("KEV enrichment must not create advisories of its own")
	}
	if got, _ := readKEV(t, pool, a); !got.exploited || got.added == nil || !got.added.Equal(added) || got.due == nil || !got.due.Equal(due) {
		t.Fatalf("A not enriched: %+v", got)
	}
	if got, _ := readKEV(t, pool, c); !got.exploited {
		t.Fatal("C is fetched by id and enriched in the same run")
	}
	if got, _ := readKEV(t, pool, b); got.exploited {
		t.Fatal("B is not in the catalog")
	}
	if len(nvd.asked) != 2 || !(nvd.asked[0] == c || nvd.asked[1] == c) {
		t.Fatalf("by-id fetch asks only for KEV CVEs without advisory (ours: %v)", nvd.asked)
	}
	var cursor, etag *string
	var success *time.Time
	var lastErr *string
	if err := pool.QueryRow(ctx, `SELECT cursor, last_success_at, last_error FROM security.feed_state WHERE source = 'nvd'`).Scan(&cursor, &success, &lastErr); err != nil {
		t.Fatal(err)
	}
	if cursor == nil || *cursor != through.Format(time.RFC3339) || success == nil || lastErr != nil {
		t.Fatalf("nvd state cursor=%v success=%v err=%v", cursor, success, lastErr)
	}
	if err := pool.QueryRow(ctx, `SELECT etag FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&etag); err != nil {
		t.Fatal(err)
	}
	if etag != nil {
		t.Fatal("the ETag must not be kept while KEV CVEs still have no advisory")
	}

	// Second run: nothing changes (idempotent); the stored cursor is the next start.
	versionA, _ := readKEV(t, pool, a)
	nvd.since = time.Time{}
	runSyncJob(t, svc, "")
	if !nvd.since.Equal(through.Add(-securityapp.FeedOverlap)) {
		t.Fatalf("the next run starts at the cursor minus the overlap: %v", nvd.since)
	}
	if got, _ := readKEV(t, pool, a); got.version != versionA.version {
		t.Fatalf("an unchanged run must not touch advisories: version %d -> %d", versionA.version, got.version)
	}
	var created int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM security.advisories WHERE external_id LIKE $1`, prefix+"%").Scan(&created); err != nil || created != 3 {
		t.Fatalf("advisories = %d (%v)", created, err)
	}

	// A changed record updates the advisory but keeps the KEV mark (only the KEV enrichment sets it).
	later := mod.Add(48 * time.Hour)
	changed := feedRecord(a, later)
	changed.Title = a + ": changed title"
	nvd.records = []advisories.AdvisoryRecord{changed}
	runSyncJob(t, svc, `{"sources":["nvd"]}`)
	var title string
	var exploited bool
	if err := pool.QueryRow(ctx, `SELECT title, known_exploited FROM security.advisories WHERE source = 'nvd' AND external_id = $1`, a).Scan(&title, &exploited); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(title, "changed title") || !exploited {
		t.Fatalf("title %q exploited %v", title, exploited)
	}

	// When every KEV CVE has an advisory the ETag is kept and the next catalog read is conditional.
	nvd.byID[missing] = feedRecord(missing, mod)
	// The failed by-id fetch of D is skipped for a week; simulate the wait.
	if _, err := pool.Exec(ctx, `UPDATE security.feed_state SET kev_miss = '{}' WHERE source = 'cisa_kev'`); err != nil {
		t.Fatal(err)
	}
	nvd.records = nil
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if err := pool.QueryRow(ctx, `SELECT etag FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&etag); err != nil || etag == nil || *etag != `"v1"` {
		t.Fatalf("etag %v (%v)", etag, err)
	}
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if last := kev.asked[len(kev.asked)-1]; last != `"v1"` {
		t.Fatalf("conditional fetch expected, asked with %q", last)
	}

	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'security.advisory.kev_enriched' AND target_id IN (SELECT id::text FROM security.advisories WHERE external_id LIKE $1)`, prefix+"%").Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 3 {
		t.Fatalf("kev enrichment audit entries = %d", audited)
	}
}

func TestAdvisorySyncSourceFailuresAreIsolatedAndRecorded(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	nvd := &fakeNVD{err: advisories.ErrRateLimited}
	kev := &fakeKEV{cat: advisories.KEVCatalog{ETag: `"x"`, Entries: []advisories.KEVEntry{{CVEID: prefix + "Z"}}}}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd, KEV: kev})
	runSyncJob(t, svc, "")
	states, err := securitypublic.NewAdvisories(svc).AdvisoryFeedHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]securitypublic.FeedHealth{}
	for _, s := range states {
		got[s.Source] = s
	}
	if got["nvd"].LastError != "rate_limited" || got["nvd"].LastSuccessAt != nil || !got["nvd"].Stale {
		t.Fatalf("nvd health %+v", got["nvd"])
	}
	if got["cisa_kev"].LastError != "" || got["cisa_kev"].LastSuccessAt == nil || got["cisa_kev"].Stale {
		t.Fatalf("a failing source must not stop the others: %+v", got["cisa_kev"])
	}
	// Recovery clears the error.
	nvd.err, nvd.through = nil, time.Now().UTC()
	runSyncJob(t, svc, `{"sources":["nvd"]}`)
	states, _ = securitypublic.NewAdvisories(svc).AdvisoryFeedHealth(ctx)
	for _, s := range states {
		if s.Source == "nvd" && (s.LastError != "" || s.Stale) {
			t.Fatalf("nvd did not recover: %+v", s)
		}
	}
}

func TestAdvisorySyncLeaseAndValidation(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	cleanFeedTest(t, pool, feedTestPrefix(t))
	nvd := &fakeNVD{through: time.Now().UTC()}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd})
	if _, err := pool.Exec(ctx, `INSERT INTO security.feed_state(source, locked_until) VALUES ('nvd', now() + interval '10 minutes')`); err != nil {
		t.Fatal(err)
	}
	res, err := svc.SyncFeeds(ctx, securityapp.FeedCaller("lease-test"), securityapp.AdvisorySyncPayload{})
	if err != nil || len(res) != 1 || !res[0].Skipped || !nvd.since.IsZero() {
		t.Fatalf("a held source must be skipped without running: %+v %v since=%v", res, err, nvd.since)
	}
	if _, err := pool.Exec(ctx, `UPDATE security.feed_state SET locked_until = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if res, err = svc.SyncFeeds(ctx, securityapp.FeedCaller("lease-test"), securityapp.AdvisorySyncPayload{}); err != nil || res[0].Skipped {
		t.Fatalf("an expired lease can be taken: %+v %v", res, err)
	}
	var locked *time.Time
	if err := pool.QueryRow(ctx, `SELECT locked_until FROM security.feed_state WHERE source = 'nvd'`).Scan(&locked); err != nil || locked != nil {
		t.Fatalf("the lease must be released: %v %v", locked, err)
	}
	if _, err := svc.SyncFeeds(ctx, securityapp.FeedCaller("lease-test"), securityapp.AdvisorySyncPayload{Sources: []string{"cisa_kev"}}); err == nil {
		t.Fatal("an unconfigured source must be refused")
	}
	if _, err := svc.SyncFeeds(ctx, securityapp.Caller{}, securityapp.AdvisorySyncPayload{}); err == nil {
		t.Fatal("a caller is required")
	}
	if err := svc.HandleAdvisorySync(ctx, jobs.Job{ID: "x", Payload: []byte(`{`)}); err == nil {
		t.Fatal("a malformed payload must fail")
	}
}

// Known exploited advisories come first in the applicable list (the briefing feed) and are counted in the
// security overview, ahead of higher-severity advisories that are not in the KEV catalog.
func TestKnownExploitedAdvisoriesSortFirst(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	crit, kev := feedRecord(prefix+"CRIT", mod), feedRecord(prefix+"KEV", mod)
	crit.Severity, kev.Severity = "critical", "low"
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: &fakeNVD{records: []advisories.AdvisoryRecord{crit, kev}, through: mod}})
	runSyncJob(t, svc, "")
	if _, err := pool.Exec(ctx, `UPDATE security.advisories SET status = 'applicable', applicable_at = now() WHERE external_id LIKE $1`, prefix+"%"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE security.advisories SET known_exploited = true, known_exploited_added_at = '2026-09-28', kev_due_date = '2026-10-19' WHERE external_id = $1`, prefix+"KEV"); err != nil {
		t.Fatal(err)
	}
	list, _, err := svc.ApplicableAdvisories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || !list[0].KnownExploited || list[0].KEVDueDate == nil || list[0].KEVDueDate.Format(time.DateOnly) != "2026-10-19" {
		t.Fatalf("the known exploited advisory must come first: %+v", list)
	}
	seenPlain := false
	for _, a := range list {
		if !a.KnownExploited {
			seenPlain = true
		} else if seenPlain {
			t.Fatal("known exploited advisories must precede all others")
		}
	}
	overview, err := securityrepository.New(pool).OverviewCounts(ctx, time.Now())
	if err != nil || overview.KnownExploitedApplicable < 1 {
		t.Fatalf("overview %+v %v", overview, err)
	}
	// The mark and its dates are consistent: dates without the mark are refused by the database.
	if _, err := pool.Exec(ctx, `UPDATE security.advisories SET kev_due_date = '2026-10-19' WHERE external_id = $1`, prefix+"CRIT"); err == nil {
		t.Fatal("kev dates without known_exploited must violate the check constraint")
	}
}
