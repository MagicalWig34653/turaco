// Package application holds Organization read use cases and their ports.
package application

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("organization: not found")

// ErrInvalidCursor is returned for a malformed pagination cursor.
var ErrInvalidCursor = errors.New("organization: invalid cursor")

// Page is keyset pagination over the UUIDv7 primary key (ascending).
// Cursor is the last ID of the previous page; empty means the first page.
type Page struct {
	Limit  int
	Cursor string
}

// Normalize applies the default and maximum limit.
func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// Result is one page. NextCursor is empty on the last page.
type Result[T any] struct {
	Items      []T
	NextCursor string
}

type User struct {
	ID                string
	DisplayName       string
	GivenName         *string
	FamilyName        *string
	PrimaryEmail      *string
	Status            string
	DepartmentID      *string
	PrimaryLocationID *string
	ManagerUserID     *string
	UpdatedAt         time.Time

	// F14 fields. Origin says who owns the profile attributes: "directory" (synchronized, read-only here),
	// "local" (created in Turaco) or "emergency" (the CLI-only break-glass account).
	EmployeeNumber  *string
	StatusSource    string
	AccountKind     string
	Origin          string
	AccessExpiresAt *time.Time
	Version         int
}

type Team struct {
	ID          string
	Name        string
	Active      bool
	UpdatedAt   time.Time
	Description string
	Version     int
	// MemberCount is the number of current members. It is filled by the list and query reads only (nil elsewhere).
	MemberCount *int
}

// UserTeam is one current Team membership of a User (valid_from <= now < valid_until).
type UserTeam struct {
	TeamID    string
	TeamName  string
	Active    bool
	Role      *string
	Source    string
	ValidFrom time.Time
}

// TeamMember is a current membership (valid_from <= now < valid_until).
type TeamMember struct {
	UserID      string
	DisplayName string
	Role        *string
	Source      string
	ValidFrom   time.Time
}

type Location struct {
	ID          string
	Name        string
	ExternalKey *string
	Active      bool
	UpdatedAt   time.Time

	Kind        string
	ParentID    *string
	Code        *string
	Description string
	Version     int
}

// Department is an organizational unit; Departments form a tree.
type Department struct {
	ID          string
	Name        string
	Code        *string
	ParentID    *string
	ExternalKey *string
	Active      bool
	UpdatedAt   time.Time
	Version     int
}

// DirectoryGroup is a group observed from an external directory. It is not a Team.
type DirectoryGroup struct {
	ID                string
	ProviderKey       string
	ExternalID        string
	DisplayName       string
	Description       *string
	FirstObservedAt   time.Time
	LastObservedAt    time.Time
	DeletedObservedAt *time.Time
}

// DirectoryGroupMember is a currently observed user membership with freshness.
type DirectoryGroupMember struct {
	UserID         string
	DisplayName    string
	ObservedFrom   time.Time
	LastObservedAt time.Time
}

// ExternalIdentity is an external account (directory or Entra) linked to a User. The subject and distinguished name
// are deliberately not exposed; only the last four characters of the subject identify it to an administrator.
type ExternalIdentity struct {
	ID            string
	ProviderKey   string
	SubjectSuffix string
	CreatedAt     time.Time
	// LinkedVia is administrator, source_anchor, provisioning or cli for Entra identities; empty otherwise.
	LinkedVia         string
	Username          *string
	Enabled           bool
	LastSeenAt        *time.Time
	DeletedObservedAt *time.Time
}

// DirectorySyncRun is one directory synchronization execution.
type DirectorySyncRun struct {
	ID          string
	ProviderKey string
	// JobID is the platform job that executed the run; nil when unknown.
	JobID         *string
	Trigger       string
	StartedAt     time.Time
	ObservedAt    *time.Time
	FinishedAt    *time.Time
	Outcome       string
	Counts        map[string]int
	Conflicts     []SyncConflict
	ConflictCount int
	Error         *string
}

// SyncConflict is a skipped or unresolved item of a run. It never carries
// attribute values beyond the directory username.
type SyncConflict struct {
	Kind       string
	ExternalID string
	Username   string
}

// RunFilter filters directory sync runs. Runs are paginated newest first:
// Cursor is the last (smallest) ID of the previous page.
type RunFilter struct {
	ProviderKey string
	Page
}

// DirectorySyncRequester enqueues a manual directory sync and audits the request.
type DirectorySyncRequester interface {
	// RequestDirectorySync enqueues a deduplicated manual sync job for
	// providerKey and audits the request with actorUserID, atomically.
	// correlationID is the HTTP request ID and becomes the audit event's
	// correlation ID (the job ID when empty). created is false when an
	// active job already exists.
	RequestDirectorySync(ctx context.Context, actorUserID, providerKey, correlationID string) (jobID string, created bool, err error)
}

type UserFilter struct {
	Query  string // case-insensitive display-name prefix; max 100 chars
	Status string // optional exact status
	Page
}

type NameFilter struct {
	Query string // case-insensitive name prefix; max 100 chars
	Page
}

// Reader is the read port implemented by the repository package.
// Repositories return ErrNotFound / ErrInvalidCursor; Page must be Normalize()d by the caller.
type Reader interface {
	ListUsers(ctx context.Context, f UserFilter) (Result[User], error)
	GetUser(ctx context.Context, id string) (User, error)
	// ListUserExternalIdentities returns the directory accounts of a user ordered by provider key and creation.
	ListUserExternalIdentities(ctx context.Context, userID string) ([]ExternalIdentity, error)
	ListTeams(ctx context.Context, f NameFilter) (Result[Team], error)
	GetTeam(ctx context.Context, id string) (Team, error)
	ListTeamMembers(ctx context.Context, teamID string, p Page) (Result[TeamMember], error)
	// ListUserTeams pages the current Teams of a User by Team id. An unknown User yields ErrNotFound.
	ListUserTeams(ctx context.Context, userID string, p Page) (Result[UserTeam], error)
	// ListTeamLeads returns the current leads of a Team (at most 50).
	ListTeamLeads(ctx context.Context, teamID string) ([]TeamMember, error)
	ListLocations(ctx context.Context, f NameFilter) (Result[Location], error)
	GetLocation(ctx context.Context, id string) (Location, error)
	ListDepartments(ctx context.Context, f NameFilter) (Result[Department], error)
	GetDepartment(ctx context.Context, id string) (Department, error)
	ListDirectoryGroups(ctx context.Context, f NameFilter) (Result[DirectoryGroup], error)
	GetDirectoryGroup(ctx context.Context, id string) (DirectoryGroup, error)
	// ListDirectoryGroupMembers returns only currently observed members.
	ListDirectoryGroupMembers(ctx context.Context, groupID string, p Page) (Result[DirectoryGroupMember], error)
	// ListDirectorySyncRuns pages newest first (descending id).
	ListDirectorySyncRuns(ctx context.Context, f RunFilter) (Result[DirectorySyncRun], error)
	GetDirectorySyncRun(ctx context.Context, id string) (DirectorySyncRun, error)
}
