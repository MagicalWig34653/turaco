package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Login account operations: resolving a login identifier to a synced
// directory account, serializing session creation with user status changes,
// and creating the local emergency User. docs/security/identity-access-design.md §6, §7.

// ErrInvalidLocalUser means the display name of a local user is unusable.
var ErrInvalidLocalUser = errors.New("organization: invalid local user")

const maxDisplayNameLength = 200

// DirectoryAccount is a synced account a login identifier resolved to.
type DirectoryAccount struct {
	UserID            string
	DistinguishedName string
}

// LocalUserInsert is the input of LoginAccountStore.InsertLocalUser.
type LocalUserInsert struct {
	DisplayName   string
	CorrelationID string
	// Actor is the JSON object recorded as audit metadata.
	Actor json.RawMessage
	At    time.Time
}

// LoginAccountStore is the persistence port of login accounts. Operations that
// take a transaction run inside the caller's transaction.
type LoginAccountStore interface {
	// FindDirectoryAccounts returns at most two matching accounts of enabled,
	// non-deleted identities with a distinguished name whose User is active.
	// by is "username" or "email"; value is matched case-insensitively.
	FindDirectoryAccounts(ctx context.Context, providerKey, by, value string) ([]DirectoryAccount, error)
	// LockActiveUser takes a FOR SHARE lock on the user row and reports
	// whether its status is active.
	LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
	// InsertLocalUser creates an active User with status_source "platform"
	// and no external identity, and audits organization.user.created_local.
	InsertLocalUser(ctx context.Context, tx pgx.Tx, in LocalUserInsert) (string, error)
}

// LoginAccounts implements the Organization side of login.
type LoginAccounts struct {
	store LoginAccountStore
	now   func() time.Time
}

func NewLoginAccounts(store LoginAccountStore, now func() time.Time) *LoginAccounts {
	if now == nil {
		now = time.Now
	}
	return &LoginAccounts{store: store, now: now}
}

// FindDirectoryAccount resolves identifier: "DOMAIN\user" matches the
// username "user"; an identifier containing "@" matches the primary email;
// anything else matches the username. Exactly one match is required;
// no match and ambiguity both report not found.
func (l *LoginAccounts) FindDirectoryAccount(ctx context.Context, providerKey, identifier string) (DirectoryAccount, bool, error) {
	by, value := parseLoginIdentifier(identifier)
	if providerKey == "" || value == "" {
		return DirectoryAccount{}, false, nil
	}
	accounts, err := l.store.FindDirectoryAccounts(ctx, providerKey, by, value)
	if err != nil {
		return DirectoryAccount{}, false, fmt.Errorf("find directory account: %w", err)
	}
	if len(accounts) != 1 || accounts[0].DistinguishedName == "" {
		return DirectoryAccount{}, false, nil
	}
	return accounts[0], true, nil
}

func parseLoginIdentifier(identifier string) (by, value string) {
	identifier = strings.TrimSpace(identifier)
	if i := strings.IndexByte(identifier, '\\'); i >= 0 {
		return "username", strings.TrimSpace(identifier[i+1:])
	}
	if strings.Contains(identifier, "@") {
		return "email", identifier
	}
	return "username", identifier
}

// LockActiveUser locks the user row FOR SHARE in tx and reports whether the
// user is active. Directory sync locks the same row FOR UPDATE when it changes
// the status, so a session cannot be created for a user that is being
// deactivated.
func (l *LoginAccounts) LockActiveUser(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	return l.store.LockActiveUser(ctx, tx, userID)
}

// CreateEmergencyUser creates the User of an emergency (break-glass) account
// in tx: status active, status_source platform, no directory identity. It is
// audited as organization.user.created_local with actor as metadata (the CLI
// passes {"actor":"cli",...}).
func (l *LoginAccounts) CreateEmergencyUser(ctx context.Context, tx pgx.Tx, displayName, correlationID string, actor json.RawMessage) (string, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" || utf8.RuneCountInString(displayName) > maxDisplayNameLength {
		return "", fmt.Errorf("%w: display name must be 1-%d characters", ErrInvalidLocalUser, maxDisplayNameLength)
	}
	for _, r := range displayName {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: display name must not contain control characters", ErrInvalidLocalUser)
		}
	}
	return l.store.InsertLocalUser(ctx, tx, LocalUserInsert{
		DisplayName: displayName, CorrelationID: correlationID, Actor: actor, At: l.now(),
	})
}
