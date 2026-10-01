package public

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

// ErrInvalidLocalUser is returned by CreateEmergencyUser for an unusable
// display name.
var ErrInvalidLocalUser = application.ErrInvalidLocalUser

// LoginAccounts is the Organization contract of authentication. It implements
// authentication.AccountDirectory and authentication.UserLocker and creates
// the User of the emergency account (authentication.EmergencyUserCreator).
type LoginAccounts struct {
	app *application.LoginAccounts
}

var (
	_ authentication.AccountDirectory     = (*LoginAccounts)(nil)
	_ authentication.UserLocker           = (*LoginAccounts)(nil)
	_ authentication.EmergencyUserCreator = (*LoginAccounts)(nil)
)

// NewLoginAccounts creates the contract on top of the Organization
// repository. now may be nil.
func NewLoginAccounts(store application.LoginAccountStore, now func() time.Time) *LoginAccounts {
	return &LoginAccounts{app: application.NewLoginAccounts(store, now)}
}

// FindDirectoryAccount implements authentication.AccountDirectory.
func (l *LoginAccounts) FindDirectoryAccount(ctx context.Context, providerKey, identifier string) (authentication.DirectoryAccount, bool, error) {
	a, ok, err := l.app.FindDirectoryAccount(ctx, providerKey, identifier)
	if err != nil || !ok {
		return authentication.DirectoryAccount{}, false, err
	}
	return authentication.DirectoryAccount{UserID: a.UserID, DistinguishedName: a.DistinguishedName, Username: a.Username}, true, nil
}

// LockActiveUser implements authentication.UserLocker.
func (l *LoginAccounts) LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	return l.app.LockActiveUser(ctx, tx, userID)
}

// CreateEmergencyUser creates the User of an emergency account inside tx and
// returns its id. It is audited as organization.user.created_local with actor.
// It implements authentication.EmergencyUserCreator.
func (l *LoginAccounts) CreateEmergencyUser(ctx context.Context, tx pgx.Tx, displayName, correlationID string, actor audit.Actor) (string, error) {
	return l.app.CreateEmergencyUser(ctx, tx, displayName, correlationID, actor)
}
