// Package transport exposes the Service Request HTTP API under /api/v1. Any
// signed-in User submits requests and sees their own; requests.view sees all;
// requests.manage also changes any request.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView   = "requests.view"
	permManage = "requests.manage"
	maxBody    = 80 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the request routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("POST /api/v1/service-requests", h.submit)
	route("GET /api/v1/service-requests", h.list)
	route("GET /api/v1/service-requests/approval-preview", h.approvalPreview)
	route("GET /api/v1/service-requests/{id}", h.get)
	route("POST /api/v1/service-requests/{id}/cancel", h.cancel)
	route("POST /api/v1/service-requests/{id}/hold", h.hold)
	route("POST /api/v1/service-requests/{id}/resume", h.resume)
	route("POST /api/v1/service-requests/{id}/complete", h.complete)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	var fe *catalogpublic.FieldErrors
	var ci *catalogpublic.InvalidInput
	switch {
	case errors.As(err, &fe):
		httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{
			"code": "requests.invalid_answers", "message": "Some answers are invalid.", "requestId": httpx.RequestID(w), "fields": fe.Errors,
		}})
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_request", inv.Message)
	case errors.As(err, &ci):
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_request", ci.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "requests.invalid_transition", "The operation is not allowed in the request's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "requests.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrItemInactive):
		httpx.WriteError(w, http.StatusConflict, "requests.item_inactive", "This catalog item is not available.")
	case errors.Is(err, application.ErrRequestedForInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "requests.requested_for_invalid", "The request cannot be made for this user.")
	case errors.Is(err, application.ErrNoEligibleApprover):
		httpx.WriteError(w, http.StatusConflict, "requests.no_eligible_approver", "No approver is available for this request; contact the IT service desk.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "requests.version_conflict", "The request was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "service request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

type requestDTO struct {
	ID               string  `json:"id"`
	Reference        string  `json:"reference"`
	CatalogItemID    string  `json:"catalogItemId"`
	CatalogItemTitle string  `json:"catalogItemTitle"`
	RequesterID      string  `json:"requesterId"`
	RequestedForID   string  `json:"requestedForId"`
	Status           string  `json:"status"`
	WaitingReason    *string `json:"waitingReason"`
	StatusReason     *string `json:"statusReason"`
	CurrentStep      *int    `json:"currentApprovalStep"`
	SubmittedAt      string  `json:"submittedAt"`
	CompletedAt      *string `json:"completedAt"`
	Version          int     `json:"version"`
	UpdatedAt        string  `json:"updatedAt"`
}

func toRequest(r application.Request) requestDTO {
	return requestDTO{
		ID: r.ID, Reference: r.Reference, CatalogItemID: r.CatalogItemID, CatalogItemTitle: r.CatalogItemTitle,
		RequesterID: r.RequesterID, RequestedForID: r.RequestedForID, Status: r.Status, WaitingReason: r.WaitingReason,
		StatusReason: r.StatusReason, CurrentStep: r.CurrentStep, SubmittedAt: ts(r.SubmittedAt), CompletedAt: tsPtr(r.CompletedAt),
		Version: r.Version, UpdatedAt: ts(r.UpdatedAt),
	}
}

type submitBody struct {
	CatalogItemID  string         `json:"catalogItemId"`
	RequestedForID *string        `json:"requestedForId"`
	Answers        map[string]any `json:"answers"`
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	var b submitBody
	if !decode(w, r, &b) {
		return
	}
	req, err := h.svc.Submit(r.Context(), caller(w, r), application.SubmitInput{
		CatalogItemID: b.CatalogItemID, RequestedForID: b.RequestedForID, Answers: b.Answers,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRequest(req))
}

// approvalPreview shows who would approve a request for a catalog item before it is submitted.
func (h *handler) approvalPreview(w http.ResponseWriter, r *http.Request) {
	var requestedFor *string
	if v := r.URL.Query().Get("requestedForId"); v != "" {
		requestedFor = &v
	}
	steps, err := h.svc.ApprovalPreview(r.Context(), caller(w, r), r.URL.Query().Get("catalogItemId"), requestedFor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type stepDTO struct {
		Index        int    `json:"index"`
		Kind         string `json:"kind"`
		Resolved     bool   `json:"resolved"`
		Fallback     bool   `json:"fallback"`
		ApproverName string `json:"approverName,omitempty"`
	}
	out := make([]stepDTO, 0, len(steps))
	ok := true
	for _, s := range steps {
		out = append(out, stepDTO{s.Index, s.Kind, s.Resolved, s.Fallback, s.ApproverName})
		ok = ok && s.Resolved
	}
	httpx.JSON(w, http.StatusOK, struct {
		ApprovalRequired bool      `json:"approvalRequired"`
		CanSubmit        bool      `json:"canSubmit"`
		Steps            []stepDTO `json:"steps"`
	}{len(out) > 0, ok, out})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_limit", "The limit must be a positive integer.")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope != "" && scope != "mine" && scope != "all" {
		httpx.WriteError(w, http.StatusBadRequest, "requests.invalid_request", "The scope must be mine or all.")
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), scope == "all", r.URL.Query().Get("status"),
		application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []requestDTO `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: make([]requestDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, it := range res.Items {
		out.Items = append(out.Items, toRequest(it))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type approvalDTO struct {
	ID              string  `json:"id"`
	StepIndex       int     `json:"stepIndex"`
	Status          string  `json:"status"`
	ApproverUserID  *string `json:"approverUserId"`
	ApproverTeamID  *string `json:"approverTeamId"`
	DecidedByUserID *string `json:"decidedByUserId"`
	DecidedAt       *string `json:"decidedAt"`
	DecisionComment *string `json:"decisionComment"`
}

type taskDTO struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	Priority       string  `json:"priority"`
	Mandatory      bool    `json:"mandatory"`
	DueAt          *string `json:"dueAt"`
	AssignedUserID *string `json:"assignedUserId"`
	AssignedTeamID *string `json:"assignedTeamId"`
	// ResultNote is the closing note of a completed task when its completer marked it for the requester.
	ResultNote *string `json:"resultNote"`
}

type referenceDTO struct {
	FieldKey string `json:"fieldKey"`
	Type     string `json:"type"`
	ID       string `json:"id"`
}

type detailDTO struct {
	requestDTO
	AllowRequestedFor bool              `json:"allowRequestedFor"`
	Fields            []json.RawMessage `json:"fields"`
	Answers           map[string]any    `json:"answers"`
	References        []referenceDTO    `json:"references"`
	Approvals         []approvalDTO     `json:"approvals"`
	Tasks             []taskDTO         `json:"tasks"`
	Names             map[string]string `json:"names"`
	Actions           []string          `json:"actions"`
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	dto := detailDTO{
		requestDTO: toRequest(d.Request), AllowRequestedFor: d.Request.Definition.AllowRequestedFor,
		Answers: d.Request.Answers, Names: d.Names, Actions: d.Actions,
		Fields: []json.RawMessage{}, References: []referenceDTO{}, Approvals: []approvalDTO{}, Tasks: []taskDTO{},
	}
	if dto.Actions == nil {
		dto.Actions = []string{}
	}
	// The form fields (label, type, select options) let a client show the answers
	// as the requester saw them; approval and fulfillment details of the
	// snapshot are not part of this view.
	for _, f := range d.Request.Definition.Fields {
		raw, _ := json.Marshal(struct {
			Key     string              `json:"key"`
			Type    string              `json:"type"`
			Label   string              `json:"label"`
			Choices []map[string]string `json:"options,omitempty"`
		}{Key: f.Key, Type: f.Type, Label: f.Label, Choices: optionMaps(f)})
		dto.Fields = append(dto.Fields, raw)
	}
	for _, ref := range d.References {
		dto.References = append(dto.References, referenceDTO{FieldKey: ref.FieldKey, Type: ref.Type, ID: ref.ID})
	}
	for _, a := range d.Approvals {
		dto.Approvals = append(dto.Approvals, approvalDTO{
			ID: a.ID, StepIndex: a.StepIndex, Status: a.Status, ApproverUserID: a.ApproverUserID, ApproverTeamID: a.ApproverTeamID,
			DecidedByUserID: a.DecidedByUserID, DecidedAt: tsPtr(a.DecidedAt), DecisionComment: a.DecisionComment,
		})
	}
	for _, t := range d.Tasks {
		dto.Tasks = append(dto.Tasks, taskDTO{
			ID: t.Task.ID, Title: t.Task.Title, Status: t.Task.Status, Priority: t.Task.Priority, Mandatory: t.Mandatory,
			DueAt: tsPtr(t.Task.DueAt), AssignedUserID: t.Task.AssignedUserID, AssignedTeamID: t.Task.AssignedTeamID,
			ResultNote: t.Task.ResultNote,
		})
	}
	httpx.JSON(w, http.StatusOK, dto)
}

func optionMaps(f catalogpublic.Field) []map[string]string {
	if len(f.Options) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(f.Options))
	for _, o := range f.Options {
		out = append(out, map[string]string{"value": o.Value, "label": o.Label})
	}
	return out
}

type reasonBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

func (h *handler) op(do func(r *http.Request, c application.Caller, p application.Principal, id string, v *int, reason string) (application.Request, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b reasonBody
		if !decode(w, r, &b) {
			return
		}
		req, err := do(r, caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, strings.TrimSpace(b.Reason))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRequest(req))
	}
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	h.op(func(r *http.Request, c application.Caller, p application.Principal, id string, v *int, reason string) (application.Request, error) {
		return h.svc.Cancel(r.Context(), c, p, id, v, reason)
	})(w, r)
}

func (h *handler) hold(w http.ResponseWriter, r *http.Request) {
	h.op(func(r *http.Request, c application.Caller, p application.Principal, id string, v *int, reason string) (application.Request, error) {
		return h.svc.PutOnHold(r.Context(), c, p, id, v, reason)
	})(w, r)
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request) {
	h.op(func(r *http.Request, c application.Caller, p application.Principal, id string, v *int, _ string) (application.Request, error) {
		return h.svc.Resume(r.Context(), c, p, id, v)
	})(w, r)
}

func (h *handler) complete(w http.ResponseWriter, r *http.Request) {
	h.op(func(r *http.Request, c application.Caller, p application.Principal, id string, v *int, reason string) (application.Request, error) {
		return h.svc.Complete(r.Context(), c, p, id, v, reason)
	})(w, r)
}
