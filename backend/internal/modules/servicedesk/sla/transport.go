package sla

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type handler struct {
	store  *Store
	logger *slog.Logger
}

func Register(mux *http.ServeMux, store *Store, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{store, logger}
	read := func(fn http.HandlerFunc) http.Handler {
		return httpx.NoStore(authorization.RequireAny(auth, "sla.view", "sla.manage")(fn))
	}
	write := func(fn http.HandlerFunc) http.Handler {
		return httpx.NoStore(authorization.Require(auth, "sla.manage")(fn))
	}
	mux.Handle("GET /api/v1/sla/policies", read(h.list))
	mux.Handle("POST /api/v1/sla/policies", write(h.create))
	mux.Handle("GET /api/v1/sla/policies/{id}", read(h.get))
	mux.Handle("PATCH /api/v1/sla/policies/{id}", write(h.update))
	mux.Handle("PUT /api/v1/sla/policies/{id}/targets", write(h.targets))
	mux.Handle("POST /api/v1/sla/policies/{id}/archive", write(h.archive))
	mux.Handle("POST /api/v1/sla/policies/{id}/restore", write(h.restore))
	mux.Handle("GET /api/v1/sla/queues", read(h.queues))
	mux.Handle("PUT /api/v1/sla/queues/{id}/policy", write(h.queuePolicy))
}
func decodeSLA(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return false
	}
	var extra any
	return errors.Is(d.Decode(&extra), io.EOF)
}
func (h *handler) bad(w http.ResponseWriter) {
	httpx.WriteError(w, 400, "sla.invalid_request", "The request is invalid.")
}
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, 404, "sla.not_found", "SLA policy or queue not found.")
	case errors.Is(err, ErrConflict):
		httpx.WriteError(w, 409, "sla.version_conflict", "The record changed; reload and try again.")
	case errors.Is(err, ErrInUse):
		httpx.WriteError(w, 409, "sla.policy_in_use", "The policy is assigned to a queue.")
	case errors.Is(err, ErrKeyTaken):
		httpx.WriteError(w, 409, "sla.key_taken", "The policy key already exists.")
	case errors.Is(err, ErrInvalid):
		httpx.WriteError(w, 400, "sla.invalid_request", "The SLA policy is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "SLA request failed", "error", err)
		httpx.WriteError(w, 500, "platform.internal_error", "An internal error occurred.")
	}
}
func slaActor(r *http.Request) string {
	p, _ := authorization.PrincipalFrom(r.Context())
	return p.UserID
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
	p, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, p)
}
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !decodeSLA(w, r, &in) {
		h.bad(w)
		return
	}
	p, err := h.store.Create(r.Context(), in, slaActor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 201, p)
}
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decodeSLA(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	p, err := h.store.Update(r.Context(), r.PathValue("id"), *in.ExpectedVersion, in.Input, slaActor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, p)
}
func (h *handler) targets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion *int     `json:"expectedVersion"`
		Targets         []Target `json:"targets"`
	}
	if !decodeSLA(w, r, &in) || in.ExpectedVersion == nil || in.Targets == nil {
		h.bad(w)
		return
	}
	p, err := h.store.ReplaceTargets(r.Context(), r.PathValue("id"), *in.ExpectedVersion, in.Targets, slaActor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, p)
}
func (h *handler) archive(w http.ResponseWriter, r *http.Request) { h.status(w, r, "archived") }
func (h *handler) restore(w http.ResponseWriter, r *http.Request) { h.status(w, r, "active") }
func (h *handler) status(w http.ResponseWriter, r *http.Request, status string) {
	var in struct {
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decodeSLA(w, r, &in) || in.ExpectedVersion == nil {
		h.bad(w)
		return
	}
	p, err := h.store.SetStatus(r.Context(), r.PathValue("id"), *in.ExpectedVersion, status, slaActor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, p)
}
func (h *handler) queues(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListQueues(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items})
}
func (h *handler) queuePolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PolicyID             *string `json:"policyId"`
		ExpectedQueueVersion *int    `json:"expectedQueueVersion"`
	}
	if !decodeSLA(w, r, &in) || in.ExpectedQueueVersion == nil {
		h.bad(w)
		return
	}
	q, err := h.store.SetQueuePolicy(r.Context(), r.PathValue("id"), in.PolicyID, *in.ExpectedQueueVersion, slaActor(r), httpx.RequestID(w))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, 200, q)
}
