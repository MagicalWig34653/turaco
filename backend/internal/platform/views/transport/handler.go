// Package transport exposes Saved Views, Shares, Pins and Pin Rules under /api/v1/views and /api/v1/me (ADR-0033).
// Every route requires a signed-in User; the service decides per operation and answers 404 for anything the
// caller may not know about. Responses are never cacheable.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

const maxBodyBytes = 64 << 10

type handler struct {
	svc    *views.Service
	logger *slog.Logger
}

// Register mounts the views routes.
func Register(mux *http.ServeMux, svc *views.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/views", h.list)
	route("GET /api/v1/views/counts", h.counts)
	route("POST /api/v1/views", h.create)
	route("GET /api/v1/views/{id}", h.get)
	route("PATCH /api/v1/views/{id}", h.update)
	route("POST /api/v1/views/{id}/archive", h.lifecycle(svc.Archive))
	route("POST /api/v1/views/{id}/restore", h.lifecycle(svc.Restore))
	route("POST /api/v1/views/{id}/take-over", h.lifecycle(svc.TakeOver))
	route("POST /api/v1/views/{id}/duplicate", h.duplicate)
	route("PUT /api/v1/views/{id}/shares", h.setShares)
	route("GET /api/v1/views/{id}/results", h.results)
	route("GET /api/v1/views/{id}/pin-rules", h.listRules)
	route("POST /api/v1/views/{id}/pin-rules", h.createRule)
	route("DELETE /api/v1/views/{id}/pin-rules/{ruleId}", h.deleteRule)
	route("GET /api/v1/me/pins", h.pins)
	route("PUT /api/v1/me/pins", h.replacePins)
	route("GET /api/v1/me/sidebar", h.sidebar)
	route("PUT /api/v1/me/sidebar-state", h.sidebarState)
}

func caller(w http.ResponseWriter, r *http.Request) views.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return views.Caller{UserID: p.UserID, Permissions: p.Permissions, CorrelationID: httpx.RequestID(w), Header: r.Header}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *views.InvalidError
	var run *views.RunError
	switch {
	case query.WriteError(w, err):
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "views.invalid_request", inv.Message)
	case errors.As(err, &run):
		httpx.WriteError(w, run.Status, run.Code, run.Message)
	case errors.Is(err, views.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "views.not_found", "The view was not found.")
	case errors.Is(err, views.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "views.not_permitted", "You do not have permission to perform this action on the view.")
	case errors.Is(err, views.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "views.conflict", "The view changed; reload it and try again.")
	case errors.Is(err, views.ErrArchived):
		httpx.WriteError(w, http.StatusConflict, "views.archived", "The view is archived.")
	case errors.Is(err, views.ErrModuleDisabled):
		httpx.WriteError(w, http.StatusNotFound, "views.module_disabled", "The module of this view is not enabled.")
	case errors.Is(err, views.ErrLimitReached):
		httpx.WriteError(w, http.StatusConflict, "views.limit_reached", "A limit was reached.")
	case errors.Is(err, views.ErrNameTaken):
		httpx.WriteError(w, http.StatusConflict, "views.name_taken", "You already have a view with this name for this resource.")
	case errors.Is(err, views.ErrSubjectNotFound):
		httpx.WriteError(w, http.StatusNotFound, "views.subject_not_found", "A user, team or role was not found.")
	default:
		h.logger.ErrorContext(r.Context(), "views request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "views.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

// ---------------------------------------------------------------- DTOs

type shareDTO struct {
	SubjectType string    `json:"subjectType"`
	SubjectID   string    `json:"subjectId,omitempty"`
	Level       string    `json:"level"`
	GrantedBy   string    `json:"grantedBy,omitempty"`
	GrantedAt   time.Time `json:"grantedAt,omitempty"`
}

type viewDTO struct {
	ID            string           `json:"id"`
	Resource      string           `json:"resource"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	OwnerID       string           `json:"ownerId"`
	OwnerName     string           `json:"ownerName,omitempty"`
	Definition    views.Definition `json:"definition"`
	Visibility    string           `json:"visibility"`
	Version       int              `json:"version"`
	Access        string           `json:"access"`
	ModuleEnabled bool             `json:"moduleEnabled"`
	Pinned        bool             `json:"pinned"`
	LastEditedBy  string           `json:"lastEditedBy,omitempty"`
	ArchivedAt    *time.Time       `json:"archivedAt,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
	UpdatedAt     time.Time        `json:"updatedAt"`
	Shares        []shareDTO       `json:"shares,omitempty"`
}

func toView(v views.ViewInfo) viewDTO {
	out := viewDTO{ID: v.ID, Resource: v.Resource, Name: v.Name, Description: v.Description, OwnerID: v.OwnerID, OwnerName: v.OwnerName,
		Definition: v.Definition, Visibility: v.Visibility, Version: v.Version, Access: v.Access, ModuleEnabled: v.ModuleEnabled, Pinned: v.Pinned,
		LastEditedBy: v.LastEditedBy, ArchivedAt: v.ArchivedAt, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	for _, s := range v.Shares {
		out.Shares = append(out.Shares, shareDTO{SubjectType: s.SubjectType, SubjectID: s.SubjectID, Level: s.Level, GrantedBy: s.GrantedBy, GrantedAt: s.GrantedAt})
	}
	return out
}

// ---------------------------------------------------------------- views

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("scope") == "system" {
		h.listSystem(w, r, q.Get("resource"))
		return
	}
	in := views.ListInput{Resource: q.Get("resource"), Scope: q.Get("scope"), Cursor: q.Get("cursor"), Archived: q.Get("archived") == "true"}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "views.invalid_request", "limit must be a positive number.")
			return
		}
		in.Limit = n
	}
	out, err := h.svc.List(r.Context(), caller(w, r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]viewDTO, 0, len(out.Items))
	for _, v := range out.Items {
		items = append(items, toView(v))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items      []viewDTO `json:"items"`
		NextCursor string    `json:"nextCursor,omitempty"`
	}{items, out.NextCursor})
}

type systemViewDTO struct {
	ID       string `json:"id"`
	Handle   string `json:"handle,omitempty"`
	Name     string `json:"name"`
	NameKey  string `json:"nameKey,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Resource string `json:"resource"`
	Group    string `json:"groupKey"`
	Position int    `json:"position"`
	System   bool   `json:"system"`
}

// listSystem returns the built-in Views the caller has (GET /views?scope=system). Run one with
// GET /views/{id}/results using its id.
func (h *handler) listSystem(w http.ResponseWriter, r *http.Request, resource string) {
	list, err := h.svc.SystemViews(r.Context(), caller(w, r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]systemViewDTO, 0, len(list))
	for _, sv := range list {
		if resource == "" || sv.Resource == resource {
			items = append(items, systemViewDTO{ID: sv.Key, Handle: sv.Handle, Name: sv.Name, NameKey: sv.NameKey, Ref: sv.Ref, Resource: sv.Resource,
				Group: sv.Group, Position: sv.Position, System: true})
		}
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []systemViewDTO `json:"items"`
	}{items})
}

type countDTO struct {
	ID     string `json:"id"`
	Count  *int   `json:"count,omitempty"`
	Capped bool   `json:"capped,omitempty"`
	Status string `json:"status"`
}

// counts returns the capped counts of up to 30 Views and System Views (ids or queue:<id> handles).
func (h *handler) counts(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ids")
	var ids []string
	if raw != "" {
		ids = strings.Split(raw, ",")
	}
	list, err := h.svc.Counts(r.Context(), caller(w, r), ids)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]countDTO, 0, len(list))
	for _, c := range list {
		d := countDTO{ID: c.ID, Status: c.Status}
		if c.Status == views.CountOK {
			n := c.Count
			d.Count, d.Capped = &n, c.Capped
		}
		items = append(items, d)
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []countDTO `json:"items"`
	}{items})
}

type createBody struct {
	Resource    string           `json:"resource"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Definition  views.Definition `json:"definition"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.svc.Create(r.Context(), caller(w, r), views.CreateInput{Resource: b.Resource, Name: b.Name, Description: b.Description, Definition: b.Definition})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toView(v))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Get(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toView(v))
}

type updateBody struct {
	ExpectedVersion int               `json:"expectedVersion"`
	Name            *string           `json:"name"`
	Description     *string           `json:"description"`
	Definition      *views.Definition `json:"definition"`
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.svc.Update(r.Context(), caller(w, r), r.PathValue("id"), views.UpdateInput{ExpectedVersion: b.ExpectedVersion,
		Name: b.Name, Description: b.Description, Definition: b.Definition})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toView(v))
}

type versionBody struct {
	ExpectedVersion int `json:"expectedVersion"`
}

type lifecycleFunc func(ctx context.Context, c views.Caller, id string, expectedVersion int) (views.ViewInfo, error)

func (h *handler) lifecycle(fn lifecycleFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		v, err := fn(r.Context(), caller(w, r), r.PathValue("id"), b.ExpectedVersion)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toView(v))
	}
}

func (h *handler) duplicate(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Duplicate(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toView(v))
}

type sharesBody struct {
	ExpectedVersion int `json:"expectedVersion"`
	Shares          []struct {
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Level       string `json:"level"`
	} `json:"shares"`
}

func (h *handler) setShares(w http.ResponseWriter, r *http.Request) {
	var b sharesBody
	if !decode(w, r, &b) {
		return
	}
	in := make([]views.ShareInput, 0, len(b.Shares))
	for _, s := range b.Shares {
		in = append(in, views.ShareInput{SubjectType: s.SubjectType, SubjectID: s.SubjectID, Level: s.Level})
	}
	v, err := h.svc.SetShares(r.Context(), caller(w, r), r.PathValue("id"), b.ExpectedVersion, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toView(v))
}

type resultsDTO struct {
	View struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		NameKey  string `json:"nameKey,omitempty"`
		Resource string `json:"resource"`
		Version  int    `json:"version"`
	} `json:"view"`
	Items       json.RawMessage `json:"items"`
	NextCursor  string          `json:"nextCursor,omitempty"`
	Count       *int            `json:"count,omitempty"`
	CountCapped bool            `json:"countCapped,omitempty"`
	Warnings    []query.Warning `json:"warnings"`
}

func (h *handler) results(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := views.ResultsInput{Cursor: q.Get("cursor"), TimeZone: q.Get("tz")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "views.invalid_request", "limit must be a positive number.")
			return
		}
		in.Limit = n
	}
	switch q.Get("count") {
	case "", "false":
	case "true":
		in.Count = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, "views.invalid_request", "count must be true or false.")
		return
	}
	out, err := h.svc.Results(r.Context(), caller(w, r), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var dto resultsDTO
	dto.View.ID, dto.View.Name, dto.View.NameKey, dto.View.Resource, dto.View.Version = out.View.ID, out.View.Name, out.View.NameKey, out.View.Resource, out.View.Version
	dto.Items, dto.NextCursor, dto.Count, dto.CountCapped = out.Items, out.NextCursor, out.Count, out.CountCapped
	dto.Warnings = out.Warnings
	if dto.Warnings == nil {
		dto.Warnings = []query.Warning{}
	}
	if len(dto.Items) == 0 {
		dto.Items = json.RawMessage("[]")
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// ---------------------------------------------------------------- pin rules

type ruleDTO struct {
	ID          string    `json:"id"`
	ViewID      string    `json:"viewId"`
	SubjectType string    `json:"subjectType"`
	SubjectID   string    `json:"subjectId"`
	GroupKey    string    `json:"groupKey"`
	Position    int       `json:"position"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toRule(r views.PinRule) ruleDTO {
	return ruleDTO{ID: r.ID, ViewID: r.ViewID, SubjectType: r.SubjectType, SubjectID: r.SubjectID, GroupKey: r.GroupKey,
		Position: r.Position, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

func (h *handler) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.svc.PinRules(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]ruleDTO, 0, len(rules))
	for _, rule := range rules {
		items = append(items, toRule(rule))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []ruleDTO `json:"items"`
	}{items})
}

type ruleBody struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	GroupKey    string `json:"groupKey"`
	Position    int    `json:"position"`
}

func (h *handler) createRule(w http.ResponseWriter, r *http.Request) {
	var b ruleBody
	if !decode(w, r, &b) {
		return
	}
	rule, err := h.svc.CreatePinRule(r.Context(), caller(w, r), r.PathValue("id"), views.PinRuleInput{SubjectType: b.SubjectType,
		SubjectID: b.SubjectID, GroupKey: b.GroupKey, Position: b.Position})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRule(rule))
}

func (h *handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeletePinRule(r.Context(), caller(w, r), r.PathValue("id"), r.PathValue("ruleId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- the user's pins and sidebar

type pinDTO struct {
	ViewID      string `json:"viewId"`
	Name        string `json:"name"`
	NameKey     string `json:"nameKey,omitempty"`
	Ref         string `json:"ref,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Resource    string `json:"resource"`
	GroupKey    string `json:"groupKey"`
	Position    int    `json:"position"`
	Hidden      bool   `json:"hidden"`
	Source      string `json:"source"`
	Count       *int   `json:"count,omitempty"`
	CountCapped bool   `json:"countCapped,omitempty"`
	CountStatus string `json:"countStatus,omitempty"`
}

func toPin(p views.PinEntry) pinDTO {
	return pinDTO{ViewID: p.ViewID, Name: p.Name, NameKey: p.NameKey, Ref: p.Ref, Kind: p.Kind, Resource: p.Resource, GroupKey: p.GroupKey, Position: p.Position,
		Hidden: p.Hidden, Source: p.Source, Count: p.Count, CountCapped: p.CountCapped, CountStatus: p.CountStatus}
}

func (h *handler) pins(w http.ResponseWriter, r *http.Request) {
	entries, err := h.svc.Pins(r.Context(), caller(w, r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]pinDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, toPin(e))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []pinDTO `json:"items"`
	}{items})
}

type pinsBody struct {
	Pins []struct {
		ViewID   string `json:"viewId"`
		GroupKey string `json:"groupKey"`
		Position int    `json:"position"`
		Hidden   bool   `json:"hidden"`
	} `json:"pins"`
}

func (h *handler) replacePins(w http.ResponseWriter, r *http.Request) {
	var b pinsBody
	if !decode(w, r, &b) {
		return
	}
	in := make([]views.PinInput, 0, len(b.Pins))
	for _, p := range b.Pins {
		in = append(in, views.PinInput{ViewID: p.ViewID, GroupKey: p.GroupKey, Position: p.Position, Hidden: p.Hidden})
	}
	if err := h.svc.ReplacePins(r.Context(), caller(w, r), in); err != nil {
		h.fail(w, r, err)
		return
	}
	h.pins(w, r)
}

func (h *handler) sidebar(w http.ResponseWriter, r *http.Request) {
	sb, err := h.svc.Sidebar(r.Context(), caller(w, r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type group struct {
		Key   string   `json:"key"`
		Items []pinDTO `json:"items"`
	}
	out := struct {
		Groups    []group  `json:"groups"`
		Collapsed []string `json:"collapsedGroups"`
	}{Groups: make([]group, 0, len(sb.Groups)), Collapsed: sb.Collapsed}
	for _, g := range sb.Groups {
		items := make([]pinDTO, 0, len(g.Items))
		for _, e := range g.Items {
			items = append(items, toPin(e))
		}
		out.Groups = append(out.Groups, group{Key: g.Key, Items: items})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) sidebarState(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CollapsedGroups []string `json:"collapsedGroups"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.SetSidebarState(r.Context(), caller(w, r), b.CollapsedGroups); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
