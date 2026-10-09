package wiring

import (
	"github.com/jackc/pgx/v5/pgxpool"

	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// TaskBoards builds the Task Board service (ADR-0033, F13 Q-D): a Board is a Saved View over tasks plus columns and
// card ranks, so it needs the Views service for name, filter and sharing. The Views service learns which pinned
// Views are Boards, so the sidebar can link them to the Board screen.
func TaskBoards(tasks *tasksapp.Service, pool *pgxpool.Pool, v *views.Service) *tasksapp.Boards {
	boards := tasksapp.NewBoards(tasks, tasksrepository.New(pool), v)
	v.WithPinAnnotator(boards)
	return boards
}
