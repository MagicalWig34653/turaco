package authentication

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// EmergencyUserCreator creates the Organization User of an emergency account
// inside tx and returns its id. It is implemented by
// organization/public.LoginAccounts (platform must not import modules; main
// and turaco-admin wire it).
type EmergencyUserCreator interface {
	CreateEmergencyUser(ctx context.Context, tx pgx.Tx, displayName, correlationID string, actor audit.Actor) (string, error)
}

// EmergencyAccounts is the lifecycle of the emergency (break-glass) account:
// create (disabled), change password, enable and disable. Every operation
// runs in one transaction with its audit entries. Callers (turaco-admin) only
// parse input and print results. docs/security/identity-access-design.md §7.
type EmergencyAccounts struct {
	pool  *pgxpool.Pool
	users EmergencyUserCreator
	now   func() time.Time
}

// NewEmergencyAccounts creates the service. now may be nil.
func NewEmergencyAccounts(pool *pgxpool.Pool, users EmergencyUserCreator, now func() time.Time) *EmergencyAccounts {
	if now == nil {
		now = time.Now
	}
	return &EmergencyAccounts{pool: pool, users: users, now: now}
}

func (s *EmergencyAccounts) begin(actor audit.Actor) (credentialAudit, error) {
	if err := actor.Validate(); err != nil {
		return credentialAudit{}, err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return credentialAudit{}, fmt.Errorf("generate correlation id: %w", err)
	}
	return credentialAudit{Actor: actor, CorrelationID: fmt.Sprintf("emergency-%x", b), At: s.now()}, nil
}

// CreateAccount creates the Organization User and a disabled credential in
// one transaction and returns the user id. The password must satisfy the
// password policy. ErrLocalCredentialExist reports a taken login name.
func (s *EmergencyAccounts) CreateAccount(ctx context.Context, actor audit.Actor, login, displayName, password string) (string, error) {
	a, err := s.begin(actor)
	if err != nil {
		return "", err
	}
	if !ValidLoginName(login) {
		return "", ErrInvalidLoginName
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return "", err
	}
	var userID string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if userID, err = s.users.CreateEmergencyUser(ctx, tx, displayName, a.CorrelationID, actor); err != nil {
			return err
		}
		return createLocalCredential(ctx, tx, userID, login, hash, a)
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}

// ChangePassword replaces the password and revokes every session of the
// account (sessions opened with the old password end with it). It returns the
// number of revoked sessions.
func (s *EmergencyAccounts) ChangePassword(ctx context.Context, actor audit.Actor, login, password string) (int, error) {
	a, err := s.begin(actor)
	if err != nil {
		return 0, err
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return 0, err
	}
	revoked := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := setLocalPassword(ctx, tx, login, hash, a)
		if err != nil {
			return err
		}
		revoked, err = RevokeUserSessions(ctx, tx, userID, "emergency_password_changed", actor, a.CorrelationID, a.At)
		return err
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// Enable enables the account and reports whether the state changed.
func (s *EmergencyAccounts) Enable(ctx context.Context, actor audit.Actor, login string) (bool, error) {
	a, err := s.begin(actor)
	if err != nil {
		return false, err
	}
	changed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		_, changed, err = setLocalEnabled(ctx, tx, login, true, a)
		return err
	})
	return changed, err
}

// Disable disables the account and always revokes its sessions, even when it
// was already disabled. It reports whether the state changed and how many
// sessions were revoked. A login racing with Disable cannot create a session
// afterwards: its session transaction requires the credential to still be
// enabled (see loginHandler.emergencyLogin).
func (s *EmergencyAccounts) Disable(ctx context.Context, actor audit.Actor, login string) (changed bool, revoked int, err error) {
	a, err := s.begin(actor)
	if err != nil {
		return false, 0, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		userID, ch, err := setLocalEnabled(ctx, tx, login, false, a)
		if err != nil {
			return err
		}
		changed = ch
		revoked, err = RevokeUserSessions(ctx, tx, userID, "emergency_account_disabled", actor, a.CorrelationID, a.At)
		return err
	})
	if err != nil {
		return false, 0, err
	}
	return changed, revoked, nil
}
