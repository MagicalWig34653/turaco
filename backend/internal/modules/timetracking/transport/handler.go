// Package transport exposes the Time Tracking HTTP API under /api/v1/time-tracking. Responses are no-store.
// Authorization is decided in the application layer from the principal's permissions and the caller's Ticket access;
// the routes only require a signed-in User. While the module switch is off the platform answers 404
// platform.module_disabled for everything except /time-tracking/settings*, so the co-determination record can be
// entered before the module is switched on.
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 16 << 10

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Time Tracking routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(signedIn(fn))) }
	route("GET /api/v1/time-tracking/settings", h.getSettings)
	route("PUT /api/v1/time-tracking/settings", h.putSettings)
	route("GET /api/v1/time-tracking/entries", h.entries)
	route("POST /api/v1/time-tracking/entries", h.create)
	route("PUT /api/v1/time-tracking/entries/{id}", h.update)
	route("POST /api/v1/time-tracking/entries/{id}/delete", h.delete)
	route("GET /api/v1/time-tracking/summary", h.summary)
	route("GET /api/v1/time-tracking/timer", h.timerGet)
	route("POST /api/v1/time-tracking/timer", h.timerStart)
	route("POST /api/v1/time-tracking/timer/stop", h.timerStop)
	route("DELETE /api/v1/time-tracking/timer", h.timerDiscard)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "timetracking.invalid_request", inv.Message)
	case errors.Is(err, application.ErrOutsideEditWindow):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "timetracking.outside_edit_window", "The work date is outside the edit window; ask someone with time management permission.")
	case errors.Is(err, application.ErrTimerRunning):
		httpx.WriteError(w, http.StatusConflict, "timetracking.timer_running", "A timer is already running; stop or discard it first.")
	case errors.Is(err, application.ErrDayLimit):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "timetracking.day_limit", "A person can log at most 24 hours per work date.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "timetracking.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "timetracking.not_permitted", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "timetracking.version_conflict", "The record was changed by someone else; reload and try again.")
	default:
		h.logger.ErrorContext(r.Context(), "time tracking request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, Log: p.Has(application.PermLog), Manage: p.Has(application.PermManage), Admin: p.Has(application.PermAdmin), Report: p.Has(application.PermReport)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "timetracking.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// ---- DTOs ------------------------------------------------------------------------------------------------

type entryDTO struct {
	ID              string  `json:"id"`
	SubjectType     string  `json:"subjectType"`
	SubjectID       string  `json:"subjectId"`
	Reference       string  `json:"reference,omitempty"`
	UserID          string  `json:"userId,omitempty"`
	UserName        string  `json:"userName,omitempty"`
	Own             bool    `json:"own"`
	Editable        bool    `json:"editable"`
	WorkDate        string  `json:"workDate"`
	DurationMinutes int     `json:"durationMinutes"`
	Source          string  `json:"source"`
	Note            *string `json:"note"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
	Version         int     `json:"version"`
}

func toEntry(v application.EntryView) entryDTO {
	e := v.Entry
	d := entryDTO{ID: e.ID, SubjectType: e.SubjectType, SubjectID: e.SubjectID, Reference: v.Reference, Own: v.Own, Editable: v.Editable,
		WorkDate: e.WorkDate, DurationMinutes: e.DurationMinutes, Source: e.Source, Note: e.Note, CreatedAt: ts(e.CreatedAt),
		UpdatedAt: ts(e.UpdatedAt), Version: e.Version}
	// The author is named only when the reader may see it: the entry is the reader's own or show_entry_authors is on
	// (the application sets UserName only then).
	if v.Own || v.UserName != "" {
		d.UserID = e.UserID
		d.UserName = v.UserName
	}
	return d
}

// single is the response to the actor's own write: the actor knows whose time it is.
func single(e application.Entry, actor string) entryDTO {
	d := toEntry(application.EntryView{Entry: e, Own: e.UserID == actor, Editable: true})
	d.UserID = e.UserID
	return d
}

type settingsDTO struct {
	CodeterminationBasis      *string `json:"codeterminationBasis"`
	CodeterminationRecordedOn *string `json:"codeterminationRecordedOn"`
	DPIARecordedOn            *string `json:"dpiaRecordedOn"`
	EditWindowDays            int     `json:"editWindowDays"`
	RetentionMonths           int     `json:"retentionMonths"`
	ShowEntryAuthors          bool    `json:"showEntryAuthors"`
	MinReportGroupSize        int     `json:"minReportGroupSize"`
	Version                   int     `json:"version"`
	UpdatedAt                 string  `json:"updatedAt"`
}

func toSettings(s application.Settings) settingsDTO {
	return settingsDTO{CodeterminationBasis: s.CodeterminationBasis, CodeterminationRecordedOn: s.CodeterminationRecordedOn, DPIARecordedOn: s.DPIARecordedOn,
		EditWindowDays: s.EditWindowDays, RetentionMonths: s.RetentionMonths, ShowEntryAuthors: s.ShowEntryAuthors,
		MinReportGroupSize: s.MinReportGroupSize, Version: s.Version, UpdatedAt: ts(s.UpdatedAt)}
}

// ---- Settings --------------------------------------------------------------------------------------------

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Settings(r.Context(), principal(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettings(st))
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CodeterminationBasis      string `json:"codeterminationBasis"`
		CodeterminationRecordedOn string `json:"codeterminationRecordedOn"`
		DPIARecordedOn            string `json:"dpiaRecordedOn"`
		EditWindowDays            int    `json:"editWindowDays"`
		RetentionMonths           int    `json:"retentionMonths"`
		ShowEntryAuthors          bool   `json:"showEntryAuthors"`
		MinReportGroupSize        int    `json:"minReportGroupSize"`
		ExpectedVersion           *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &in) {
		return
	}
	st, err := h.svc.UpdateSettings(r.Context(), caller(w, r), principal(r), application.SettingsInput{CodeterminationBasis: in.CodeterminationBasis,
		CodeterminationRecordedOn: in.CodeterminationRecordedOn, DPIARecordedOn: in.DPIARecordedOn, EditWindowDays: in.EditWindowDays,
		RetentionMonths: in.RetentionMonths, ShowEntryAuthors: in.ShowEntryAuthors, MinReportGroupSize: in.MinReportGroupSize}, in.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettings(st))
}

// ---- Entries ---------------------------------------------------------------------------------------------

// entries serves the Ticket panel (subjectType=ticket&subjectId=) and the own-time view (mine=true&from&to).
func (h *handler) entries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("mine") == "true" {
		res, err := h.svc.Mine(r.Context(), principal(r), q.Get("from"), q.Get("to"))
		if err != nil {
			h.writeErr(w, r, err)
			return
		}
		items := make([]entryDTO, 0, len(res.Entries))
		for _, e := range res.Entries {
			items = append(items, toEntry(e))
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "totalMinutes": res.TotalMinutes, "editWindowDays": res.EditWindowDays})
		return
	}
	if q.Get("subjectType") != application.SubjectTicket {
		httpx.WriteError(w, http.StatusBadRequest, "timetracking.invalid_request", "subjectType must be ticket, or mine must be true.")
		return
	}
	res, err := h.svc.ForTicket(r.Context(), principal(r), q.Get("subjectId"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]entryDTO, 0, len(res.Entries))
	for _, e := range res.Entries {
		items = append(items, toEntry(e))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "totalMinutes": res.TotalMinutes, "othersMinutes": res.OthersMinutes,
		"canLog": res.CanLog, "editWindowDays": res.EditWindowDays, "showEntryAuthors": res.ShowEntryAuthors})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SubjectType     string `json:"subjectType"`
		SubjectID       string `json:"subjectId"`
		UserID          string `json:"userId"`
		WorkDate        string `json:"workDate"`
		DurationMinutes int    `json:"durationMinutes"`
		Note            string `json:"note"`
		Reason          string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.NewEntry{SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		UserID: in.UserID, WorkDate: in.WorkDate, DurationMinutes: in.DurationMinutes, Note: in.Note, Reason: in.Reason})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, single(e, principal(r).UserID))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SubjectID       string `json:"subjectId"`
		WorkDate        string `json:"workDate"`
		DurationMinutes int    `json:"durationMinutes"`
		Note            string `json:"note"`
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := h.svc.Update(r.Context(), caller(w, r), principal(r), r.PathValue("id"), application.EntryChange{SubjectID: in.SubjectID,
		WorkDate: in.WorkDate, DurationMinutes: in.DurationMinutes, Note: in.Note, Reason: in.Reason}, in.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, single(e, principal(r).UserID))
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeleteReason    string `json:"deleteReason"`
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := h.svc.Delete(r.Context(), caller(w, r), principal(r), r.PathValue("id"), application.DeleteInput{DeleteReason: in.DeleteReason, Reason: in.Reason}, in.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type timerDTO struct {
	SubjectType string  `json:"subjectType"`
	SubjectID   string  `json:"subjectId"`
	StartedAt   string  `json:"startedAt"`
	Note        *string `json:"note"`
}

func toTimer(t application.Timer) timerDTO {
	return timerDTO{SubjectType: t.SubjectType, SubjectID: t.SubjectID, StartedAt: ts(t.StartedAt), Note: t.Note}
}

// timerGet answers 200 with {"timer": null} when none runs, so the Ticket panel needs no error handling.
func (h *handler) timerGet(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.CurrentTimer(r.Context(), principal(r))
	switch {
	case errors.Is(err, application.ErrNotFound):
		httpx.JSON(w, http.StatusOK, map[string]any{"timer": nil})
	case err != nil:
		h.writeErr(w, r, err)
	default:
		httpx.JSON(w, http.StatusOK, map[string]any{"timer": toTimer(t)})
	}
}

func (h *handler) timerStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Note        string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := h.svc.StartTimer(r.Context(), principal(r), in.SubjectType, in.SubjectID, in.Note)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTimer(t))
}

func (h *handler) timerStop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DurationMinutes int `json:"durationMinutes"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	e, err := h.svc.StopTimer(r.Context(), caller(w, r), principal(r), in.DurationMinutes)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, single(e, principal(r).UserID))
}

func (h *handler) timerDiscard(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DiscardTimer(r.Context(), principal(r)); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// summary is the aggregated effort report (no per-person grouping exists).
func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.Report(r.Context(), principal(r), q.Get("from"), q.Get("to"), q.Get("groupBy"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	groups := make([]map[string]any, 0, len(res.Groups))
	for _, g := range res.Groups {
		item := map[string]any{"key": g.Key, "suppressed": g.Suppressed, "partial": g.Partial}
		if g.Label != "" {
			item["label"] = g.Label
		}
		if !g.Suppressed {
			item["minutes"], item["entries"], item["contributors"] = g.Minutes, g.Entries, g.Contributors
		}
		groups = append(groups, item)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": res.From, "to": res.To, "groupBy": res.GroupBy,
		"minGroupSize": res.MinGroupSize, "groups": groups, "truncated": res.Truncated})
}
