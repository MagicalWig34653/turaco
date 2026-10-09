package transport

import (
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permTeamsManage = "organization.teams.manage"
	maxTeamBody     = 4 << 10
)

type teamHandler struct {
	teams  *application.Teams
	logger *slog.Logger
}

// RegisterTeams mounts the Team write routes (create, rename, activate,
// deactivate, add/remove member). The read routes stay in Register.
func RegisterTeams(mux *http.ServeMux, teams *application.Teams, auth authorization.Authenticator, logger *slog.Logger) {
	h := &teamHandler{teams: teams, logger: logger}
	manage := authorization.Require(auth, permTeamsManage)
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(manage(fn)))
	}
	route("POST /api/v1/teams", h.create)
	route("PATCH /api/v1/teams/{id}", h.rename)
	route("POST /api/v1/teams/{id}/activate", h.activate)
	route("POST /api/v1/teams/{id}/deactivate", h.deactivate)
	route("POST /api/v1/teams/{id}/members", h.addMember)
	route("PUT /api/v1/teams/{id}/members/{userId}/role", h.setMemberRole)
	route("DELETE /api/v1/teams/{id}/members/{userId}", h.removeMember)
}

func (h *teamHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failPeople(h.logger, w, r, err)
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w), ExternalPartiesManage: p.Has(permExternalParties)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxTeamBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type nameBody struct {
	Name string `json:"name"`
}

func (h *teamHandler) create(w http.ResponseWriter, r *http.Request) {
	var body nameBody
	if !decode(w, r, &body) {
		return
	}
	t, err := h.teams.Create(r.Context(), caller(w, r), body.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTeam(t))
}

// rename renames the Team (legacy body {name}) and/or changes its description. A description change needs
// expectedVersion, like every new operation.
func (h *teamHandler) rename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		ExpectedVersion int     `json:"expectedVersion"`
	}
	if !decode(w, r, &body) {
		return
	}
	if (body.Name == nil) == (body.Description == nil) {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "Send either name or description (with expectedVersion).")
		return
	}
	var t application.Team
	var err error
	id := r.PathValue("id")
	if body.Name != nil {
		if t, err = h.teams.Rename(r.Context(), caller(w, r), id, *body.Name); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	if body.Description != nil {
		if t, err = h.teams.SetDescription(r.Context(), caller(w, r), id, body.ExpectedVersion, *body.Description); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	ok(w, toTeam(t))
}

func (h *teamHandler) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	m, err := h.teams.SetMemberRole(r.Context(), caller(w, r), r.PathValue("id"), r.PathValue("userId"), body.Role)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toTeamMember(m))
}

func (h *teamHandler) activate(w http.ResponseWriter, r *http.Request) {
	t, err := h.teams.Activate(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toTeam(t))
}

func (h *teamHandler) deactivate(w http.ResponseWriter, r *http.Request) {
	t, err := h.teams.Deactivate(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toTeam(t))
}

func (h *teamHandler) addMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID string  `json:"userId"`
		Role   *string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	m, err := h.teams.AddMember(r.Context(), caller(w, r), r.PathValue("id"), body.UserID, body.Role)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTeamMember(m))
}

func (h *teamHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	if err := h.teams.RemoveMember(r.Context(), caller(w, r), r.PathValue("id"), r.PathValue("userId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
