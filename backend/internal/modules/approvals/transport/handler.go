// Package transport exposes the approval inbox and decisions under /api/v1.
// Access is by assignment, not by permission: any signed-in User sees and
// decides exactly the approvals assigned to them (directly or through a Team).
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the approval routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/approvals", h.list)
	route("GET /api/v1/approvals/{id}", h.get)
	route("POST /api/v1/approvals/{id}/approve", h.decide(application.DecisionApprove))
	route("POST /api/v1/approvals/{id}/reject", h.decide(application.DecisionReject))
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "approvals.invalid_request", inv.Message)
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "approvals.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrNotApprover):
		httpx.WriteError(w, http.StatusForbidden, "approvals.not_approver", "You may not decide this approval.")
	case errors.Is(err, application.ErrNotPending):
		httpx.WriteError(w, http.StatusConflict, "approvals.not_pending", "The approval has already been decided or cancelled.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "approvals.version_conflict", "The approval was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "approvals.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "approval request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func userID(r *http.Request) string {
	p, _ := authorization.PrincipalFrom(r.Context())
	return p.UserID
}

type approvalDTO struct {
	ID              string  `json:"id"`
	SubjectType     string  `json:"subjectType"`
	SubjectID       string  `json:"subjectId"`
	SubjectLabel    string  `json:"subjectLabel"`
	StepIndex       int     `json:"stepIndex"`
	Status          string  `json:"status"`
	ApproverUserID  *string `json:"approverUserId"`
	ApproverTeamID  *string `json:"approverTeamId"`
	DecidedByUserID *string `json:"decidedByUserId"`
	DecidedAt       *string `json:"decidedAt"`
	DecisionComment *string `json:"decisionComment"`
	Version         int     `json:"version"`
	CreatedAt       string  `json:"createdAt"`
}

func toDTO(a application.Approval) approvalDTO {
	d := approvalDTO{
		ID: a.ID, SubjectType: a.SubjectType, SubjectID: a.SubjectID, SubjectLabel: a.SubjectLabel, StepIndex: a.StepIndex,
		Status: a.Status, ApproverUserID: a.ApproverUserID, ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID,
		DecisionComment: a.DecisionComment, Version: a.Version, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.DecidedAt != nil {
		s := a.DecidedAt.UTC().Format(time.RFC3339)
		d.DecidedAt = &s
	}
	return d
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "approvals.invalid_limit", "The limit must be a positive integer.")
		return
	}
	res, err := h.svc.Inbox(r.Context(), userID(r), r.URL.Query().Get("status"), application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []approvalDTO `json:"items"`
		NextCursor string        `json:"nextCursor,omitempty"`
	}{Items: make([]approvalDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, a := range res.Items {
		out.Items = append(out.Items, toDTO(a))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	a, err := h.svc.Get(r.Context(), userID(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDTO(a))
}

func (h *handler) decide(decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Comment         string `json:"comment"`
			ExpectedVersion *int   `json:"expectedVersion"`
		}
		if err := httpx.DecodeJSON(w, r, &b, 8<<10); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "approvals.invalid_request", "The request body is not valid JSON for this operation.")
			return
		}
		c := application.Caller{Actor: audit.UserActor(userID(r)), CorrelationID: httpx.RequestID(w)}
		a, err := h.svc.Decide(r.Context(), c, r.PathValue("id"), decision, b.Comment, b.ExpectedVersion)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toDTO(a))
	}
}
