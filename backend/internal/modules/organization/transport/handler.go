// Package transport exposes the Organization HTTP API under /api/v1: read
// routes plus the request of a manual directory synchronization.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView          = "organization.view"
	permDirectoryView = "organization.directory.view"
	permDirectorySync = "organization.directory.sync"
	maxQueryLen       = 100
)

var userStatuses = map[string]struct{}{"active": {}, "inactive": {}, "departed": {}, "external": {}, "unknown": {}}

type handler struct {
	reader application.Reader
	syncer application.DirectorySyncRequester
	// syncProviderKey is the configured directory provider; empty when
	// directory synchronization is disabled on this API instance.
	syncProviderKey string
	logger          *slog.Logger
}

// Register mounts the Organization routes on mux. Every route requires
// authentication and the named permission; other methods yield 405.
// syncProviderKey is the configured directory provider key, or empty when
// directory synchronization is not configured (manual requests then yield 409).
func Register(mux *http.ServeMux, reader application.Reader, syncer application.DirectorySyncRequester, syncProviderKey string, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{reader: reader, syncer: syncer, syncProviderKey: syncProviderKey, logger: logger}
	org := authorization.Require(auth, permView)
	dir := authorization.Require(auth, permDirectoryView)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle("GET "+pattern, noStore(mw(fn)))
	}
	route("/api/v1/users", org, h.listUsers)
	route("/api/v1/users/{id}", org, h.getUser)
	route("/api/v1/teams", org, h.listTeams)
	route("/api/v1/teams/{id}", org, h.getTeam)
	route("/api/v1/teams/{id}/members", org, h.listTeamMembers)
	route("/api/v1/locations", org, h.listLocations)
	route("/api/v1/locations/{id}", org, h.getLocation)
	route("/api/v1/directory-groups", dir, h.listDirectoryGroups)
	route("/api/v1/directory-groups/{id}", dir, h.getDirectoryGroup)
	// Members expose user identities, so both permissions are required.
	route("/api/v1/directory-groups/{id}/members", func(next http.Handler) http.Handler { return org(dir(next)) }, h.listDirectoryGroupMembers)
	// Run conflicts carry directory usernames, so both permissions are required.
	both := func(next http.Handler) http.Handler { return org(dir(next)) }
	route("/api/v1/directory-sync-runs", both, h.listDirectorySyncRuns)
	route("/api/v1/directory-sync-runs/{id}", both, h.getDirectorySyncRun)
	mux.Handle("POST /api/v1/directory-sync-runs", noStore(authorization.Require(auth, permDirectorySync)(http.HandlerFunc(h.requestDirectorySync))))
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

// fail maps application errors to HTTP responses without leaking error text.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrNotFound):
		writeError(w, http.StatusNotFound, "organization.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "organization.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "organization request failed", "request_id", w.Header().Get("X-Request-ID"), "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	p := application.Page{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "organization.invalid_limit", "The limit must be a positive integer.")
			return p, false
		}
		p.Limit = n
	}
	return p.Normalize(), true
}

func parseQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > maxQueryLen || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		writeError(w, http.StatusBadRequest, "organization.invalid_query", "The search query is invalid.")
		return "", false
	}
	return q, true
}

func parseNameFilter(w http.ResponseWriter, r *http.Request) (application.NameFilter, bool) {
	p, ok := parsePage(w, r)
	if !ok {
		return application.NameFilter{}, false
	}
	q, ok := parseQuery(w, r)
	if !ok {
		return application.NameFilter{}, false
	}
	return application.NameFilter{Query: q, Page: p}, true
}

func ok(w http.ResponseWriter, v any) { httpx.JSON(w, http.StatusOK, v) }

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	nf, valid := parseNameFilter(w, r)
	if !valid {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" {
		if _, known := userStatuses[status]; !known {
			writeError(w, http.StatusBadRequest, "organization.invalid_status", "The status filter is invalid.")
			return
		}
	}
	res, err := h.reader.ListUsers(r.Context(), application.UserFilter{Query: nf.Query, Status: status, Page: nf.Page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toUser))
}

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.reader.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	identities, err := h.reader.ListUserExternalIdentities(r.Context(), u.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toUserDetail(u, identities))
}

func (h *handler) listTeams(w http.ResponseWriter, r *http.Request) {
	f, valid := parseNameFilter(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListTeams(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toTeam))
}

func (h *handler) getTeam(w http.ResponseWriter, r *http.Request) {
	t, err := h.reader.GetTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toTeam(t))
}

func (h *handler) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	p, valid := parsePage(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListTeamMembers(r.Context(), r.PathValue("id"), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toTeamMember))
}

func (h *handler) listLocations(w http.ResponseWriter, r *http.Request) {
	f, valid := parseNameFilter(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListLocations(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toLocation))
}

func (h *handler) getLocation(w http.ResponseWriter, r *http.Request) {
	l, err := h.reader.GetLocation(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toLocation(l))
}

func (h *handler) listDirectoryGroups(w http.ResponseWriter, r *http.Request) {
	f, valid := parseNameFilter(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListDirectoryGroups(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toDirectoryGroup))
}

func (h *handler) getDirectoryGroup(w http.ResponseWriter, r *http.Request) {
	g, err := h.reader.GetDirectoryGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toDirectoryGroup(g))
}

func (h *handler) listDirectoryGroupMembers(w http.ResponseWriter, r *http.Request) {
	p, valid := parsePage(w, r)
	if !valid {
		return
	}
	res, err := h.reader.ListDirectoryGroupMembers(r.Context(), r.PathValue("id"), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toDirectoryGroupMember))
}

func (h *handler) listDirectorySyncRuns(w http.ResponseWriter, r *http.Request) {
	p, valid := parsePage(w, r)
	if !valid {
		return
	}
	key := r.URL.Query().Get("providerKey")
	if utf8.RuneCountInString(key) > maxQueryLen || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		writeError(w, http.StatusBadRequest, "organization.invalid_provider_key", "The provider key is invalid.")
		return
	}
	res, err := h.reader.ListDirectorySyncRuns(r.Context(), application.RunFilter{ProviderKey: key, Page: p})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toList(res, toSyncRun))
}

func (h *handler) getDirectorySyncRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.reader.GetDirectorySyncRun(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toSyncRun(run))
}

// requestDirectorySync enqueues a manual sync. The worker, not this process,
// talks to the directory.
func (h *handler) requestDirectorySync(w http.ResponseWriter, r *http.Request) {
	if h.syncProviderKey == "" {
		writeError(w, http.StatusConflict, "organization.directory_sync_not_configured", "Directory synchronization is not configured.")
		return
	}
	principal, found := authorization.PrincipalFrom(r.Context())
	if !found {
		writeError(w, http.StatusUnauthorized, "platform.unauthenticated", "Authentication is required.")
		return
	}
	jobID, created, err := h.syncer.RequestDirectorySync(r.Context(), principal.UserID, h.syncProviderKey, w.Header().Get("X-Request-ID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, syncRequestDTO{JobID: jobID, Created: created})
}
