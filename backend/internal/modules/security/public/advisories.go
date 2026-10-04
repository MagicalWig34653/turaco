// Package public is Security's read contract for other modules. Callers authorize the requesting
// user before selecting detail scope; no repository or Security-owned table crosses this boundary.
package public

import (
	"context"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// ReadScope selects the fields the caller may show. Zero scope exposes only
// reference/state for Advisories and the review date for RiskReviewsDue.
type ReadScope struct{ IncludeDetails bool }

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
}

func (a *Advisories) ApplicableAdvisorySummaries(ctx context.Context, scope ReadScope) ([]ApplicableSummary, error) {
	out := []ApplicableSummary{}
	cursor := ""
	for {
		page, err := a.service.ListAdvisories(ctx, application.Principal{View: true}, application.AdvisoryFilter{Page: application.Page{Limit: 200, Cursor: cursor}})
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.Status != application.AdvisoryApplicable && item.Status != application.AdvisoryRemediationPlanned && item.Status != application.AdvisoryRemediating {
				continue
			}
			v := ApplicableSummary{ID: item.ID, Reference: item.Reference, Status: item.Status}
			if scope.IncludeDetails {
				v.Severity = item.Severity
				v.Title = item.Title
				s, e := a.service.Summary(ctx, application.Principal{View: true}, item.ID)
				if e != nil {
					return nil, e
				}
				v.AffectedDevices = s.AffectedDevices
			}
			out = append(out, v)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
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

func (a *Advisories) RiskReviewsDue(ctx context.Context, scope ReadScope) ([]RiskReview, error) {
	out := []RiskReview{}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	until := today.AddDate(0, 0, 30)
	records, err := a.service.RiskReviewsDue(ctx, today, until)
	if err != nil {
		return nil, err
	}
	for _, f := range records {
		v := RiskReview{ReviewBy: f.ReviewBy}
		if scope.IncludeDetails {
			v.FindingID = f.FindingID
			v.FindingReference = f.FindingReference
			v.AdvisoryID = f.AdvisoryID
		}
		out = append(out, v)
	}
	return out, nil
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
