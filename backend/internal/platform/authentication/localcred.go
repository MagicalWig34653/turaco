package authentication

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Local credentials belong to the emergency (break-glass) account only: an
// ordinary Organization User without a directory identity. Only the argon2id
// hash is stored. The lifecycle is EmergencyAccounts (used by turaco-admin);
// each operation runs in one transaction together with its audit entry.

var loginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// Local credential errors.
var (
	ErrInvalidLoginName     = errors.New("authentication: login name must match [a-z0-9][a-z0-9._-]{1,62}")
	ErrLocalCredentialExist = errors.New("authentication: login name already exists")
	ErrLocalCredentialNone  = errors.New("authentication: emergency account not found")
)

// Audit actions of the emergency account lifecycle.
const (
	ActionEmergencyAccountCreated         = "auth.emergency_account.created"
	ActionEmergencyAccountPasswordChanged = "auth.emergency_account.password_changed"
	ActionEmergencyAccountEnabled         = "auth.emergency_account.enabled"
	ActionEmergencyAccountDisabled        = "auth.emergency_account.disabled"
)

// ValidLoginName reports whether name is an acceptable emergency login name.
func ValidLoginName(name string) bool { return loginNamePattern.MatchString(name) }

// LocalCredential is the stored emergency credential of a user.
type LocalCredential struct {
	UserID       string
	LoginName    string
	PasswordHash string
	Enabled      bool
}

// FindLocalCredential returns the emergency credential with the given (lower-case) login name. Local-account
// credentials (kind "local") are never found here: there is no path where one kind authenticates at the other
// endpoint (review rule R3).
func FindLocalCredential(ctx context.Context, pool *pgxpool.Pool, loginName string) (LocalCredential, bool, error) {
	var c LocalCredential
	err := pool.QueryRow(ctx, `
		SELECT user_id::text, login_name, password_hash, enabled
		FROM platform.local_credentials WHERE login_name = $1 AND kind = 'emergency'`, loginName).
		Scan(&c.UserID, &c.LoginName, &c.PasswordHash, &c.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return LocalCredential{}, false, nil
	}
	if err != nil {
		return LocalCredential{}, false, fmt.Errorf("find local credential: %w", err)
	}
	return c, true, nil
}

// credentialAudit carries who performs a lifecycle operation.
type credentialAudit struct {
	Actor         audit.Actor
	CorrelationID string
	At            time.Time
}

// createLocalCredential stores a new, disabled credential for userID and
// audits auth.emergency_account.created. hash comes from HashPassword.
func createLocalCredential(ctx context.Context, tx pgx.Tx, userID, loginName, hash string, a credentialAudit) error {
	if !uuidPattern.MatchString(userID) {
		return errors.New("create local credential: user id must be a UUID")
	}
	if !ValidLoginName(loginName) {
		return ErrInvalidLoginName
	}
	at := a.At.UTC().Truncate(time.Microsecond)
	tag, err := tx.Exec(ctx, `
		INSERT INTO platform.local_credentials (user_id, login_name, password_hash, enabled, created_at, updated_at, password_changed_at)
		VALUES ($1, $2, $3, false, $4, $4, $4)
		ON CONFLICT DO NOTHING`, userID, loginName, hash, at)
	if err != nil {
		return fmt.Errorf("create local credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLocalCredentialExist
	}
	return auditLocalCredential(ctx, tx, ActionEmergencyAccountCreated, userID, loginName, a,
		nil, map[string]any{"enabled": false})
}

// setLocalPassword replaces the hash of the account and audits
// auth.emergency_account.password_changed. It returns the user id.
func setLocalPassword(ctx context.Context, tx pgx.Tx, loginName, hash string, a credentialAudit) (string, error) {
	at := a.At.UTC().Truncate(time.Microsecond)
	var userID string
	err := tx.QueryRow(ctx, `
		UPDATE platform.local_credentials
		SET password_hash = $2, password_changed_at = $3, updated_at = $3
		WHERE login_name = $1 AND kind = 'emergency' RETURNING user_id::text`, loginName, hash, at).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLocalCredentialNone
	}
	if err != nil {
		return "", fmt.Errorf("set local password: %w", err)
	}
	return userID, auditLocalCredential(ctx, tx, ActionEmergencyAccountPasswordChanged, userID, loginName, a, nil, nil)
}

// setLocalEnabled enables or disables the account and audits
// auth.emergency_account.enabled|disabled. It returns the user id and
// whether the state changed (an unchanged state is not audited). The
// credential row stays locked until tx ends, which serializes the change with
// a login creating its session (see loginHandler.emergencyLogin).
func setLocalEnabled(ctx context.Context, tx pgx.Tx, loginName string, enabled bool, a credentialAudit) (string, bool, error) {
	at := a.At.UTC().Truncate(time.Microsecond)
	var userID string
	var before bool
	err := tx.QueryRow(ctx, `
		WITH old AS (SELECT user_id, enabled FROM platform.local_credentials WHERE login_name = $1 AND kind = 'emergency' FOR UPDATE)
		UPDATE platform.local_credentials c SET enabled = $2, updated_at = $3
		FROM old WHERE c.user_id = old.user_id
		RETURNING c.user_id::text, old.enabled`, loginName, enabled, at).Scan(&userID, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrLocalCredentialNone
	}
	if err != nil {
		return "", false, fmt.Errorf("set local credential enabled: %w", err)
	}
	if before == enabled {
		return userID, false, nil
	}
	action := ActionEmergencyAccountDisabled
	if enabled {
		action = ActionEmergencyAccountEnabled
	}
	return userID, true, auditLocalCredential(ctx, tx, action, userID, loginName, a,
		map[string]any{"enabled": before}, map[string]any{"enabled": enabled})
}

func auditLocalCredential(ctx context.Context, tx pgx.Tx, action, userID, loginName string, a credentialAudit, before, after map[string]any) error {
	e := audit.Change{
		Action: action, TargetType: "emergency_account", TargetID: userID, Actor: a.Actor,
		CorrelationID: a.CorrelationID, Metadata: map[string]any{"loginName": loginName}, OccurredAt: a.At,
	}
	if before != nil {
		e.Before = before
	}
	if after != nil {
		e.After = after
	}
	return audit.Record(ctx, tx, e)
}
