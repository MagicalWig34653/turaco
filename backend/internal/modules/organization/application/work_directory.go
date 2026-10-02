package application

import "context"

// WorkDirectoryStore is the storage port behind the work-directory contract
// (organization/public): the questions task and notification code asks about
// Users and Teams. Ids are validated UUIDs.
type WorkDirectoryStore interface {
	// ActiveUsers returns id -> true for each id that is a User with status "active".
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	// UserNames returns id -> display name for existing Users.
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
	// ActiveTeams returns id -> true for each id that is an active Team.
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	// TeamNames returns id -> name for existing Teams.
	TeamNames(ctx context.Context, ids []string) (map[string]string, error)
	// CurrentTeamIDs returns the active Teams the User currently belongs to.
	CurrentTeamIDs(ctx context.Context, userID string) ([]string, error)
}

// WorkDirectory answers Organization questions for work modules. Malformed
// ids are "not found/inactive", never errors.
type WorkDirectory struct{ store WorkDirectoryStore }

func NewWorkDirectory(store WorkDirectoryStore) *WorkDirectory { return &WorkDirectory{store: store} }

func validIDs(in []string) []string {
	var out []string
	for _, id := range in {
		if subjectUUID.MatchString(id) {
			out = append(out, id)
		}
	}
	return out
}

func (w *WorkDirectory) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	valid := validIDs(ids)
	if len(valid) == 0 {
		return map[string]bool{}, nil
	}
	return w.store.ActiveUsers(ctx, valid)
}

func (w *WorkDirectory) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	valid := validIDs(ids)
	if len(valid) == 0 {
		return map[string]string{}, nil
	}
	return w.store.UserNames(ctx, valid)
}

func (w *WorkDirectory) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	valid := validIDs(ids)
	if len(valid) == 0 {
		return map[string]bool{}, nil
	}
	return w.store.ActiveTeams(ctx, valid)
}

func (w *WorkDirectory) TeamNames(ctx context.Context, ids []string) (map[string]string, error) {
	valid := validIDs(ids)
	if len(valid) == 0 {
		return map[string]string{}, nil
	}
	return w.store.TeamNames(ctx, valid)
}

func (w *WorkDirectory) CurrentTeamIDs(ctx context.Context, userID string) ([]string, error) {
	if !subjectUUID.MatchString(userID) {
		return []string{}, nil
	}
	return w.store.CurrentTeamIDs(ctx, userID)
}
