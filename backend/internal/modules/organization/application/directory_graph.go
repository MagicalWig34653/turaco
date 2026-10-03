package application

import (
	"context"
	"time"
)

// MaxGraphRows bounds every directory-graph lookup. A lookup that had more rows returns the first MaxGraphRows
// (or fewer, as requested) and truncated=true; callers must then treat everything derived from the result as
// incomplete, never as a complete answer.
const MaxGraphRows = 5000

// MaxNestingDepth bounds how many nesting levels NestingUp/NestingDown walk. Deeper groups are reported as truncated.
const MaxNestingDepth = 10

// DirectoryGroupRef is a live (not deleted) Directory Group as other modules may see it.
type DirectoryGroupRef struct {
	ID             string
	ProviderKey    string
	ExternalID     string
	DisplayName    string
	LastObservedAt time.Time
}

// GroupNestingEdge says that Child is a direct member group of Parent as currently observed.
type GroupNestingEdge struct{ ChildID, ParentID string }

// GroupUserMembership is a currently observed membership of a User in a Directory Group.
type GroupUserMembership struct {
	UserID         string
	GroupID        string
	LastObservedAt time.Time
}

// DirectoryGraphStore is the storage port of the read-only directory graph contract.
// All methods use only currently observed intervals, non-deleted groups and the groups of one provider key.
// Every list method returns at most limit rows and truncated=true when more rows (or deeper nesting) existed.
// Ids are validated UUIDs.
type DirectoryGraphStore interface {
	GroupsByExternalIDs(ctx context.Context, providerKey string, externalIDs []string, limit int) ([]DirectoryGroupRef, bool, error)
	GroupsByIDs(ctx context.Context, providerKey string, ids []string, limit int) ([]DirectoryGroupRef, bool, error)
	// NestingUp returns the nesting edges reachable from the groups towards their parents (transitively, at most MaxNestingDepth levels).
	NestingUp(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupNestingEdge, bool, error)
	// NestingDown returns the nesting edges reachable from the groups towards their children (transitively, at most MaxNestingDepth levels).
	NestingDown(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupNestingEdge, bool, error)
	// UserMemberships returns the direct group memberships of the users ordered by (user id, group id).
	UserMemberships(ctx context.Context, providerKey string, userIDs []string, limit int) ([]GroupUserMembership, bool, error)
	// GroupMembers returns the direct User members of the groups ordered by (group id, user id).
	GroupMembers(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupUserMembership, bool, error)
	// UsersWithIdentity returns the users that currently have a (not deleted) directory identity of the provider.
	UsersWithIdentity(ctx context.Context, providerKey string, userIDs []string) ([]string, error)
}

// DirectoryGraph answers read-only graph questions for other modules (endpoint management views).
// Malformed ids are ignored, never errors. Every lookup is limited to the groups and identities of one provider
// key, so external ids of different providers cannot be confused.
type DirectoryGraph struct{ store DirectoryGraphStore }

func NewDirectoryGraph(store DirectoryGraphStore) *DirectoryGraph {
	return &DirectoryGraph{store: store}
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > MaxGraphRows {
		return MaxGraphRows
	}
	return limit
}

func (g *DirectoryGraph) GroupsByExternalIDs(ctx context.Context, providerKey string, externalIDs []string, limit int) ([]DirectoryGroupRef, bool, error) {
	if providerKey == "" || len(externalIDs) == 0 {
		return nil, false, nil
	}
	return g.store.GroupsByExternalIDs(ctx, providerKey, externalIDs, clampLimit(limit))
}

func (g *DirectoryGraph) GroupsByIDs(ctx context.Context, providerKey string, ids []string, limit int) ([]DirectoryGroupRef, bool, error) {
	valid := validIDs(ids)
	if providerKey == "" || len(valid) == 0 {
		return nil, false, nil
	}
	return g.store.GroupsByIDs(ctx, providerKey, valid, clampLimit(limit))
}

func (g *DirectoryGraph) NestingUp(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupNestingEdge, bool, error) {
	valid := validIDs(groupIDs)
	if providerKey == "" || len(valid) == 0 {
		return nil, false, nil
	}
	return g.store.NestingUp(ctx, providerKey, valid, clampLimit(limit))
}

func (g *DirectoryGraph) NestingDown(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupNestingEdge, bool, error) {
	valid := validIDs(groupIDs)
	if providerKey == "" || len(valid) == 0 {
		return nil, false, nil
	}
	return g.store.NestingDown(ctx, providerKey, valid, clampLimit(limit))
}

func (g *DirectoryGraph) UserMemberships(ctx context.Context, providerKey string, userIDs []string, limit int) ([]GroupUserMembership, bool, error) {
	valid := validIDs(userIDs)
	if providerKey == "" || len(valid) == 0 {
		return nil, false, nil
	}
	return g.store.UserMemberships(ctx, providerKey, valid, clampLimit(limit))
}

func (g *DirectoryGraph) GroupMembers(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]GroupUserMembership, bool, error) {
	valid := validIDs(groupIDs)
	if providerKey == "" || len(valid) == 0 {
		return nil, false, nil
	}
	return g.store.GroupMembers(ctx, providerKey, valid, clampLimit(limit))
}

// UsersWithIdentity returns the set of users that have a current directory identity of the provider. A User
// without one has no synced membership data: their memberships are unknown, not empty.
func (g *DirectoryGraph) UsersWithIdentity(ctx context.Context, providerKey string, userIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	valid := validIDs(userIDs)
	if providerKey == "" || len(valid) == 0 {
		return out, nil
	}
	found, err := g.store.UsersWithIdentity(ctx, providerKey, valid)
	if err != nil {
		return nil, err
	}
	for _, id := range found {
		out[id] = true
	}
	return out, nil
}
