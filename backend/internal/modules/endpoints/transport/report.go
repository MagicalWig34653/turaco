package transport

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Rollout reporting and the security context of a Deployment (F9 G4). Reads need deployments.view (manage and execute
// include it); a caller without it gets 404 for the Deployment. Device names (CSV) need endpoints.view; advisory
// references and titles in the security context need security.view, otherwise only counts are returned.

func registerReports(route func(string, func(http.Handler) http.Handler, http.HandlerFunc), auth authorization.Authenticator, h *handler) {
	read := authorization.RequireAny(auth, permDeploymentsView, permDeploymentsManage, permDeploymentsExecute)
	route("GET /api/v1/deployments/{id}/report", read, h.deploymentReport)
	route("GET /api/v1/deployments/{id}/report.csv", read, h.deploymentReportCSV)
	route("GET /api/v1/deployments/{id}/security-context", read, h.deploymentSecurityContext)
	route("GET /api/v1/software/rollouts", read, h.softwareRollouts)
}

type gateDTO struct {
	Gate   string  `json:"gate"`
	Passed bool    `json:"passed"`
	At     string  `json:"at"`
	Reason *string `json:"reason"`
}

type ringReportDTO struct {
	RingID                  string         `json:"ringId"`
	RingRunID               string         `json:"ringRunId"`
	Position                int            `json:"position"`
	Name                    string         `json:"name"`
	Status                  string         `json:"status"`
	StatusReason            *string        `json:"statusReason"`
	StartedAt               *string        `json:"startedAt"`
	EndedAt                 *string        `json:"endedAt"`
	SuccessThresholdPercent int            `json:"successThresholdPercent"`
	Counts                  map[string]int `json:"counts"`
	SuccessRatePercent      *float64       `json:"successRatePercent"`
	MedianSecondsToSuccess  *int64         `json:"medianSecondsToSuccess"`
	Gates                   []gateDTO      `json:"gates"`
}

func (h *handler) deploymentReport(w http.ResponseWriter, r *http.Request) {
	rep, err := h.svc.DeploymentReport(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	rings := mapItems(rep.Rings, func(x application.RingReport) ringReportDTO {
		return ringReportDTO{RingID: x.Run.RingID, RingRunID: x.Run.ID, Position: x.Run.Position, Name: x.Ring.Name, Status: x.Run.Status,
			StatusReason: x.Run.StatusReason, StartedAt: tsPtr(x.Run.ActivatedAt), EndedAt: tsPtr(x.EndedAt),
			SuccessThresholdPercent: x.Ring.SuccessThresholdPercent, Counts: x.Counts, SuccessRatePercent: x.SuccessRatePercent,
			MedianSecondsToSuccess: x.MedianSecondsToSuccess,
			Gates: mapItems(x.Gates, func(g application.GateResult) gateDTO {
				return gateDTO{Gate: g.Gate, Passed: g.Passed, At: ts(g.At), Reason: g.Reason}
			})}
	})
	d := rep.Deployment
	httpx.JSON(w, http.StatusOK, map[string]any{
		"deployment":             toDeployment(d),
		"generatedAt":            ts(rep.GeneratedAt),
		"totals":                 rep.Totals,
		"successRatePercent":     rep.SuccessRatePercent,
		"medianSecondsToSuccess": rep.MedianSecondsToSuccess,
		"rings":                  rings,
		"topFailureReasons": mapItems(rep.TopFailureReasons, func(f application.FailureReason) map[string]any {
			return map[string]any{"code": f.Code, "count": f.Count}
		}),
		"clusters": mapItems(rep.Clusters, func(c application.ClusterInfo) map[string]any {
			return map[string]any{"findingId": c.FindingID, "dimension": c.Dimension, "value": c.Value, "failed": c.Failed, "total": c.Total, "raisedAt": ts(c.RaisedAt)}
		}),
		"people": map[string]any{
			"ownerUserId": d.OwnerUserID, "createdBy": d.CreatedBy, "submittedBy": d.SubmittedBy, "scheduledBy": d.ScheduledBy,
			"startedBy": d.StartedBy, "cancelledBy": d.CancelledBy, "approvedAt": tsPtr(d.ApprovedAt),
			"promotions": mapItems(rep.Promotions, func(p application.Promotion) map[string]any {
				return map[string]any{"ringId": p.RingID, "by": p.By, "at": ts(p.At)}
			}),
		},
		"transitions": mapItems(rep.Transitions, func(t application.DeploymentTransition) map[string]any {
			return map[string]any{"from": t.FromStatus, "to": t.ToStatus, "operation": t.Operation, "reason": t.Reason,
				"actorUserId": t.ActorUserID, "actorSystem": t.ActorSystem, "at": ts(t.CreatedAt)}
		}),
		"followUps": mapItems(rep.FollowUps, func(f application.FollowUpInfo) map[string]any {
			return map[string]any{"reason": f.Reason, "ringId": f.RingID, "taskId": f.TaskID, "createdAt": ts(f.CreatedAt)}
		}),
	})
}

// csvCell neutralizes a cell that a spreadsheet would read as a formula: a leading =, +, -, @, tab or carriage return
// (also behind leading spaces) gets a single quote in front. Control characters other than the tab are replaced.
func csvCell(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if t := strings.TrimLeft(s, " "); t != "" {
		switch t[0] {
		case '=', '+', '-', '@', '\t':
			return "'" + s
		}
	}
	return s
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

func (h *handler) deploymentReportCSV(w http.ResponseWriter, r *http.Request) {
	var cw *csv.Writer
	begin := func(d application.Deployment) {
		name := "deployment-" + strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return '_'
		}, d.Reference) + "-report.csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		cw = csv.NewWriter(w)
		_ = cw.Write([]string{"ring_position", "ring", "device_id", "device_name", "state", "state_reason", "error_code", "resolved_at", "assignment_requested_at", "decided_at"})
	}
	truncated, err := h.svc.ExportReportRows(r.Context(), principal(r), r.PathValue("id"), begin, func(x application.ReportRow) error {
		reason := ""
		if x.StateReason != nil {
			reason = *x.StateReason
		}
		return cw.Write([]string{strconv.Itoa(x.RingPosition), csvCell(x.RingName), x.DeviceID, csvCell(x.DeviceName), x.State, csvCell(reason),
			csvCell(x.ErrorCode), ts(x.ResolvedAt), csvTime(x.AssignmentRequestedAt), csvTime(x.DecidedAt)})
	})
	if cw == nil {
		// Nothing was written: the Deployment is unknown to the caller or the store failed.
		h.deploymentFail(w, r, err)
		return
	}
	if err == nil && truncated {
		_ = cw.Write([]string{"truncated", "export cut at " + strconv.Itoa(application.MaxCSVRows) + " rows"})
	}
	cw.Flush()
}

type rolloutDTO struct {
	Deployment      deploymentDTO  `json:"deployment"`
	RingCount       int            `json:"ringCount"`
	CurrentPosition *int           `json:"currentPosition"`
	CurrentRingName *string        `json:"currentRingName"`
	CurrentRing     *string        `json:"currentRingStatus"`
	AwaitingPromo   bool           `json:"awaitingPromotion"`
	HaltedRings     int            `json:"haltedRings"`
	Counts          map[string]int `json:"counts"`
	TargetTotal     int            `json:"targetTotal"`
	Decided         int            `json:"decided"`
	SuccessRate     *float64       `json:"successRatePercent"`
}

func (h *handler) softwareRollouts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	var statuses []string
	if s := r.URL.Query().Get("status"); s != "" {
		statuses = strings.Split(s, ",")
	}
	rows, next, err := h.svc.ListRollouts(r.Context(), principal(r), statuses, page)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	items := mapItems(rows, func(x application.RolloutRow) rolloutDTO {
		counts := map[string]int{}
		total := 0
		for _, st := range application.TargetStates {
			counts[st] = x.Counts[st]
			total += x.Counts[st]
		}
		return rolloutDTO{Deployment: toDeployment(x.Deployment), RingCount: x.RingCount, CurrentPosition: x.CurrentPosition,
			CurrentRingName: x.CurrentRingName, CurrentRing: x.CurrentRingStat, AwaitingPromo: x.AwaitingPromo, HaltedRings: x.HaltedRings,
			Counts: counts, TargetTotal: total, Decided: counts[application.TargetSuccessful] + counts[application.TargetFailed] + counts[application.TargetExpired],
			SuccessRate: application.SuccessRate(counts[application.TargetSuccessful], counts[application.TargetFailed], counts[application.TargetExpired])}
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (h *handler) deploymentSecurityContext(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.DeploymentSecurityContext(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	var advisories []map[string]any
	if res.Detailed {
		advisories = mapItems(res.Context.Advisories, func(a application.SecurityContextAdvisory) map[string]any {
			return map[string]any{"id": a.ID, "reference": a.Reference, "title": a.Title, "severity": a.Severity, "status": a.Status,
				"knownExploited": a.KnownExploited, "openFindings": a.OpenFindings, "affectedDevices": a.AffectedDevices}
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"deploymentId": res.Deployment.ID, "productId": res.Deployment.ProductID, "productName": res.Deployment.ProductName,
		"productVersion": res.Deployment.ProductVersion, "detailed": res.Detailed, "advisories": advisories,
		"advisoryCount": res.Context.AdvisoryCount, "openFindingCount": res.Context.OpenFindings,
		"targetDevices": res.TargetsSeen, "truncated": res.TargetsTrunc,
	})
}
