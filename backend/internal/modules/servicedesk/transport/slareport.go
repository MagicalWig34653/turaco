package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type slaReportKindDTO struct {
	Kind          string   `json:"kind"`
	Measured      int      `json:"measured"`
	Met           int      `json:"met"`
	Breached      int      `json:"breached"`
	Exempted      int      `json:"exempted"`
	MetRate       *float64 `json:"metRate"`
	MedianSeconds *int64   `json:"medianSeconds"`
	P90Seconds    *int64   `json:"p90Seconds"`
}

type slaReportGroupDTO struct {
	Key   string             `json:"key"`
	Queue *queueRefDTO       `json:"queue,omitempty"`
	Kinds []slaReportKindDTO `json:"kinds"`
}

// slaReport handles GET /api/v1/sla/report?from&to&groupBy&queueId: finished SLA clocks per Queue, Priority or month for
// the Queues the caller may view. Dates are UTC days (YYYY-MM-DD), both inclusive, at most 366 days. There is no
// per-person breakdown and no export.
func (h *handler) slaReport(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	from, errFrom := time.Parse(time.DateOnly, v.Get("from"))
	to, errTo := time.Parse(time.DateOnly, v.Get("to"))
	if errFrom != nil || errTo != nil {
		h.fail(w, r, &application.InvalidInputError{Message: "from and to must be dates (YYYY-MM-DD)"})
		return
	}
	rep, err := h.svc.SLAReport(r.Context(), principal(r), from, to, v.Get("groupBy"), v.Get("queueId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	groups := make([]slaReportGroupDTO, 0, len(rep.Groups))
	for _, g := range rep.Groups {
		dto := slaReportGroupDTO{Key: g.Key, Kinds: make([]slaReportKindDTO, 0, len(g.Kinds))}
		if g.Queue != nil {
			dto.Queue = &queueRefDTO{ID: g.Queue.ID, Key: g.Queue.Key, Prefix: g.Queue.Prefix, Name: g.Queue.Name}
		}
		for _, k := range g.Kinds {
			dto.Kinds = append(dto.Kinds, slaReportKindDTO{Kind: k.Kind, Measured: k.Measured, Met: k.Met, Breached: k.Breached,
				Exempted: k.Exempted, MetRate: k.MetRate, MedianSeconds: k.MedianSeconds, P90Seconds: k.P90Seconds})
		}
		groups = append(groups, dto)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"from": rep.From.Format(time.DateOnly), "to": rep.To.Format(time.DateOnly), "groupBy": rep.GroupBy, "groups": groups,
	})
}
