package transport

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

type lookupHandler struct {
	svc     *application.PeopleLookup
	limiter *query.Limiter
	logger  *slog.Logger
}

// RegisterLookup mounts GET /api/v1/people/lookup for every signed-in User. Without enabled (PEOPLE_LOOKUP_ENABLED)
// the route answers 404. The route is limited to 1 request per second and caller (burst 10) and each use is audited
// as organization.people.lookup with the text length and the number of results, never the text or the names.
func RegisterLookup(mux *http.ServeMux, svc *application.PeopleLookup, enabled bool, auth authorization.Authenticator, logger *slog.Logger) {
	h := &lookupHandler{svc: svc, limiter: query.NewLimiter(1, 10), logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	mux.Handle("GET /api/v1/people/lookup", httpx.NoStore(authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			httpx.WriteError(w, http.StatusNotFound, "organization.not_found", "The requested resource was not found.")
			return
		}
		h.lookup(w, r)
	}))))
}

func (h *lookupHandler) lookup(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	if !h.limiter.Allow(p.UserID) {
		w.Header().Set("Retry-After", "1")
		httpx.WriteError(w, http.StatusTooManyRequests, "organization.lookup_rate_limited", "Too many lookups; wait a moment and try again.")
		return
	}
	text := r.URL.Query().Get("q")
	res, err := h.svc.Find(r.Context(), audit.UserActor(p.UserID), httpx.RequestID(w), text)
	switch {
	case errors.Is(err, application.ErrLookupQueryTooShort):
		httpx.WriteErrorDetails(w, http.StatusBadRequest, "query.query_too_short", "The search text is too short.", map[string]any{"minLength": application.LookupMinChars})
		return
	case errors.Is(err, application.ErrLookupQueryInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_query", "The search text is invalid.")
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "people lookup failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
		return
	}
	type person struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
		Department  string `json:"department,omitempty"`
	}
	items := make([]person, 0, len(res))
	for _, x := range res {
		items = append(items, person{x.ID, x.DisplayName, x.Department})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []person `json:"items"`
	}{items})
}
