package authentication

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Consumer-side contracts of login. Implementations live in modules and
// integrations (platform must not import them); main wires them.

// Password verification outcomes.
var (
	// ErrInvalidCredentials means the directory rejected the password (or the
	// request carried none).
	ErrInvalidCredentials = errors.New("authentication: invalid credentials")
	// ErrProviderUnavailable means the identity provider could not be reached
	// or answered unexpectedly; the credentials were not judged.
	ErrProviderUnavailable = errors.New("authentication: identity provider unavailable")
	// ErrUserInactive means the user is unknown or not active at the moment a
	// login session would be created.
	ErrUserInactive = errors.New("authentication: user is not active")
	// ErrInvalidTicket means a Kerberos ticket was malformed, expired,
	// replayed, for another realm or service, or otherwise not acceptable. It
	// is the only ticket error callers distinguish; details never leave the
	// validator.
	ErrInvalidTicket = errors.New("authentication: invalid kerberos ticket")
)

// DirectoryAccount is the synced account a login identifier resolves to.
type DirectoryAccount struct {
	UserID            string
	DistinguishedName string
	// Username is the synced directory username as stored.
	Username string
}

// AccountDirectory resolves a login identifier (username, DOMAIN\user or
// primary email) to exactly one enabled directory account of an active user.
// Anything else, including ambiguity, is "not found" without an error.
type AccountDirectory interface {
	FindDirectoryAccount(ctx context.Context, providerKey, identifier string) (DirectoryAccount, bool, error)
}

// UserLocker locks a user row in tx (FOR SHARE) so that session creation
// serializes with concurrent status changes, and reports whether the user is
// active. The lock is held until tx ends.
type UserLocker interface {
	LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
}

// PasswordVerifier checks a password against the identity provider by binding
// as the account's distinguished name. It returns nil, ErrInvalidCredentials
// or ErrProviderUnavailable (possibly wrapped). An empty password must be
// refused before any network access.
type PasswordVerifier interface {
	VerifyPassword(ctx context.Context, dn, password string) error
}

// KerberosPrincipal is the authenticated client of a validated Kerberos
// ticket: the single-component user name (no realm, no instance) and the
// realm, already checked against the configured realm.
type KerberosPrincipal struct {
	Username string
	Realm    string
}

// KerberosValidator validates the raw SPNEGO/Kerberos token of a
// `Authorization: Negotiate` header against the service keytab. It returns
// ErrInvalidTicket (possibly wrapped) for every problem with the ticket and
// any other error for failures of the validator itself. Implementations must
// not include ticket contents in errors.
type KerberosValidator interface {
	Validate(ctx context.Context, token []byte) (KerberosPrincipal, error)
}
