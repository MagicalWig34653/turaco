package application

import (
	"context"
	"errors"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"time"
)

// Progress is derived from current Turaco records, not a deployment outcome.
type Progress struct {
	GeneratedAt              time.Time                  `json:"generatedAt"`
	TasksTruncated           bool                       `json:"tasksTruncated"`
	Source                   string                     `json:"source"`
	FindingByStatus          map[string]int             `json:"findingByStatus"`
	FindingByConfidence      map[string]int             `json:"findingByConfidence"`
	ShareRemediated          float64                    `json:"shareRemediated"`
	AcceptedRiskCount        int                        `json:"acceptedRiskCount"`
	EarliestRiskReviewBy     *time.Time                 `json:"earliestRiskReviewBy"`
	OldestOpenFindingAgeDays *int                       `json:"oldestOpenFindingAgeDays"`
	Tasks                    taskspublic.ContextSummary `json:"tasks"`
	LinkedChangesByStatus    map[string]int             `json:"linkedChangesByStatus"`
	HiddenLinkedChanges      int                        `json:"hiddenLinkedChanges"`
	ResidualRisk             string                     `json:"residualRisk"`
}

// ResidualRisk is a deterministic Turaco label: no live exposure = none;
// potential only = low for none/low/medium severity, medium for high/critical;
// probable exposure = medium for none/low/medium, high for high/critical.
// Risk-accepted findings still count as live exposure; false positives do not.
func ResidualRisk(severity string, probable, potential int) string {
	if probable == 0 && potential == 0 {
		return "none"
	}
	high := severity == "high" || severity == "critical"
	if probable > 0 {
		if high {
			return "high"
		}
		return "medium"
	}
	if high {
		return "medium"
	}
	return "low"
}
func liveFinding(status string) bool {
	for _, v := range append(append([]string{}, openFinding...), FindingRiskAccepted) {
		if status == v {
			return true
		}
	}
	return false
}

// FindingAggregate is a Security-owned snapshot of counts; Tasks and Changes remain live.
type FindingAggregate struct {
	ByStatus, ByConfidence               map[string]int
	Total, Accepted, Probable, Potential int
	EarliestReview, Oldest               *time.Time
}

type progressSnapshotReader interface {
	ProgressSnapshot(context.Context, string) (Advisory, FindingAggregate, []string, bool, error)
}

func (s *Service) Progress(ctx context.Context, p Principal, id string) (Progress, error) {
	if !p.reads() {
		return Progress{}, ErrForbidden
	}
	reader, ok := s.store.(progressSnapshotReader)
	if !ok {
		return Progress{}, errors.New("security: progress snapshot unavailable")
	}
	a, agg, ids, truncated, err := reader.ProgressSnapshot(ctx, id)
	if err != nil {
		return Progress{}, err
	}
	out := Progress{Source: "turaco_derived", GeneratedAt: s.now().UTC(), TasksTruncated: truncated, FindingByStatus: agg.ByStatus, FindingByConfidence: agg.ByConfidence, LinkedChangesByStatus: map[string]int{}, AcceptedRiskCount: agg.Accepted, EarliestRiskReviewBy: agg.EarliestReview}
	for _, status := range FindingStatuses {
		if _, ok := out.FindingByStatus[status]; !ok {
			out.FindingByStatus[status] = 0
		}
	}
	for _, confidence := range Confidences {
		if _, ok := out.FindingByConfidence[confidence]; !ok {
			out.FindingByConfidence[confidence] = 0
		}
	}
	total := agg.Total - out.FindingByStatus[FindingFalsePositive]
	if total > 0 {
		out.ShareRemediated = float64(out.FindingByStatus[FindingRemediated]) / float64(total)
	}
	if agg.Oldest != nil {
		days := int(out.GeneratedAt.Sub(*agg.Oldest).Hours() / 24)
		if days < 0 {
			days = 0
		}
		out.OldestOpenFindingAgeDays = &days
	}
	summary, err := s.tasks.SummaryByContexts(ctx, "security_advisory", []string{a.ID})
	if err != nil {
		return Progress{}, err
	}
	out.Tasks = summary
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		part, e := s.tasks.SummaryByContexts(ctx, "security_finding", ids[start:end])
		if e != nil {
			return Progress{}, e
		}
		out.Tasks.Open += part.Open
		out.Tasks.Done += part.Done
		out.Tasks.Cancelled += part.Cancelled
		out.Tasks.Overdue += part.Overdue
	}
	links, err := s.LinkedChanges(ctx, p, a.ID)
	if err != nil {
		return Progress{}, err
	}
	for _, link := range links {
		if link.Hidden {
			out.HiddenLinkedChanges++
		} else {
			out.LinkedChangesByStatus[link.Status]++
		}
	}
	out.ResidualRisk = ResidualRisk(a.Severity, agg.Probable, agg.Potential)
	if a.Status != AdvisoryApplicable && a.Status != AdvisoryRemediationPlanned && a.Status != AdvisoryRemediating || a.MatchedAt == nil || a.MatchedRevision == nil || *a.MatchedRevision != a.CriteriaRevision || a.MatchTruncated || a.MatchedIngestionAt == nil || out.GeneratedAt.Sub(*a.MatchedIngestionAt) > 48*time.Hour {
		out.ResidualRisk = "unknown"
	}
	return out, nil
}

// Overview is a current, Turaco-derived feed seed for F8c.
type Overview struct {
	Source                         string         `json:"source"`
	ApplicableBySeverity           map[string]int `json:"applicableBySeverity"`
	OpenFindingsByConfidence       map[string]int `json:"openFindingsByConfidence"`
	OverdueTasks                   int            `json:"overdueTasks"`
	RiskAcceptancesDueWithin30Days int            `json:"riskAcceptancesDueWithin30Days"`
}

type overviewReader interface {
	OverviewCounts(context.Context, time.Time) (Overview, error)
	ApplicableTaskContextIDs(context.Context) (map[string][]string, error)
}

func (s *Service) Overview(ctx context.Context, p Principal) (Overview, error) {
	if !p.reads() {
		return Overview{}, ErrForbidden
	}
	reader, ok := s.store.(overviewReader)
	if !ok {
		return Overview{}, ErrNotFound
	}
	out, err := reader.OverviewCounts(ctx, s.now())
	if err != nil {
		return Overview{}, err
	}
	contexts, err := reader.ApplicableTaskContextIDs(ctx)
	if err != nil {
		return Overview{}, err
	}
	if counter, ok := s.tasks.(interface {
		OverdueByTwoTypes(context.Context, string, []string, string, []string) (int, error)
	}); ok {
		advisories, findings := contexts["security_advisory"], contexts["security_finding"]
		max := len(advisories)
		if len(findings) > max {
			max = len(findings)
		}
		for start := 0; start < max; start += 500 {
			var a, f []string
			if start < len(advisories) {
				end := start + 500
				if end > len(advisories) {
					end = len(advisories)
				}
				a = advisories[start:end]
			}
			if start < len(findings) {
				end := start + 500
				if end > len(findings) {
					end = len(findings)
				}
				f = findings[start:end]
			}
			count, e := counter.OverdueByTwoTypes(ctx, "security_advisory", a, "security_finding", f)
			if e != nil {
				return Overview{}, e
			}
			out.OverdueTasks += count
		}
	} else {
		for typ, ids := range contexts {
			for start := 0; start < len(ids); start += 500 {
				end := start + 500
				if end > len(ids) {
					end = len(ids)
				}
				summary, e := s.tasks.SummaryByContexts(ctx, typ, ids[start:end])
				if e != nil {
					return Overview{}, e
				}
				out.OverdueTasks += summary.Overdue
			}
		}
	}
	out.Source = "turaco_derived"
	return out, nil
}
