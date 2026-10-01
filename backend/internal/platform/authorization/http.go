package authorization

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permRolesView   = "platform.roles.view"
	permRolesManage = "platform.roles.manage"
	maxBodyBytes    = 64 << 10
)

type httpHandler struct {
	svc    *Service
	logger *slog.Logger
}

// Register mounts /permissions, /roles* and /role-assignments* under /api/v1.
// Every route requires authentication and the permission named in the design
// (view or manage); responses are never cacheable.
func Register(mux *http.ServeMux, svc *Service, auth Authenticator, logger *slog.Logger) {
	h := &httpHandler{svc: svc, logger: logger}
	view := Require(auth, permRolesView)
	manage := Require(auth, permRolesManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, noStore(mw(fn)))
	}
	route("GET /api/v1/permissions", view, h.listPermissions)
	route("GET /api/v1/roles", view, h.listRoles)
	route("POST /api/v1/roles", manage, h.createRole)
	route("GET /api/v1/roles/{id}", view, h.getRole)
	route("PATCH /api/v1/roles/{id}", manage, h.updateRole)
	route("DELETE /api/v1/roles/{id}", manage, h.deleteRole)
	route("PUT /api/v1/roles/{id}/permissions", manage, h.setPermissions)
	route("GET /api/v1/role-assignments", view, h.listAssignments)
	route("POST /api/v1/role-assignments", manage, h.assign)
	route("POST /api/v1/role-assignments/{id}/revoke", manage, h.revoke)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpx.JSON(w, status, httpx.ErrorEnvelope{Error: httpx.APIError{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID")}})
}

func (h *httpHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *InvalidError
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "authorization.invalid_request", inv.Message)
	case errors.Is(err, ErrUnknownPermission):
		msg := "A permission name is not registered."
		var up *UnknownPermissionError
		if errors.As(err, &up) && len(up.Names) > 0 {
			msg = "Unknown permission: " + up.Names[0] + "."
		}
		writeError(w, http.StatusBadRequest, "authorization.unknown_permission", msg)
	case errors.Is(err, ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "authorization.invalid_cursor", "The cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "authorization.not_found", "The requested resource was not found.")
	case errors.Is(err, ErrSubjectNotFound):
		writeError(w, http.StatusNotFound, "authorization.subject_not_found", "The subject was not found.")
	case errors.Is(err, ErrDuplicateKey):
		writeError(w, http.StatusConflict, "authorization.duplicate_key", "A role with this key already exists.")
	case errors.Is(err, ErrBuiltInRole):
		writeError(w, http.StatusConflict, "authorization.built_in_role", "The built-in role cannot be changed.")
	case errors.Is(err, ErrRoleInUse):
		writeError(w, http.StatusConflict, "authorization.role_in_use", "The role has active assignments.")
	case errors.Is(err, ErrDuplicateAssignment):
		writeError(w, http.StatusConflict, "authorization.duplicate_assignment", "The role is already assigned to this subject.")
	case errors.Is(err, ErrLastAdministrator):
		writeError(w, http.StatusConflict, "authorization.last_administrator", "The last administrator assignment cannot be revoked.")
	default:
		h.logger.ErrorContext(r.Context(), "authorization request failed", "request_id", w.Header().Get("X-Request-ID"), "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

// decodeBody reads one JSON object of at most 64 KiB, rejecting unknown fields
// and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "authorization.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "authorization.invalid_request", "The request body must contain a single JSON object.")
		return false
	}
	return true
}

// actor derives the audit actor from the authenticated principal.
func actor(w http.ResponseWriter, r *http.Request) Actor {
	p, _ := PrincipalFrom(r.Context())
	return UserActor(p.UserID, w.Header().Get("X-Request-ID"))
}

type permissionDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

type roleDTO struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	BuiltIn           bool      `json:"builtIn"`
	Permissions       []string  `json:"permissions"`
	ActiveAssignments int       `json:"activeAssignments"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func toRoleDTO(r Role) roleDTO {
	perms := r.Permissions
	if perms == nil {
		perms = []string{}
	}
	return roleDTO{ID: r.ID, Key: r.Key, Name: r.Name, Description: r.Description, BuiltIn: r.BuiltIn, Permissions: perms,
		ActiveAssignments: r.ActiveAssignments, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type assignmentDTO struct {
	ID                 string     `json:"id"`
	RoleID             string     `json:"roleId"`
	RoleKey            string     `json:"roleKey"`
	SubjectType        string     `json:"subjectType"`
	SubjectID          string     `json:"subjectId"`
	SubjectDisplayName string     `json:"subjectDisplayName"`
	Scope              string     `json:"scope"`
	CreatedAt          time.Time  `json:"createdAt"`
	CreatedBy          string     `json:"createdBy,omitempty"`
	RevokedAt          *time.Time `json:"revokedAt,omitempty"`
	RevokedBy          string     `json:"revokedBy,omitempty"`
}

func toAssignmentDTO(a Assignment) assignmentDTO {
	return assignmentDTO{ID: a.ID, RoleID: a.RoleID, RoleKey: a.RoleKey, SubjectType: a.SubjectType, SubjectID: a.SubjectID,
		SubjectDisplayName: a.SubjectDisplayName, Scope: a.Scope, CreatedAt: a.CreatedAt, CreatedBy: a.CreatedBy,
		RevokedAt: a.RevokedAt, RevokedBy: a.RevokedBy}
}

func (h *httpHandler) listPermissions(w http.ResponseWriter, _ *http.Request) {
	items := []permissionDTO{}
	for _, p := range h.svc.ListPermissions() {
		items = append(items, permissionDTO{Name: p.Name, Description: p.Description, Risk: p.Risk})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *httpHandler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.ListRoles(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]roleDTO, 0, len(roles))
	for _, role := range roles {
		items = append(items, toRoleDTO(role))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *httpHandler) getRole(w http.ResponseWriter, r *http.Request) {
	role, err := h.svc.GetRole(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoleDTO(role))
}

func (h *httpHandler) createRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	role, err := h.svc.CreateRole(r.Context(), actor(w, r), CreateRoleInput{Key: body.Key, Name: body.Name, Description: body.Description, Permissions: body.Permissions})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRoleDTO(role))
}

func (h *httpHandler) updateRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	role, err := h.svc.UpdateRole(r.Context(), actor(w, r), r.PathValue("id"), UpdateRoleInput{Name: body.Name, Description: body.Description})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoleDTO(role))
}

func (h *httpHandler) setPermissions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Permissions *[]string `json:"permissions"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Permissions == nil {
		writeError(w, http.StatusBadRequest, "authorization.invalid_request", "permissions is required.")
		return
	}
	role, err := h.svc.SetRolePermissions(r.Context(), actor(w, r), r.PathValue("id"), *body.Permissions)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoleDTO(role))
}

func (h *httpHandler) deleteRole(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteRole(r.Context(), actor(w, r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *httpHandler) listAssignments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := AssignmentFilter{RoleID: q.Get("roleId"), SubjectType: q.Get("subjectType"), SubjectID: q.Get("subjectId"), Page: Page{Cursor: q.Get("cursor")}}
	switch q.Get("includeRevoked") {
	case "", "false":
	case "true":
		f.IncludeRevoked = true
	default:
		writeError(w, http.StatusBadRequest, "authorization.invalid_request", "includeRevoked must be true or false.")
		return
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "authorization.invalid_limit", "The limit must be a positive integer.")
			return
		}
		f.Page.Limit = n
	}
	res, err := h.svc.ListAssignments(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]assignmentDTO, 0, len(res.Items))
	for _, a := range res.Items {
		items = append(items, toAssignmentDTO(a))
	}
	out := map[string]any{"items": items}
	if res.NextCursor != "" {
		out["nextCursor"] = res.NextCursor
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *httpHandler) assign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoleID      string `json:"roleId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	a, err := h.svc.AssignRole(r.Context(), actor(w, r), AssignInput{RoleID: body.RoleID, SubjectType: body.SubjectType, SubjectID: body.SubjectID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAssignmentDTO(a))
}

func (h *httpHandler) revoke(w http.ResponseWriter, r *http.Request) {
	a, _, err := h.svc.RevokeAssignment(r.Context(), actor(w, r), r.PathValue("id"), false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAssignmentDTO(a))
}
