// Package public is the Organization module's contract for other modules.
package public

import (
	"context"
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

// UserAccess answers user questions other modules may ask without touching
// Organization storage.
type UserAccess struct {
	reader application.Reader
}

func NewUserAccess(reader application.Reader) *UserAccess {
	return &UserAccess{reader: reader}
}

var _ authentication.AccountGate = (*UserAccess)(nil)

// IsActive reports whether the user exists, has status "active" and, for an external account, has not reached the
// end of access (ADR-0034 point 5: the end of access is enforced on every request, the expiry job only writes the
// status). An unknown user is not active and is not an error.
func (a *UserAccess) IsActive(ctx context.Context, userID string) (bool, error) {
	st, err := a.Account(ctx, userID)
	return st.Active, err
}

// Account answers "may this user hold a session" and "is this a restricted external account" in one read.
func (a *UserAccess) Account(ctx context.Context, userID string) (authentication.AccountState, error) {
	u, err := a.reader.GetUser(ctx, userID)
	if errors.Is(err, application.ErrNotFound) {
		return authentication.AccountState{}, nil
	}
	if err != nil {
		return authentication.AccountState{}, err
	}
	external := u.AccountKind == application.AccountKindExternal
	active := u.Status == "active"
	if external && (u.AccessExpiresAt == nil || !u.AccessExpiresAt.After(time.Now())) {
		active = false
	}
	return authentication.AccountState{Active: active, External: external}, nil
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
