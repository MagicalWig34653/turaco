package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Deployment execution (F9 G3). Operations need deployments.execute and expectedVersion (the Deployment's version);
// the service additionally applies the write capability, the gates and high-impact rules. Reads follow the plan read
// rule; device names in target lists need endpoints.view.

func registerExecution(route func(string, func(http.Handler) http.Handler, http.HandlerFunc), auth authorization.Authenticator, h *handler) {
	execute := authorization.Require(auth, permDeploymentsExecute)
	session := authorization.RequireAuthenticated(auth)
	route("POST /api/v1/deployments/{id}/start", execute, h.startDeployment)
	route("POST /api/v1/deployments/{id}/pause", execute, h.pauseDeployment)
	route("POST /api/v1/deployments/{id}/resume", execute, h.resumeDeployment)
	route("POST /api/v1/deployments/{id}/halt", execute, h.haltDeployment)
	route("POST /api/v1/deployments/{id}/rings/{ringId}/halt", execute, h.haltRing)
	route("POST /api/v1/deployments/{id}/rings/{ringId}/resume", execute, h.resumeRing)
	route("POST /api/v1/deployments/{id}/rings/{ringId}/promote", execute, h.promoteRing)
	route("POST /api/v1/deployments/{id}/rings/{ringId}/request-approval", execute, h.requestRingApproval)
	route("GET /api/v1/deployments/{id}/progress", session, h.deploymentProgress)
	route("GET /api/v1/deployments/{id}/rings/{ringId}/targets", session, h.ringTargets)
	route("GET /api/v1/deployments/{id}/attempts", session, h.deploymentAttempts)
}

func (h *handler) simpleOp(w http.ResponseWriter, r *http.Request, op func(application.Caller, application.Principal, string, *int) (application.Deployment, error)) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := op(caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) startDeployment(w http.ResponseWriter, r *http.Request) {
	h.simpleOp(w, r, func(c application.Caller, p application.Principal, id string, v *int) (application.Deployment, error) {
		return h.svc.StartDeployment(r.Context(), c, p, id, v)
	})
}

func (h *handler) pauseDeployment(w http.ResponseWriter, r *http.Request) {
	h.simpleOp(w, r, func(c application.Caller, p application.Principal, id string, v *int) (application.Deployment, error) {
		return h.svc.PauseDeployment(r.Context(), c, p, id, v)
	})
}

func (h *handler) resumeDeployment(w http.ResponseWriter, r *http.Request) {
	h.simpleOp(w, r, func(c application.Caller, p application.Principal, id string, v *int) (application.Deployment, error) {
		return h.svc.ResumeDeployment(r.Context(), c, p, id, v)
	})
}

type reasonBody struct {
	Reason          string `json:"reason"`
	ExpectedVersion *int   `json:"expectedVersion"`
}

func (h *handler) haltDeployment(w http.ResponseWriter, r *http.Request) {
	var b reasonBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.HaltDeployment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) haltRing(w http.ResponseWriter, r *http.Request) {
	var b reasonBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.HaltRing(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("ringId"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) resumeRing(w http.ResponseWriter, r *http.Request) {
	h.simpleOp(w, r, func(c application.Caller, p application.Principal, id string, v *int) (application.Deployment, error) {
		return h.svc.ResumeRing(r.Context(), c, p, id, r.PathValue("ringId"), v)
	})
}

func (h *handler) promoteRing(w http.ResponseWriter, r *http.Request) {
	h.simpleOp(w, r, func(c application.Caller, p application.Principal, id string, v *int) (application.Deployment, error) {
		return h.svc.PromoteRing(r.Context(), c, p, id, r.PathValue("ringId"), v)
	})
}

func (h *handler) requestRingApproval(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ApproverUserID  *string `json:"approverUserId"`
		ApproverTeamID  *string `json:"approverTeamId"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.RequestRingApproval(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("ringId"), b.ExpectedVersion,
		application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID})
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

type ringProgressDTO struct {
	RingID                  string         `json:"ringId"`
	RingRunID               string         `json:"ringRunId"`
	Position                int            `json:"position"`
	Name                    string         `json:"name"`
	Status                  string         `json:"status"`
	StatusReason            *string        `json:"statusReason"`
	ActivatedAt             *string        `json:"activatedAt"`
	SettledAt               *string        `json:"settledAt"`
	AwaitingSince           *string        `json:"awaitingSince"`
	PromotedAt              *string        `json:"promotedAt"`
	HaltedAt                *string        `json:"haltedAt"`
	AssignmentRequestedAt   *string        `json:"assignmentRequestedAt"`
	AssignmentClearedAt     *string        `json:"assignmentClearedAt"`
	PromotionApprovalStatus *string        `json:"promotionApprovalStatus"`
	ApprovalRequired        bool           `json:"approvalRequired"`
	SuccessThresholdPercent int            `json:"successThresholdPercent"`
	SoakMinutes             int            `json:"soakMinutes"`
	Counts                  map[string]int `json:"counts"`
	FreshSuccessful         int            `json:"freshSuccessful"`
	FreshObserved           int            `json:"freshObserved"`
	SuccessRatePercent      *float64       `json:"successRatePercent"`
	SoakRemainingSeconds    int64          `json:"soakRemainingSeconds"`
	NextGate                string         `json:"nextGate"`
}

func (h *handler) deploymentProgress(w http.ResponseWriter, r *http.Request) {
	pr, err := h.svc.DeploymentProgress(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	rings := mapItems(pr.Rings, func(x application.RingProgress) ringProgressDTO {
		counts := map[string]int{}
		for _, st := range application.TargetStates {
			counts[st] = x.Counts.ByState[st]
		}
		return ringProgressDTO{RingID: x.Run.RingID, RingRunID: x.Run.ID, Position: x.Run.Position, Name: x.Ring.Name, Status: x.Run.Status,
			StatusReason: x.Run.StatusReason, ActivatedAt: tsPtr(x.Run.ActivatedAt), SettledAt: tsPtr(x.Run.SettledAt),
			AwaitingSince: tsPtr(x.Run.AwaitingSince), PromotedAt: tsPtr(x.Run.PromotedAt), HaltedAt: tsPtr(x.Run.HaltedAt),
			AssignmentRequestedAt: tsPtr(x.Run.AssignmentRequestedAt), AssignmentClearedAt: tsPtr(x.Run.AssignmentClearedAt),
			PromotionApprovalStatus: x.Run.PromotionApprovalStatus, ApprovalRequired: x.Ring.ApprovalRequired,
			SuccessThresholdPercent: x.Ring.SuccessThresholdPercent, SoakMinutes: x.Ring.SoakMinutes, Counts: counts,
			FreshSuccessful: x.Counts.FreshSuccessful, FreshObserved: x.Counts.FreshObserved, SuccessRatePercent: x.Rate,
			SoakRemainingSeconds: int64(x.SoakLeft / time.Second), NextGate: x.NextGate}
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"deployment": toDeployment(pr.Deployment), "rings": rings})
}

type targetDTO struct {
	ID                    string  `json:"id"`
	DeviceID              string  `json:"deviceId"`
	DeviceName            *string `json:"deviceName"`
	State                 string  `json:"state"`
	StateReason           *string `json:"stateReason"`
	ResolvedAt            string  `json:"resolvedAt"`
	AssignmentRequestedAt *string `json:"assignmentRequestedAt"`
	ReadBackAt            *string `json:"readBackAt"`
	ExpiresAt             *string `json:"expiresAt"`
	DecidedAt             *string `json:"decidedAt"`
	EvidenceObservedAt    *string `json:"evidenceObservedAt"`
}

func (h *handler) ringTargets(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListRingTargets(r.Context(), principal(r), r.PathValue("id"), r.PathValue("ringId"), r.URL.Query().Get("state"), page)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	items := mapItems(res.Items, func(t application.DeploymentTarget) targetDTO {
		var name *string
		if !res.NamesRedacted {
			n := t.DeviceName
			name = &n
		}
		return targetDTO{ID: t.ID, DeviceID: t.DeviceID, DeviceName: name, State: t.State, StateReason: t.StateReason, ResolvedAt: ts(t.ResolvedAt),
			AssignmentRequestedAt: tsPtr(t.AssignmentRequestedAt), ReadBackAt: tsPtr(t.ReadBackAt), ExpiresAt: tsPtr(t.ExpiresAt),
			DecidedAt: tsPtr(t.DecidedAt), EvidenceObservedAt: tsPtr(t.EvidenceObservedAt)}
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": res.NextCursor, "namesRedacted": res.NamesRedacted})
}

type attemptDTO struct {
	ID          string `json:"id"`
	RingRunID   string `json:"ringRunId"`
	Kind        string `json:"kind"`
	Attempt     int    `json:"attempt"`
	OperationID string `json:"operationId"`
	RequestedAt string `json:"requestedAt"`
	OutcomeCode string `json:"outcomeCode"`
}

func (h *handler) deploymentAttempts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListDeploymentAttempts(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	items := mapItems(res.Items, func(a application.DeploymentAttempt) attemptDTO {
		return attemptDTO{ID: a.ID, RingRunID: a.RingRunID, Kind: a.Kind, Attempt: a.Attempt, OperationID: a.OperationID, RequestedAt: ts(a.RequestedAt), OutcomeCode: a.OutcomeCode}
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": res.NextCursor})
}
