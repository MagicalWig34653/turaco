package application

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Rollout reporting (F9 G4). Everything is derived from the Deployment, ring runs, targets, transitions and findings;
// nothing is stored for the report. Reads need a deployments read permission (deployments.view, manage or execute);
// without it the Deployment does not exist for the caller. Device names appear only for holders of endpoints.view.
const (
	// MaxReportFailureReasons bounds the top failure reasons of a report.
	MaxReportFailureReasons = 10
	// MaxCSVRows bounds the targets of one CSV export.
	MaxCSVRows = 50000
	// MaxConcurrentExports is the number of report exports that may run at the same time on one instance.
	MaxConcurrentExports = 2
	// csvPage is the rows read per step of an export.
	csvPage = 2000
	// MaxRollouts bounds one page of the rollout list.
	MaxRollouts = 100
	// MaxSecurityContextDevices bounds the target Devices handed to the Security contract.
	MaxSecurityContextDevices = 20000
)

// ActiveRolloutStatuses are the statuses the rollout list shows by default.
var ActiveRolloutStatuses = []string{DeploymentResolvingTargets, DeploymentReady, DeploymentRunning, DeploymentPaused}

// FailureReason is a failure code with the number of failed or expired targets that carry it. The code is the
// provider's raw status of the artifact on the Device, or Turaco's own reason code when the provider reported none.
type FailureReason struct {
	Code  string
	Count int
}

// ClusterInfo is an open failure cluster.
type ClusterInfo struct {
	FindingID string
	Dimension string
	Value     string
	Failed    int
	Total     int
	RaisedAt  time.Time
}

// FollowUpInfo is a follow-up Task created for a Deployment.
type FollowUpInfo struct {
	Reason    string
	RingID    *string
	TaskID    string
	CreatedAt time.Time
}

// ReportRingTransition is a ring state change with its time.
type ReportRingTransition struct {
	RingRunID   string
	From        *string
	To          string
	Operation   string
	Reason      *string
	ActorUserID *string
	ActorSystem *string
	At          time.Time
}

// GateResult is a gate of a ring that passed or failed, derived from the ring's transitions: settled (the success
// threshold was met on enough evidence), soak (the soak time ran out, the ring awaits promotion) and promotion are
// passed; every halt is a failed gate with its reason code.
type GateResult struct {
	Gate   string
	Passed bool
	At     time.Time
	Reason *string
}

// ReportData is what the store provides for a report, aggregated in the database.
type ReportData struct {
	// Counts are the targets by state per ring run id.
	Counts map[string]map[string]int
	// Medians are per ring run id; OverallMedian over all successful targets (nil without any).
	Medians       map[string]int64
	OverallMedian *int64
	Reasons       []FailureReason
	Clusters      []ClusterInfo
	RingChanges   []ReportRingTransition
	FollowUps     []FollowUpInfo
}

// ReportRow is one target of an export.
type ReportRow struct {
	ID                    string
	RingPosition          int
	RingName              string
	DeviceID              string
	DeviceName            string
	State                 string
	StateReason           *string
	ErrorCode             string
	ResolvedAt            time.Time
	AssignmentRequestedAt *time.Time
	DecidedAt             *time.Time
}

// RolloutRow is one Deployment of the rollout list.
type RolloutRow struct {
	Deployment      Deployment
	CurrentPosition *int
	CurrentRingName *string
	CurrentRingStat *string
	RingCount       int
	Counts          map[string]int
	AwaitingPromo   bool
	HaltedRings     int
}

// ReportStore is the persistence port of the reports.
type ReportStore interface {
	ReportData(ctx context.Context, deploymentID string) (ReportData, error)
	// ReportTargets reads targets after the cursor (the target id) ordered by id.
	ReportTargets(ctx context.Context, deploymentID, after string, limit int) ([]ReportRow, error)
	// Rollouts lists Deployments in the statuses, newest first, with their ring summary (cursor: the Deployment id).
	Rollouts(ctx context.Context, statuses []string, after string, limit int) ([]RolloutRow, error)
	// DeploymentDeviceIDs returns the Devices of the Deployment's targets (at most limit; more reports true).
	DeploymentDeviceIDs(ctx context.Context, deploymentID string, limit int) ([]string, bool, error)
}

// RingReport is the report of one ring.
type RingReport struct {
	Run                    DeploymentRingRun
	Ring                   DeploymentRing
	Counts                 map[string]int
	SuccessRatePercent     *float64
	MedianSecondsToSuccess *int64
	EndedAt                *time.Time
	Gates                  []GateResult
}

// Promotion records who promoted a ring when.
type Promotion struct {
	RingID string
	By     *string
	At     time.Time
}

// DeploymentReport is the derived report of a Deployment.
type DeploymentReport struct {
	Deployment             Deployment
	GeneratedAt            time.Time
	Rings                  []RingReport
	Totals                 map[string]int
	SuccessRatePercent     *float64
	MedianSecondsToSuccess *int64
	TopFailureReasons      []FailureReason
	Clusters               []ClusterInfo
	Transitions            []DeploymentTransition
	Promotions             []Promotion
	FollowUps              []FollowUpInfo
}

// SuccessRate is successful / (successful + failed + expired) in percent with one decimal; nil without a decision.
// Targets that were already satisfied, not applicable, cancelled or are still open are not decided and do not count.
func SuccessRate(successful, failed, expired int) *float64 {
	decided := successful + failed + expired
	if decided <= 0 || successful < 0 || failed < 0 || expired < 0 {
		return nil
	}
	r := math.Round(float64(successful)*1000/float64(decided)) / 10
	return &r
}

func zeroCounts() map[string]int {
	out := make(map[string]int, len(TargetStates))
	for _, st := range TargetStates {
		out[st] = 0
	}
	return out
}

func (s *Service) reportDeployment(ctx context.Context, p Principal, id string) (Deployment, error) {
	if !p.canViewDeployments() || !validUUID(id) {
		return Deployment{}, ErrNotFound
	}
	return s.store.GetDeployment(ctx, strings.ToLower(id))
}

// DeploymentReport builds the report of a Deployment: per ring the run times, counts by target state, success rate,
// median time to success and the gates; over all rings the totals, the top failure reasons, the open failure clusters
// and the people behind the transitions.
func (s *Service) DeploymentReport(ctx context.Context, p Principal, id string) (DeploymentReport, error) {
	d, err := s.reportDeployment(ctx, p, id)
	if err != nil {
		return DeploymentReport{}, err
	}
	rings, err := s.store.Rings(ctx, d.ID)
	if err != nil {
		return DeploymentReport{}, err
	}
	runs, err := s.store.RingRuns(ctx, d.ID)
	if err != nil {
		return DeploymentReport{}, err
	}
	data, err := s.store.ReportData(ctx, d.ID)
	if err != nil {
		return DeploymentReport{}, err
	}
	transitions, err := s.store.DeploymentTransitions(ctx, d.ID)
	if err != nil {
		return DeploymentReport{}, err
	}
	out := DeploymentReport{Deployment: d, GeneratedAt: s.now(), Rings: []RingReport{}, Totals: zeroCounts(), TopFailureReasons: data.Reasons,
		Clusters: data.Clusters, Transitions: transitions, Promotions: []Promotion{}, FollowUps: data.FollowUps, MedianSecondsToSuccess: data.OverallMedian}
	if out.TopFailureReasons == nil {
		out.TopFailureReasons = []FailureReason{}
	}
	if out.Clusters == nil {
		out.Clusters = []ClusterInfo{}
	}
	if out.FollowUps == nil {
		out.FollowUps = []FollowUpInfo{}
	}
	for _, run := range runs {
		ring, ok := findRing(rings, run.RingID)
		if !ok {
			continue
		}
		counts := zeroCounts()
		for st, n := range data.Counts[run.ID] {
			counts[st] = n
			out.Totals[st] += n
		}
		rr := RingReport{Run: run, Ring: ring, Counts: counts, Gates: []GateResult{},
			SuccessRatePercent: SuccessRate(counts[TargetSuccessful], counts[TargetFailed], counts[TargetExpired])}
		if m, ok := data.Medians[run.ID]; ok {
			rr.MedianSecondsToSuccess = &m
		}
		switch {
		case run.PromotedAt != nil:
			rr.EndedAt = run.PromotedAt
		case run.Status == RingHalted:
			rr.EndedAt = run.HaltedAt
		}
		if run.PromotedAt != nil {
			out.Promotions = append(out.Promotions, Promotion{RingID: run.RingID, By: run.PromotedBy, At: *run.PromotedAt})
		}
		for _, t := range data.RingChanges {
			if t.RingRunID != run.ID {
				continue
			}
			if g, ok := gateOf(t); ok {
				rr.Gates = append(rr.Gates, g)
			}
		}
		out.Rings = append(out.Rings, rr)
	}
	out.SuccessRatePercent = SuccessRate(out.Totals[TargetSuccessful], out.Totals[TargetFailed], out.Totals[TargetExpired])
	return out, nil
}

// gateOf maps a ring transition to a gate result.
func gateOf(t ReportRingTransition) (GateResult, bool) {
	switch {
	case t.To == RingHalted:
		reason := "halted"
		if t.Reason != nil {
			reason = *t.Reason
		}
		return GateResult{Gate: reason, Passed: false, At: t.At, Reason: t.Reason}, true
	case t.Operation == "settled":
		return GateResult{Gate: "success_threshold", Passed: true, At: t.At}, true
	case t.To == RingAwaitingPromotion:
		return GateResult{Gate: "soak", Passed: true, At: t.At}, true
	case t.To == RingPromoted:
		return GateResult{Gate: "promotion", Passed: true, At: t.At}, true
	}
	return GateResult{}, false
}

// ListRollouts lists the Deployments in the given statuses (default: the active ones) with their progress, newest
// first. An unknown status is an input error.
func (s *Service) ListRollouts(ctx context.Context, p Principal, statuses []string, page Page) ([]RolloutRow, string, error) {
	if !p.canViewDeployments() {
		return nil, "", ErrForbidden
	}
	if len(statuses) == 0 {
		statuses = ActiveRolloutStatuses
	}
	for _, st := range statuses {
		if !containsString(DeploymentStatuses, st) {
			return nil, "", invalid("status must be one of %s", strings.Join(DeploymentStatuses, ", "))
		}
	}
	page = page.Normalize()
	if page.Cursor != "" && !validUUID(page.Cursor) {
		return nil, "", ErrInvalidCursor
	}
	rows, err := s.store.Rollouts(ctx, statuses, page.Cursor, min(page.Limit, MaxRollouts)+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if limit := min(page.Limit, MaxRollouts); len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].Deployment.ID
	}
	return rows, next, nil
}

// ExportReportRows reads the targets of a Deployment in pages (at most MaxCSVRows). begin is called once with the
// Deployment before the first row (nothing is called for an unknown Deployment); emit gets every row. It reports
// whether the export was cut at the cap. Names are cleared unless the caller holds endpoints.view. At most
// MaxConcurrentExports run at once (ErrExportBusy), and every export is audited (ids only) before the first row.
func (s *Service) ExportReportRows(ctx context.Context, p Principal, c Caller, id string, begin func(Deployment), emit func(ReportRow) error) (bool, error) {
	d, err := s.reportDeployment(ctx, p, id)
	if err != nil {
		return false, err
	}
	if s.exportsRunning.Add(1) > MaxConcurrentExports {
		s.exportsRunning.Add(-1)
		return false, ErrExportBusy
	}
	defer s.exportsRunning.Add(-1)
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: "endpoints.deployment.report_exported", TargetType: "deployment", TargetID: d.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"deploymentId": d.ID, "format": "csv"}})
	})
	if err != nil {
		return false, err
	}
	begin(d)
	after, n := "", 0
	for {
		rows, err := s.store.ReportTargets(ctx, d.ID, after, csvPage)
		if err != nil {
			return false, err
		}
		for _, r := range rows {
			if n >= MaxCSVRows {
				return true, nil
			}
			if !p.View {
				r.DeviceName = ""
			}
			if err := emit(r); err != nil {
				return false, err
			}
			n++
			after = r.ID
		}
		if len(rows) < csvPage {
			return false, nil
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
