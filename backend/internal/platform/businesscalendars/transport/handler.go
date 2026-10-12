package transport

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	scheduling "github.com/MagicalWig34653/turaco/backend/internal/platform/businesscalendars"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type handler struct {
	store  *scheduling.CalendarStore
	logger *slog.Logger
}

func Register(mux *http.ServeMux, store *scheduling.CalendarStore, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{store, logger}
	mux.Handle("GET /api/v1/business-calendars", httpx.NoStore(authorization.RequireAny(auth, "calendars.manage", "sla.manage", "sla.view")(http.HandlerFunc(h.list))))
	mux.Handle("GET /api/v1/business-calendars/{id}", httpx.NoStore(authorization.RequireAny(auth, "calendars.manage", "sla.manage", "sla.view")(http.HandlerFunc(h.get))))
	mux.Handle("POST /api/v1/business-calendars", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.create))))
	mux.Handle("PATCH /api/v1/business-calendars/{id}", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.update))))
	mux.Handle("PUT /api/v1/business-calendars/{id}/closures/{year}", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.closures))))
	mux.Handle("POST /api/v1/business-calendars/{id}/copy-closures", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.copyClosures))))
	mux.Handle("POST /api/v1/business-calendars/{id}/archive", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.archive))))
	mux.Handle("POST /api/v1/business-calendars/{id}/restore", httpx.NoStore(authorization.Require(auth, "calendars.manage")(http.HandlerFunc(h.restore))))
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return false
	}
	var extra any
	return errors.Is(d.Decode(&extra), io.EOF)
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items})
}
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, c)
}
func actor(r *http.Request) string { p, _ := authorization.PrincipalFrom(r.Context()); return p.UserID }
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in scheduling.CalendarInput
	if !decode(w, r, &in) {
		h.bad(w)
		return
	}
	c, err := h.store.Create(r.Context(), in, actor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 201, c)
}
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		scheduling.CalendarInput
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	c, err := h.store.Update(r.Context(), r.PathValue("id"), *in.ExpectedVersion, in.CalendarInput, actor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, c)
}
func (h *handler) closures(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int                 `json:"expectedVersion"`
		Items           []scheduling.Closure `json:"items"`
	}
	year, e := strconv.Atoi(r.PathValue("year"))
	if e != nil || !decode(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	c, err := h.store.ReplaceClosures(r.Context(), r.PathValue("id"), *in.ExpectedVersion, year, in.Items, actor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, c)
}
func (h *handler) copyClosures(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int `json:"expectedVersion"`
		FromYear        int  `json:"fromYear"`
		ToYear          int  `json:"toYear"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	c, err := h.store.CopyClosures(r.Context(), r.PathValue("id"), *in.ExpectedVersion, in.FromYear, in.ToYear, actor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, c)
}
func (h *handler) archive(w http.ResponseWriter, r *http.Request) { h.status(w, r, "archived") }
func (h *handler) restore(w http.ResponseWriter, r *http.Request) { h.status(w, r, "active") }
func (h *handler) status(w http.ResponseWriter, r *http.Request, status string) {
	var in struct {
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	c, err := h.store.SetStatus(r.Context(), r.PathValue("id"), *in.ExpectedVersion, status, actor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, c)
}
func (h *handler) bad(w http.ResponseWriter) {
	httpx.WriteError(w, 400, "calendars.invalid_request", "The request is invalid.")
}
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scheduling.ErrCalendarNotFound):
		httpx.WriteError(w, 404, "calendars.not_found", "Calendar not found.")
	case errors.Is(err, scheduling.ErrCalendarConflict):
		httpx.WriteError(w, 409, "calendars.version_conflict", "The calendar changed; reload and try again.")
	case errors.Is(err, scheduling.ErrCalendarKeyTaken):
		httpx.WriteError(w, 409, "calendars.key_taken", "The calendar key already exists.")
	case errors.Is(err, scheduling.ErrCalendarInUse):
		httpx.WriteError(w, 409, "calendars.in_use", "An active SLA policy uses this calendar.")
	case errors.Is(err, scheduling.ErrCalendarInvalid):
		httpx.WriteError(w, 400, "calendars.invalid_request", "The calendar is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "business calendar request failed", "error", err)
		httpx.WriteError(w, 500, "platform.internal_error", "An internal error occurred.")
	}
}
