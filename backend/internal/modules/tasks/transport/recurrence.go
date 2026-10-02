package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type recurrenceHandler struct {
	svc    *application.RecurrenceService
	logger *slog.Logger
}

// RegisterRecurrence mounts the Recurring Task Definition routes; all of them
// need tasks.recurrence.manage.
func RegisterRecurrence(mux *http.ServeMux, svc *application.RecurrenceService, auth authorization.Authenticator, logger *slog.Logger) {
	h := &recurrenceHandler{svc: svc, logger: logger}
	manage := authorization.Require(auth, permRecurrence)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(manage(fn))) }
	route("GET /api/v1/recurring-task-definitions", h.list)
	route("POST /api/v1/recurring-task-definitions", h.create)
	route("GET /api/v1/recurring-task-definitions/{id}", h.get)
	route("PATCH /api/v1/recurring-task-definitions/{id}", h.update)
	route("DELETE /api/v1/recurring-task-definitions/{id}", h.delete)
	route("POST /api/v1/recurring-task-definitions/{id}/pause", h.pause)
	route("POST /api/v1/recurring-task-definitions/{id}/resume", h.resume)
}

func (h *recurrenceHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", inv.Message)
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tasks.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "tasks.version_conflict", "The definition was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrAssigneeInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.assignee_invalid", "The assignee must be an active user or team.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "recurring task definition request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

type ruleDTO struct {
	Frequency  string `json:"frequency"`
	Interval   int    `json:"interval"`
	Weekday    *int   `json:"weekday"`
	DayOfMonth *int   `json:"dayOfMonth"`
	TimeOfDay  string `json:"timeOfDay"`
	Timezone   string `json:"timezone"`
	StartsOn   string `json:"startsOn"`
}

func (d ruleDTO) toRule() application.Rule {
	r := application.Rule{Frequency: d.Frequency, Interval: d.Interval, TimeOfDay: d.TimeOfDay, Timezone: d.Timezone, StartsOn: d.StartsOn}
	if d.Weekday != nil {
		r.Weekday = *d.Weekday
	}
	if d.DayOfMonth != nil {
		r.DayOfMonth = *d.DayOfMonth
	}
	return r
}

func toRuleDTO(r application.Rule) ruleDTO {
	d := ruleDTO{Frequency: r.Frequency, Interval: r.Interval, TimeOfDay: r.TimeOfDay, Timezone: r.Timezone, StartsOn: r.StartsOn}
	if r.Weekday != 0 {
		d.Weekday = &r.Weekday
	}
	if r.DayOfMonth != 0 {
		d.DayOfMonth = &r.DayOfMonth
	}
	return d
}

type definitionDTO struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Description     *string `json:"description"`
	Priority        string  `json:"priority"`
	AssignedUserID  *string `json:"assignedUserId"`
	AssignedTeamID  *string `json:"assignedTeamId"`
	DueAfterHours   *int    `json:"dueAfterHours"`
	Rule            ruleDTO `json:"rule"`
	Active          bool    `json:"active"`
	NextRunAt       *string `json:"nextRunAt"`
	LastGeneratedAt *string `json:"lastGeneratedAt"`
	CreatedByUserID *string `json:"createdByUserId"`
	Version         int     `json:"version"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

func toDefinitionDTO(d application.Definition) definitionDTO {
	return definitionDTO{
		ID: d.ID, Title: d.Title, Description: d.Description, Priority: d.Priority,
		AssignedUserID: d.AssignedUserID, AssignedTeamID: d.AssignedTeamID, DueAfterHours: d.DueAfterHours,
		Rule: toRuleDTO(d.Rule), Active: d.Active, NextRunAt: tsPtr(d.NextRunAt), LastGeneratedAt: tsPtr(d.LastGeneratedAt),
		CreatedByUserID: d.CreatedByUserID, Version: d.Version, CreatedAt: ts(d.CreatedAt), UpdatedAt: ts(d.UpdatedAt),
	}
}

func (h *recurrenceHandler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []definitionDTO `json:"items"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}{Items: make([]definitionDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, d := range res.Items {
		out.Items = append(out.Items, toDefinitionDTO(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *recurrenceHandler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDefinitionDTO(d))
}

type definitionBody struct {
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Priority       string  `json:"priority"`
	AssignedUserID *string `json:"assignedUserId"`
	AssignedTeamID *string `json:"assignedTeamId"`
	DueAfterHours  *int    `json:"dueAfterHours"`
	Rule           ruleDTO `json:"rule"`
}

func (h *recurrenceHandler) create(w http.ResponseWriter, r *http.Request) {
	var b definitionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.CreateDefinition(r.Context(), caller(w, r), principal(r), application.DefinitionInput{
		Title: b.Title, Description: b.Description, Priority: b.Priority, AssignedUserID: b.AssignedUserID,
		AssignedTeamID: b.AssignedTeamID, DueAfterHours: b.DueAfterHours, Rule: b.Rule.toRule(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDefinitionDTO(d))
}

type updateDefinitionBody struct {
	ExpectedVersion *int     `json:"expectedVersion"`
	Title           *string  `json:"title"`
	Description     *string  `json:"description"`
	Priority        *string  `json:"priority"`
	AssignedUserID  *string  `json:"assignedUserId"`
	AssignedTeamID  *string  `json:"assignedTeamId"`
	ClearAssignment bool     `json:"clearAssignment"`
	DueAfterHours   *int     `json:"dueAfterHours"`
	ClearDueAfter   bool     `json:"clearDueAfter"`
	Rule            *ruleDTO `json:"rule"`
}

func (h *recurrenceHandler) update(w http.ResponseWriter, r *http.Request) {
	var b updateDefinitionBody
	if !decode(w, r, &b) {
		return
	}
	in := application.UpdateDefinitionInput{
		Title: b.Title, Description: b.Description, Priority: b.Priority, AssignedUserID: b.AssignedUserID,
		AssignedTeamID: b.AssignedTeamID, ClearAssignment: b.ClearAssignment, DueAfterHours: b.DueAfterHours,
		ClearDueAfter: b.ClearDueAfter,
	}
	if b.Rule != nil {
		rule := b.Rule.toRule()
		in.Rule = &rule
	}
	d, err := h.svc.UpdateDefinition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDefinitionDTO(d))
}

func (h *recurrenceHandler) pause(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.Pause(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDefinitionDTO(d))
}

func (h *recurrenceHandler) resume(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.Resume(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDefinitionDTO(d))
}

func (h *recurrenceHandler) delete(w http.ResponseWriter, r *http.Request) {
	var expected *int
	if v := r.URL.Query().Get("expectedVersion"); v != "" {
		n, err := parsePositiveInt(v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "The expected version must be a positive integer.")
			return
		}
		expected = &n
	}
	if err := h.svc.DeleteDefinition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), expected); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
