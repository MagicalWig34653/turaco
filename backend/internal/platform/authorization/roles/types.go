// Package roles implements Roles, Role assignments and the effective
// permission evaluation behind authorization.Principal. See
// docs/security/identity-access-design.md sections 5 and 9.
package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
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
	GroupResolver
	// GroupMemberUserIDs returns the ids of the Users that currently belong to any of the groups, directly or
	// through observed nesting (child group into parent group), at most limit, ordered by id.
	GroupMemberUserIDs(ctx context.Context, groupIDs []string, limit int) ([]string, error)
	UserExists(ctx context.Context, id string) (bool, error)
	// DirectoryGroupObserved reports whether the group exists and is not
	// marked deleted.
	DirectoryGroupObserved(ctx context.Context, id string) (bool, error)
	// DisplayNames returns id -> current display name; unknown ids are absent.
	DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error)
	// ActiveUsers returns the subset of ids that are Users with status
	// "active"; unknown and non-active Users are absent (value false or
	// missing). The last-administrator guard counts only these.
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
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

	// Escalation guards (F14 section 2.5). Each maps to one API error code.
	ErrGrantExceedsHolder         = errors.New("authorization: grant exceeds the permissions the actor holds")
	ErrHighRiskNeedsAdministrator = errors.New("authorization: high-risk permissions need a platform administrator")
	ErrSelfAssignment             = errors.New("authorization: nobody changes their own assignments or the roles they hold")
	ErrRoleNotHeld                = errors.New("authorization: the actor does not hold the role")
	ErrAdminNoExpiry              = errors.New("authorization: the administrator role is never time-limited")
	ErrLocalAccountHighRisk       = errors.New("authorization: local accounts cannot hold high-risk permissions")
	ErrVersionConflict            = errors.New("authorization: version conflict")
	ErrUnknownTemplate            = errors.New("authorization: unknown role template")
)

// SoDRequiredError reports separation-of-duties rules the change would violate and the caller has not
// acknowledged. It matches ErrSoDAcknowledgementRequired.
type SoDRequiredError struct{ Rules []string }

// ErrSoDAcknowledgementRequired is the sentinel of SoDRequiredError.
var ErrSoDAcknowledgementRequired = errors.New("authorization: separation-of-duties acknowledgement required")

func (e *SoDRequiredError) Error() string {
	return "authorization: separation-of-duties acknowledgement required: " + strings.Join(e.Rules, ", ")
}
func (e *SoDRequiredError) Is(target error) bool { return target == ErrSoDAcknowledgementRequired }

// Acknowledgement acknowledges separation-of-duties warnings of a change (design section 2.4): the rule keys the
// caller saw and a reason. Warnings never block; they need this to be sent.
type Acknowledgement struct {
	Rules  []string
	Reason string
}

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
	ID          string
	Key         string
	Name        string
	Description string
	BuiltIn     bool
	Permissions []string // sorted effective permissions
	// Version counts changes of name, description and permissions (expectedVersion of the mutations).
	Version int
	// TemplateKey and TemplateVersion record the Role Template a role was created from; a template never updates a role.
	TemplateKey       string
	TemplateVersion   int
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
	// ExpiresAt, when set, ends the assignment: the evaluator ignores it from then on and the job
	// access.expire_assignments revokes it.
	ExpiresAt *time.Time
	CreatedAt time.Time
	CreatedBy string // User id; empty for CLI
	RevokedAt *time.Time
	RevokedBy string // User id; empty for CLI or when active
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

// validateActor checks the audit actor and the correlation id of a mutation.
// Sessions pass audit.UserActor (the id must be a UUID); the CLI passes
// audit.CLIActor.
func validateActor(a audit.Actor, correlationID string) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.UserID != "" && !uuidPattern.MatchString(a.UserID) {
		return errors.New("authorization: actor user id must be a UUID")
	}
	if correlationID == "" {
		return errors.New("authorization: correlation id is required")
	}
	return nil
}

// actorRef is the created_by/revoked_by JSON: {"userId": ...} for a User,
// {"actor": ..., "osUser": ...} for a system actor.
func actorRef(a audit.Actor) json.RawMessage {
	m := map[string]string{}
	if a.UserID != "" {
		m["userId"] = a.UserID
	} else {
		m["actor"] = a.System
		if a.OSUser != "" {
			m["osUser"] = a.OSUser
		}
	}
	b, _ := json.Marshal(m)
	return b
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
