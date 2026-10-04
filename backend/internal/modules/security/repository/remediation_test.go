package repository_test

import (
	"context"
	"errors"
	"fmt"
	changespublic "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/public"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"
)

type remediationTasks struct {
	mu    sync.Mutex
	items map[string][]taskspublic.Task
}

func (t *remediationTasks) CreateInTx(ctx context.Context, tx pgx.Tx, _ taskspublic.Caller, in taskspublic.CreateInput) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items[in.ContextID] = append(t.items[in.ContextID], taskspublic.Task{ID: id, Title: in.Title, Status: taskspublic.StatusOpen, DueAt: in.DueAt, AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID})
	return id, nil
}
func (t *remediationTasks) ByContext(_ context.Context, _, id string, _ int) ([]taskspublic.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]taskspublic.Task{}, t.items[id]...), nil
}
func (t *remediationTasks) ByContextInTx(ctx context.Context, _ pgx.Tx, typ, id string, limit int) ([]taskspublic.Task, error) {
	return t.ByContext(ctx, typ, id, limit)
}
func (t *remediationTasks) SummaryByContexts(_ context.Context, _ string, ids []string) (taskspublic.ContextSummary, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var s taskspublic.ContextSummary
	for _, id := range ids {
		for _, v := range t.items[id] {
			if v.Status == taskspublic.StatusCompleted {
				s.Done++
			} else {
				s.Open++
				if v.DueAt != nil && v.DueAt.Before(time.Now()) {
					s.Overdue++
				}
			}
		}
	}
	return s, nil
}
func (t *remediationTasks) SummaryByType(context.Context, string) (taskspublic.ContextSummary, error) {
	return taskspublic.ContextSummary{}, nil
}

type remediationChanges struct {
	items map[string]changespublic.ChangeInfo
}

func (c remediationChanges) Lookup(_ context.Context, ids []string, _ changespublic.ReadScope) (map[string]changespublic.ChangeInfo, error) {
	out := map[string]changespublic.ChangeInfo{}
	for _, id := range ids {
		if v, ok := c.items[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type remediationNames struct{}

func (remediationNames) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = "Analyst"
	}
	return out, nil
}
func (remediationNames) TeamNames(context.Context, []string) (map[string]string, error) {
	return nil, nil
}

func TestRemediationTasksAndChangeLinks(t *testing.T) {
	pool, base, inv, caller := securityEnv(t)
	ctx := context.Background()
	p := securityapp.Principal{UserID: securityTestUser, View: true, Manage: true, TasksView: true, ChangesView: true}
	reg := relationships.NewRegistry()
	reg.Register(securityapp.Triples...)
	graph := relationships.New(reg)
	tasks := &remediationTasks{items: map[string][]taskspublic.Task{}}
	changeID := "00000000-0000-7000-8000-0000000000bb"
	changes := remediationChanges{items: map[string]changespublic.ChangeInfo{changeID: {ID: changeID, Reference: "CHG-TEST", Status: "draft", RequesterID: securityTestUser}}}
	svc := base.WithRemediation(tasks, changes, graph, remediationNames{}, pool)
	_ = inv
	a, _, err := svc.Create(ctx, caller, p, securityapp.AdvisoryInput{Title: "F8b test", Severity: "high"})
	if err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, a.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_type='advisory' AND source_id=$1::uuid`, a.ID)
	})
	due := time.Now().Add(24 * time.Hour)
	user := securityTestUser
	if _, err := svc.CreateRemediationTask(ctx, caller, securityapp.Principal{}, "advisory", a.ID, &a.Version, &user, nil, &due); !errors.Is(err, securityapp.ErrForbidden) {
		t.Fatal(err)
	}
	id, err := svc.CreateRemediationTask(ctx, caller, p, "advisory", a.ID, &a.Version, &user, nil, &due)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := svc.RemediationTasks(ctx, p, "advisory", a.ID)
	if err != nil || len(visible) != 1 || visible[0].ID != id || visible[0].AssigneeName == nil {
		t.Fatalf("visible tasks: %+v %v", visible, err)
	}
	tasks.mu.Lock()
	if got := tasks.items[a.ID][0].Title; got != "Remediate security advisory "+a.Reference {
		t.Errorf("task title = %q", got)
	}
	tasks.mu.Unlock()
	hidden, err := svc.RemediationTasks(ctx, securityapp.Principal{View: true}, "advisory", a.ID)
	if err != nil || !hidden[0].AssigneeHidden || hidden[0].AssignedUserID != nil {
		t.Fatalf("hidden tasks: %+v %v", hidden, err)
	}
	current, err := svc.GetAdvisory(ctx, p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRemediationTask(ctx, caller, p, "advisory", a.ID, &a.Version, &user, nil, &due); !errors.Is(err, securityapp.ErrVersionConflict) {
		t.Fatal("stale version allowed", err)
	}
	tasks.mu.Lock()
	for i := len(tasks.items[a.ID]); i < securityapp.MaxRemediationTasks; i++ {
		tasks.items[a.ID] = append(tasks.items[a.ID], taskspublic.Task{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", i), Status: taskspublic.StatusOpen})
	}
	tasks.mu.Unlock()
	if _, err := svc.CreateRemediationTask(ctx, caller, p, "advisory", a.ID, &current.Version, &user, nil, &due); err == nil {
		t.Fatal("task cap allowed")
	}
	link, err := svc.LinkChange(ctx, caller, p, a.ID, changeID, &current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if link.ChangeID != changeID {
		t.Fatal(link)
	}
	links, err := svc.LinkedChanges(ctx, securityapp.Principal{View: true}, a.ID)
	if err != nil || len(links) != 1 || !links[0].Hidden || links[0].ChangeID != "" {
		t.Fatalf("hidden changes: %+v %v", links, err)
	}
	if _, err := svc.LinkChange(ctx, caller, p, a.ID, "00000000-0000-7000-8000-0000000000bc", &current.Version); !errors.Is(err, securityapp.ErrNotFound) {
		t.Fatal("missing change accepted", err)
	}
	hiddenID := "00000000-0000-7000-8000-0000000000bd"
	changes.items[hiddenID] = changespublic.ChangeInfo{ID: hiddenID, Reference: "CHG-HIDDEN", RequesterID: "00000000-0000-7000-8000-0000000000be"}
	noChanges := p
	noChanges.ChangesView = false
	if _, err := svc.LinkChange(ctx, caller, noChanges, a.ID, hiddenID, &current.Version); !errors.Is(err, securityapp.ErrNotFound) {
		t.Fatalf("hidden change IDOR: %v", err)
	}
	// A duplicate is idempotent at the same version, while concurrent creation
	// with the stale parent version cannot create a second row.
	current, err = svc.GetAdvisory(ctx, p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := svc.LinkChange(ctx, caller, p, a.ID, changeID, &current.Version)
			if e != nil && !errors.Is(e, securityapp.ErrVersionConflict) {
				t.Errorf("duplicate link: %v", e)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.relationships WHERE source_type='advisory' AND source_id=$1::uuid AND type='REMEDIATED_BY' AND valid_until IS NULL`, a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate rows = %d, %v", count, err)
	}
	for i := 1; i < securityapp.MaxRemediationChanges; i++ {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", 1000+i)
		changes.items[id] = changespublic.ChangeInfo{ID: id, Reference: "CHG-TEST", Status: "draft", RequesterID: securityTestUser}
		current, err = svc.GetAdvisory(ctx, p, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.LinkChange(ctx, caller, p, a.ID, id, &current.Version); err != nil {
			t.Fatal(err)
		}
	}
	extra := "00000000-0000-7000-8000-000000009999"
	changes.items[extra] = changespublic.ChangeInfo{ID: extra, Reference: "CHG-TEST", Status: "draft", RequesterID: securityTestUser}
	current, err = svc.GetAdvisory(ctx, p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.LinkChange(ctx, caller, p, a.ID, extra, &current.Version); err == nil {
		t.Fatal("change cap allowed")
	}
	current, err = svc.GetAdvisory(ctx, p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.MarkApplicable(ctx, caller, p, a.ID, &current.Version); err != nil {
		t.Fatal(err)
	}
	contract := securitypublic.NewAdvisories(svc)
	minimal, err := contract.ApplicableAdvisorySummaries(ctx, securitypublic.ReadScope{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range minimal {
		if v.ID == a.ID && (v.Title != "" || v.Severity != "" || v.AffectedDevices != 0) {
			t.Fatalf("zero scope leaked advisory detail: %+v", v)
		}
	}
}

func TestProgressOverviewAndPublicRiskReview(t *testing.T) {
	pool, base, inv, caller := securityEnv(t)
	ctx := context.Background()
	p := securityapp.Principal{UserID: securityTestUser, View: true, Manage: true, AcceptRisk: true, ChangesView: true}
	reg := relationships.NewRegistry()
	reg.Register(securityapp.Triples...)
	tasks := &remediationTasks{items: map[string][]taskspublic.Task{}}
	svc := base.WithRemediation(tasks, remediationChanges{items: map[string]changespublic.ChangeInfo{}}, relationships.New(reg), remediationNames{}, pool)
	before, err := svc.Overview(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := svc.Create(ctx, caller, p, securityapp.AdvisoryInput{Title: "F8b progress test", Severity: "high", Criteria: []securityapp.CriterionInput{{SoftwareProductID: securityTestProduct, Rules: []securityapp.Rule{{Kind: "fixed", Version: "2.0"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupAdvisory(t, pool, a.ID)
	a, err = svc.MarkApplicable(ctx, caller, p, a.ID, &a.Version)
	if err != nil {
		t.Fatal(err)
	}
	inv.set(securityapp.Installation{ID: "00000000-0000-7000-8000-0000000000a4", DeviceID: securityTestDevice, SoftwareProductID: securityTestProduct, DevicePlatform: "windows", RawVersion: "1.0", ObservedAt: time.Now().UTC().Add(-time.Hour)})
	if _, err = svc.Match(ctx, securityapp.MatchCaller(caller.CorrelationID), a.ID); err != nil {
		t.Fatal(err)
	}
	progress, err := svc.Progress(ctx, p, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Source != "turaco_derived" || progress.FindingByStatus[securityapp.FindingOpen] != 1 || progress.FindingByConfidence[securityapp.ConfidenceProbable] != 1 || progress.ResidualRisk != "high" || progress.ShareRemediated != 0 || progress.OldestOpenFindingAgeDays == nil {
		t.Fatalf("progress: %+v", progress)
	}
	after, err := svc.Overview(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if after.ApplicableBySeverity["high"] != before.ApplicableBySeverity["high"]+1 || after.OpenFindingsByConfidence[securityapp.ConfidenceProbable] != before.OpenFindingsByConfidence[securityapp.ConfidenceProbable]+1 {
		t.Fatalf("overview before %+v after %+v", before, after)
	}
	findings, err := svc.ListFindings(ctx, p, securityapp.FindingFilter{AdvisoryID: a.ID})
	if err != nil || len(findings.Items) != 1 {
		t.Fatal(err)
	}
	f := findings.Items[0].Finding
	_, err = svc.AcceptRisk(ctx, caller, p, f.ID, &f.Version, "business_need", time.Now().UTC().AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	progress, err = svc.Progress(ctx, p, a.ID)
	if err != nil || progress.AcceptedRiskCount != 1 || progress.EarliestRiskReviewBy == nil {
		t.Fatalf("risk progress: %+v %v", progress, err)
	}
	contract := securitypublic.NewAdvisories(svc)
	due, err := contract.RiskReviewsDue(ctx, securitypublic.ReadScope{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range due {
		if v.ReviewBy.Equal(*progress.EarliestRiskReviewBy) {
			if v.FindingID != "" || v.AdvisoryID != "" {
				t.Fatal("zero scope leaked risk review")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("risk review missing")
	}
}
