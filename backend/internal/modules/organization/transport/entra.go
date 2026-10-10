package transport

import (
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type entraHandler struct {
	linking *application.EntraLinking
	// homeTenantID pre-fills the tenant field of the UI (ENTRA_TENANT_ID); empty in multi-tenant mode.
	homeTenantID string
	logger       *slog.Logger
}

// RegisterEntra mounts the administrator operations on the Entra identity of a User (ADR-0035, slice E-B). They
// are takeover-capable, so every route needs platform.admin; the application layer checks again inside the
// transaction (not oneself, dominance, state, local credential replacement).
func RegisterEntra(mux *http.ServeMux, linking *application.EntraLinking, homeTenantID string, auth authorization.Authenticator, logger *slog.Logger) {
	h := &entraHandler{linking: linking, homeTenantID: homeTenantID, logger: logger}
	admin := authorization.Require(auth, permPlatformAdmin)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(admin(fn))) }
	route("GET /api/v1/users/entra-linking", h.info)
	route("POST /api/v1/users/{id}/external-identities/entra", h.link)
	route("DELETE /api/v1/users/{id}/external-identities/{identityId}", h.unlink)
}

func (h *entraHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failPeople(h.logger, w, r, err)
}

func (h *entraHandler) info(w http.ResponseWriter, _ *http.Request) {
	ok(w, map[string]any{"configured": h.linking.Configured(), "tenantId": h.homeTenantID, "tenantIds": h.linking.Tenants()})
}

type entraResultDTO struct {
	User              userDTO `json:"user"`
	CredentialDeleted bool    `json:"credentialDeleted"`
	NoticeSent        bool    `json:"noticeSent"`
}

func (h *entraHandler) link(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int    `json:"expectedVersion"`
		TenantID        string `json:"tenantId"`
		ObjectID        string `json:"objectId"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	res, err := h.linking.Link(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, body.TenantID, body.ObjectID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, entraResultDTO{toUser(res.User), res.CredentialDeleted, res.NoticeSent})
}

func (h *entraHandler) unlink(w http.ResponseWriter, r *http.Request) {
	res, err := h.linking.Unlink(r.Context(), caller(w, r), r.PathValue("id"), r.PathValue("identityId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, entraResultDTO{toUser(res.User), false, res.NoticeSent})
}
