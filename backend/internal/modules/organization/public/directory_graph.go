package public

import "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"

// Directory graph contract: read-only questions about Directory Groups, their nesting and User
// memberships for modules that evaluate group targeting (endpoint management views). Only currently
// observed intervals, non-deleted groups and the groups of the given provider key are returned; every lookup
// is bounded and reports truncation: a list method returns (rows, truncated, error), and truncated=true means the
// answer is incomplete (row limit or nesting depth), so callers must not present derived results as certain.
// UsersWithIdentity tells which Users have synced directory data at all.
type (
	DirectoryGraph      = application.DirectoryGraph
	DirectoryGroupRef   = application.DirectoryGroupRef
	GroupNestingEdge    = application.GroupNestingEdge
	GroupUserMembership = application.GroupUserMembership
)

// NewDirectoryGraph wraps the Organization repository (or any store).
func NewDirectoryGraph(store application.DirectoryGraphStore) *DirectoryGraph {
	return application.NewDirectoryGraph(store)
}

// MaxGraphRows is the bound of every DirectoryGraph lookup; MaxNestingDepth the depth of the nesting walks.
const (
	MaxGraphRows    = application.MaxGraphRows
	MaxNestingDepth = application.MaxNestingDepth
)
