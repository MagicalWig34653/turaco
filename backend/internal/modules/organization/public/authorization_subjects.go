package public

import (
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// AuthorizationSubjects is the Organization contract for platform/authorization:
// it implements authorization.GroupResolver and authorization.SubjectDirectory
// so the platform never reads Organization storage.
type AuthorizationSubjects struct {
	*application.AuthorizationSubjects
}

var (
	_ authorization.GroupResolver    = (*AuthorizationSubjects)(nil)
	_ authorization.SubjectDirectory = (*AuthorizationSubjects)(nil)
)

// NewAuthorizationSubjects wraps the application service; store is normally
// the organization repository.
func NewAuthorizationSubjects(store application.AuthorizationSubjectStore) *AuthorizationSubjects {
	return &AuthorizationSubjects{application.NewAuthorizationSubjects(store)}
}
