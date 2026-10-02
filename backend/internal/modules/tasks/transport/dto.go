package transport

import (
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
)

type listResponse struct {
	Items      []taskDTO `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type taskDTO struct {
	ID                     string  `json:"id"`
	Title                  string  `json:"title"`
	Description            *string `json:"description"`
	Status                 string  `json:"status"`
	StatusReason           *string `json:"statusReason"`
	Priority               string  `json:"priority"`
	AssignedUserID         *string `json:"assignedUserId"`
	AssignedUserName       *string `json:"assignedUserName"`
	AssignedTeamID         *string `json:"assignedTeamId"`
	AssignedTeamName       *string `json:"assignedTeamName"`
	ContextType            *string `json:"contextType"`
	ContextID              *string `json:"contextId"`
	DueAt                  *string `json:"dueAt"`
	CompletedAt            *string `json:"completedAt"`
	CreatedByUserID        *string `json:"createdByUserId"`
	CompletedByUserID      *string `json:"completedByUserId"`
	RecurrenceDefinitionID *string `json:"recurrenceDefinitionId"`
	Version                int     `json:"version"`
	CreatedAt              string  `json:"createdAt"`
	UpdatedAt              string  `json:"updatedAt"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func toTask(v application.TaskView) taskDTO {
	return taskDTO{
		ID: v.ID, Title: v.Title, Description: v.Description, Status: v.Status, StatusReason: v.StatusReason,
		Priority: v.Priority, AssignedUserID: v.AssignedUserID, AssignedUserName: v.AssignedUserName,
		AssignedTeamID: v.AssignedTeamID, AssignedTeamName: v.AssignedTeamName,
		ContextType: v.ContextType, ContextID: v.ContextID, DueAt: tsPtr(v.DueAt), CompletedAt: tsPtr(v.CompletedAt),
		CreatedByUserID: v.CreatedByUserID, CompletedByUserID: v.CompletedByUserID, RecurrenceDefinitionID: v.RecurrenceDefinitionID,
		Version: v.Version, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt),
	}
}

func toList(r application.Result[application.TaskView]) listResponse {
	out := listResponse{Items: make([]taskDTO, 0, len(r.Items)), NextCursor: r.NextCursor}
	for _, v := range r.Items {
		out.Items = append(out.Items, toTask(v))
	}
	return out
}
