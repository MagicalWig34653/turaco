// Package transport exposes the Task HTTP API under /api/v1: task CRUD,
// explicit lifecycle actions and My Work.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

const (
	permView       = "tasks.view"
	permManage     = "tasks.manage"
	permWork       = "tasks.work"
	permRecurrence = "tasks.recurrence.manage"
	permBoardsTeam = "tasks.boards.manage_team"

	maxBody     = 32 << 10
	maxQueryLen = 100
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Task routes. Route permissions are only the outer gate;
// the service decides per task what the caller may see and do.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	anyTask := authorization.RequireAny(auth, application.TaskPermissions...)
	manage := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/tasks", anyTask, h.list)
	route("GET /api/v1/tasks/fields", anyTask, h.fields)
	route("POST /api/v1/tasks/query", anyTask, h.query)
	route("POST /api/v1/tasks", manage, h.create)
	route("GET /api/v1/tasks/{id}", anyTask, h.get)
	route("PATCH /api/v1/tasks/{id}", manage, h.update)
	route("POST /api/v1/tasks/{id}/assign", manage, h.assign)
	route("POST /api/v1/tasks/{id}/unassign", manage, h.unassign)
	route("POST /api/v1/tasks/{id}/cancel", manage, h.transition(application.OpCancel))
	route("POST /api/v1/tasks/{id}/reopen", manage, h.transition(application.OpReopen))
	for _, op := range []application.Operation{application.OpStart, application.OpBlock, application.OpUnblock, application.OpComplete} {
		route("POST /api/v1/tasks/{id}/"+string(op), anyTask, h.transition(op))
	}
	route("GET /api/v1/my-work", anyTask, h.myWork)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if query.WriteError(w, err) {
		return
	}
	var inv *application.InvalidInputError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", inv.Message)
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "tasks.invalid_transition", "The operation is not allowed in the task's current status.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tasks.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "tasks.version_conflict", "The task was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrAssigneeInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.assignee_invalid", "The assignee must be an active user or team.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "task request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{
		UserID: p.UserID, ViewAll: p.Has(permView), Manage: p.Has(permManage), Work: p.Has(permWork),
		RecurrenceManage: p.Has(permRecurrence), BoardsManageTeam: p.Has(permBoardsTeam),
	}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func parseBool(w http.ResponseWriter, r *http.Request, name string) (value, ok bool) {
	switch r.URL.Query().Get(name) {
	case "":
		return false, true
	case "true":
		return true, true
	}
	httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "The "+name+" filter must be true.")
	return false, false
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := application.ListFilter{
		Priority: q.Get("priority"), AssignedUserID: q.Get("assignedUserId"), AssignedTeamID: q.Get("assignedTeamId"),
		TitlePrefix: q.Get("q"), Page: page,
	}
	if utf8.RuneCountInString(f.TitlePrefix) > maxQueryLen || !utf8.ValidString(f.TitlePrefix) || strings.ContainsRune(f.TitlePrefix, 0) {
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "The search query is invalid.")
		return
	}
	for _, v := range q["status"] {
		f.Statuses = append(f.Statuses, strings.Split(v, ",")...)
	}
	if f.Overdue, ok = parseBool(w, r, "overdue"); !ok {
		return
	}
	if f.Mine, ok = parseBool(w, r, "mine"); !ok {
		return
	}
	if query.HasParams(q) {
		// Additive filter/sort/search/count parameters; the plain parameters are ANDed to the filter.
		req, err := query.ParseParams(q, page.Cursor, page.Limit)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		res, err := h.svc.Query(r.Context(), principal(r), req, f.Mine, h.svc.CompatNodes(f))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, query.NewEnvelope(res, shaperFor(r)))
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, shaperFor(r)))
}

// fields returns the task Field Catalog as the caller may use it.
func (h *handler) fields(w http.ResponseWriter, r *http.Request) {
	info, err := h.svc.QueryFields(r.Context(), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, info)
}

// query runs a Filter AST over the tasks the caller may see.
func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	req, err := query.DecodeBody(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.svc.Query(r.Context(), principal(r), req, false, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, query.NewEnvelope(res, shaperFor(r)))
}

func (h *handler) myWork(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.MyWork(r.Context(), principal(r), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, shaperFor(r)))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, shaperFor(r)(v))
}

type createBody struct {
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Priority       string     `json:"priority"`
	DueAt          *time.Time `json:"dueAt"`
	AssignedUserID *string    `json:"assignedUserId"`
	AssignedTeamID *string    `json:"assignedTeamId"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{
		Title: b.Title, Description: b.Description, Priority: b.Priority, DueAt: b.DueAt,
		AssignedUserID: b.AssignedUserID, AssignedTeamID: b.AssignedTeamID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTask(v))
}

// optionalTime distinguishes an absent dueAt (unchanged) from null (cleared).
type optionalTime struct {
	Set   bool
	Value *time.Time
}

func (o *optionalTime) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if string(raw) == "null" {
		o.Value = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	o.Value = &t
	return nil
}

type updateBody struct {
	ExpectedVersion *int         `json:"expectedVersion"`
	Title           *string      `json:"title"`
	Description     *string      `json:"description"`
	Priority        *string      `json:"priority"`
	DueAt           optionalTime `json:"dueAt"`
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "expectedVersion is required to change a task.")
		return
	}
	in := application.UpdateInput{Title: b.Title, Description: b.Description, Priority: b.Priority}
	if b.DueAt.Set {
		in.DueAt, in.ClearDueAt = b.DueAt.Value, b.DueAt.Value == nil
	}
	v, err := h.svc.UpdateDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTask(v))
}

type assignBody struct {
	ExpectedVersion *int    `json:"expectedVersion"`
	UserID          *string `json:"userId"`
	TeamID          *string `json:"teamId"`
}

func (h *handler) assign(w http.ResponseWriter, r *http.Request) {
	var b assignBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.svc.Assign(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.UserID, b.TeamID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTask(v))
}

type versionBody struct {
	ExpectedVersion *int `json:"expectedVersion"`
}

func (h *handler) unassign(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.svc.Unassign(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTask(v))
}

type transitionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
	// ResultNote is the optional closing comment of complete (at most 1000 characters).
	ResultNote string `json:"resultNote"`
	// ResultNoteForRequester shows the note to the requester of the request the task belongs to (needs a note).
	ResultNoteForRequester bool `json:"resultNoteForRequester"`
}

func (h *handler) transition(op application.Operation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b transitionBody
		if !decode(w, r, &b) {
			return
		}
		var v application.TaskView
		var err error
		if op == application.OpComplete {
			v, err = h.svc.CompleteWithVisibility(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.ResultNote, b.ResultNoteForRequester)
		} else if b.ResultNote != "" || b.ResultNoteForRequester {
			httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "A result note is only valid when completing a task.")
			return
		} else {
			v, err = h.svc.Transition(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, op, b.Reason)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, shaperFor(r)(v))
	}
}

func parsePositiveInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errors.New("not a positive integer")
	}
	return n, nil
}
