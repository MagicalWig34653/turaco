package application

import (
	"context"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
	"time"
)

// Progress is derived from current Turaco records, not a deployment outcome.
type Progress struct {
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

func (s *Service) Progress(ctx context.Context, p Principal, id string) (Progress, error) {
	if !p.reads() {
		return Progress{}, ErrForbidden
	}
	a, err := s.store.GetAdvisory(ctx, id)
	if err != nil {
		return Progress{}, err
	}
	findings, err := s.store.FindingsOfAdvisory(ctx, a.ID)
	if err != nil {
		return Progress{}, err
	}
	out := Progress{Source: "turaco_derived", FindingByStatus: map[string]int{}, FindingByConfidence: map[string]int{}, LinkedChangesByStatus: map[string]int{}}
	for _, status := range FindingStatuses {
		out.FindingByStatus[status] = 0
	}
	for _, confidence := range Confidences {
		out.FindingByConfidence[confidence] = 0
	}
	ids := make([]string, 0, len(findings))
	var oldest *time.Time
	probable, potential := 0, 0
	for _, f := range findings {
		ids = append(ids, f.ID)
		out.FindingByStatus[f.Status]++
		out.FindingByConfidence[f.Confidence]++
		if f.Status == FindingRiskAccepted {
			out.AcceptedRiskCount++
			if f.RiskReviewBy != nil && (out.EarliestRiskReviewBy == nil || f.RiskReviewBy.Before(*out.EarliestRiskReviewBy)) {
				out.EarliestRiskReviewBy = f.RiskReviewBy
			}
		}
		if liveFinding(f.Status) {
			if f.Confidence == ConfidenceProbable {
				probable++
			} else {
				potential++
			}
			if oldest == nil || f.FirstSeenAt.Before(*oldest) {
				v := f.FirstSeenAt
				oldest = &v
			}
		}
	}
	total := len(findings) - out.FindingByStatus[FindingFalsePositive]
	if total > 0 {
		out.ShareRemediated = float64(out.FindingByStatus[FindingRemediated]) / float64(total)
	}
	if oldest != nil {
		days := int(s.now().Sub(*oldest).Hours() / 24)
		if days < 0 {
			days = 0
		}
		out.OldestOpenFindingAgeDays = &days
	}
	advisoryTasks, err := s.tasks.SummaryByContexts(ctx, "security_advisory", []string{a.ID})
	if err != nil {
		return Progress{}, err
	}
	findingTasks, err := s.tasks.SummaryByContexts(ctx, "security_finding", ids)
	if err != nil {
		return Progress{}, err
	}
	out.Tasks = taskspublic.ContextSummary{Open: advisoryTasks.Open + findingTasks.Open, Done: advisoryTasks.Done + findingTasks.Done, Overdue: advisoryTasks.Overdue + findingTasks.Overdue}
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
	out.ResidualRisk = ResidualRisk(a.Severity, probable, potential)
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
	for _, typ := range []string{"security_advisory", "security_finding"} {
		summary, e := s.tasks.SummaryByType(ctx, typ)
		if e != nil {
			return Overview{}, e
		}
		out.OverdueTasks += summary.Overdue
	}
	out.Source = "turaco_derived"
	return out, nil
}
