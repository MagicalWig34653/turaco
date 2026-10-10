package transport

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/csvsafe"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// retention answers the policy: the configured retention, the safe floor and the oldest stored event.
func (h *handler) retention(w http.ResponseWriter, r *http.Request) {
	oldest, err := h.reader.Oldest(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "audit oldest failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
		return
	}
	out := map[string]any{"retentionDays": h.retentionDays, "minimumDays": audit.MinRetentionDays, "policy": "keep"}
	if h.retentionDays > 0 {
		out["policy"] = "purge"
	}
	if oldest != nil {
		out["oldestEventAt"] = oldest.UTC()
	}
	httpx.JSON(w, http.StatusOK, out)
}

// export streams the matching events as CSV. It needs platform.audit.view (route) and platform.audit.export.
func (h *handler) export(w http.ResponseWriter, r *http.Request) {
	p, _ := authorization.PrincipalFrom(r.Context())
	if !p.Has(permAuditExport) {
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
		return
	}
	q := r.URL.Query()
	f, ok := h.filterFromQuery(w, q)
	if !ok {
		return
	}
	if f.From == nil || f.To == nil {
		httpx.WriteError(w, http.StatusBadRequest, "audit.range_required", "An export needs a time range (from and to).")
		return
	}
	if f.To.Sub(*f.From) > audit.ExportMaxRange {
		httpx.WriteErrorDetails(w, http.StatusBadRequest, "audit.range_too_long", "An export covers at most 92 days.", map[string]any{"maxDays": 92})
		return
	}
	details := q.Get("includeDetails") == "true"
	used, err := h.reader.RecentExports(r.Context(), p.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if used >= audit.ExportsPerHour {
		h.exportLimited(w)
		return
	}
	n, err := h.reader.Count(r.Context(), f, audit.ExportMaxRows+1)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if n > audit.ExportMaxRows {
		httpx.WriteErrorDetails(w, http.StatusRequestEntityTooLarge, "audit.export_too_large", "The export would exceed 10000 events: narrow the range or filters.",
			map[string]any{"maxRows": audit.ExportMaxRows, "countCapped": true})
		return
	}
	// The export is recorded before anything streams: an interrupted download is still accounted for.
	if err := h.reader.RecordExport(r.Context(), p.UserID, httpx.RequestID(w), f, n, details); err != nil {
		if errors.Is(err, audit.ErrExportRateLimited) {
			h.exportLimited(w)
			return
		}
		h.fail(w, r, err)
		return
	}
	events := make([]audit.Event, 0, n)
	if _, err := h.reader.Each(r.Context(), f, audit.ExportMaxRows, func(e audit.Event) error { events = append(events, e); return nil }); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="audit-%s.csv"`, time.Now().UTC().Format("20060102-150405")))
	cw := csvsafe.NewWriter(w)
	header := []string{"time", "actor", "actor_id", "action", "target_type", "target", "target_id", "correlation_id", "via"}
	if details {
		header = append(header, "before", "after", "metadata")
	}
	_ = cw.Write(header)
	for start := 0; start < len(events); start += 200 {
		end := start + 200
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		labels := h.resolvers.Resolve(r.Context(), chunk)
		for _, e := range chunk {
			d := toDTO(e, labels)
			row := []string{e.OccurredAt.UTC().Format(time.RFC3339), actorText(d), d.ActorID, e.Action, e.TargetType, targetText(d), e.TargetID, e.CorrelationID, e.Via}
			if details {
				row = append(row, string(e.Before), string(e.After), string(e.Metadata))
			}
			_ = cw.Write(row)
		}
	}
	_ = cw.Flush()
}

func (h *handler) exportLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "600")
	httpx.WriteErrorDetails(w, http.StatusTooManyRequests, "audit.export_rate_limited", "At most 5 exports per hour are allowed.", map[string]any{"perHour": audit.ExportsPerHour})
}

func actorText(d eventDTO) string {
	switch {
	case d.Actor != nil && d.Actor.Gone:
		return "(removed) " + shortID(d.ActorID)
	case d.Actor != nil:
		return d.Actor.Text
	case d.SystemActor != "":
		return "system:" + d.SystemActor
	default:
		return d.ActorID
	}
}

func targetText(d eventDTO) string {
	switch {
	case d.Target != nil && d.Target.Gone:
		return "(removed) " + shortID(d.TargetID)
	case d.Target != nil:
		return d.Target.Text
	default:
		return ""
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, audit.ErrInvalidFilter):
		httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_filter", "A filter value is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "audit request failed", "request_id", httpx.RequestID(w), "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

// filterFromQuery parses the shared filter parameters; it answers the 400 itself.
func (h *handler) filterFromQuery(w http.ResponseWriter, q map[string][]string) (audit.Filter, bool) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	f := audit.Filter{TargetType: get("targetType"), TargetID: get("targetId"), Action: get("action"), ActionPrefix: get("actionPrefix"),
		ActorID: get("actorId"), CorrelationID: get("correlationId"), Via: get("via"), ActorKind: get("actorKind"), SystemActor: get("systemActor")}
	if m := get("module"); m != "" {
		if f.ActionPrefix != "" || !modulePattern.MatchString(m) {
			httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_filter", "A filter value is invalid.")
			return f, false
		}
		f.ActionPrefix = m + "."
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := strings.TrimSpace(get(name)); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "audit.invalid_filter", "The "+name+" filter must be an RFC 3339 timestamp.")
				return f, false
			}
			*dst = &t
		}
	}
	// The system actor lives in event metadata and has no index: it needs a bounded time range.
	if f.SystemActor != "" && (f.From == nil || f.To == nil || f.To.Sub(*f.From) > audit.ExportMaxRange) {
		httpx.WriteErrorDetails(w, http.StatusBadRequest, "audit.range_required", "A system actor filter needs a time range (from and to) of at most 92 days.", map[string]any{"maxDays": 92})
		return f, false
	}
	return f, true
}
