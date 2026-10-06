// Package public is the Organization module's contract for other modules.
package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// UserAccess answers user questions other modules may ask without touching
// Organization storage.
type UserAccess struct {
	reader application.Reader
}

func NewUserAccess(reader application.Reader) *UserAccess {
	return &UserAccess{reader: reader}
}

// IsActive reports whether the user exists and has status "active". An unknown
// user is not active and is not an error.
func (a *UserAccess) IsActive(ctx context.Context, userID string) (bool, error) {
	u, err := a.reader.GetUser(ctx, userID)
	if errors.Is(err, application.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return u.Status == "active", nil
}

// SessionNames returns the user's own display name and given name for the
// session response. An unknown user has no names and is not an error.
func (a *UserAccess) SessionNames(ctx context.Context, userID string) (displayName, givenName string, err error) {
	u, err := a.reader.GetUser(ctx, userID)
	if errors.Is(err, application.ErrNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if u.GivenName != nil {
		givenName = *u.GivenName
	}
	return u.DisplayName, givenName, nil
}
