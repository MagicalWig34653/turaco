package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/workitems"
)

// WorkItems is the Tasks source of My Work: the caller's unfinished tasks assigned to them or to one of their Teams,
// in the shared task order. A caller without any tasks permission gets nothing.
type WorkItems struct{ svc *application.Service }

// NewWorkItems builds the source over the Tasks service.
func NewWorkItems(svc *application.Service) *WorkItems { return &WorkItems{svc: svc} }

var _ workitems.Source = (*WorkItems)(nil)

func (w *WorkItems) Key() string    { return "tasks" }
func (w *WorkItems) Module() string { return "tasks" }

func principal(p authorization.Principal) (application.Principal, bool) {
	ap := application.Principal{UserID: p.UserID, ViewAll: p.Has("tasks.view"), Manage: p.Has("tasks.manage"), Work: p.Has("tasks.work")}
	return ap, ap.UserID != "" && (ap.ViewAll || ap.Manage || ap.Work)
}

// Items implements workitems.Source.
func (w *WorkItems) Items(ctx context.Context, p authorization.Principal, cursor string, limit int) ([]workitems.Entry, error) {
	ap, ok := principal(p)
	if !ok {
		return nil, nil
	}
	res, err := w.svc.MyWork(ctx, ap, application.Page{Limit: limit, Cursor: cursor})
	if errors.Is(err, application.ErrInvalidCursor) {
		return nil, workitems.ErrInvalidCursor
	}
	if err != nil {
		return nil, err
	}
	out := make([]workitems.Entry, 0, len(res.Items))
	for _, t := range res.Items {
		out = append(out, workitems.Entry{
			Item: workitems.Item{ID: t.ID, Source: "tasks", Kind: "task", Title: t.Title, Status: t.Status, Priority: t.Priority, DueAt: t.DueAt,
				UpdatedAt: t.UpdatedAt, Href: "/tasks/" + t.ID},
			Rank: application.PriorityRank(t.Priority), Cursor: application.EncodeCursor(t.Task),
		})
	}
	return out, nil
}

// Count implements workitems.Source.
func (w *WorkItems) Count(ctx context.Context, p authorization.Principal) (workitems.Count, error) {
	ap, ok := principal(p)
	if !ok {
		return workitems.Count{}, nil
	}
	n, err := w.svc.MyWorkCount(ctx, ap, workitems.CountCap)
	if err != nil {
		return workitems.Count{}, err
	}
	return workitems.Count{N: n, Capped: n > workitems.CountCap}, nil
}
