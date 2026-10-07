// Package public is Security's read contract for other modules. Callers authorize the requesting
// user before selecting detail scope; no repository or Security-owned table crosses this boundary.
package public

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// ReadScope selects the fields the caller may show. Zero scope exposes only
// reference/state for Advisories and the review date for RiskReviewsDue.
var cursorIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ReadScope struct {
	IncludeDetails bool
	Cursor         string
	Limit          int
}

type AdvisoryInfo struct {
	ID              string
	Reference       string
	Status          string
	Severity        string
	Title           string
	Summary         *string
	AffectedDevices int
	MatchTruncated  bool
}

// ApplicableSummary is a bounded feed item for F8c. Zero scope omits severity,
// title and counts; the caller must authorize security.view before enabling them.
type ApplicableSummary struct {
	ID              string
	Reference       string
	Status          string
	Severity        string
	Title           string
	AffectedDevices int
	Truncated       bool
	// KnownExploited marks advisories listed in the CISA KEV catalog (shown with the detail scope only);
	// KEVDueDate is the catalog's due date.
	KnownExploited bool
	KEVDueDate     *time.Time
}

func (a *Advisories) ApplicableAdvisorySummaries(ctx context.Context, scope ReadScope) ([]ApplicableSummary, error) {
	items, truncated, err := a.service.ApplicableAdvisories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ApplicableSummary, 0, len(items))
	ids := []string{}
	for _, item := range items {
		v := ApplicableSummary{ID: item.ID, Reference: item.Reference, Status: item.Status, Truncated: truncated}
		if scope.IncludeDetails {
			v.Severity = item.Severity
			v.Title = item.Title
			v.KnownExploited, v.KEVDueDate = item.KnownExploited, item.KEVDueDate
			ids = append(ids, item.ID)
		}
		out = append(out, v)
	}
	if scope.IncludeDetails {
		counts, err := a.service.AffectedDeviceCounts(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range out {
			out[i].AffectedDevices = counts[out[i].ID]
		}
	}
	return out, nil
}

// RiskReview is an accepted Finding with a review due in the next 30 days.
// Zero scope leaves its identifying fields empty and supplies only the date.
type RiskReview struct {
	FindingID        string
	FindingReference string
	AdvisoryID       string
	ReviewBy         time.Time
}

type RiskReviewPage struct {
	Items      []RiskReview
	NextCursor string
}

func (a *Advisories) RiskReviewsDuePage(ctx context.Context, scope ReadScope) (RiskReviewPage, error) {
	out := RiskReviewPage{Items: []RiskReview{}}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	until := today.AddDate(0, 0, 30)
	limit := scope.Limit
	if limit < 1 || limit > 200 {
		limit = 200
	}
	var afterDate time.Time
	afterID := ""
	if scope.Cursor != "" {
		parts := strings.Split(scope.Cursor, "/")
		if len(parts) != 2 || !cursorIDPattern.MatchString(parts[1]) {
			return out, application.ErrInvalidCursor
		}
		var err error
		afterDate, err = time.Parse(time.DateOnly, parts[0])
		if err != nil {
			return out, application.ErrInvalidCursor
		}
		afterID = parts[1]
	}
	records, err := a.service.RiskReviewsDue(ctx, today, until, afterDate, afterID, limit)
	if err != nil {
		return out, err
	}
	if len(records) == limit {
		last := records[len(records)-1]
		out.NextCursor = last.ReviewBy.Format(time.DateOnly) + "/" + last.FindingID
	}
	for _, f := range records {
		v := RiskReview{ReviewBy: f.ReviewBy}
		if scope.IncludeDetails {
			v.FindingID = f.FindingID
			v.FindingReference = f.FindingReference
			v.AdvisoryID = f.AdvisoryID
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

func (a *Advisories) RiskReviewsDue(ctx context.Context, scope ReadScope) ([]RiskReview, error) {
	page, err := a.RiskReviewsDuePage(ctx, scope)
	return page.Items, err
}

type Advisories struct{ service *application.Service }

func NewAdvisories(service *application.Service) *Advisories { return &Advisories{service: service} }

// Lookup returns a single Advisory's current state and affected count. The caller must check
// security.view before requesting details; a zero scope omits sensitive advisory text and counts.
func (a *Advisories) Lookup(ctx context.Context, id string, scope ReadScope) (AdvisoryInfo, error) {
	p := application.Principal{View: true}
	detail, err := a.service.GetAdvisory(ctx, p, id)
	if err != nil {
		return AdvisoryInfo{}, err
	}
	out := AdvisoryInfo{ID: detail.ID, Reference: detail.Reference, Status: detail.Status}
	if scope.IncludeDetails {
		summary, err := a.service.Summary(ctx, p, id)
		if err != nil {
			return AdvisoryInfo{}, err
		}
		out.Title, out.Summary, out.Severity = detail.Title, detail.Summary, detail.Severity
		out.AffectedDevices, out.MatchTruncated = summary.AffectedDevices, detail.MatchTruncated
	}
	return out, nil
}

// FeedHealth is the state of one advisory feed source for the briefing's integration health. LastError is a
// constant code ("rate_limited", "unavailable", ...), never an error text.
type FeedHealth struct {
	Source        string
	LastSuccessAt *time.Time
	LastAttemptAt *time.Time
	LastError     string
	// Stale is set when the last success is older than 48 hours or missing.
	Stale bool
}

// AdvisoryFeedHealth lists the feed sources that have run. The caller authorizes the requesting user.
func (a *Advisories) AdvisoryFeedHealth(ctx context.Context) ([]FeedHealth, error) {
	states, err := a.service.FeedStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FeedHealth, 0, len(states))
	for _, s := range states {
		stale := s.LastSuccessAt == nil || time.Since(*s.LastSuccessAt) > application.FeedStaleAfter
		out = append(out, FeedHealth{Source: s.Source, LastSuccessAt: s.LastSuccessAt, LastAttemptAt: s.LastAttemptAt, LastError: s.LastError, Stale: stale})
	}
	return out, nil
}

// DeploymentAdvisory is an applicable Advisory with open Findings on the given Devices for a product.
type DeploymentAdvisory = application.DeploymentAdvisory

// DeploymentContext is the security context of a Deployment: Advisories and open Finding counts for one Software
// Product on an explicit set of Devices. Zero details return counts only (no references or titles); the caller must
// authorize security.view before enabling them. The Device ids are the scope; nothing else is read.
type DeploymentContext struct {
	Advisories    []DeploymentAdvisory
	AdvisoryCount int
	OpenFindings  int
	Truncated     bool
}

// deploymentContextLimit bounds the advisories listed for a Deployment.
const deploymentContextLimit = 50

func (a *Advisories) DeploymentContext(ctx context.Context, productID string, deviceIDs []string, details bool) (DeploymentContext, error) {
	list, totals, err := a.service.DeploymentAdvisories(ctx, productID, deviceIDs, deploymentContextLimit)
	if err != nil {
		return DeploymentContext{}, err
	}
	out := DeploymentContext{AdvisoryCount: totals.Advisories, OpenFindings: totals.Findings, Truncated: totals.Advisories > len(list)}
	if details {
		out.Advisories = list
	} else {
		out.Truncated = false
	}
	return out, nil
}
