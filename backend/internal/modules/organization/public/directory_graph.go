package public

import "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"

// Directory graph contract: read-only questions about Directory Groups, their nesting and User
// memberships for modules that evaluate group targeting (endpoint management views). Only currently
// observed intervals and non-deleted groups are returned; every lookup is bounded.
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

// MaxGraphRows is the bound of every DirectoryGraph lookup.
const MaxGraphRows = application.MaxGraphRows
