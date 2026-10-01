// Package transport exposes the Role and Role assignment HTTP API under
// /api/v1: /permissions, /roles* and /role-assignments*.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permRolesView   = "platform.roles.view"
	permRolesManage = "platform.roles.manage"
	maxBodyBytes    = 64 << 10
)

type httpHandler struct {
	svc    *roles.Service
	logger *slog.Logger
}

// Register mounts /permissions, /roles* and /role-assignments* under /api/v1.
// Every route requires authentication and the permission named in the design
// (view or manage); responses are never cacheable.
func Register(mux *http.ServeMux, svc *roles.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &httpHandler{svc: svc, logger: logger}
	view := authorization.Require(auth, permRolesView)
	manage := authorization.Require(auth, permRolesManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
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

func (h *httpHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *roles.InvalidError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_request", inv.Message)
	case errors.Is(err, roles.ErrUnknownPermission):
		msg := "A permission name is not registered."
		var up *roles.UnknownPermissionError
		if errors.As(err, &up) && len(up.Names) > 0 {
			msg = "Unknown permission: " + up.Names[0] + "."
		}
		httpx.WriteError(w, http.StatusBadRequest, "authorization.unknown_permission", msg)
	case errors.Is(err, roles.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_cursor", "The cursor is invalid.")
	case errors.Is(err, roles.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "authorization.not_found", "The requested resource was not found.")
	case errors.Is(err, roles.ErrSubjectNotFound):
		httpx.WriteError(w, http.StatusNotFound, "authorization.subject_not_found", "The subject was not found.")
	case errors.Is(err, roles.ErrDuplicateKey):
		httpx.WriteError(w, http.StatusConflict, "authorization.duplicate_key", "A role with this key already exists.")
	case errors.Is(err, roles.ErrBuiltInRole):
		httpx.WriteError(w, http.StatusConflict, "authorization.built_in_role", "The built-in role cannot be changed.")
	case errors.Is(err, roles.ErrRoleInUse):
		httpx.WriteError(w, http.StatusConflict, "authorization.role_in_use", "The role has active assignments.")
	case errors.Is(err, roles.ErrDuplicateAssignment):
		httpx.WriteError(w, http.StatusConflict, "authorization.duplicate_assignment", "The role is already assigned to this subject.")
	case errors.Is(err, roles.ErrLastAdministrator):
		httpx.WriteError(w, http.StatusConflict, "authorization.last_administrator", "The last administrator assignment cannot be revoked.")
	default:
		h.logger.ErrorContext(r.Context(), "authorization request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

// decodeBody reads one JSON object of at most 64 KiB, rejecting unknown fields
// and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

// actor derives the audit actor from the authenticated principal.
func actor(r *http.Request) audit.Actor {
	p, _ := authorization.PrincipalFrom(r.Context())
	return audit.UserActor(p.UserID)
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

func toRoleDTO(r roles.Role) roleDTO {
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

func toAssignmentDTO(a roles.Assignment) assignmentDTO {
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
	role, err := h.svc.CreateRole(r.Context(), actor(r), httpx.RequestID(w), roles.CreateRoleInput{Key: body.Key, Name: body.Name, Description: body.Description, Permissions: body.Permissions})
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
	role, err := h.svc.UpdateRole(r.Context(), actor(r), httpx.RequestID(w), r.PathValue("id"), roles.UpdateRoleInput{Name: body.Name, Description: body.Description})
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
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_request", "permissions is required.")
		return
	}
	role, err := h.svc.SetRolePermissions(r.Context(), actor(r), httpx.RequestID(w), r.PathValue("id"), *body.Permissions)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoleDTO(role))
}

func (h *httpHandler) deleteRole(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteRole(r.Context(), actor(r), httpx.RequestID(w), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *httpHandler) listAssignments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := roles.AssignmentFilter{RoleID: q.Get("roleId"), SubjectType: q.Get("subjectType"), SubjectID: q.Get("subjectId"), Page: roles.Page{Cursor: q.Get("cursor")}}
	switch q.Get("includeRevoked") {
	case "", "false":
	case "true":
		f.IncludeRevoked = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_request", "includeRevoked must be true or false.")
		return
	}
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "authorization.invalid_limit", "The limit must be a positive integer.")
		return
	}
	f.Page.Limit = limit
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
	a, err := h.svc.AssignRole(r.Context(), actor(r), httpx.RequestID(w), roles.AssignInput{RoleID: body.RoleID, SubjectType: body.SubjectType, SubjectID: body.SubjectID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAssignmentDTO(a))
}

func (h *httpHandler) revoke(w http.ResponseWriter, r *http.Request) {
	a, _, err := h.svc.RevokeAssignment(r.Context(), actor(r), httpx.RequestID(w), r.PathValue("id"), false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAssignmentDTO(a))
}
