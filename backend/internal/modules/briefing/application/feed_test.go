package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
)

type feedSecurity struct {
	fail   bool
	items  []securitypublic.ApplicableSummary
	health []securitypublic.FeedHealth
}

func (f feedSecurity) ApplicableAdvisorySummaries(_ context.Context, scope securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error) {
	if f.fail {
		return nil, errors.New("unavailable")
	}
	if f.items != nil {
		return f.items, nil
	}
	return []securitypublic.ApplicableSummary{{ID: "adv", Reference: "ADV-1", Title: "private", Severity: "critical", AffectedDevices: 5}}, nil
}
func (f feedSecurity) AdvisoryFeedHealth(context.Context) ([]securitypublic.FeedHealth, error) {
	return f.health, nil
}
func (f feedSecurity) RiskReviewsDuePage(context.Context, securitypublic.ReadScope) (securitypublic.RiskReviewPage, error) {
	return securitypublic.RiskReviewPage{}, nil
}

type feedPlanning struct {
	items []planningpublic.UpcomingMaintenance
	scope planningpublic.MaintenanceScope
}

func (f *feedPlanning) UpcomingMaintenance(_ context.Context, _, _ time.Time, scope planningpublic.MaintenanceScope) ([]planningpublic.UpcomingMaintenance, bool, error) {
	f.scope = scope
	return f.items, false, nil
}
func (f *feedPlanning) DueMilestones(context.Context, time.Time, time.Time, planningpublic.DueMilestoneScope) ([]planningpublic.DueMilestone, error) {
	return nil, nil
}

type feedApprovals struct{}

func (feedApprovals) PendingForUserCount(context.Context, string) (int, error) { return 2, nil }

func TestFeedPermissionsAndRedaction(t *testing.T) {
	now := time.Now()
	p := &feedPlanning{items: []planningpublic.UpcomingMaintenance{{ChangeID: "change", Reference: "CHG-1", Title: "private change", WindowStart: now.Add(time.Hour)}}}
	s := NewFeedService(nil, FeedSources{Security: feedSecurity{}, Planning: p, Approvals: feedApprovals{}})
	out, err := s.Feed(context.Background(), FeedPrincipal{UserID: "user", Briefing: true, Planning: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Entries {
		if e.Kind == "security_advisory" {
			t.Fatal("security entry leaked")
		}
		if e.Kind == "maintenance" {
			if _, ok := e.Params["title"]; ok {
				t.Fatal("change title leaked")
			}
		}
	}
	if p.scope.IncludeChangeDetails {
		t.Fatal("change details requested without permission")
	}
	out, err = s.Feed(context.Background(), FeedPrincipal{UserID: "user", Security: true, Changes: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, e := range out.Entries {
		if e.Kind == "security_advisory" {
			seen = true
			if e.Params["title"] != "private" {
				t.Fatal("authorized title absent")
			}
		}
	}
	if !seen || !p.scope.IncludeChangeDetails {
		t.Fatal("authorized source absent")
	}
}

func TestFeedFailureBoundsAndSort(t *testing.T) {
	now := time.Now()
	items := make([]planningpublic.UpcomingMaintenance, 25)
	for i := range items {
		items[i] = planningpublic.UpcomingMaintenance{ChangeID: "id", Reference: "CHG-1", WindowStart: now.Add(time.Duration(i) * time.Hour)}
	}
	s := NewFeedService(nil, FeedSources{Security: feedSecurity{fail: true}, Planning: &feedPlanning{items: items}, Approvals: feedApprovals{}})
	out, err := s.Feed(context.Background(), FeedPrincipal{UserID: "user", Security: true, Planning: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Unavailable) != 1 || out.Unavailable[0].Source != "security_advisory" || !out.Truncated["maintenance"] {
		t.Fatalf("degradation or truncation wrong: %+v", out)
	}
	if len(out.Entries) != 21 {
		t.Fatalf("want 20 maintenance plus approval, got %d", len(out.Entries))
	}
	if out.Entries[0].Severity != SeverityInfo {
		t.Fatal("unexpected sort")
	}
}

func TestFeedRequiresSourceAuthority(t *testing.T) {
	s := NewFeedService(nil, FeedSources{})
	_, err := s.Feed(context.Background(), FeedPrincipal{UserID: "user"}, time.Now())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want forbidden, got %v", err)
	}
}

func TestFeedSortsSeverityThenTime(t *testing.T) {
	now := time.Now()
	p := &feedPlanning{items: []planningpublic.UpcomingMaintenance{
		{ChangeID: "later", Reference: "CHG-2", WindowStart: now.Add(3 * time.Hour)},
		{ChangeID: "sooner", Reference: "CHG-1", WindowStart: now.Add(time.Hour)},
	}}
	s := NewFeedService(nil, FeedSources{Security: feedSecurity{}, Planning: p, Approvals: feedApprovals{}})
	out, err := s.Feed(context.Background(), FeedPrincipal{UserID: "user", Security: true, Planning: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 4 || out.Entries[0].Kind != "security_advisory" || out.Entries[1].Reference == nil || out.Entries[1].Reference.ID != "sooner" || out.Entries[2].Reference.ID != "later" || out.Entries[3].Kind != "pending_approvals" {
		t.Fatalf("wrong order: %+v", out.Entries)
	}
}

type countingApprovals struct{ calls int }

func (a *countingApprovals) PendingForUserCount(ctx context.Context, _ string) (int, error) {
	a.calls++
	return 1, ctx.Err()
}
func TestFeedCachesPerEffectivePermissionSet(t *testing.T) {
	approvals := &countingApprovals{}
	feed := NewFeedService(nil, FeedSources{Approvals: approvals})
	now := time.Now()
	for _, p := range []FeedPrincipal{{UserID: "a", Briefing: true}, {UserID: "a", Briefing: true}, {UserID: "a", Security: true}, {UserID: "b", Briefing: true}} {
		if _, err := feed.Feed(context.Background(), p, now); err != nil {
			t.Fatal(err)
		}
	}
	if approvals.calls != 3 {
		t.Fatalf("source calls = %d, want 3", approvals.calls)
	}
	if _, err := feed.Feed(context.Background(), FeedPrincipal{UserID: "a", Briefing: true}, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if approvals.calls != 4 {
		t.Fatalf("source calls after expiry = %d", approvals.calls)
	}
}

type timedOutSecurity struct{ feedSecurity }

func (timedOutSecurity) ApplicableAdvisorySummaries(ctx context.Context, _ securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestFeedMarksSourceTimeoutWithoutLeakingError(t *testing.T) {
	feed := NewFeedService(nil, FeedSources{Security: timedOutSecurity{}})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, err := feed.Feed(ctx, FeedPrincipal{UserID: "user", Security: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Unavailable) == 0 || result.Unavailable[0] != (FeedFailure{Source: "security_advisory", Reason: "source_timeout"}) {
		t.Fatalf("unavailable = %+v", result.Unavailable)
	}
}

func TestFeedKnownExploitedFirstAndFeedHealth(t *testing.T) {
	now := time.Now()
	due := now.Add(72 * time.Hour)
	last := now.Add(-time.Hour)
	old := now.Add(-100 * time.Hour)
	sec := feedSecurity{
		items: []securitypublic.ApplicableSummary{
			{ID: "plain", Reference: "ADV-1", Title: "plain critical", Severity: "critical", AffectedDevices: 9},
			{ID: "kev", Reference: "ADV-2", Title: "exploited medium", Severity: "medium", KnownExploited: true, KEVDueDate: &due, AffectedDevices: 1},
		},
		health: []securitypublic.FeedHealth{
			{Source: "nvd", LastSuccessAt: &last},
			{Source: "cisa_kev", LastSuccessAt: &old, Stale: true},
			{Source: "other", LastSuccessAt: &last, LastError: "rate_limited"},
		},
	}
	out, err := NewFeedService(nil, FeedSources{Security: sec}).Feed(context.Background(), FeedPrincipal{UserID: "user", Security: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	var advisories []FeedEntry
	reasons := map[string]string{}
	for _, e := range out.Entries {
		switch e.Kind {
		case "security_advisory":
			advisories = append(advisories, e)
		case "integration_health":
			reasons[e.Params["source"].(string)] = e.Params["reason"].(string)
		}
	}
	if len(advisories) != 2 || advisories[0].Reference.ID != "kev" || advisories[0].Severity != SeverityCritical || advisories[0].TitleKey != "briefing.feed.security_advisory_kev" || advisories[0].DueAt == nil {
		t.Fatalf("known exploited advisory must come first and be critical: %+v", advisories)
	}
	if len(reasons) != 2 || reasons["cisa_kev"] != "stale" || reasons["other"] != "rate_limited" {
		t.Fatalf("health entries: %v", reasons)
	}
}

type feedDeployments struct {
	scope   endpointspublic.DeploymentScope
	calls   int
	engine  endpointspublic.EngineStatus
	summary endpointspublic.RolloutSummary
}

func (f *feedDeployments) RolloutSummary(_ context.Context, scope endpointspublic.DeploymentScope) (endpointspublic.RolloutSummary, error) {
	f.scope = scope
	f.calls++
	out := f.summary
	if !scope.IncludeItems {
		out.Items = nil
	}
	if !scope.IncludeNames {
		items := make([]endpointspublic.RolloutItem, len(out.Items))
		copy(items, out.Items)
		for i := range items {
			items[i].Name, items[i].ProductName = "", ""
		}
		out.Items = items
	}
	return out, nil
}
func (f *feedDeployments) DeploymentEngineStatus(context.Context) (endpointspublic.EngineStatus, error) {
	return f.engine, nil
}

func TestFeedDeploymentsSourcePermissionsAndHealth(t *testing.T) {
	now := time.Now()
	f := &feedDeployments{
		summary: endpointspublic.RolloutSummary{InProgress: 2, UnassignedFollowups: 3,
			AttentionCounts: map[string]int{endpointspublic.RolloutRingHalted: 1, endpointspublic.RolloutPaused: 1, endpointspublic.RolloutAwaitingPromotion: 1}, Items: []endpointspublic.RolloutItem{
				{ID: "d1", Reference: "DEP-1", Kind: endpointspublic.RolloutRingHalted, Name: "Secret rollout", ProductName: "Secret product", Since: now},
				{ID: "d2", Reference: "DEP-2", Kind: endpointspublic.RolloutPaused, Since: now},
				{ID: "d3", Reference: "DEP-3", Kind: endpointspublic.RolloutAwaitingPromotion, Since: now}}},
		engine: endpointspublic.EngineStatus{Active: 1, Stale: true, ClearPending: 2},
	}
	s := NewFeedService(nil, FeedSources{Deployments: f})
	entries := func(p FeedPrincipal) map[string]FeedEntry {
		out, err := s.Feed(context.Background(), p, now)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]FeedEntry{}
		for _, e := range out.Entries {
			m[e.TitleKey] = e
		}
		return m
	}
	// deployments.view: names, rollouts, severities by kind, but no engine health.
	got := entries(FeedPrincipal{UserID: "u", Deployments: true})
	if f.scope.IncludeNames != true {
		t.Fatal("names not requested for deployments.view")
	}
	if e := got["briefing.feed.deployment_ring_halted"]; e.Severity != SeverityCritical || e.Params["name"] != "Secret rollout" || e.Reference == nil || e.Reference.Type != "deployment" || e.LinkPath != "/deployments/d1" {
		t.Fatalf("halted entry %+v", e)
	}
	if e := got["briefing.feed.deployment_paused"]; e.Severity != SeverityWarning {
		t.Fatalf("paused entry %+v", e)
	}
	if e := got["briefing.feed.deployment_awaiting_promotion"]; e.Severity != SeverityInfo {
		t.Fatalf("awaiting entry %+v", e)
	}
	if e := got["briefing.feed.deployments_in_progress"]; e.Count == nil || *e.Count != 2 {
		t.Fatalf("in progress entry %+v", e)
	}
	if _, ok := got["briefing.feed.deployment_engine_stale"]; ok {
		t.Fatal("engine health without endpoints.manage")
	}
	if e := got["briefing.feed.deployments_unassigned_followups"]; e.Count == nil || *e.Count != 3 || e.Reference != nil {
		t.Fatalf("unassigned follow-ups entry %+v", e)
	}
	// endpoints.manage only: counts per kind and engine health, no ids, references, names or links to single rollouts.
	s = NewFeedService(nil, FeedSources{Deployments: f})
	got = entries(FeedPrincipal{UserID: "u", Endpoints: true})
	if f.scope.IncludeNames || f.scope.IncludeItems {
		t.Fatal("names or items requested without deployments.view")
	}
	out, err := s.Feed(context.Background(), FeedPrincipal{UserID: "u", Endpoints: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Entries {
		if e.Reference != nil || strings.Contains(e.LinkPath, "/deployments/") || e.Params["reference"] != nil || e.Params["name"] != nil {
			t.Fatalf("entry with a rollout id or reference for endpoints.manage only: %+v", e)
		}
	}
	if e := got["briefing.feed.deployments_ring_halted_count"]; e.Count == nil || *e.Count != 1 || e.Severity != SeverityCritical {
		t.Fatalf("halted count entry %+v", e)
	}
	if e := got["briefing.feed.deployments_paused_count"]; e.Count == nil || *e.Count != 1 {
		t.Fatalf("paused count entry %+v", e)
	}
	if _, ok := got["briefing.feed.deployment_ring_halted"]; ok {
		t.Fatal("single rollout entry for endpoints.manage only")
	}
	if _, ok := got["briefing.feed.deployment_engine_stale"]; !ok {
		t.Fatal("stale engine not reported")
	}
	if c := got["briefing.feed.deployment_engine_clear_pending"]; c.Count == nil || *c.Count != 2 {
		t.Fatalf("clear pending entry %+v", c)
	}
	// No relevant permission: the source is not called.
	f.calls = 0
	s = NewFeedService(nil, FeedSources{Deployments: f, Approvals: feedApprovals{}})
	entries(FeedPrincipal{UserID: "u", Briefing: true})
	if f.calls != 0 {
		t.Fatal("deployment source read without permission")
	}
}

func TestFeedCacheFollowsModuleSwitches(t *testing.T) {
	sec := feedSecurity{items: []securitypublic.ApplicableSummary{{ID: "adv", Reference: "ADV-1", Title: "t", Severity: "critical", AffectedDevices: 1}}}
	securityOn := true
	feed := NewFeedService(nil, FeedSources{Security: sec}).WithSourceFilter(func(_ context.Context, s FeedSources) FeedSources {
		if !securityOn {
			s.Security = nil
		}
		return s
	})
	p := FeedPrincipal{UserID: "u", Briefing: true, Security: true}
	now := time.Now()
	count := func() int {
		out, err := feed.Feed(context.Background(), p, now)
		if err != nil {
			t.Fatal(err)
		}
		return len(out.Entries)
	}
	if count() == 0 {
		t.Fatal("security entries expected while the module is on")
	}
	securityOn = false
	if n := count(); n != 0 {
		t.Fatalf("a cached feed must not show a switched-off module's entries, got %d", n)
	}
	securityOn = true
	if count() == 0 {
		t.Fatal("entries must return when the module is switched on again")
	}
}
