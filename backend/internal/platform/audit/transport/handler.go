// Package transport exposes GET /api/v1/audit-events.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const permAuditView = "platform.audit.view"

type handler struct {
	reader *audit.Reader
	logger *slog.Logger
}

// Register mounts GET /api/v1/audit-events; it requires platform.audit.view.
func Register(mux *http.ServeMux, reader *audit.Reader, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{reader: reader, logger: logger}
	mux.Handle("GET /api/v1/audit-events", noStore(authorization.Require(auth, permAuditView)(http.HandlerFunc(h.list))))
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
}

// rawOrOmit is raw JSON that is omitted when empty or SQL NULL.
type rawOrOmit []byte

func (r rawOrOmit) MarshalJSON() ([]byte, error) { return r, nil }

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := audit.Filter{TargetType: q.Get("targetType"), TargetID: q.Get("targetId"), Action: q.Get("action"),
		ActionPrefix: q.Get("actionPrefix"), ActorID: q.Get("actorId"), CorrelationID: q.Get("correlationId")}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "audit.invalid_filter", "The "+name+" filter must be an RFC 3339 timestamp.")
				return
			}
			*dst = &t
		}
	}
	p := audit.Page{Cursor: q.Get("cursor")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "audit.invalid_limit", "The limit must be a positive integer.")
			return
		}
		p.Limit = n
	}
	res, err := h.reader.List(r.Context(), f, p)
	switch {
	case errors.Is(err, audit.ErrInvalidFilter):
		writeError(w, http.StatusBadRequest, "audit.invalid_filter", "A filter value is invalid.")
		return
	case errors.Is(err, audit.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "audit.invalid_cursor", "The cursor is invalid.")
		return
	case errors.Is(err, audit.ErrInvalidLimit):
		writeError(w, http.StatusBadRequest, "audit.invalid_limit", "The limit must be a positive integer.")
		return
	case err != nil:
		h.logger.ErrorContext(r.Context(), "audit query failed", "request_id", w.Header().Get("X-Request-ID"), "error", err)
		writeError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
		return
	}
	items := make([]eventDTO, 0, len(res.Items))
	for _, e := range res.Items {
		d := eventDTO{ID: e.ID, OccurredAt: e.OccurredAt, Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
			CorrelationID: e.CorrelationID, Before: nonNull(e.Before), After: nonNull(e.After), Metadata: map[string]any{}}
		if e.ActorID != nil {
			d.ActorID = *e.ActorID
		}
		if len(e.Metadata) > 0 {
			_ = jsonUnmarshal(e.Metadata, &d.Metadata)
		}
		items = append(items, d)
	}
	out := map[string]any{"items": items}
	if res.NextCursor != "" {
		out["nextCursor"] = res.NextCursor
	}
	httpx.JSON(w, http.StatusOK, out)
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// nonNull maps SQL NULL and JSON null to "omit".
func nonNull(b []byte) rawOrOmit {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}
