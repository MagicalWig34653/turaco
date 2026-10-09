package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
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
	ResultNote             *string `json:"resultNote"`
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
		CreatedByUserID: v.CreatedByUserID, CompletedByUserID: v.CompletedByUserID, RecurrenceDefinitionID: v.RecurrenceDefinitionID, ResultNote: v.ResultNote,
		Version: v.Version, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt),
	}
}

func toList(r application.Result[application.TaskView], f func(application.TaskView) taskDTO) listResponse {
	out := listResponse{Items: make([]taskDTO, 0, len(r.Items)), NextCursor: r.NextCursor}
	for _, v := range r.Items {
		out.Items = append(out.Items, f(v))
	}
	return out
}

// restrictedViewer reports a caller whose only permission is tasks.work: the closed ceiling of the external vendor
// template (docs: ADR-0034). Such a caller works the tasks assigned to them or their Team and learns nothing else
// from them. The explicit external account kind (slice A-G) will replace this test.
func restrictedViewer(p authorization.Principal) bool {
	return p.Has(permWork) && len(p.Permissions) == 1
}

// shaperFor returns the Task DTO function for the caller: restricted callers get no reference to the record a
// task belongs to (contextType, contextId), to its creator or to the recurrence that generated it, and see
// completedByUserId only when they completed the task themselves. What the title and description say is the
// author's decision.
func shaperFor(r *http.Request) func(application.TaskView) taskDTO {
	p, _ := authorization.PrincipalFrom(r.Context())
	if !restrictedViewer(p) {
		return toTask
	}
	return func(v application.TaskView) taskDTO {
		d := toTask(v)
		d.ContextType, d.ContextID, d.CreatedByUserID, d.RecurrenceDefinitionID = nil, nil, nil, nil
		if d.CompletedByUserID != nil && *d.CompletedByUserID != p.UserID {
			d.CompletedByUserID = nil
		}
		return d
	}
}
