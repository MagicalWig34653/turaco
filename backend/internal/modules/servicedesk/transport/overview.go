package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type overviewQueueDTO struct {
	Queue        queueRefDTO `json:"queue"`
	Level        string      `json:"level"`
	Open         int         `json:"open"`
	Unassigned   int         `json:"unassigned"`
	Waiting      int         `json:"waiting"`
	OldestOpenAt *string     `json:"oldestOpenAt"`
}

type overviewAssigneeDTO struct {
	UserID  string `json:"userId"`
	Name    string `json:"name"`
	Open    int    `json:"open"`
	Waiting int    `json:"waiting"`
}

type overviewTicketDTO struct {
	ID           string      `json:"id"`
	Reference    string      `json:"reference"`
	Title        string      `json:"title"`
	Priority     string      `json:"priority"`
	Status       string      `json:"status"`
	Queue        queueRefDTO `json:"queue"`
	AssigneeID   *string     `json:"assigneeId"`
	AssigneeName string      `json:"assigneeName,omitempty"`
	CreatedAt    string      `json:"createdAt"`
	UpdatedAt    string      `json:"updatedAt"`
}

func toOverviewTickets(in []application.OverviewTicketRow) []overviewTicketDTO {
	out := make([]overviewTicketDTO, 0, len(in))
	for _, t := range in {
		out = append(out, overviewTicketDTO{ID: t.ID, Reference: t.Reference, Title: t.Title, Priority: t.Priority, Status: t.Status,
			Queue:      queueRefDTO{ID: t.Queue.ID, Key: t.Queue.Key, Prefix: t.Queue.Prefix, Name: t.Queue.Name},
			AssigneeID: t.AssigneeID, AssigneeName: t.AssigneeName, CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)})
	}
	return out
}

// overview handles GET /api/v1/tickets/overview: the manager overview of the Queues the caller may view.
func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.TicketOverview(r.Context(), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	queues := make([]overviewQueueDTO, 0, len(o.Queues))
	for _, q := range o.Queues {
		queues = append(queues, overviewQueueDTO{Queue: queueRefDTO{ID: q.Queue.ID, Key: q.Queue.Key, Prefix: q.Queue.Prefix, Name: q.Queue.Name},
			Level: q.Level, Open: q.Open, Unassigned: q.Unassigned, Waiting: q.Waiting, OldestOpenAt: tsPtr(q.OldestOpenAt)})
	}
	assignees := make([]overviewAssigneeDTO, 0, len(o.Assignees))
	for _, a := range o.Assignees {
		assignees = append(assignees, overviewAssigneeDTO{UserID: a.UserID, Name: a.Name, Open: a.Open, Waiting: a.Waiting})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"generatedAt":        ts(o.GeneratedAt),
		"totals":             map[string]int{"open": o.Totals.Open, "unassigned": o.Totals.Unassigned, "waiting": o.Totals.Waiting},
		"queues":             queues,
		"assignees":          assignees,
		"assigneesTruncated": o.AssigneesTruncated,
		"oldestUnassigned":   toOverviewTickets(o.OldestUnassigned),
		"oldestWaiting":      toOverviewTickets(o.OldestWaiting),
	})
}
