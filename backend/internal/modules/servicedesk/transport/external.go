package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type externalHandler struct {
	svc    *application.ExternalSync
	logger *slog.Logger
}

// RegisterExternal mounts the routes that show and retry the synchronization of a ticket with the
// external service desk: tickets.view reads, tickets.manage retries.
func RegisterExternal(mux *http.ServeMux, svc *application.ExternalSync, auth authorization.Authenticator, logger *slog.Logger) {
	h := &externalHandler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	mux.Handle("GET /api/v1/tickets/{id}/external-sync", httpx.NoStore(authed(http.HandlerFunc(h.get))))
	mux.Handle("POST /api/v1/tickets/{id}/external-sync", httpx.NoStore(authed(http.HandlerFunc(h.retry))))
}

func (h *externalHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "tickets.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrSyncDisabled):
		httpx.WriteError(w, http.StatusConflict, "tickets.sync_disabled", "The synchronization with the external service desk is not enabled.")
	default:
		h.logger.ErrorContext(r.Context(), "external sync request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func (h *externalHandler) get(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.StateOf(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Enabled           bool    `json:"enabled"`
		SyncState         *string `json:"syncState"`
		ExternalID        *string `json:"externalId"`
		LastError         *string `json:"lastError"`
		LastSyncedAt      *string `json:"lastSyncedAt"`
		ExternalUpdatedAt *string `json:"externalUpdatedAt"`
		Attempts          int     `json:"attempts"`
	}{st.Enabled, nilIfEmpty(st.SyncState), st.ExternalID, st.LastError, tsPtr(st.LastSyncedAt), tsPtr(st.ExternalUpdatedAt), st.Attempts})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *externalHandler) retry(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	c := application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
	if err := h.svc.Retry(r.Context(), c, principal(r), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
