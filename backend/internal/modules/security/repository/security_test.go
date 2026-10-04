package repository_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

const (
	securityTestUser    = "00000000-0000-7000-8000-0000000000a1"
	securityTestProduct = "00000000-0000-7000-8000-0000000000a2"
	securityTestDevice  = "00000000-0000-7000-8000-0000000000a3"
)

type inventory struct {
	mu      sync.Mutex
	items   []application.Installation
	retired *time.Time
}

func (v *inventory) set(items ...application.Installation) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.items = items
}
func (v *inventory) InstallationsByProducts(_ context.Context, _ []string, cursor string, _ int) ([]application.Installation, string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if cursor != "" {
		return nil, "", nil
	}
	var out []application.Installation
	for _, item := range v.items {
		if !item.Retired {
			out = append(out, item)
		}
	}
	return out, "", nil
}
func (v *inventory) InstallationsOnDevices(context.Context, []string, []string) ([]application.Installation, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]application.Installation(nil), v.items...), false, nil
}
func (v *inventory) DeviceRetired(context.Context, []string) (map[string]*time.Time, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return map[string]*time.Time{securityTestDevice: v.retired}, nil
}
func (v *inventory) DeviceNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{securityTestDevice: "Test workstation"}, nil
}
func (v *inventory) LatestIngestionAt(context.Context) (*time.Time, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.items) == 0 {
		return nil, nil
	}
	t := v.items[len(v.items)-1].ObservedAt
	return &t, nil
}
func (v *inventory) SoftwareProducts(context.Context, []string) (map[string]application.SoftwareProduct, error) {
	return map[string]application.SoftwareProduct{securityTestProduct: {ID: securityTestProduct, Name: "Test software"}}, nil
}
func (v *inventory) FindSoftwareProduct(context.Context, string, string) (application.SoftwareProduct, string, bool, error) {
	return application.SoftwareProduct{}, "", false, nil
}

func securityEnv(t *testing.T) (*pgxpool.Pool, *application.Service, *inventory, application.Caller) {
	t.Helper()
	pool := dbtest.Pool(t)
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('security.advisories') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		dbtest.Unavailable(t, "security migration 000047 is required")
	}
	v := &inventory{}
	c := application.Caller{Actor: audit.UserActor(securityTestUser), CorrelationID: fmt.Sprintf("security-test-%d", time.Now().UnixNano())}
	return pool, application.NewService(repository.New(pool), v), v, c
}

func cleanupAdvisory(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_id = $1 OR correlation_id LIKE 'security-test-%'`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id LIKE 'security-test-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE dedupe_key = $1`, application.MatchJobType+":"+id)
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM security.finding_transitions WHERE finding_id IN (SELECT id FROM security.vulnerability_findings WHERE advisory_id = $1::uuid)`, id)
		_, _ = conn.Exec(ctx, `DELETE FROM security.vulnerability_findings WHERE advisory_id = $1::uuid`, id)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_transitions WHERE advisory_id = $1::uuid`, id)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria_rules WHERE criteria_id IN (SELECT id FROM security.advisory_criteria WHERE advisory_id = $1::uuid)`, id)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria WHERE advisory_id = $1::uuid`, id)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisories WHERE id = $1::uuid`, id)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
	})
}

func TestAdvisoryFindingMatchAndFreshRemediation(t *testing.T) {
	pool, svc, inv, caller := securityEnv(t)
	ctx := context.Background()
	p := application.Principal{UserID: securityTestUser, View: true, Manage: true, AcceptRisk: true, EndpointsView: true}
	created, _, err := svc.Create(ctx, caller, p, application.AdvisoryInput{Title: "Security test advisory", Severity: "high", Criteria: []application.CriterionInput{{SoftwareProductID: securityTestProduct, Rules: []application.Rule{{Kind: "fixed", Version: "2.0"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, created.ID)
	if created.Reference == "" || created.Status != application.AdvisoryNew {
		t.Fatalf("created = %+v", created)
	}
	if _, err := svc.MarkApplicable(ctx, caller, p, created.ID, nil); err == nil {
		t.Fatal("accepted transition without expectedVersion")
	}
	applicable, err := svc.MarkApplicable(ctx, caller, p, created.ID, &created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if applicable.Status != application.AdvisoryApplicable {
		t.Fatal(applicable.Status)
	}
	if _, err := pool.Exec(ctx, `UPDATE security.advisories SET status = 'not_applicable', status_reason = NULL WHERE id = $1::uuid`, created.ID); err == nil {
		t.Fatal("advisory per-status CHECK allowed missing reason")
	}

	t1 := time.Now().UTC().Add(-2 * time.Hour)
	item := application.Installation{ID: "00000000-0000-7000-8000-0000000000a4", DeviceID: securityTestDevice, SoftwareProductID: securityTestProduct, DevicePlatform: "windows", RawVersion: "1.9", ObservedAt: t1}
	inv.set(item)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := svc.Match(ctx, application.MatchCaller(caller.CorrelationID), created.ID); e != nil {
				t.Errorf("concurrent match: %v", e)
			}
		}()
	}
	wg.Wait()
	list, err := svc.ListFindings(ctx, p, application.FindingFilter{AdvisoryID: created.ID})
	if err != nil || len(list.Items) != 1 || list.Items[0].Confidence != application.ConfidenceProbable {
		t.Fatalf("findings = %+v, %v", list, err)
	}
	f := list.Items[0].Finding
	if _, err := pool.Exec(ctx, `UPDATE security.vulnerability_findings SET status = 'risk_accepted' WHERE id = $1::uuid`, f.ID); err == nil {
		t.Fatal("finding per-status CHECK allowed risk acceptance without actor and review")
	}
	acknowledged, err := svc.AcceptFinding(ctx, caller, p, f.ID, &f.Version)
	if err != nil || acknowledged.Status != application.FindingAccepted {
		t.Fatalf("finding acknowledgement = %+v, %v", acknowledged, err)
	}
	f = acknowledged
	if _, err := svc.AcceptRisk(ctx, caller, application.Principal{UserID: securityTestUser, Manage: true}, f.ID, &f.Version, "business_need", time.Now().AddDate(0, 1, 0)); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("manage accepted risk: %v", err)
	}
	accepted, err := svc.AcceptRisk(ctx, caller, p, f.ID, &f.Version, "business_need", time.Now().AddDate(0, 1, 0))
	if err != nil || accepted.Status != application.FindingRiskAccepted {
		t.Fatalf("risk accepted = %+v, %v", accepted, err)
	}
	item.RawVersion, item.ObservedAt = "2.0", t1.Add(time.Hour)
	inv.set(item)
	if _, err := svc.Match(ctx, application.MatchCaller(caller.CorrelationID), created.ID); err != nil {
		t.Fatal(err)
	}
	remediated, err := svc.GetFinding(ctx, p, f.ID)
	if err != nil || remediated.Status != application.FindingRemediated || remediated.RemediatedAt == nil {
		t.Fatalf("remediated = %+v, %v", remediated, err)
	}
	if remediated.RiskReviewBy != nil {
		t.Fatal("risk acceptance was not cleared")
	}
	item.RawVersion, item.ObservedAt = "1.8", t1.Add(90*time.Minute)
	inv.set(item)
	if _, err := svc.Match(ctx, application.MatchCaller(caller.CorrelationID), created.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := svc.GetFinding(ctx, p, f.ID)
	if err != nil || reopened.Status != application.FindingOpen {
		t.Fatalf("observed again = %+v, %v", reopened, err)
	}
	retiredAt := t1.Add(100 * time.Minute)
	inv.mu.Lock()
	inv.retired = &retiredAt
	inv.mu.Unlock()
	item.Retired, item.RetiredAt = true, &retiredAt
	inv.set(item)
	if _, err := svc.Match(ctx, application.MatchCaller(caller.CorrelationID), created.ID); err != nil {
		t.Fatal(err)
	}
	retired, err := svc.GetFinding(ctx, p, f.ID)
	if err != nil || retired.Status != application.FindingRemediated {
		t.Fatalf("tombstoned device = %+v, %v", retired, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM security.finding_transitions WHERE finding_id = $1::uuid`, f.ID).Scan(&count); err != nil || count != 6 {
		t.Fatalf("finding transitions = %d, %v", count, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM security.finding_transitions WHERE finding_id = $1::uuid`, f.ID); err == nil {
		t.Fatal("history DELETE was allowed")
	}
	if _, err := pool.Exec(ctx, `UPDATE security.finding_transitions SET operation = 'tamper' WHERE finding_id = $1::uuid`, f.ID); err == nil {
		t.Fatal("history UPDATE was allowed")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE security.finding_transitions`); err == nil {
		t.Fatal("history TRUNCATE was allowed")
	}
}

func TestImportIdempotencyAndCriteriaReanalysis(t *testing.T) {
	pool, svc, _, caller := securityEnv(t)
	ctx := context.Background()
	p := application.Principal{UserID: securityTestUser, Manage: true, View: true}
	key := fmt.Sprintf("BULLETIN-%d", time.Now().UnixNano())
	in := application.AdvisoryInput{Source: "test_feed", ExternalID: key, Title: "Imported bulletin", Severity: "medium"}
	first, err := svc.Import(ctx, caller, p, []application.AdvisoryInput{in})
	if err != nil || first.Created != 1 {
		t.Fatalf("first import = %+v, %v", first, err)
	}
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM security.advisories WHERE source = $1 AND external_id = $2`, in.Source, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, id)
	second, err := svc.Import(ctx, caller, p, []application.AdvisoryInput{in})
	if err != nil || second.Unchanged != 1 {
		t.Fatalf("second import = %+v, %v", second, err)
	}
	a, err := svc.GetAdvisory(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MarkApplicable(ctx, caller, p, id, &a.Version); err != nil {
		t.Fatal(err)
	}
	in.Criteria = []application.CriterionInput{{ProductName: "Not yet normalized", Rules: []application.Rule{{Kind: "fixed", Version: "2.0"}}}}
	changed, err := svc.Import(ctx, caller, p, []application.AdvisoryInput{in})
	if err != nil || changed.Reanalyze != 1 {
		t.Fatalf("criteria change = %+v, %v", changed, err)
	}
	a, err = svc.GetAdvisory(ctx, p, id)
	if err != nil || a.Status != application.AdvisoryAnalyzing {
		t.Fatalf("reanalysis = %+v, %v", a, err)
	}
	if _, err := svc.Import(ctx, caller, p, make([]application.AdvisoryInput, application.MaxImportRecords+1)); err == nil {
		t.Fatal("import cap ignored")
	}
}
