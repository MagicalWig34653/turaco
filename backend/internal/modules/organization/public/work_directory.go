package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// WorkDirectory is the Organization contract for work modules (tasks,
// notifications): activity checks, display names and Team membership of
// Users, without exposing Organization storage.
type WorkDirectory struct{ app *application.WorkDirectory }

// NewWorkDirectory wraps the Organization repository (or any store).
func NewWorkDirectory(store application.WorkDirectoryStore) *WorkDirectory {
	return &WorkDirectory{app: application.NewWorkDirectory(store)}
}

// ActiveUsers returns id -> true for each id that is a User with status "active".
func (w *WorkDirectory) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	return w.app.ActiveUsers(ctx, ids)
}

// UserNames returns id -> display name; unknown ids are absent.
func (w *WorkDirectory) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	return w.app.UserNames(ctx, ids)
}

// ActiveTeams returns id -> true for each id that is an active Team.
func (w *WorkDirectory) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	return w.app.ActiveTeams(ctx, ids)
}

// TeamNames returns id -> name; unknown ids are absent.
func (w *WorkDirectory) TeamNames(ctx context.Context, ids []string) (map[string]string, error) {
	return w.app.TeamNames(ctx, ids)
}

// CurrentTeamIDs returns the ids of the active Teams the User currently belongs to.
func (w *WorkDirectory) CurrentTeamIDs(ctx context.Context, userID string) ([]string, error) {
	return w.app.CurrentTeamIDs(ctx, userID)
}

// Contact is the reachability of a User: display name, primary email address (empty when
// none) and whether the User is active.
type Contact = application.Contact

// Contacts returns id -> Contact for existing Users; unknown ids are absent.
func (w *WorkDirectory) Contacts(ctx context.Context, ids []string) (map[string]Contact, error) {
	return w.app.Contacts(ctx, ids)
}

// CurrentMemberIDs returns the Users currently in an active Team (at most
// application.MaxTeamMembers); an inactive or unknown Team has none.
func (w *WorkDirectory) CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error) {
	return w.app.CurrentMemberIDs(ctx, teamID)
}

// ManagerIDs returns user id -> manager user id for Users that have a manager.
func (w *WorkDirectory) ManagerIDs(ctx context.Context, userIDs []string) (map[string]string, error) {
	return w.app.ManagerIDs(ctx, userIDs)
}
