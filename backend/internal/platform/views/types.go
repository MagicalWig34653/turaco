// Package views implements Saved Views, View Shares, Pins and Pin Rules (ADR-0033, F13 slice Q-B).
//
// A View stores a resource key, a Filter AST of platform/query, a sort and the visible columns. It never stores
// results and never carries authorization: results are always evaluated as the viewer, through the owning
// module's own query endpoint, so the viewer's resource permission, row scope and field redaction apply exactly as
// for a direct query. A Share only decides who may see and use the View itself; it never grants access to data.
//
// The package imports no business module (make archcheck). Modules enter through three ports the composition
// root fills: the Runner (runs a query as the viewer), the Directory (Teams, Directory Groups and Users) and the
// ModuleGate (module switches of ADR-0032).
package views

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Permissions of the views platform. Using Views (creating private ones, pinning, running shared ones) needs only a
// signed-in User plus the permission to read the View's resource.
const (
	PermShare        = "views.share"
	PermPublish      = "views.publish"
	PermPinForGroups = "views.pin_for_groups"
	PermAdmin        = "views.admin"
)

// Share levels and subject types.
const (
	LevelUse  = "use"
	LevelEdit = "edit"

	SubjectUser     = "user"
	SubjectTeam     = "team"
	SubjectRole     = "role"
	SubjectEveryone = "everyone"

	VisibilityPrivate = "private"
	VisibilityShared  = "shared"
)

// Access is the viewer's relation to one View.
const (
	AccessOwner = "owner"
	AccessEdit  = "edit"
	AccessUse   = "use"
	// AccessAdmin is held by views.admin without any share: metadata and lifecycle operations, never execution.
	AccessAdmin = "admin"
)

// Limits. They bound storage and the cost of resolving shares.
const (
	MaxViewsPerOwner  = 200
	MaxSharesPerView  = 50
	MaxPinsPerUser    = 100
	MaxRulesPerView   = 50
	MaxColumns        = 40
	MaxNameLength     = 80
	MaxDescription    = 500
	MaxListPage       = 100
	DefaultListPage   = 50
	ArchiveRetention  = 90 * 24 * time.Hour
	purgeBatch        = 200
	schemaVersion     = 1
	maxCollapsedGroup = 20
)

// Errors. The transport maps them to stable API codes.
var (
	// ErrNotFound answers every request for a View the caller may not know about: unknown, private to someone else,
	// archived, of an unusable resource, or owned by a deactivated User. Nothing distinguishes these cases.
	ErrNotFound = errors.New("views: not found")
	// ErrForbidden: the caller may see the View but not perform the operation.
	ErrForbidden = errors.New("views: not permitted")
	// ErrConflict: the expected version is stale.
	ErrConflict = errors.New("views: version conflict")
	// ErrArchived: the View is archived (only its owner can see that).
	ErrArchived = errors.New("views: archived")
	// ErrModuleDisabled: the module owning the resource is switched off (ADR-0032).
	ErrModuleDisabled = errors.New("views: module disabled")
	// ErrLimitReached: a per-owner, per-View or per-user limit.
	ErrLimitReached = errors.New("views: limit reached")
	// ErrNameTaken: the owner already has an active View with this name for the resource.
	ErrNameTaken = errors.New("views: name taken")
	// ErrSubjectNotFound: a share or pin-rule subject (User, Team, role) does not exist or is inactive.
	ErrSubjectNotFound = errors.New("views: subject not found")
)

// InvalidError is a validation failure whose message is safe to show.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "views: invalid request: " + e.Message }

func invalid(msg string) error { return &InvalidError{Message: msg} }

// Definition is what a View stores: the Filter AST (with its search and sort) and the visible columns.
type Definition struct {
	Filter  *query.Filter `json:"filter,omitempty"`
	Columns []string      `json:"columns,omitempty"`
}

// View is a stored Saved View.
type View struct {
	ID           string
	Resource     string
	Name         string
	Description  string
	OwnerID      string
	Definition   Definition
	Hash         string
	Visibility   string
	Version      int
	LastEditedBy string
	ArchivedAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Share gives a User, Team, role or everyone access to a View.
type Share struct {
	SubjectType string
	SubjectID   string // empty for everyone
	Level       string
	GrantedBy   string
	GrantedAt   time.Time
}

// ViewInfo is a View as one viewer sees it.
type ViewInfo struct {
	View
	Access        string
	OwnerName     string
	ModuleEnabled bool
	Pinned        bool
	// Shares is set only for the owner and views.admin.
	Shares []Share
}

// PinRule shows a View to the members of a Team or role.
type PinRule struct {
	ID          string
	ViewID      string
	SubjectType string
	SubjectID   string
	GroupKey    string
	Position    int
	CreatedBy   string
	CreatedAt   time.Time
}

// PinEntry is one View in a user's sidebar state.
type PinEntry struct {
	ViewID   string
	Name     string
	Resource string
	GroupKey string
	Position int
	Hidden   bool
	// Source is "user" (the user's own pin or override) or "rule" (shown by a Pin Rule).
	Source string
}

// PinInput is one row of PUT /me/pins.
type PinInput struct {
	ViewID   string
	GroupKey string
	Position int
	Hidden   bool
}

// Caller identifies who acts: the authenticated principal, the request correlation id and the request headers the
// in-process Runner needs to authenticate the follow-up query as the same User (cookies only).
type Caller struct {
	UserID        string
	Permissions   map[string]struct{}
	CorrelationID string
	Header        http.Header
}

// Has reports whether the caller holds the permission.
func (c Caller) Has(permission string) bool {
	_, ok := c.Permissions[permission]
	return ok
}

func (c Caller) hasAny(permissions []string) bool {
	if len(permissions) == 0 {
		return true
	}
	for _, p := range permissions {
		if c.Has(p) {
			return true
		}
	}
	return false
}

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	groupKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	columnPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }

// cleanName trims and validates a View name.
func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > MaxNameLength {
		return "", invalid("The name must have 1 to 80 characters.")
	}
	if !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("The name contains characters that are not allowed.")
	}
	return s, nil
}

func cleanDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxDescription {
		return "", invalid("The description is too long.")
	}
	if !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return "", invalid("The description contains characters that are not allowed.")
	}
	return s, nil
}
