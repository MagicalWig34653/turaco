package application

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
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
	UserID                                                                               string
	Briefing, Security, Planning, Changes, Desk, Tickets, Autotask, Endpoints, Directory bool
	// Deployments is deployments.view, manage or execute: it adds Deployment names to the rollout entries.
	Deployments bool
}

func (p FeedPrincipal) Allowed() bool {
	return p.UserID != "" && (p.Briefing || p.Security || p.Planning || p.Changes || p.Desk || p.Tickets || p.Endpoints || p.Autotask || p.Directory || p.Deployments)
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

// kevTitleKey is the title key of an advisory listed in the CISA Known Exploited Vulnerabilities catalog.
const kevTitleKey = "briefing.feed.security_advisory_kev"

type SecurityFeed interface {
	ApplicableAdvisorySummaries(context.Context, securitypublic.ReadScope) ([]securitypublic.ApplicableSummary, error)
	RiskReviewsDuePage(context.Context, securitypublic.ReadScope) (securitypublic.RiskReviewPage, error)
	AdvisoryFeedHealth(context.Context) ([]securitypublic.FeedHealth, error)
}
type PlanningFeed interface {
	UpcomingMaintenance(context.Context, time.Time, time.Time, planningpublic.MaintenanceScope) ([]planningpublic.UpcomingMaintenance, bool, error)
	DueMilestones(context.Context, time.Time, time.Time, planningpublic.DueMilestoneScope) ([]planningpublic.DueMilestone, error)
}
type DeskFeed interface {
	OpenMajorIncidents(context.Context, deskpublic.ReadScope) ([]deskpublic.MajorIncident, error)
	UnassignedOpenTickets(context.Context, deskpublic.ReadScope) (int, error)
	SyncHealth(context.Context) (deskpublic.SyncStatus, error)
}
type EndpointFeed interface {
	SyncHealth(context.Context) ([]endpointspublic.SyncStatus, error)
	OpenProviderErrors(context.Context) (int, error)
}

// DeploymentFeed is the Endpoints read contract for rollouts: counts and references, names only with IncludeNames, and
// the health of the execution engine.
type DeploymentFeed interface {
	RolloutSummary(context.Context, endpointspublic.DeploymentScope) (endpointspublic.RolloutSummary, error)
	DeploymentEngineStatus(context.Context) (endpointspublic.EngineStatus, error)
}
type DirectoryFeed interface {
	DirectorySyncStatus(context.Context, orgpublic.SyncScope) ([]orgpublic.DirectorySyncStatus, error)
}
type ApprovalFeed interface {
	PendingForUserCount(context.Context, string) (int, error)
}
type FeedSources struct {
	Security    SecurityFeed
	Planning    PlanningFeed
	Desk        DeskFeed
	Endpoints   EndpointFeed
	Deployments DeploymentFeed
	Directory   DirectoryFeed
	Approvals   ApprovalFeed
}
type FeedService struct {
	manual  *Service
	sources FeedSources
	mu      sync.Mutex
	cache   map[FeedPrincipal]cachedFeed
}

func NewFeedService(manual *Service, sources FeedSources) *FeedService {
	return &FeedService{manual: manual, sources: sources, cache: make(map[FeedPrincipal]cachedFeed)}
}

type cachedFeed struct {
	result  FeedResult
	expires time.Time
}

// sourceCall bounds each synchronous read. The source must honor context cancellation.
func sourceCall[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	sourceCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := fn(sourceCtx)
	if errors.Is(sourceCtx.Err(), context.DeadlineExceeded) {
		return result, context.DeadlineExceeded
	}
	return result, err
}

// Feed computes the caller's briefing. Each authorized source fails independently.
// No source is allowed to contribute more than 20 entries to the response.
func (s *FeedService) Feed(ctx context.Context, p FeedPrincipal, now time.Time) (FeedResult, error) {
	out := FeedResult{Entries: []FeedEntry{}, Truncated: map[string]bool{}, Unavailable: []FeedFailure{}}
	if !p.Allowed() {
		return out, ErrForbidden
	}
	now = now.UTC()
	s.mu.Lock()
	if cached, ok := s.cache[p]; ok && now.Before(cached.expires) {
		s.mu.Unlock()
		return cached.result, nil
	}
	s.mu.Unlock()
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
	fail := func(source string, err error) {
		reason := "source_error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "source_timeout"
		}
		slog.ErrorContext(ctx, "briefing source failed", "source", source, "error", err)
		out.Unavailable = append(out.Unavailable, FeedFailure{source, reason})
	}
	count := func(n int) *int { return &n }
	if p.Briefing && s.manual != nil {
		r, e := sourceCall(ctx, func(c context.Context) (Result, error) {
			return s.manual.store.List(c, ListQuery{PublishedOnly: true, Page: Page{Limit: feedLimit + 1}})
		})
		if e != nil {
			fail("briefing", e)
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
		a, e := sourceCall(ctx, func(c context.Context) ([]securitypublic.ApplicableSummary, error) {
			return s.sources.Security.ApplicableAdvisorySummaries(c, securitypublic.ReadScope{IncludeDetails: true})
		})
		if e != nil {
			fail("security_advisory", e)
		} else {
			v := []FeedEntry{}
			more := false
			for _, x := range a {
				sev := SeverityWarning
				if x.Severity == "critical" || x.KnownExploited {
					sev = SeverityCritical
				}
				entry := FeedEntry{Kind: "security_advisory", Severity: sev, TitleKey: "briefing.feed.security_advisory", Params: map[string]any{"reference": x.Reference, "title": x.Title}, Count: count(x.AffectedDevices), Reference: &FeedReference{"security_advisory", x.ID}, LinkPath: "/security/advisories/" + x.ID, Source: "security"}
				if x.KnownExploited {
					entry.TitleKey, entry.DueAt = kevTitleKey, x.KEVDueDate
				}
				v = append(v, entry)
				more = more || x.Truncated
			}
			add("security_advisory", v, more)
		}
		r, e := sourceCall(ctx, func(c context.Context) (securitypublic.RiskReviewPage, error) {
			return s.sources.Security.RiskReviewsDuePage(c, securitypublic.ReadScope{IncludeDetails: true, Limit: feedLimit + 1})
		})
		if e != nil {
			fail("risk_review_due", e)
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
		fh, e := sourceCall(ctx, s.sources.Security.AdvisoryFeedHealth)
		if e != nil {
			fail("advisory_feed", e)
		} else {
			v := []FeedEntry{}
			for _, x := range fh {
				reason := x.LastError
				if reason == "" && x.Stale {
					reason = "stale"
				}
				if reason == "" {
					continue
				}
				at := x.LastSuccessAt
				if at == nil {
					at = x.LastAttemptAt
				}
				v = append(v, FeedEntry{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.advisory_feed", Params: map[string]any{"source": x.Source, "reason": reason}, OccurredAt: at, LinkPath: "/security/advisories", Source: "security"})
			}
			add("advisory_feed", v, false)
		}
	}
	if s.sources.Planning != nil {
		if p.Planning || p.Changes {
			type maintenancePage struct {
				items []planningpublic.UpcomingMaintenance
				more  bool
			}
			page, e := sourceCall(ctx, func(c context.Context) (maintenancePage, error) {
				items, more, err := s.sources.Planning.UpcomingMaintenance(c, now, until, planningpublic.MaintenanceScope{IncludeChangeDetails: p.Changes})
				return maintenancePage{items, more}, err
			})
			m, more := page.items, page.more
			if e != nil {
				fail("maintenance", e)
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
			scope := planningpublic.DueMilestoneScope{OwnerID: p.UserID, Limit: feedLimit + 1}
			if p.Planning {
				scope = planningpublic.DueMilestoneScope{AllOwners: true, Limit: feedLimit + 1}
			}
			m, e := sourceCall(ctx, func(c context.Context) ([]planningpublic.DueMilestone, error) {
				return s.sources.Planning.DueMilestones(c, now, until, scope)
			})
			if e != nil {
				fail("milestone_due", e)
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
		m, e := sourceCall(ctx, func(c context.Context) ([]deskpublic.MajorIncident, error) {
			return s.sources.Desk.OpenMajorIncidents(c, deskpublic.ReadScope{IncludeDetails: true})
		})
		if e != nil {
			fail("major_incident", e)
		} else {
			v := []FeedEntry{}
			for _, x := range m {
				d := x.StartedAt
				v = append(v, FeedEntry{Kind: "major_incident", Severity: SeverityCritical, TitleKey: "briefing.feed.major_incident", Params: map[string]any{"reference": x.Reference, "title": x.Title}, Count: count(x.OpenLinkedTickets), Reference: &FeedReference{"major_incident", x.ID}, LinkPath: "/incidents/" + x.ID, OccurredAt: &d, Source: "servicedesk"})
			}
			add("major_incident", v, false)
		}
	}
	if p.Tickets && s.sources.Desk != nil {
		n, e := sourceCall(ctx, func(c context.Context) (int, error) {
			return s.sources.Desk.UnassignedOpenTickets(c, deskpublic.ReadScope{IncludeDetails: true})
		})
		if e != nil {
			fail("ticket_backlog", e)
		} else if n > 0 {
			add("ticket_backlog", []FeedEntry{{Kind: "ticket_backlog", Severity: SeverityInfo, TitleKey: "briefing.feed.ticket_backlog", Params: map[string]any{}, Count: count(n), LinkPath: "/service-desk?assignee=none", Source: "servicedesk"}}, false)
		}
	}
	if p.Autotask && s.sources.Desk != nil {
		h, e := sourceCall(ctx, s.sources.Desk.SyncHealth)
		if e != nil {
			fail("autotask", e)
		} else if h.Failed > 0 || h.Pending > 0 {
			add("autotask", []FeedEntry{{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.autotask", Params: map[string]any{"failed": h.Failed, "pending": h.Pending}, Count: count(h.Failed + h.Pending), OccurredAt: h.OldestFailureAt, LinkPath: "/settings/integrations", Source: "servicedesk"}}, false)
		}
	}
	if p.Endpoints && s.sources.Endpoints != nil {
		h, e := sourceCall(ctx, s.sources.Endpoints.SyncHealth)
		if e != nil {
			fail("endpoints", e)
		} else {
			v := []FeedEntry{}
			for _, x := range h {
				v = append(v, FeedEntry{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.endpoint_sync", Params: map[string]any{"provider": x.Provider}, OccurredAt: x.LastCompletedAt, LinkPath: "/endpoints", Source: "endpoints"})
			}
			add("endpoints", v, false)
		}
		n, e := sourceCall(ctx, s.sources.Endpoints.OpenProviderErrors)
		if e != nil {
			fail("endpoint_errors", e)
		} else if n > 0 {
			add("endpoint_errors", []FeedEntry{{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.endpoint_errors", Params: map[string]any{}, Count: count(n), LinkPath: "/endpoint-findings", Source: "endpoints"}}, false)
		}
	}
	if (p.Endpoints || p.Deployments) && s.sources.Deployments != nil {
		sum, e := sourceCall(ctx, func(c context.Context) (endpointspublic.RolloutSummary, error) {
			return s.sources.Deployments.RolloutSummary(c, endpointspublic.DeploymentScope{IncludeNames: p.Deployments, Limit: feedLimit})
		})
		if e != nil {
			fail("deployments", e)
		} else {
			v := []FeedEntry{}
			for _, x := range sum.Items {
				sev, key := SeverityInfo, "briefing.feed.deployment_awaiting_promotion"
				switch x.Kind {
				case endpointspublic.RolloutRingHalted:
					sev, key = SeverityCritical, "briefing.feed.deployment_ring_halted"
				case endpointspublic.RolloutPaused:
					sev, key = SeverityWarning, "briefing.feed.deployment_paused"
				}
				params := map[string]any{"reference": x.Reference}
				if p.Deployments {
					params["name"], params["product"] = x.Name, x.ProductName
				}
				since := x.Since
				v = append(v, FeedEntry{Kind: "deployment_" + x.Kind, Severity: sev, TitleKey: key, Params: params, Reference: &FeedReference{"deployment", x.ID}, LinkPath: "/deployments/" + x.ID, OccurredAt: &since, Source: "endpoints"})
			}
			if sum.InProgress > 0 {
				v = append(v, FeedEntry{Kind: "deployments_in_progress", Severity: SeverityInfo, TitleKey: "briefing.feed.deployments_in_progress", Params: map[string]any{}, Count: count(sum.InProgress), LinkPath: "/deployments", Source: "endpoints"})
			}
			add("deployments", v, sum.More)
		}
	}
	if p.Endpoints && s.sources.Deployments != nil {
		eh, e := sourceCall(ctx, s.sources.Deployments.DeploymentEngineStatus)
		if e != nil {
			fail("deployment_engine", e)
		} else {
			v := []FeedEntry{}
			for _, problem := range []struct {
				reason string
				n      int
				bad    bool
			}{{"stale", eh.Active, eh.Stale}, {"clear_pending", eh.ClearPending, eh.ClearPending > 0}, {"resolving_stuck", eh.ResolvingStuck, eh.ResolvingStuck > 0}} {
				if problem.bad {
					v = append(v, FeedEntry{Kind: "integration_health", Severity: SeverityWarning, TitleKey: "briefing.feed.deployment_engine_" + problem.reason, Params: map[string]any{},
						Count: count(problem.n), OccurredAt: eh.LastTickAt, LinkPath: "/deployments", Source: "endpoints"})
				}
			}
			add("deployment_engine", v, false)
		}
	}
	if p.Directory && s.sources.Directory != nil {
		h, e := sourceCall(ctx, func(c context.Context) ([]orgpublic.DirectorySyncStatus, error) {
			return s.sources.Directory.DirectorySyncStatus(c, orgpublic.SyncScope{})
		})
		if e != nil {
			fail("directory", e)
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
		n, e := sourceCall(ctx, func(c context.Context) (int, error) { return s.sources.Approvals.PendingForUserCount(c, p.UserID) })
		if e != nil {
			fail("pending_approvals", e)
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
		if ka, kb := a.TitleKey == kevTitleKey, b.TitleKey == kevTitleKey; ka != kb {
			return ka // known exploited advisories come first within their severity
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
	if len(out.Unavailable) == 0 {
		s.mu.Lock()
		if len(s.cache) >= 256 {
			for key, item := range s.cache {
				if !now.Before(item.expires) {
					delete(s.cache, key)
				}
			}
			if len(s.cache) >= 256 {
				for key := range s.cache {
					delete(s.cache, key)
					break
				}
			}
		}
		s.cache[p] = cachedFeed{result: out, expires: now.Add(10 * time.Second)}
		s.mu.Unlock()
	}
	return out, nil
}

var _ ApprovalFeed = (*approvalspublic.Approvals)(nil)
