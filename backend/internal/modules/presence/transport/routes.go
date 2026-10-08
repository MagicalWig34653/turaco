package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type recurrenceDTO struct {
	Frequency  string `json:"frequency"`
	Interval   int    `json:"interval"`
	Weekday    int    `json:"weekday,omitempty"`
	DayOfMonth int    `json:"dayOfMonth,omitempty"`
	EndsOn     string `json:"endsOn"`
}

func (d *recurrenceDTO) input() *application.RecurrenceInput {
	if d == nil {
		return nil
	}
	return &application.RecurrenceInput{Frequency: d.Frequency, Interval: d.Interval, Weekday: d.Weekday, DayOfMonth: d.DayOfMonth, EndsOn: d.EndsOn}
}

type timeDTO struct {
	StartsAt  *time.Time `json:"startsAt"`
	EndsAt    *time.Time `json:"endsAt"`
	StartDate string     `json:"startDate"`
	EndDate   string     `json:"endDate"`
	Timezone  string     `json:"timezone"`
}

func (d timeDTO) input() application.TimeInput {
	return application.TimeInput{StartsAt: d.StartsAt, EndsAt: d.EndsAt, StartDate: d.StartDate, EndDate: d.EndDate, Timezone: d.Timezone}
}

type intervalDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type entryDTO struct {
	ID           string         `json:"id"`
	UserID       string         `json:"userId"`
	Kind         string         `json:"kind"`
	LocationType *string        `json:"locationType"`
	LocationID   *string        `json:"locationId"`
	StartsAt     string         `json:"startsAt"`
	EndsAt       string         `json:"endsAt"`
	AllDay       bool           `json:"allDay"`
	Timezone     *string        `json:"timezone"`
	Recurrence   *recurrenceDTO `json:"recurrence"`
	Source       string         `json:"source"`
	Status       string         `json:"status"`
	Visibility   string         `json:"visibility"`
	Occurrences  []intervalDTO  `json:"occurrences"`
	CreatedBy    *string        `json:"createdBy"`
	Version      int            `json:"version"`
	CreatedAt    string         `json:"createdAt"`
	UpdatedAt    string         `json:"updatedAt"`
}

func toEntry(v application.EntryView) entryDTO {
	e := v.Entry
	d := entryDTO{ID: e.ID, UserID: e.UserID, Kind: e.Kind, LocationType: e.LocationType, LocationID: e.LocationID, StartsAt: ts(e.StartsAt),
		EndsAt: ts(e.EndsAt), AllDay: e.AllDay, Source: e.Source, Status: e.Status, Visibility: e.Visibility, CreatedBy: e.CreatedBy,
		Version: e.Version, CreatedAt: ts(e.CreatedAt), UpdatedAt: ts(e.UpdatedAt), Occurrences: []intervalDTO{}}
	if e.Recurrence != nil {
		d.Recurrence = &recurrenceDTO{Frequency: e.Recurrence.Frequency, Interval: e.Recurrence.Interval, Weekday: e.Recurrence.Weekday,
			DayOfMonth: e.Recurrence.DayOfMonth, EndsOn: e.Recurrence.EndsOn}
		tz := e.Recurrence.Timezone
		d.Timezone = &tz
	}
	for _, o := range v.Occurrences {
		d.Occurrences = append(d.Occurrences, intervalDTO{From: ts(o.From), To: ts(o.To)})
	}
	return d
}

func single(e application.Entry) entryDTO {
	return toEntry(application.EntryView{Entry: e, Occurrences: []application.Interval{{From: e.StartsAt, To: e.EndsAt}}})
}

type settingsDTO struct {
	Enabled                bool    `json:"enabled"`
	DPIARecordedOn         *string `json:"dpiaRecordedOn"`
	CouncilConfirmedOn     *string `json:"councilConfirmedOn"`
	RetentionDays          int     `json:"retentionDays"`
	MaxRetentionDays       int     `json:"maxRetentionDays"`
	ExternalSourcesEnabled bool    `json:"externalSourcesEnabled"`
	DisabledAt             *string `json:"disabledAt"`
	Version                int     `json:"version"`
	UpdatedAt              string  `json:"updatedAt"`
}

func (h *handler) toSettings(s application.Settings) settingsDTO {
	return settingsDTO{Enabled: s.Enabled, DPIARecordedOn: s.DPIARecordedOn, CouncilConfirmedOn: s.CouncilConfirmedOn,
		RetentionDays: h.svc.RetentionDays(s), MaxRetentionDays: h.svc.MaxRetentionDays(), ExternalSourcesEnabled: s.ExternalSourcesEnabled,
		DisabledAt: tsPtr(s.DisabledAt), Version: s.Version, UpdatedAt: ts(s.UpdatedAt)}
}

// status tells every signed-in User whether Presence is on, so the UI can hide everything when it is off.
func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": h.svc.IsEnabled(r.Context()), "permissions": map[string]bool{
		"manageOwn": p.ManageOwn, "viewAvailability": p.ViewAvailability, "viewEntries": p.ViewEntries,
		"manageEntries": p.ManageEntries, "manageTeams": p.ManageTeams, "admin": p.Admin}})
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Settings(r.Context(), principal(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, h.toSettings(s))
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled                bool   `json:"enabled"`
		DPIARecordedOn         string `json:"dpiaRecordedOn"`
		CouncilConfirmedOn     string `json:"councilConfirmedOn"`
		RetentionDays          int    `json:"retentionDays"`
		ExternalSourcesEnabled bool   `json:"externalSourcesEnabled"`
		ExpectedVersion        *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.UpdateSettings(r.Context(), caller(w, r), principal(r), application.SettingsInput{Enabled: b.Enabled,
		DPIARecordedOn: b.DPIARecordedOn, CouncilConfirmedOn: b.CouncilConfirmedOn, RetentionDays: b.RetentionDays,
		ExternalSourcesEnabled: b.ExternalSourcesEnabled}, b.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, h.toSettings(s))
}

func (h *handler) purge(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.PurgeNow(r.Context(), caller(w, r), principal(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deletedEntries": c.Entries})
}

func entryList(list []application.EntryView) map[string]any {
	items := make([]entryDTO, 0, len(list))
	for _, v := range list {
		items = append(items, toEntry(v))
	}
	return map[string]any{"items": items}
}

func (h *handler) myEntries(w http.ResponseWriter, r *http.Request) {
	from, to, err := window(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	list, err := h.svc.MyEntries(r.Context(), principal(r), from, to)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, entryList(list))
}

func (h *handler) entries(w http.ResponseWriter, r *http.Request) {
	from, to, err := window(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	list, err := h.svc.Entries(r.Context(), caller(w, r), principal(r), application.EntryFilter{
		UserID: r.URL.Query().Get("userId"), TeamID: r.URL.Query().Get("teamId"), From: from, To: to})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, entryList(list))
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		timeDTO
		UserID       string         `json:"userId"`
		Kind         string         `json:"kind"`
		LocationType string         `json:"locationType"`
		LocationID   string         `json:"locationId"`
		Recurrence   *recurrenceDTO `json:"recurrence"`
		Visibility   string         `json:"visibility"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.NewEntry{UserID: b.UserID, Kind: b.Kind,
		LocationType: b.LocationType, LocationID: b.LocationID, Time: b.timeDTO.input(), Recurrence: b.Recurrence.input(), Visibility: b.Visibility})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, single(e))
}

func (h *handler) reschedule(w http.ResponseWriter, r *http.Request) {
	var b struct {
		timeDTO
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := h.svc.Reschedule(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.timeDTO.input())
	h.entryResult(w, r, e, err)
}

func (h *handler) changeLocation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		LocationType    string `json:"locationType"`
		LocationID      string `json:"locationId"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := h.svc.ChangeLocation(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.LocationType, b.LocationID)
	h.entryResult(w, r, e, err)
}

func (h *handler) changeRecurrence(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Timezone        string         `json:"timezone"`
		Recurrence      *recurrenceDTO `json:"recurrence"`
		ExpectedVersion *int           `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := h.svc.ChangeRecurrence(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Timezone, b.Recurrence.input())
	h.entryResult(w, r, e, err)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := h.svc.Cancel(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	h.entryResult(w, r, e, err)
}

func (h *handler) entryResult(w http.ResponseWriter, r *http.Request, e application.Entry, err error) {
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, single(e))
}

type sourceDTO struct {
	Source     string  `json:"source"`
	Freshness  string  `json:"freshness"`
	ObservedAt *string `json:"observedAt"`
}

func sources(in []application.SourceInfo) []sourceDTO {
	out := make([]sourceDTO, 0, len(in))
	for _, s := range in {
		out = append(out, sourceDTO{Source: s.Source, Freshness: s.Freshness, ObservedAt: tsPtr(s.ObservedAt)})
	}
	return out
}

func need(r *http.Request) application.Need {
	q := r.URL.Query()
	return application.Need{OnSite: q.Get("onSite") == "true", LocationID: q.Get("locationId")}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *handler) availability(w http.ResponseWriter, r *http.Request) {
	at := time.Now().UTC()
	if r.URL.Query().Get("at") != "" {
		var err error
		if at, err = timeParam(r, "at"); err != nil {
			h.writeErr(w, r, err)
			return
		}
	}
	res, err := h.svc.Availability(r.Context(), principal(r), splitCSV(r.URL.Query().Get("userIds")), at, need(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(res))
	for _, a := range res {
		items = append(items, map[string]any{"userId": a.UserID, "value": a.Value, "explanation": a.Explanation, "limitedBy": nilIfEmpty(a.LimitedBy),
			"until": tsPtr(a.Until), "sources": sources(a.Sources)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"at": ts(at), "items": items})
}

func (h *handler) availabilityWindow(w http.ResponseWriter, r *http.Request) {
	from, to, err := window(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	segs, err := h.svc.AvailabilityWindow(r.Context(), principal(r), r.URL.Query().Get("userId"), from, to, need(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(segs))
	for _, s := range segs {
		items = append(items, map[string]any{"from": ts(s.From), "to": ts(s.To), "value": s.Value, "explanation": s.Explanation,
			"limitedBy": nilIfEmpty(s.LimitedBy), "sources": sources(s.Sources)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type minimumDTO struct {
	TeamID        string  `json:"teamId"`
	Minimum       int     `json:"minimum"`
	OnsiteMinimum *int    `json:"onsiteMinimum"`
	LocationID    *string `json:"locationId"`
	Version       int     `json:"version"`
}

func toMinimum(m *application.Minimum) *minimumDTO {
	if m == nil {
		return nil
	}
	return &minimumDTO{TeamID: m.TeamID, Minimum: m.Minimum, OnsiteMinimum: m.OnsiteMinimum, LocationID: m.LocationID, Version: m.Version}
}

func (h *handler) coverage(w http.ResponseWriter, r *http.Request) {
	from, to, err := window(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	p := principal(r)
	res, err := h.svc.TeamCoverage(r.Context(), caller(w, r), p, r.PathValue("id"), from, to, r.URL.Query().Get("timezone"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	days := make([]map[string]any, 0, len(res.Days))
	for _, d := range res.Days {
		m := map[string]any{"date": d.Date, "available": d.Available, "limited": d.Limited, "unavailable": d.Unavailable, "unknown": d.Unknown,
			"onsiteAvailable": d.OnsiteAvailable, "state": d.State}
		if p.ViewEntries {
			m["unavailableUserIds"] = append([]string{}, d.UnavailableUserIDs...)
		}
		days = append(days, m)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"teamId": res.TeamID, "members": res.Members, "state": res.State, "minimum": toMinimum(res.Minimum), "days": days})
}

func (h *handler) getMinimum(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.GetMinimum(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"minimum": toMinimum(m)})
}

func (h *handler) putMinimum(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Minimum         int    `json:"minimum"`
		OnsiteMinimum   *int   `json:"onsiteMinimum"`
		LocationID      string `json:"locationId"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := h.svc.SetMinimum(r.Context(), caller(w, r), principal(r), r.PathValue("id"),
		application.MinimumInput{Minimum: b.Minimum, OnsiteMinimum: b.OnsiteMinimum, LocationID: b.LocationID}, b.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMinimum(&m))
}
