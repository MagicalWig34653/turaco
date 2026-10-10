// Package transport exposes GET /api/v1/audit-events.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permAuditView   = "platform.audit.view"
	permAuditExport = "platform.audit.export"
)

type handler struct {
	reader        *audit.Reader
	logger        *slog.Logger
	resolvers     *audit.Resolvers
	retentionDays int
}

// Option configures Register.
type Option func(*handler)

// WithResolvers labels actors and targets with display names (the resolvers run after the page is authorized).
func WithResolvers(rs *audit.Resolvers) Option { return func(h *handler) { h.resolvers = rs } }

// WithRetentionDays states the configured retention (0 keeps everything) for GET /audit-events/retention.
func WithRetentionDays(days int) Option { return func(h *handler) { h.retentionDays = days } }

// Register mounts GET /api/v1/audit-events, /retention and /export.csv; all require platform.audit.view and the
// export additionally platform.audit.export.
func Register(mux *http.ServeMux, reader *audit.Reader, auth authorization.Authenticator, logger *slog.Logger, opts ...Option) {
	h := &handler{reader: reader, logger: logger}
	for _, o := range opts {
		o(h)
	}
	view := authorization.Require(auth, permAuditView)
	mux.Handle("GET /api/v1/audit-events", httpx.NoStore(view(http.HandlerFunc(h.list))))
	mux.Handle("GET /api/v1/audit-events/retention", httpx.NoStore(view(http.HandlerFunc(h.retention))))
	mux.Handle("GET /api/v1/audit-events/export.csv", httpx.NoStore(view(http.HandlerFunc(h.export))))
}

type eventDTO struct {
	ID            string         `json:"id"`
	OccurredAt    time.Time      `json:"occurredAt"`
	ActorID       string         `json:"actorId,omitempty"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetID      string         `json:"targetId"`
	CorrelationID string         `json:"correlationId"`
	Before        rawOrOmit      `json:"before,omitempty"`
	After         rawOrOmit      `json:"after,omitempty"`
	Metadata      map[string]any `json:"metadata"`
	// Resolved display names (omitted when no resolver knows the type); SystemActor names a non-human actor.
	Actor       *labelDTO `json:"actor,omitempty"`
	SystemActor string    `json:"systemActor,omitempty"`
	Target      *labelDTO `json:"target,omitempty"`
	Via         string    `json:"via,omitempty"`
}

type labelDTO struct {
	Text string `json:"text,omitempty"`
	Gone bool   `json:"gone,omitempty"`
}

// rawOrOmit is raw JSON that is omitted when empty or SQL NULL.
type rawOrOmit []byte

func (r rawOrOmit) MarshalJSON() ([]byte, error) { return r, nil }

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, ok := h.filterFromQuery(w, q)
	if !ok {
		return
	}
	p := audit.Page{Cursor: q.Get("cursor")}
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_limit", "The limit must be a positive integer.")
		return
	}
	p.Limit = limit
	res, err := h.reader.List(r.Context(), f, p)
	switch {
	case errors.Is(err, audit.ErrInvalidFilter):
		httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_filter", "A filter value is invalid.")
		return
	case errors.Is(err, audit.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_cursor", "The cursor is invalid.")
		return
	case errors.Is(err, audit.ErrInvalidLimit):
		httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_limit", "The limit must be a positive integer.")
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "audit query failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
		return
	}
	labels := h.resolvers.Resolve(r.Context(), res.Items)
	items := make([]eventDTO, 0, len(res.Items))
	for _, e := range res.Items {
		items = append(items, toDTO(e, labels))
	}
	out := map[string]any{"items": items}
	if res.NextCursor != "" {
		out["nextCursor"] = res.NextCursor
	}
	if len(labels.Unavailable) > 0 {
		out["unresolvedTypes"] = labels.Unavailable
	}
	httpx.JSON(w, http.StatusOK, out)
}

var modulePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func toDTO(e audit.Event, labels audit.Resolved) eventDTO {
	d := eventDTO{ID: e.ID, OccurredAt: e.OccurredAt, Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		CorrelationID: e.CorrelationID, Before: nonNull(e.Before), After: nonNull(e.After), Metadata: map[string]any{}, Via: e.Via}
	if e.ActorID != nil {
		d.ActorID = *e.ActorID
		if l, ok := labels.Actors[d.ActorID]; ok {
			d.Actor = &labelDTO{Text: l.Text, Gone: l.Gone}
		}
	}
	if len(e.Metadata) > 0 {
		_ = jsonUnmarshal(e.Metadata, &d.Metadata)
	}
	if e.ActorID == nil {
		if sys, ok := d.Metadata["actor"].(string); ok {
			d.SystemActor = sys
		}
	}
	if l, ok := labels.Targets[audit.TargetKey(e.TargetType, e.TargetID)]; ok {
		d.Target = &labelDTO{Text: l.Text, Gone: l.Gone}
	}
	return d
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// nonNull maps SQL NULL and JSON null to "omit".
func nonNull(b []byte) rawOrOmit {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}
