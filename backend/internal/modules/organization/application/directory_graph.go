package application

import (
	"context"
	"time"
)

// MaxGraphRows bounds every directory-graph lookup; callers learn about truncation from the returned flag.
const MaxGraphRows = 5000

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
// All methods use only currently observed intervals and non-deleted groups. Ids are validated UUIDs.
type DirectoryGraphStore interface {
	// GroupsByExternalIDs returns the live groups with these provider external ids (any provider key).
	GroupsByExternalIDs(ctx context.Context, externalIDs []string) ([]DirectoryGroupRef, error)
	GroupsByIDs(ctx context.Context, ids []string) ([]DirectoryGroupRef, error)
	// NestingUp returns the nesting edges reachable from the groups towards their parents (transitively), at most limit.
	NestingUp(ctx context.Context, groupIDs []string, limit int) ([]GroupNestingEdge, error)
	// NestingDown returns the nesting edges reachable from the groups towards their children (transitively), at most limit.
	NestingDown(ctx context.Context, groupIDs []string, limit int) ([]GroupNestingEdge, error)
	// UserMemberships returns the direct group memberships of the users, at most limit.
	UserMemberships(ctx context.Context, userIDs []string, limit int) ([]GroupUserMembership, error)
	// GroupMembers returns the direct User members of the groups, ordered by user id, at most limit.
	GroupMembers(ctx context.Context, groupIDs []string, limit int) ([]GroupUserMembership, error)
}

// DirectoryGraph answers read-only graph questions for other modules (endpoint management views).
// Malformed ids are ignored, never errors.
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

func (g *DirectoryGraph) GroupsByExternalIDs(ctx context.Context, externalIDs []string) ([]DirectoryGroupRef, error) {
	if len(externalIDs) == 0 {
		return nil, nil
	}
	return g.store.GroupsByExternalIDs(ctx, externalIDs)
}

func (g *DirectoryGraph) GroupsByIDs(ctx context.Context, ids []string) ([]DirectoryGroupRef, error) {
	valid := validIDs(ids)
	if len(valid) == 0 {
		return nil, nil
	}
	return g.store.GroupsByIDs(ctx, valid)
}

func (g *DirectoryGraph) NestingUp(ctx context.Context, groupIDs []string, limit int) ([]GroupNestingEdge, error) {
	valid := validIDs(groupIDs)
	if len(valid) == 0 {
		return nil, nil
	}
	return g.store.NestingUp(ctx, valid, clampLimit(limit))
}

func (g *DirectoryGraph) NestingDown(ctx context.Context, groupIDs []string, limit int) ([]GroupNestingEdge, error) {
	valid := validIDs(groupIDs)
	if len(valid) == 0 {
		return nil, nil
	}
	return g.store.NestingDown(ctx, valid, clampLimit(limit))
}

func (g *DirectoryGraph) UserMemberships(ctx context.Context, userIDs []string, limit int) ([]GroupUserMembership, error) {
	valid := validIDs(userIDs)
	if len(valid) == 0 {
		return nil, nil
	}
	return g.store.UserMemberships(ctx, valid, clampLimit(limit))
}

func (g *DirectoryGraph) GroupMembers(ctx context.Context, groupIDs []string, limit int) ([]GroupUserMembership, error) {
	valid := validIDs(groupIDs)
	if len(valid) == 0 {
		return nil, nil
	}
	return g.store.GroupMembers(ctx, valid, clampLimit(limit))
}
