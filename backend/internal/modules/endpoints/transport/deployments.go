package transport

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Target Sets and Deployment planning (F9 G2) under /api/v1/target-sets and /api/v1/deployments. Target Set reads
// need deployments.view, deployments.manage or deployments.execute; changes need deployments.manage. Deployment
// reads need only a session: the service shows all plans to holders of a deployments read permission and only
// their own (owner, creator, approver) to everybody else. Changes need deployments.manage; high-impact plans
// additionally deployments.high_impact (checked by the service).

const (
	permDeploymentsView       = application.PermDeploymentsView
	permDeploymentsManage     = application.PermDeploymentsManage
	permDeploymentsExecute    = application.PermDeploymentsExecute
	permDeploymentsHighImpact = application.PermDeploymentsHighImpact
	// maxTargetSetBody fits a definition with both explicit lists full.
	maxTargetSetBody = 96 << 10
)

func registerDeployments(route func(string, func(http.Handler) http.Handler, http.HandlerFunc), auth authorization.Authenticator, h *handler) {
	read := authorization.RequireAny(auth, permDeploymentsView, permDeploymentsManage, permDeploymentsExecute)
	manage := authorization.Require(auth, permDeploymentsManage)
	session := authorization.RequireAuthenticated(auth)
	route("GET /api/v1/target-sets", read, h.listTargetSets)
	route("POST /api/v1/target-sets", manage, h.createTargetSet)
	route("GET /api/v1/target-sets/{id}", read, h.getTargetSet)
	route("PATCH /api/v1/target-sets/{id}", manage, h.updateTargetSet)
	route("POST /api/v1/target-sets/{id}/archive", manage, h.archiveTargetSet)
	route("GET /api/v1/target-sets/{id}/evaluate", read, h.evaluateTargetSet)
	route("GET /api/v1/target-sets/{id}/explain", read, h.explainTargetSet)
	route("GET /api/v1/deployments", session, h.listDeployments)
	route("POST /api/v1/deployments", manage, h.createDeployment)
	route("GET /api/v1/deployments/{id}", session, h.getDeployment)
	route("PATCH /api/v1/deployments/{id}", manage, h.updateDeployment)
	route("POST /api/v1/deployments/{id}/rings", manage, h.addRing)
	route("POST /api/v1/deployments/{id}/rings/reorder", manage, h.reorderRings)
	route("PATCH /api/v1/deployments/{id}/rings/{ringId}", manage, h.updateRing)
	route("DELETE /api/v1/deployments/{id}/rings/{ringId}", manage, h.removeRing)
	route("POST /api/v1/deployments/{id}/validate", manage, h.validateDeployment)
	route("POST /api/v1/deployments/{id}/submit", manage, h.submitDeployment)
	route("POST /api/v1/deployments/{id}/schedule", manage, h.scheduleDeployment)
	route("POST /api/v1/deployments/{id}/cancel", manage, h.cancelDeployment)
}

// deploymentFail maps the planning errors and hands everything else to softwareFail.
func (h *handler) deploymentFail(w http.ResponseWriter, r *http.Request, err error) {
	var plan *application.PlanInvalidError
	switch {
	case errors.As(err, &plan):
		httpx.JSON(w, http.StatusConflict, struct {
			httpx.ErrorEnvelope
			Issues []issueDTO `json:"issues"`
		}{httpx.ErrorEnvelope{Error: httpx.APIError{Code: "endpoints.deployment_invalid", Message: "The deployment plan has blocking issues.", RequestID: httpx.RequestID(w)}},
			mapItems(plan.Issues, toIssue)})
	case errors.Is(err, application.ErrHighImpactForbidden):
		httpx.WriteError(w, http.StatusForbidden, "endpoints.high_impact_required", "This plan is high impact and needs the deployments.high_impact permission.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.no_eligible_approver", "The approver is inactive or took part in the plan.")
	case errors.Is(err, application.ErrTargetSetInUse):
		httpx.WriteError(w, http.StatusConflict, "endpoints.target_set_in_use", "The target set belongs to a submitted, approved or scheduled deployment.")
	case errors.Is(err, application.ErrTargetSetNameTaken):
		httpx.WriteError(w, http.StatusConflict, "endpoints.target_set_name_taken", "A target set with this name exists.")
	default:
		h.softwareFail(w, r, err)
	}
}

func decodeBig(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxTargetSetBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

// ---- target sets ----

type targetSetDTO struct {
	ID          string                       `json:"id"`
	Reference   string                       `json:"reference"`
	Name        string                       `json:"name"`
	Description *string                      `json:"description"`
	OwnerUserID string                       `json:"ownerUserId"`
	Definition  application.TargetDefinition `json:"definition"`
	AllDevices  bool                         `json:"allDevices"`
	ArchivedAt  *string                      `json:"archivedAt"`
	CreatedBy   string                       `json:"createdBy"`
	Version     int                          `json:"version"`
	CreatedAt   string                       `json:"createdAt"`
	UpdatedAt   string                       `json:"updatedAt"`
}

func toTargetSet(t application.TargetSet) targetSetDTO {
	def := t.Definition
	if def.IncludeDeviceIDs == nil {
		def.IncludeDeviceIDs = []string{}
	}
	if def.ExcludeDeviceIDs == nil {
		def.ExcludeDeviceIDs = []string{}
	}
	return targetSetDTO{ID: t.ID, Reference: t.Reference, Name: t.Name, Description: t.Description, OwnerUserID: t.OwnerUserID,
		Definition: def, AllDevices: t.AllDevices, ArchivedAt: tsPtr(t.ArchivedAt), CreatedBy: t.CreatedBy, Version: t.Version,
		CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)}
}

// targetSetBody is the full editable state of a Target Set (PATCH replaces all of it). The definition is decoded
// strictly: unknown fields anywhere are refused.
type targetSetBody struct {
	Name            string                       `json:"name"`
	Description     string                       `json:"description"`
	OwnerUserID     string                       `json:"ownerUserId"`
	Definition      application.TargetDefinition `json:"definition"`
	ExpectedVersion *int                         `json:"expectedVersion"`
}

func (b targetSetBody) input() application.TargetSetInput {
	return application.TargetSetInput{Name: b.Name, Description: b.Description, OwnerUserID: b.OwnerUserID, Definition: b.Definition}
}

func (h *handler) listTargetSets(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListTargetSets(r.Context(), principal(r), application.TargetSetFilter{IncludeArchived: r.URL.Query().Get("includeArchived") == "true", Page: page})
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(res.Items, toTargetSet), "nextCursor": res.NextCursor})
}

func (h *handler) createTargetSet(w http.ResponseWriter, r *http.Request) {
	var b targetSetBody
	if !decodeBig(w, r, &b) {
		return
	}
	if b.ExpectedVersion != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "A new target set takes no expectedVersion.")
		return
	}
	t, err := h.svc.CreateTargetSet(r.Context(), caller(w, r), principal(r), b.input())
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTargetSet(t))
}

func (h *handler) getTargetSet(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.GetTargetSet(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTargetSet(t))
}

func (h *handler) updateTargetSet(w http.ResponseWriter, r *http.Request) {
	var b targetSetBody
	if !decodeBig(w, r, &b) {
		return
	}
	t, err := h.svc.UpdateTargetSet(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.input())
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTargetSet(t))
}

type versionBody struct {
	ExpectedVersion *int `json:"expectedVersion"`
}

func (h *handler) archiveTargetSet(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	t, err := h.svc.ArchiveTargetSet(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTargetSet(t))
}

type targetExampleDTO struct {
	DeviceID        string  `json:"deviceId"`
	Name            *string `json:"name"`
	Redacted        bool    `json:"redacted"`
	OSPlatform      string  `json:"osPlatform"`
	ComplianceState string  `json:"complianceState"`
}

func (h *handler) evaluateTargetSet(w http.ResponseWriter, r *http.Request) {
	ev, err := h.svc.EvaluateTargetSet(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"matched": ev.Matched, "scanned": ev.Scanned, "truncated": ev.Truncated, "incomplete": ev.Incomplete,
		"cap": application.MaxTargetDevices, "byPlatform": ev.ByPlatform, "byCompliance": ev.ByCompliance, "evaluatedAt": ts(ev.EvaluatedAt),
		"examples": mapItems(ev.Examples, func(e application.TargetExample) targetExampleDTO {
			out := targetExampleDTO{DeviceID: e.DeviceID, Redacted: e.Redacted, OSPlatform: e.OSPlatform, ComplianceState: e.ComplianceState}
			if !e.Redacted {
				out.Name = &e.Name
			}
			return out
		})})
}

type clauseDTO struct {
	Clause string  `json:"clause"`
	Result string  `json:"result"`
	Reason *string `json:"reason"`
}

func (h *handler) explainTargetSet(w http.ResponseWriter, r *http.Request) {
	ex, err := h.svc.ExplainTargetSet(r.Context(), principal(r), r.PathValue("id"), r.URL.Query().Get("deviceId"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	var name *string
	if !ex.Redacted {
		name = &ex.DeviceName
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"targetSetId": ex.TargetSetID, "deviceId": ex.DeviceID, "deviceName": name, "redacted": ex.Redacted,
		"matched": ex.Matched, "incomplete": ex.Incomplete, "evaluatedAt": ts(ex.EvaluatedAt),
		"clauses": mapItems(ex.Clauses, func(c application.ClauseResult) clauseDTO {
			out := clauseDTO{Clause: c.Clause, Result: c.Result}
			if c.Reason != "" {
				reason := c.Reason
				out.Reason = &reason
			}
			return out
		})})
}

// ---- deployments ----

type deploymentDTO struct {
	ID                string   `json:"id"`
	Reference         string   `json:"reference"`
	Name              string   `json:"name"`
	SoftwareVersionID string   `json:"softwareVersionId"`
	ProductID         string   `json:"productId"`
	ProductName       string   `json:"productName"`
	ProductVersion    string   `json:"productVersion"`
	Intent            string   `json:"intent"`
	Supersede         bool     `json:"supersede"`
	Status            string   `json:"status"`
	StatusReason      *string  `json:"statusReason"`
	OwnerUserID       string   `json:"ownerUserId"`
	CreatedBy         string   `json:"createdBy"`
	Editors           []string `json:"editors"`
	HighImpact        bool     `json:"highImpact"`
	ApprovalID        *string  `json:"approvalId"`
	SubmittedBy       *string  `json:"submittedBy"`
	SubmittedAt       *string  `json:"submittedAt"`
	PlanSHA256        *string  `json:"planSha256"`
	ApprovedAt        *string  `json:"approvedAt"`
	ScheduledBy       *string  `json:"scheduledBy"`
	ScheduledAt       *string  `json:"scheduledAt"`
	CancelledBy       *string  `json:"cancelledBy"`
	CancelledAt       *string  `json:"cancelledAt"`
	Version           int      `json:"version"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

func toDeployment(d application.Deployment) deploymentDTO {
	return deploymentDTO{ID: d.ID, Reference: d.Reference, Name: d.Name, SoftwareVersionID: d.SoftwareVersionID, ProductID: d.ProductID,
		ProductName: d.ProductName, ProductVersion: d.ProductVersion, Intent: d.Intent, Supersede: d.Supersede, Status: d.Status,
		StatusReason: d.StatusReason, OwnerUserID: d.OwnerUserID, CreatedBy: d.CreatedBy, Editors: d.Editors, HighImpact: d.HighImpact,
		ApprovalID: d.ApprovalID, SubmittedBy: d.SubmittedBy, SubmittedAt: tsPtr(d.SubmittedAt), PlanSHA256: d.PlanSHA256,
		ApprovedAt: tsPtr(d.ApprovedAt), ScheduledBy: d.ScheduledBy, ScheduledAt: tsPtr(d.ScheduledAt), CancelledBy: d.CancelledBy,
		CancelledAt: tsPtr(d.CancelledAt), Version: d.Version, CreatedAt: ts(d.CreatedAt), UpdatedAt: ts(d.UpdatedAt)}
}

type ringDTO struct {
	ID                      string  `json:"id"`
	Position                int     `json:"position"`
	Name                    string  `json:"name"`
	TargetSetID             string  `json:"targetSetId"`
	TargetSetReference      string  `json:"targetSetReference"`
	TargetSetName           string  `json:"targetSetName"`
	ApprovalRequired        bool    `json:"approvalRequired"`
	SuccessThresholdPercent int     `json:"successThresholdPercent"`
	MinFreshEvidencePercent *int    `json:"minFreshEvidencePercent"`
	SoakMinutes             int     `json:"soakMinutes"`
	ChangeID                *string `json:"changeId"`
	NoWindowRequired        bool    `json:"noWindowRequired"`
	MaxTargets              int     `json:"maxTargets"`
}

func toRing(g application.DeploymentRing) ringDTO {
	return ringDTO{ID: g.ID, Position: g.Position, Name: g.Name, TargetSetID: g.TargetSetID, TargetSetReference: g.TargetSetReference,
		TargetSetName: g.TargetSetName, ApprovalRequired: g.ApprovalRequired, SuccessThresholdPercent: g.SuccessThresholdPercent,
		MinFreshEvidencePercent: g.MinFreshEvidencePercent, SoakMinutes: g.SoakMinutes, ChangeID: g.ChangeID, NoWindowRequired: g.NoWindowRequired,
		MaxTargets: g.MaxTargets}
}

type issueDTO struct {
	Code         string  `json:"code"`
	Blocking     bool    `json:"blocking"`
	RingID       *string `json:"ringId"`
	Count        *int    `json:"count"`
	DeploymentID *string `json:"deploymentId"`
}

func toIssue(i application.PlanIssue) issueDTO {
	return issueDTO{Code: i.Code, Blocking: i.Blocking, RingID: i.RingID, Count: i.Count, DeploymentID: i.DeploymentID}
}

type ringTargetsDTO struct {
	RingID     string `json:"ringId"`
	Matched    int    `json:"matched"`
	Truncated  bool   `json:"truncated"`
	Incomplete bool   `json:"incomplete"`
}

func toValidation(v application.PlanValidation) map[string]any {
	return map[string]any{"valid": v.Valid, "highImpact": v.HighImpact, "evaluated": v.Evaluated, "validatedAt": ts(v.ValidatedAt),
		"issues": mapItems(v.Issues, toIssue),
		"rings": mapItems(v.Rings, func(t application.RingTargets) ringTargetsDTO {
			return ringTargetsDTO{RingID: t.RingID, Matched: t.Matched, Truncated: t.Truncated, Incomplete: t.Incomplete}
		})}
}

func (h *handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := h.svc.ListDeployments(r.Context(), principal(r), application.DeploymentFilter{Status: q.Get("status"), ProductID: q.Get("productId"),
		VersionID: q.Get("versionId"), Page: page})
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(res.Items, toDeployment), "nextCursor": res.NextCursor})
}

func (h *handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetDeployment(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	type changeDTO struct {
		ID          string  `json:"id"`
		Reference   string  `json:"reference"`
		Status      string  `json:"status"`
		WindowStart *string `json:"windowStart"`
		WindowEnd   *string `json:"windowEnd"`
	}
	changes := map[string]changeDTO{}
	for id, c := range d.Changes {
		changes[id] = changeDTO{ID: c.ID, Reference: c.Reference, Status: c.Status, WindowStart: tsPtr(c.WindowStart), WindowEnd: tsPtr(c.WindowEnd)}
	}
	type approvalStateDTO struct {
		ID              string  `json:"id"`
		Status          string  `json:"status"`
		ApproverUserID  *string `json:"approverUserId"`
		ApproverTeamID  *string `json:"approverTeamId"`
		DecidedByUserID *string `json:"decidedByUserId"`
		DecidedAt       *string `json:"decidedAt"`
	}
	type transitionDTO struct {
		FromStatus  *string `json:"fromStatus"`
		ToStatus    string  `json:"toStatus"`
		Operation   string  `json:"operation"`
		Reason      *string `json:"reason"`
		PlanSHA256  *string `json:"planSha256"`
		ActorUserID *string `json:"actorUserId"`
		ActorSystem *string `json:"actorSystem"`
		CreatedAt   string  `json:"createdAt"`
	}
	httpx.JSON(w, http.StatusOK, struct {
		deploymentDTO
		Rings       []ringDTO            `json:"rings"`
		Transitions []transitionDTO      `json:"transitions"`
		Approvals   []approvalStateDTO   `json:"approvals"`
		Changes     map[string]changeDTO `json:"changes"`
		Validation  map[string]any       `json:"validation"`
	}{deploymentDTO: toDeployment(d.Deployment), Rings: mapItems(d.Rings, toRing), Changes: changes, Validation: toValidation(d.Validation),
		Transitions: mapItems(d.Transitions, func(t application.DeploymentTransition) transitionDTO {
			return transitionDTO{FromStatus: t.FromStatus, ToStatus: t.ToStatus, Operation: t.Operation, Reason: t.Reason, PlanSHA256: t.PlanSHA256,
				ActorUserID: t.ActorUserID, ActorSystem: t.ActorSystem, CreatedAt: ts(t.CreatedAt)}
		}),
		Approvals: mapItems(d.Approvals, func(a application.DeploymentApprovalInfo) approvalStateDTO {
			return approvalStateDTO{ID: a.ID, Status: a.Status, ApproverUserID: a.ApproverUserID, ApproverTeamID: a.ApproverTeamID,
				DecidedByUserID: a.DecidedByUserID, DecidedAt: tsPtr(a.DecidedAt)}
		})})
}

type deploymentBody struct {
	Name              string `json:"name"`
	SoftwareVersionID string `json:"softwareVersionId"`
	Intent            string `json:"intent"`
	Supersede         bool   `json:"supersede"`
	OwnerUserID       string `json:"ownerUserId"`
	ExpectedVersion   *int   `json:"expectedVersion"`
}

func (b deploymentBody) input() application.DeploymentInput {
	return application.DeploymentInput{Name: b.Name, SoftwareVersionID: b.SoftwareVersionID, Intent: b.Intent, Supersede: b.Supersede, OwnerUserID: b.OwnerUserID}
}

func (h *handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	var b deploymentBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "A new deployment takes no expectedVersion.")
		return
	}
	d, err := h.svc.CreateDeployment(r.Context(), caller(w, r), principal(r), b.input())
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDeployment(d))
}

func (h *handler) updateDeployment(w http.ResponseWriter, r *http.Request) {
	var b deploymentBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.UpdateDeployment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.input())
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

type ringBody struct {
	Name                    string  `json:"name"`
	TargetSetID             string  `json:"targetSetId"`
	ApprovalRequired        bool    `json:"approvalRequired"`
	SuccessThresholdPercent int     `json:"successThresholdPercent"`
	MinFreshEvidencePercent *int    `json:"minFreshEvidencePercent"`
	SoakMinutes             int     `json:"soakMinutes"`
	ChangeID                *string `json:"changeId"`
	NoWindowRequired        bool    `json:"noWindowRequired"`
	MaxTargets              *int    `json:"maxTargets"`
	ExpectedVersion         *int    `json:"expectedVersion"`
}

func (b ringBody) input() application.RingInput {
	return application.RingInput{Name: b.Name, TargetSetID: b.TargetSetID, ApprovalRequired: b.ApprovalRequired, SuccessThresholdPercent: b.SuccessThresholdPercent,
		MinFreshEvidencePercent: b.MinFreshEvidencePercent, SoakMinutes: b.SoakMinutes, ChangeID: b.ChangeID, NoWindowRequired: b.NoWindowRequired, MaxTargets: b.MaxTargets}
}

func (h *handler) ringResponse(w http.ResponseWriter, r *http.Request, status int, d application.Deployment, g application.DeploymentRing, err error) {
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, status, map[string]any{"deployment": toDeployment(d), "ring": toRing(g)})
}

func (h *handler) addRing(w http.ResponseWriter, r *http.Request) {
	var b ringBody
	if !decode(w, r, &b) {
		return
	}
	d, g, err := h.svc.AddRing(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.input())
	h.ringResponse(w, r, http.StatusCreated, d, g, err)
}

func (h *handler) updateRing(w http.ResponseWriter, r *http.Request) {
	var b ringBody
	if !decode(w, r, &b) {
		return
	}
	d, g, err := h.svc.UpdateRing(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("ringId"), b.ExpectedVersion, b.input())
	h.ringResponse(w, r, http.StatusOK, d, g, err)
}

func (h *handler) removeRing(w http.ResponseWriter, r *http.Request) {
	var expected *int
	if v := r.URL.Query().Get("expectedVersion"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "expectedVersion must be a number.")
			return
		}
		expected = &n
	}
	d, err := h.svc.RemoveRing(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("ringId"), expected)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) reorderRings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RingIDs         []string `json:"ringIds"`
		ExpectedVersion *int     `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.ReorderRings(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.RingIDs)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) validateDeployment(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.ValidateDeployment(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toValidation(v))
}

func (h *handler) submitDeployment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ApproverUserID  *string `json:"approverUserId"`
		ApproverTeamID  *string `json:"approverTeamId"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.SubmitDeployment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion,
		application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID})
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) scheduleDeployment(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.ScheduleDeployment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}

func (h *handler) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.CancelDeployment(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.deploymentFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDeployment(d))
}
