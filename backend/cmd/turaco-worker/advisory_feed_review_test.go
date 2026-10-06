package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	securityrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/security/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

type advRow struct {
	id, status, severity        string
	version, revision           int
	incomplete, changedUpstream bool
	skipped                     int
}

func readAdv(t *testing.T, pool *pgxpool.Pool, ext string) advRow {
	t.Helper()
	var r advRow
	err := pool.QueryRow(context.Background(), `SELECT id::text, status, severity, version, criteria_revision, criteria_incomplete, criteria_skipped, criteria_changed_upstream
		FROM security.advisories WHERE source = 'nvd' AND external_id = $1`, ext).
		Scan(&r.id, &r.status, &r.severity, &r.version, &r.revision, &r.incomplete, &r.skipped, &r.changedUpstream)
	if err != nil {
		t.Fatalf("read %s: %v", ext, err)
	}
	return r
}

func ptr(v int) *int { return &v }

// An archived advisory is rejected (counted) and never wedges the sync: the other records of the run are imported.
func TestFeedImportRejectsArchivedAdvisoryWithoutWedgingTheSync(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	a, b := prefix+"A", prefix+"B"
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	nvd := &fakeNVD{records: []advisories.AdvisoryRecord{feedRecord(a, mod)}, through: mod}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd})
	runSyncJob(t, svc, "")
	if _, err := pool.Exec(ctx, `UPDATE security.advisories SET status = 'archived', archived_at = now() WHERE external_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	nvd.records = []advisories.AdvisoryRecord{feedRecord(a, mod.Add(time.Hour)), feedRecord(b, mod)}
	nvd.through = mod.Add(time.Hour)
	res, err := svc.SyncFeeds(ctx, securityapp.FeedCaller("t"), securityapp.AdvisorySyncPayload{})
	if err != nil || res[0].Error != "" || res[0].Import.Rejected != 1 || res[0].Import.Created != 1 {
		t.Fatalf("archived must be rejected, not fatal: %+v %v", res, err)
	}
	if got := readAdv(t, pool, a); got.status != "archived" {
		t.Fatalf("status %s", got.status)
	}
	var cursor string
	if err := pool.QueryRow(ctx, `SELECT cursor FROM security.feed_state WHERE source = 'nvd'`).Scan(&cursor); err != nil || cursor != mod.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("the cursor must advance: %q %v", cursor, err)
	}
}

// Feed imports never undo analyst decisions.
func TestFeedImportKeepsAnalystDecisions(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	notApplicable, applicable, remediating, analyzing := prefix+"N", prefix+"A", prefix+"R", prefix+"Y"
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	nvd := &fakeNVD{through: mod}
	for _, id := range []string{notApplicable, applicable, remediating, analyzing} {
		nvd.records = append(nvd.records, feedRecord(id, mod))
	}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd})
	runSyncJob(t, svc, "")
	p, c := securityapp.Principal{Manage: true, AcceptRisk: true}, securityapp.FeedCaller("analyst")
	for _, step := range []struct {
		ext string
		do  func(id string, v *int) error
	}{
		{notApplicable, func(id string, v *int) error {
			_, err := svc.MarkNotApplicable(ctx, c, p, id, v, "product_not_used")
			return err
		}},
		{applicable, func(id string, v *int) error { _, err := svc.MarkApplicable(ctx, c, p, id, v); return err }},
		{remediating, func(id string, v *int) error { _, err := svc.MarkApplicable(ctx, c, p, id, v); return err }},
		{analyzing, func(id string, v *int) error { _, err := svc.StartAnalysis(ctx, c, p, id, v); return err }},
	} {
		r := readAdv(t, pool, step.ext)
		if err := step.do(r.id, ptr(r.version)); err != nil {
			t.Fatal(err)
		}
	}
	r := readAdv(t, pool, remediating)
	if _, err := svc.StartRemediation(ctx, c, p, r.id, ptr(r.version)); err != nil {
		t.Fatal(err)
	}

	rev0 := map[string]int{}
	for _, ext := range []string{notApplicable, applicable, remediating, analyzing} {
		rev0[ext] = readAdv(t, pool, ext).revision
	}

	// Severity/summary change only: the applicable advisory stays applicable (no re-analysis).
	later := mod.Add(24 * time.Hour)
	nvd.records = nil
	for _, id := range []string{notApplicable, applicable, remediating, analyzing} {
		rec := feedRecord(id, later)
		rec.Severity, rec.Summary = "critical", "new summary"
		nvd.records = append(nvd.records, rec)
	}
	runSyncJob(t, svc, "")
	for ext, want := range map[string]string{notApplicable: "not_applicable", applicable: "applicable", remediating: "remediating", analyzing: "analyzing"} {
		got := readAdv(t, pool, ext)
		if got.status != want || got.severity != "critical" || got.changedUpstream || got.revision != rev0[ext] {
			t.Fatalf("%s: %+v", ext, got)
		}
	}

	// Criteria change: applicable and analyzing replace them (applicable returns to analyzing); the
	// decisions in not_applicable and remediating stay, flagged for the analysts and audited.
	evenLater := later.Add(24 * time.Hour)
	nvd.records = nil
	for _, id := range []string{notApplicable, applicable, remediating, analyzing} {
		rec := feedRecord(id, evenLater)
		rec.Severity = "critical"
		rec.Criteria = []advisories.Criteria{{ProductName: "another product " + id}}
		nvd.records = append(nvd.records, rec)
	}
	runSyncJob(t, svc, "")
	for ext, want := range map[string]struct {
		status  string
		rev     int
		flagged bool
	}{
		notApplicable: {"not_applicable", 0, true}, remediating: {"remediating", 0, true},
		applicable: {"analyzing", 1, false}, analyzing: {"analyzing", 1, false},
	} {
		got := readAdv(t, pool, ext)
		if got.status != want.status || got.revision != rev0[ext]+want.rev || got.changedUpstream != want.flagged {
			t.Fatalf("%s: %+v want %+v", ext, got, want)
		}
	}
	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE action = 'security.advisory.updated' AND metadata->>'criteriaChangedUpstream' = 'true'
		AND target_id IN (SELECT id::text FROM security.advisories WHERE external_id LIKE $1)`, prefix+"%").Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("upstream criteria change audit entries = %d (%v)", audited, err)
	}
	var criteriaRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM security.advisory_criteria WHERE advisory_id = $1::uuid AND product_name = $2`, readAdv(t, pool, notApplicable).id, "feed test product "+notApplicable).Scan(&criteriaRows); err != nil || criteriaRows != 1 {
		t.Fatalf("the criteria of a decided advisory must stay: %d %v", criteriaRows, err)
	}
}

func TestFeedImportMarksIncompleteCriteria(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	id := prefix + "I"
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rec := feedRecord(id, mod)
	rec.CriteriaSkipped = 4
	nvd := &fakeNVD{records: []advisories.AdvisoryRecord{rec}, through: mod}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd})
	runSyncJob(t, svc, "")
	if got := readAdv(t, pool, id); !got.incomplete || got.skipped != 4 {
		t.Fatalf("%+v", got)
	}
	// A later complete record clears the mark.
	nvd.records = []advisories.AdvisoryRecord{feedRecord(id, mod.Add(time.Hour))}
	runSyncJob(t, svc, "")
	if got := readAdv(t, pool, id); got.incomplete || got.skipped != 0 {
		t.Fatalf("%+v", got)
	}
	_ = ctx
}

func TestFeedErrorKeepsProgress(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	through := mod.Add(48 * time.Hour)
	nvd := &fakeNVD{records: []advisories.AdvisoryRecord{feedRecord(prefix+"P", mod)}, through: through, err: advisories.ErrUnavailable}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd})
	runSyncJob(t, svc, "")
	readAdv(t, pool, prefix+"P")
	var cursor, lastErr string
	if err := pool.QueryRow(ctx, `SELECT cursor, last_error FROM security.feed_state WHERE source = 'nvd'`).Scan(&cursor, &lastErr); err != nil {
		t.Fatal(err)
	}
	if cursor != through.Format(time.RFC3339) || lastErr != securityapp.FeedErrUnavailable {
		t.Fatalf("cursor %q error %q", cursor, lastErr)
	}
}

func TestKEVUnfetchableIdsDoNotStarveOthersAndETagIsNotKept(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{prefix + "1", prefix + "2", prefix + "3"}
	var entries []advisories.KEVEntry
	for _, id := range ids {
		entries = append(entries, advisories.KEVEntry{CVEID: id})
	}
	nvd := &fakeNVD{through: mod, byID: map[string]advisories.AdvisoryRecord{ids[2]: feedRecord(ids[2], mod)}}
	kev := &fakeKEV{cat: advisories.KEVCatalog{ETag: `"v1"`, Entries: entries}}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd, KEV: kev, KEVFetchPerRun: 2})
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if fmt.Sprint(nvd.asked) != fmt.Sprint(ids[:2]) {
		t.Fatalf("first run asks %v", nvd.asked)
	}
	nvd.asked = nil
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if fmt.Sprint(nvd.asked) != fmt.Sprint(ids[2:]) {
		t.Fatalf("the ids NVD could not provide are skipped, the next one is asked: %v", nvd.asked)
	}
	var etag *string
	if err := pool.QueryRow(ctx, `SELECT etag FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&etag); err != nil || etag != nil {
		t.Fatalf("the ETag must not be kept while KEV CVEs have no advisory: %v %v", etag, err)
	}
	// Without NVD configured the ETag is not stored either.
	svc = wiring.Security(pool).WithFeeds(securityapp.FeedSources{KEV: kev})
	if _, err := pool.Exec(ctx, `INSERT INTO security.advisories(source, external_id, title, severity, status) SELECT 'feedtest', id, 't', 'low', 'new' FROM unnest($1::text[]) AS id`, ids[:2]); err != nil {
		t.Fatal(err)
	}
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if err := pool.QueryRow(ctx, `SELECT etag FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&etag); err != nil || etag != nil {
		t.Fatalf("no ETag without a configured NVD source: %v %v", etag, err)
	}
}

func TestKEVEnrichmentDoesNotBumpTheAdvisoryVersion(t *testing.T) {
	pool := dbtest.Pool(t)
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	id := prefix + "V"
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	nvd := &fakeNVD{records: []advisories.AdvisoryRecord{feedRecord(id, mod)}, through: mod}
	kev := &fakeKEV{cat: advisories.KEVCatalog{ETag: `"v"`, Entries: []advisories.KEVEntry{{CVEID: id}}}}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd, KEV: kev})
	runSyncJob(t, svc, `{"sources":["nvd"]}`)
	before := readAdv(t, pool, id).version
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if got, _ := readKEV(t, pool, id); !got.exploited || got.version != before {
		t.Fatalf("exploited %v version %d -> %d", got.exploited, before, got.version)
	}
}

func TestKEVImplausibleCatalogAbortsEnrichment(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	prefix := feedTestPrefix(t)
	cleanFeedTest(t, pool, prefix)
	mod := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	nvd := &fakeNVD{through: mod}
	if _, err := pool.Exec(ctx, `INSERT INTO security.advisories(source, external_id, title, severity, status)
		SELECT 'nvd', $1 || i, 't', 'low', 'new' FROM generate_series(1, 501) AS i`, prefix); err != nil {
		t.Fatal(err)
	}
	var entries []advisories.KEVEntry
	for i := 1; i <= 501; i++ {
		entries = append(entries, advisories.KEVEntry{CVEID: fmt.Sprintf("%s%d", prefix, i)})
	}
	kev := &fakeKEV{cat: advisories.KEVCatalog{ETag: `"big"`, Entries: entries}}
	svc := wiring.Security(pool).WithFeeds(securityapp.FeedSources{NVD: nvd, KEV: kev})
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	var flagged int
	var lastErr string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM security.advisories WHERE external_id LIKE $1 AND known_exploited`, prefix+"%").Scan(&flagged); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_error FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 || lastErr != "kev_catalog_implausible" {
		t.Fatalf("flagged %d error %q", flagged, lastErr)
	}
	// A catalog that grew far beyond the last accepted one is implausible as well.
	if _, err := pool.Exec(ctx, `UPDATE security.feed_state SET kev_count = 100 WHERE source = 'cisa_kev'`); err != nil {
		t.Fatal(err)
	}
	kev.cat.Entries = entries[:301]
	runSyncJob(t, svc, `{"sources":["cisa_kev"]}`)
	if err := pool.QueryRow(ctx, `SELECT last_error FROM security.feed_state WHERE source = 'cisa_kev'`).Scan(&lastErr); err != nil || lastErr != "kev_catalog_implausible" {
		t.Fatalf("growth: %q %v", lastErr, err)
	}
}

func TestFeedLeaseTokenProtectsAgainstLateFinisher(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	cleanFeedTest(t, pool, feedTestPrefix(t))
	repo := securityrepository.New(pool)
	first, ok, err := repo.ClaimFeed(ctx, "nvd", time.Minute)
	if err != nil || !ok || first.LeaseToken == "" {
		t.Fatalf("claim: %+v %v %v", first, ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE security.feed_state SET locked_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	second, ok, err := repo.ClaimFeed(ctx, "nvd", time.Minute)
	if err != nil || !ok || second.LeaseToken == first.LeaseToken {
		t.Fatalf("second claim: %+v %v %v", second, ok, err)
	}
	cursor := "2026-01-01T00:00:00Z"
	if err := repo.FinishFeed(ctx, "nvd", first.LeaseToken, securityapp.FeedFinish{Cursor: &cursor, Success: true}); !errors.Is(err, securityapp.ErrFeedLeaseLost) {
		t.Fatalf("late finisher: %v", err)
	}
	var locked *time.Time
	var stored *string
	if err := pool.QueryRow(ctx, `SELECT locked_until, cursor FROM security.feed_state WHERE source = 'nvd'`).Scan(&locked, &stored); err != nil || locked == nil || stored != nil {
		t.Fatalf("the late finisher must not clear the lock or write: %v %v %v", locked, stored, err)
	}
	if err := repo.FinishFeed(ctx, "nvd", second.LeaseToken, securityapp.FeedFinish{Cursor: &cursor, Success: true}); err != nil {
		t.Fatal(err)
	}
}
