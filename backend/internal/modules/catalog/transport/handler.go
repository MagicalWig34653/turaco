// Package transport exposes the Catalog HTTP API under /api/v1. Employees
// (every signed-in User) read active items and their forms; catalog.manage
// administers items and sees their full definitions.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permManage = "catalog.manage"
	maxBody    = application.MaxDefinitionBytes + 8<<10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the catalog routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	manage := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/catalog-items", authed, h.list)
	route("GET /api/v1/catalog-items/{id}", authed, h.get)
	route("POST /api/v1/catalog-items", manage, h.create)
	route("PATCH /api/v1/catalog-items/{id}", manage, h.update)
	route("POST /api/v1/catalog-items/{id}/activate", manage, h.activate(true))
	route("POST /api/v1/catalog-items/{id}/deactivate", manage, h.activate(false))
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_request", inv.Message)
	case errors.Is(err, application.ErrReferenceInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_reference", "The definition references a user, team, product or category that does not exist or is not active.")
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "catalog.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "catalog.conflict", "A catalog item with this key already exists.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "catalog.version_conflict", "The item was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "catalog request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, Manage: p.Has(permManage)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// itemDTO is the summary used by lists.
type itemDTO struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

func toItem(it application.Item) itemDTO {
	return itemDTO{ID: it.ID, Key: it.Key, Title: it.Title, Description: it.Description, Active: it.Active, Version: it.Version, UpdatedAt: ts(it.UpdatedAt)}
}

type productOptionDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fieldDTO struct {
	application.Field
	ProductOptions []productOptionDTO `json:"productOptions,omitempty"`
}

// formDTO is what employees get: the form without approval and fulfillment details.
type formDTO struct {
	itemDTO
	AllowRequestedFor bool       `json:"allowRequestedFor"`
	Fields            []fieldDTO `json:"fields"`
	// Definition is the full definition, present only for managers.
	Definition *application.Definition `json:"definition,omitempty"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_limit", "The limit must be a positive integer.")
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), r.URL.Query().Get("status"), application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []itemDTO `json:"items"`
		NextCursor string    `json:"nextCursor,omitempty"`
	}{Items: make([]itemDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, it := range res.Items {
		out.Items = append(out.Items, toItem(it))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	f, err := h.svc.Form(r.Context(), p, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	dto := formDTO{itemDTO: toItem(f.Item), AllowRequestedFor: f.Item.Definition.AllowRequestedFor, Fields: make([]fieldDTO, 0, len(f.Fields))}
	for _, ff := range f.Fields {
		fd := fieldDTO{Field: ff.Field}
		for _, o := range ff.ProductOptions {
			fd.ProductOptions = append(fd.ProductOptions, productOptionDTO{ID: o.ID, Name: o.Name})
		}
		// Product restrictions are admin detail; employees get the resolved options.
		fd.CategoryID, fd.ProductIDs = "", nil
		if p.Manage {
			fd.Field = ff.Field
		}
		dto.Fields = append(dto.Fields, fd)
	}
	if p.Manage {
		d := f.Item.Definition
		dto.Definition = &d
	}
	httpx.JSON(w, http.StatusOK, dto)
}

type createBody struct {
	Key         string          `json:"key"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !decode(w, r, &b) {
		return
	}
	it, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.CreateInput{Key: b.Key, Title: b.Title, Description: b.Description, Definition: b.Definition})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toItem(it))
}

type updateBody struct {
	ExpectedVersion *int            `json:"expectedVersion"`
	Title           *string         `json:"title"`
	Description     *string         `json:"description"`
	Definition      json.RawMessage `json:"definition"`
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBody
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "catalog.invalid_request", "expectedVersion is required to change an item.")
		return
	}
	it, err := h.svc.Update(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, application.UpdateInput{
		Title: b.Title, Description: b.Description, Definition: b.Definition,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItem(it))
}

func (h *handler) activate(active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int `json:"expectedVersion"`
		}
		if !decode(w, r, &b) {
			return
		}
		it, err := h.svc.SetActive(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, active)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toItem(it))
	}
}
