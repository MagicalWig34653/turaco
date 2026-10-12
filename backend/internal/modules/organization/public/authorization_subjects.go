package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// AuthorizationSubjects is the Organization contract for platform/authorization:
// it implements roles.GroupResolver and roles.SubjectDirectory so the platform
// never reads Organization storage. FindUser is for in-repository callers
// (turaco-admin) and deliberately not part of roles.SubjectDirectory.
type AuthorizationSubjects struct {
	svc *application.AuthorizationSubjects
}

var (
	_ roles.GroupResolver    = (*AuthorizationSubjects)(nil)
	_ roles.SubjectDirectory = (*AuthorizationSubjects)(nil)
	_ roles.LocationTree     = (*AuthorizationSubjects)(nil)
)

// NewAuthorizationSubjects wraps the application service; store is normally
// the organization repository.
func NewAuthorizationSubjects(store application.AuthorizationSubjectStore) *AuthorizationSubjects {
	return &AuthorizationSubjects{svc: application.NewAuthorizationSubjects(store)}
}

// GroupIDsOfUser returns the ids of the non-deleted Directory Groups the User
// belongs to, directly or through observed nesting.
func (s *AuthorizationSubjects) GroupIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	return s.svc.GroupIDsOfUser(ctx, userID)
}

// SubtreeIDs is the LocationTree contract (ADR-0040): the given Locations and all their descendants in the current
// tree, active or inactive, sorted and without duplicates. Unknown ids contribute nothing. Consumers only see id
// sets; how Organization stores or walks the tree is not part of the contract.
func (s *AuthorizationSubjects) SubtreeIDs(ctx context.Context, rootIDs []string) ([]string, error) {
	return s.svc.SubtreeIDs(ctx, rootIDs)
}

// IsLocalOriginAccount reports whether the User is a local account by origin.
func (s *AuthorizationSubjects) IsLocalOriginAccount(ctx context.Context, userID string) (bool, error) {
	return s.svc.IsLocalOriginAccount(ctx, userID)
}

// IsExternalAccount reports whether the User is an external account (a restricted principal, ADR-0034).
func (s *AuthorizationSubjects) IsExternalAccount(ctx context.Context, userID string) (bool, error) {
	return s.svc.IsExternalAccount(ctx, userID)
}

// GroupMemberUserIDs returns the Users currently in any of the groups, directly or through observed nesting.
func (s *AuthorizationSubjects) GroupMemberUserIDs(ctx context.Context, groupIDs []string, limit int) ([]string, error) {
	return s.svc.GroupMemberUserIDs(ctx, groupIDs, limit)
}

// UserExists reports whether the User exists.
func (s *AuthorizationSubjects) UserExists(ctx context.Context, id string) (bool, error) {
	return s.svc.UserExists(ctx, id)
}

// DirectoryGroupObserved reports whether the group exists and is not marked deleted.
func (s *AuthorizationSubjects) DirectoryGroupObserved(ctx context.Context, id string) (bool, error) {
	return s.svc.DirectoryGroupObserved(ctx, id)
}

// DisplayNames returns id -> current display name; unknown ids are absent.
func (s *AuthorizationSubjects) DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error) {
	return s.svc.DisplayNames(ctx, userIDs, groupIDs)
}

// ActiveUsers returns id -> true for each id that is a User with status "active".
func (s *AuthorizationSubjects) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	return s.svc.ActiveUsers(ctx, ids)
}

// FindUser resolves a UUID, a username that is unique across providers, or a
// primary email to a User id. Ambiguous or unknown references are not found.
func (s *AuthorizationSubjects) FindUser(ctx context.Context, ref string) (string, bool, error) {
	return s.svc.FindUser(ctx, ref)
}
