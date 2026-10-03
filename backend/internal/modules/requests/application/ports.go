package application

import (
	"context"

	"github.com/jackc/pgx/v5"

	approvalspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/public"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	taskspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/public"
)

// Catalog loads items for submission (catalog/public.Catalog).
type Catalog interface {
	ForSubmission(ctx context.Context, id string) (catalogpublic.Submission, error)
}

// Approvals is the approvals contract (approvals/public.Approvals).
type Approvals interface {
	RequestInTx(ctx context.Context, tx pgx.Tx, c approvalspublic.Caller, r approvalspublic.Request) (string, error)
	CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, c approvalspublic.Caller, subjectType, subjectID string) (int, error)
	ForSubject(ctx context.Context, subjectType, subjectID string) ([]approvalspublic.Approval, error)
	CanView(ctx context.Context, subjectType, subjectID, userID string) (bool, error)
}

// Tasks is the tasks contract (tasks/public.Creator).
type Tasks interface {
	CreateInTx(ctx context.Context, tx pgx.Tx, c taskspublic.Caller, in taskspublic.CreateInput) (string, error)
	CancelByContextInTx(ctx context.Context, tx pgx.Tx, c taskspublic.Caller, contextType, contextID, reason string) (int, error)
	Tasks(ctx context.Context, ids []string) ([]taskspublic.Task, error)
	StatusesInTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error)
}

// Directory answers the Organization questions requests need.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	TeamNames(ctx context.Context, ids []string) (map[string]string, error)
	ManagerIDs(ctx context.Context, userIDs []string) (map[string]string, error)
}
