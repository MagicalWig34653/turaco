package application

import (
	"context"
	"errors"
	"testing"
	"time"

	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
)

type feedSecurity struct{ fail bool }

func (f feedSecurity) ApplicableAdvisorySummaries(_ context.Context, scope securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error) {
	if f.fail {
		return nil, errors.New("unavailable")
	}
	return []securitypublic.ApplicableSummary{{ID: "adv", Reference: "ADV-1", Title: "private", Severity: "critical", AffectedDevices: 5}}, nil
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
