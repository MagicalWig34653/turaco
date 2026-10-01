package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Built-in role key. The role implicitly holds every registered permission,
// has no role_permissions rows and cannot be changed or deleted.
const AdministratorRoleKey = "platform-administrator"

const (
	SubjectUser           = "user"
	SubjectDirectoryGroup = "directory_group"
	ScopeGlobal           = "global"

	DefaultLimit = 50
	MaxLimit     = 200
)

// GroupResolver returns the Directory Groups a User currently belongs to,
// including groups reached through currently observed nesting. It is
// implemented by organization/public; platform code must not import modules.
type GroupResolver interface {
	GroupIDsOfUser(ctx context.Context, userID string) ([]string, error)
}

// SubjectDirectory answers questions about assignment subjects without
// touching Organization storage. Implemented by organization/public.
type SubjectDirectory interface {
	UserExists(ctx context.Context, id string) (bool, error)
	// DirectoryGroupObserved reports whether the group exists and is not
	// marked deleted.
	DirectoryGroupObserved(ctx context.Context, id string) (bool, error)
	// DisplayNames returns id -> current display name; unknown ids are absent.
	DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error)
	// FindUser resolves a uuid, username (unique across providers) or primary
	// email to a User id.
	FindUser(ctx context.Context, ref string) (id string, found bool, err error)
}

var (
	ErrNotFound            = errors.New("authorization: not found")
	ErrSubjectNotFound     = errors.New("authorization: subject not found")
	ErrUnknownPermission   = errors.New("authorization: unknown permission")
	ErrDuplicateKey        = errors.New("authorization: duplicate role key")
	ErrBuiltInRole         = errors.New("authorization: built-in role cannot be changed")
	ErrRoleInUse           = errors.New("authorization: role has active assignments")
	ErrDuplicateAssignment = errors.New("authorization: duplicate active assignment")
	ErrLastAdministrator   = errors.New("authorization: last administrator assignment")
	ErrInvalidCursor       = errors.New("authorization: invalid cursor")
)

// InvalidError is a validation failure whose message is safe to show.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "authorization: invalid request: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// UnknownPermissionError carries the offending names. It matches
// ErrUnknownPermission.
type UnknownPermissionError struct{ Names []string }

func (e *UnknownPermissionError) Error() string {
	return "authorization: unknown permission: " + strings.Join(e.Names, ", ")
}
func (e *UnknownPermissionError) Is(target error) bool { return target == ErrUnknownPermission }

// Role is a named set of registered permissions.
type Role struct {
	ID                string
	Key               string
	Name              string
	Description       string
	BuiltIn           bool
	Permissions       []string // sorted effective permissions
	ActiveAssignments int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Assignment grants a Role to a User or Directory Group (global scope).
type Assignment struct {
	ID                 string
	RoleID             string
	RoleKey            string
	SubjectType        string
	SubjectID          string
	SubjectDisplayName string
	Scope              string
	CreatedAt          time.Time
	CreatedBy          string // User id; empty for CLI
	RevokedAt          *time.Time
	RevokedBy          string // User id; empty for CLI or when active
}

// Page is keyset pagination (descending id). Cursor is the last id of the
// previous page.
type Page struct {
	Limit  int
	Cursor string
}

// Normalize applies default and maximum limit.
func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// AssignmentFilter narrows ListAssignments. Empty fields do not filter.
type AssignmentFilter struct {
	RoleID         string
	SubjectType    string
	SubjectID      string
	IncludeRevoked bool
	Page           Page
}

// AssignmentPage is one page of assignments.
type AssignmentPage struct {
	Items      []Assignment
	NextCursor string
}

// Actor identifies who performs a mutation: a session User, or the CLI
// (actor id NULL, metadata carries the CLI marker).
type Actor struct {
	UserID        string
	CLI           json.RawMessage // e.g. {"actor":"cli","osUser":"root"}
	CorrelationID string          // generated when empty
}

// UserActor is the Actor of an authenticated request.
func UserActor(userID, correlationID string) Actor {
	return Actor{UserID: userID, CorrelationID: correlationID}
}

// CLIActor is the Actor of a turaco-admin invocation.
func CLIActor(marker json.RawMessage) Actor { return Actor{CLI: marker} }

func (a Actor) validate() error {
	switch {
	case a.UserID != "" && len(a.CLI) > 0:
		return errors.New("authorization: actor must be a user or the CLI, not both")
	case a.UserID != "":
		if !uuidPattern.MatchString(a.UserID) {
			return errors.New("authorization: actor user id must be a UUID")
		}
	case len(a.CLI) > 0:
		var m map[string]any
		if err := json.Unmarshal(a.CLI, &m); err != nil || len(m) == 0 {
			return errors.New("authorization: CLI actor must be a non-empty JSON object")
		}
	default:
		return errors.New("authorization: actor is required")
	}
	return nil
}

// ref is the created_by/revoked_by JSON.
func (a Actor) ref() json.RawMessage {
	if a.UserID != "" {
		b, _ := json.Marshal(map[string]string{"userId": a.UserID})
		return b
	}
	return a.CLI
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	keyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
)

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 200 || !validText(name) {
		return "", invalid("name must be 1 to 200 characters")
	}
	return name, nil
}

func validateDescription(d string) error {
	if utf8.RuneCountInString(d) > 2000 || !validText(d) {
		return invalid("description must be at most 2000 characters")
	}
	return nil
}
