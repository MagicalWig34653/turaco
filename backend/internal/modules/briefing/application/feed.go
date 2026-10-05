package application

import (
	"context"
	"sort"
	"time"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	planningpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/public"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	deskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
)

const feedLimit = 20

type FeedPrincipal struct {
	UserID                                                                          string
	Briefing, Security, Planning, Changes, Desk, Endpoints, Integrations, Directory bool
}

func (p FeedPrincipal) Allowed() bool {
	return p.UserID != "" && (p.Briefing || p.Security || p.Planning || p.Changes || p.Desk || p.Endpoints || p.Integrations || p.Directory)
}

type FeedReference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type FeedEntry struct {
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity"`
	TitleKey   string         `json:"titleKey"`
	Params     map[string]any `json:"params"`
	Count      *int           `json:"count,omitempty"`
	Reference  *FeedReference `json:"reference,omitempty"`
	LinkPath   string         `json:"linkPath"`
	OccurredAt *time.Time     `json:"occurredAt,omitempty"`
	DueAt      *time.Time     `json:"dueAt,omitempty"`
	Source     string         `json:"source"`
}
type FeedFailure struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}
type FeedResult struct {
	Entries     []FeedEntry     `json:"entries"`
	Truncated   map[string]bool `json:"truncated"`
	Unavailable []FeedFailure   `json:"unavailable"`
}
type SecurityFeed interface {
	ApplicableAdvisorySummaries(context.Context, securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error)
	RiskReviewsDuePage(context.Context, securitypublic.ReadScope) (securitypublic.RiskReviewPage, error)
}
type PlanningFeed interface {
	UpcomingMaintenance(context.Context, time.Time, time.Time, planningpublic.MaintenanceScope) ([]planningpublic.UpcomingMaintenance, bool, error)
	DueMilestones(context.Context, time.Time, time.Time, planningpublic.DueMilestoneScope) ([]planningpublic.DueMilestone, error)
}
type DeskFeed interface {
	OpenMajorIncidents(context.Context, deskpublic.ReadScope) ([]deskpublic.MajorIncident, error)
	SyncHealth(context.Context) (deskpublic.SyncStatus, error)
}
type EndpointFeed interface {
	SyncHealth(context.Context) ([]endpointspublic.SyncStatus, error)
	OpenProviderErrors(context.Context) (int, error)
}
type DirectoryFeed interface {
	DirectorySyncStatus(context.Context, orgpublic.SyncScope) ([]orgpublic.DirectorySyncStatus, error)
}
type ApprovalFeed interface {
	PendingForUserCount(context.Context, string) (int, error)
}
type FeedSources struct {
	Security  SecurityFeed
	Planning  PlanningFeed
	Desk      DeskFeed
	Endpoints EndpointFeed
	Directory DirectoryFeed
	Approvals ApprovalFeed
}
type FeedService struct {
	manual  *Service
	sources FeedSources
}

func NewFeedService(manual *Service, sources FeedSources) *FeedService {
	return &FeedService{manual: manual, sources: sources}
}

// Feed computes the caller's briefing. Each authorized source fails independently.
// No source is allowed to contribute more than 20 entries to the response.
func (s *FeedService) Feed(ctx context.Context, p FeedPrincipal, now time.Time) (FeedResult, error) {
	out := FeedResult{Entries: []FeedEntry{}, Truncated: map[string]bool{}, Unavailable: []FeedFailure{}}
	if !p.Allowed() {
		return out, ErrForbidden
	}
	now = now.UTC()
	until := now.AddDate(0, 0, 14)
	add := func(source string, entries []FeedEntry, more bool) {
		if len(entries) > feedLimit {
			entries = entries[:feedLimit]
			more = true
		}
		out.Entries = append(out.Entries, entries...)
		if more {
			out.Truncated[source] = true
		}
	}
	fail := func(source string) { out.Unavailable = append(out.Unavailable, FeedFailure{source, "source_error"}) }
	count := func(n int) *int { return &n }
	if p.Briefing && s.manual != nil {
		r, e := s.manual.store.List(ctx, ListQuery{PublishedOnly: true, Page: Page{Limit: feedLimit + 1}})
		if e != nil {
			fail("briefing")
		} else {
			v := []FeedEntry{}
			for _, it := range r.Items {
				if it.Status != StatusPublished || (it.ValidUntil != nil && !it.ValidUntil.After(now)) {
					continue
				}
				at := it.CreatedAt
				if it.PublishedAt != nil {
					at = *it.PublishedAt
				}
				v = append(v, FeedEntry{Kind: "manual_item", Severity: it.Severity, TitleKey: "briefing.feed.manual_item", Params: map[string]any{"title": it.Title}, Reference: &FeedReference{"briefing_item", it.ID}, LinkPath: "/briefing/" + it.ID, OccurredAt: &at, Source: "briefing"})
			}
			add("briefing", v, r.NextCursor != "")
		}
	}
	if p.Security && s.sources.Security != nil {
		a, e := s.sources.Security.ApplicableAdvisorySummaries(ctx, securitypublic.ReadScope{IncludeDetails: true})
		if e != nil {
			fail("security_advisory")
		} else {
			v := []FeedEntry{}
			more := false
			for _, x := range a {
				sev := SeverityWarning
				if x.Severity == "critical" {
					sev = SeverityCritical
				}
				v = append(v, FeedEntry{Kind: "security_advisory", Severity: sev, TitleKey: "briefing.feed.security_advisory", Params: map[string]any{"reference": x.Reference, "title": x.Title}, Count: count(x.AffectedDevices), Reference: &FeedReference{"security_advisory", x.ID}, LinkPath: "/security/advisories/" + x.ID, Source: "security"})
				more = more || x.Truncated
			}
			add("security_advisory", v, more)
		}
		r, e := s.sources.Security.RiskReviewsDuePage(ctx, securitypublic.ReadScope{IncludeDetails: true, Limit: feedLimit + 1})
		if e != nil {
			fail("risk_review_due")
		} else {
			v := []FeedEntry{}
			for _, x := range r.Items {
				d := x.ReviewBy
				sev := SeverityWarning
				if !d.After(now) {
					sev = SeverityCritical
				}
				v = append(v, FeedEntry{Kind: "risk_review_due", Severity: sev, TitleKey: "briefing.feed.risk_review_due", Params: map[string]any{"reference": x.FindingReference}, Reference: &FeedReference{"security_finding", x.FindingID}, LinkPath: "/security/findings/" + x.FindingID, DueAt: &d, Source: "security"})
			}
			add("risk_review_due", v, r.NextCursor != "")
		}
	}
	if s.sources.Planning != nil {
		if p.Planning || p.Changes {
			m, more, e := s.sources.Planning.UpcomingMaintenance(ctx, now, until, planningpublic.MaintenanceScope{IncludeChangeDetails: p.Changes})
			if e != nil {
				fail("maintenance")
			} else {
				v := []FeedEntry{}
				for _, x := range m {
					d := x.WindowStart
					params := map[string]any{"reference": x.Reference}
					if p.Changes {
						params["title"] = x.Title
					}
					v = append(v, FeedEntry{Kind: "maintenance", Severity: SeverityInfo, TitleKey: "briefing.feed.maintenance", Params: params, Reference: &FeedReference{"change", x.ChangeID}, LinkPath: "/changes/" + x.ChangeID, DueAt: &d, Source: "planning"})
				}
				add("maintenance", v, more)
			}
		}
		if p.Planning || p.UserID != "" {
			scope := planningpublic.DueMilestoneScope{OwnerID: p.UserID}
			if p.Planning {
				scope = planningpublic.DueMilestoneScope{AllOwners: true}
			}
			m, e := s.sources.Planning.DueMilestones(ctx, now, until, scope)
			if e != nil {
				fail("milestone_due")
			} else {
				v := []FeedEntry{}
				for _, x := range m {
					d := x.DueDate
					v = append(v, FeedEntry{Kind: "milestone_due", Severity: SeverityWarning, TitleKey: "briefing.feed.milestone_due", Params: map[string]any{"reference": x.InitiativeReference, "title": x.Title}, Reference: &FeedReference{"milestone", x.ID}, LinkPath: "/initiatives/" + x.InitiativeID, DueAt: &d, Source: "planning"})
				}
				add("milestone_due", v, false)
			}
		}
	}
	if p.Desk && s.sources.Desk != nil {
		m, e := s.sources.Desk.OpenMajorIncidents(ctx, deskpublic.ReadScope{IncludeDetails: true})
		if e != nil {
			fail("major_incident")
		} else {
			v := []FeedEntry{}
			for _, x := range m {
				d := x.StartedAt
				v = append(v, FeedEntry{Kind: "major_incident", Severity: SeverityCritical, TitleKey: "briefing.feed.major_incident", Params: map[string]any{"reference": x.Reference, "title": x.Title}, Count: count(x.LinkedTickets), Reference: &FeedReference{"major_incident", x.ID}, LinkPath: "/incidents/" + x.ID, OccurredAt: &d, Source: "servicedesk"})
			}
			add("major_incident", v, false)
		}
	}
	if p.Integrations && s.sources.Desk != nil {
		h, e := s.sources.Desk.SyncHealth(ctx)
		if e != nil {
			fail("autotask")
		} else if h.Failed > 0 || h.Pending > 0 {
			add("autotask", []FeedEntry{{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.autotask", Params: map[string]any{"failed": h.Failed, "pending": h.Pending}, Count: count(h.Failed + h.Pending), OccurredAt: h.OldestFailureAt, LinkPath: "/settings/integrations", Source: "servicedesk"}}, false)
		}
	}
	if p.Endpoints && s.sources.Endpoints != nil {
		h, e := s.sources.Endpoints.SyncHealth(ctx)
		if e != nil {
			fail("endpoints")
		} else {
			v := []FeedEntry{}
			for _, x := range h {
				if x.LastCompletedAt != nil && now.Sub(*x.LastCompletedAt) < 24*time.Hour {
					continue
				}
				v = append(v, FeedEntry{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.endpoint_sync", Params: map[string]any{"provider": x.Provider}, OccurredAt: x.LastCompletedAt, LinkPath: "/endpoints", Source: "endpoints"})
			}
			add("endpoints", v, false)
		}
		n, e := s.sources.Endpoints.OpenProviderErrors(ctx)
		if e != nil {
			fail("endpoint_errors")
		} else if n > 0 {
			add("endpoint_errors", []FeedEntry{{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.endpoint_errors", Params: map[string]any{}, Count: count(n), LinkPath: "/endpoint-findings", Source: "endpoints"}}, false)
		}
	}
	if p.Directory && s.sources.Directory != nil {
		h, e := s.sources.Directory.DirectorySyncStatus(ctx, orgpublic.SyncScope{})
		if e != nil {
			fail("directory")
		} else {
			v := []FeedEntry{}
			for _, x := range h {
				if x.LastErrorCode == "" {
					continue
				}
				v = append(v, FeedEntry{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.directory_sync", Params: map[string]any{"provider": x.Provider, "reason": x.LastErrorCode}, OccurredAt: x.LastFailureAt, LinkPath: "/admin/directory-sync", Source: "organization"})
			}
			add("directory", v, false)
		}
	}
	if s.sources.Approvals != nil {
		n, e := s.sources.Approvals.PendingForUserCount(ctx, p.UserID)
		if e != nil {
			fail("pending_approvals")
		} else if n > 0 {
			add("pending_approvals", []FeedEntry{{Kind: "pending_approvals", Severity: SeverityInfo, TitleKey: "briefing.feed.pending_approvals", Params: map[string]any{}, Count: count(n), LinkPath: "/approvals", Source: "approvals"}}, false)
		}
	}
	rank := func(s string) int {
		switch s {
		case SeverityCritical:
			return 0
		case SeverityWarning:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if rank(a.Severity) != rank(b.Severity) {
			return rank(a.Severity) < rank(b.Severity)
		}
		ta, tb := time.Time{}, time.Time{}
		if a.DueAt != nil {
			ta = *a.DueAt
		} else if a.OccurredAt != nil {
			ta = *a.OccurredAt
		}
		if b.DueAt != nil {
			tb = *b.DueAt
		} else if b.OccurredAt != nil {
			tb = *b.OccurredAt
		}
		if ta.IsZero() != tb.IsZero() {
			return !ta.IsZero()
		}
		return ta.Before(tb)
	})
	return out, nil
}

var _ ApprovalFeed = (*approvalspublic.Approvals)(nil)
