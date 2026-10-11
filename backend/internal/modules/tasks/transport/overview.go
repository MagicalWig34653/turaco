package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type overviewOwnerDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Open    int    `json:"open"`
	Overdue int    `json:"overdue"`
}

// overviewTaskDTO is a list row: no description, context or creator.
type overviewTaskDTO struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Status           string  `json:"status"`
	Priority         string  `json:"priority"`
	AssignedUserID   *string `json:"assignedUserId"`
	AssignedUserName *string `json:"assignedUserName"`
	AssignedTeamID   *string `json:"assignedTeamId"`
	AssignedTeamName *string `json:"assignedTeamName"`
	DueAt            *string `json:"dueAt"`
	CreatedAt        string  `json:"createdAt"`
}

func toOverviewOwners(in []application.OverviewOwnerView) []overviewOwnerDTO {
	out := make([]overviewOwnerDTO, 0, len(in))
	for _, o := range in {
		out = append(out, overviewOwnerDTO{ID: o.ID, Name: o.Name, Open: o.Open, Overdue: o.Overdue})
	}
	return out
}

func toOverviewTasks(in []application.TaskView) []overviewTaskDTO {
	out := make([]overviewTaskDTO, 0, len(in))
	for _, t := range in {
		out = append(out, overviewTaskDTO{ID: t.ID, Title: t.Title, Status: t.Status, Priority: t.Priority,
			AssignedUserID: t.AssignedUserID, AssignedUserName: t.AssignedUserName,
			AssignedTeamID: t.AssignedTeamID, AssignedTeamName: t.AssignedTeamName,
			DueAt: tsPtr(t.DueAt), CreatedAt: ts(t.CreatedAt)})
	}
	return out
}

// overview handles GET /api/v1/tasks/overview: the manager overview, for callers who see every Task.
func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.Overview(r.Context(), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"generatedAt": ts(o.GeneratedAt),
		"totals": map[string]int{"open": o.Totals.Open, "overdue": o.Totals.Overdue, "blocked": o.Totals.Blocked,
			"unowned": o.Totals.Unowned},
		"users":          toOverviewOwners(o.Users),
		"usersTruncated": o.UsersTruncated,
		"teams":          toOverviewOwners(o.Teams),
		"teamsTruncated": o.TeamsTruncated,
		"oldestOverdue":  toOverviewTasks(o.OldestOverdue),
		"oldestUnowned":  toOverviewTasks(o.OldestUnowned),
	})
}
