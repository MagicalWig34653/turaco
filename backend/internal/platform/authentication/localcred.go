package authentication

import (
	"context"
	"encoding/json"
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
// hash is stored. Lifecycle operations are used by turaco-admin; each runs in
// the caller's transaction together with its audit entry.

var loginNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// Local credential errors.
var (
	ErrInvalidLoginName     = errors.New("authentication: login name must match [a-z0-9][a-z0-9._-]{1,62}")
	ErrLocalCredentialExist = errors.New("authentication: login name already exists")
	ErrLocalCredentialNone  = errors.New("authentication: emergency account not found")
)

// Audit actions of the emergency account lifecycle.
const (
	ActionEmergencyAccountCreated         = "authentication.emergency_account.created"
	ActionEmergencyAccountPasswordChanged = "authentication.emergency_account.password_changed"
	ActionEmergencyAccountEnabled         = "authentication.emergency_account.enabled"
	ActionEmergencyAccountDisabled        = "authentication.emergency_account.disabled"
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

// FindLocalCredential returns the credential with the given (lower-case) login
// name.
func FindLocalCredential(ctx context.Context, pool *pgxpool.Pool, loginName string) (LocalCredential, bool, error) {
	var c LocalCredential
	err := pool.QueryRow(ctx, `
		SELECT user_id::text, login_name, password_hash, enabled
		FROM platform.local_credentials WHERE login_name = $1`, loginName).
		Scan(&c.UserID, &c.LoginName, &c.PasswordHash, &c.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return LocalCredential{}, false, nil
	}
	if err != nil {
		return LocalCredential{}, false, fmt.Errorf("find local credential: %w", err)
	}
	return c, true, nil
}

// LocalCredentialAudit carries who performs a lifecycle operation. Actor is
// the audit metadata (the CLI passes {"actor":"cli","osUser":...}); lifecycle
// operations never have a human session actor.
type LocalCredentialAudit struct {
	Actor         json.RawMessage
	CorrelationID string
	At            time.Time
}

// CreateLocalCredential stores a new, disabled credential for userID and
// audits authentication.emergency_account.created. hash comes from HashPassword.
func CreateLocalCredential(ctx context.Context, tx pgx.Tx, userID, loginName, hash string, a LocalCredentialAudit) error {
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

// SetLocalPassword replaces the hash of the account and audits
// authentication.emergency_account.password_changed. It returns the user id.
func SetLocalPassword(ctx context.Context, tx pgx.Tx, loginName, hash string, a LocalCredentialAudit) (string, error) {
	at := a.At.UTC().Truncate(time.Microsecond)
	var userID string
	err := tx.QueryRow(ctx, `
		UPDATE platform.local_credentials
		SET password_hash = $2, password_changed_at = $3, updated_at = $3
		WHERE login_name = $1 RETURNING user_id::text`, loginName, hash, at).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLocalCredentialNone
	}
	if err != nil {
		return "", fmt.Errorf("set local password: %w", err)
	}
	return userID, auditLocalCredential(ctx, tx, ActionEmergencyAccountPasswordChanged, userID, loginName, a, nil, nil)
}

// SetLocalEnabled enables or disables the account and audits
// authentication.emergency_account.enabled|disabled. It returns the user id and
// whether the state changed (an unchanged state is not audited).
func SetLocalEnabled(ctx context.Context, tx pgx.Tx, loginName string, enabled bool, a LocalCredentialAudit) (string, bool, error) {
	at := a.At.UTC().Truncate(time.Microsecond)
	var userID string
	var before bool
	err := tx.QueryRow(ctx, `
		WITH old AS (SELECT user_id, enabled FROM platform.local_credentials WHERE login_name = $1 FOR UPDATE)
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

func auditLocalCredential(ctx context.Context, tx pgx.Tx, action, userID, loginName string, a LocalCredentialAudit, before, after map[string]any) error {
	meta := map[string]any{"loginName": loginName}
	if len(a.Actor) > 0 {
		var actor map[string]any
		if err := json.Unmarshal(a.Actor, &actor); err != nil {
			return fmt.Errorf("audit actor must be a JSON object: %w", err)
		}
		for k, v := range actor {
			if k != "loginName" {
				meta[k] = v
			}
		}
	}
	e := audit.Entry{OccurredAt: a.At.UTC().Truncate(time.Microsecond), Action: action, TargetType: "emergency_account", TargetID: userID, CorrelationID: a.CorrelationID}
	var err error
	if e.Metadata, err = json.Marshal(meta); err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if before != nil {
		if e.Before, err = json.Marshal(before); err != nil {
			return fmt.Errorf("marshal audit before: %w", err)
		}
	}
	if after != nil {
		if e.After, err = json.Marshal(after); err != nil {
			return fmt.Errorf("marshal audit after: %w", err)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&e.ID); err != nil {
		return fmt.Errorf("generate audit id: %w", err)
	}
	return audit.Insert(ctx, tx, e)
}
