package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Errors of the Entra identity link operations (ADR-0035). The identity key is provider `entra:<tenant id>` and the
// object id as subject; an email address never links an identity.
var (
	ErrEntraUserNotFound      = errors.New("organization: user not found")
	ErrEntraUserNotLinkable   = errors.New("organization: user cannot be linked to an Entra identity")
	ErrEntraLocalCredential   = errors.New("organization: user has a local credential; remove it before linking")
	ErrEntraIdentityTaken     = errors.New("organization: identity is already linked")
	ErrEntraTenantAlreadyUsed = errors.New("organization: user already has an identity of this tenant")
	ErrEntraNotLinked         = errors.New("organization: identity link not found")
)

// FindUserByExternalIdentity returns the User of an enabled external identity.
func (r *Repository) FindUserByExternalIdentity(ctx context.Context, providerKey, subject string) (string, bool, error) {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT user_id::text FROM organization.external_identities
		WHERE provider_key = $1 AND external_subject = $2 AND enabled`, providerKey, subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find external identity: %w", err)
	}
	return id, true, nil
}

// LinkExternalIdentity links an identity to an active User, audited in the same transaction. The emergency account
// can never be linked, a User with a local credential must have it removed first (the atomic replacement is the
// directory-link operation of the people administration), and one User has at most one identity per provider.
func (r *Repository) LinkExternalIdentity(ctx context.Context, actor audit.Actor, correlationID, userID, providerKey, subject, via string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var origin, status string
		err := tx.QueryRow(ctx, `SELECT origin, status FROM organization.users WHERE id = $1::uuid FOR UPDATE`, userID).Scan(&origin, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEntraUserNotFound
		}
		if err != nil {
			return err
		}
		if origin == "emergency" || status != "active" {
			return ErrEntraUserNotLinkable
		}
		var hasCredential bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.local_credentials WHERE user_id = $1::uuid)`, userID).Scan(&hasCredential); err != nil {
			return err
		}
		if hasCredential {
			return ErrEntraLocalCredential
		}
		var sameProvider bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.external_identities WHERE user_id = $1::uuid AND provider_key = $2)`, userID, providerKey).Scan(&sameProvider); err != nil {
			return err
		}
		if sameProvider {
			return ErrEntraTenantAlreadyUsed
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject, linked_via) VALUES ($1::uuid, $2, $3, $4)`,
			userID, providerKey, subject, via); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return ErrEntraIdentityTaken
			}
			return err
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "organization.external_identity.linked", TargetType: "user", TargetID: userID, Actor: actor, CorrelationID: correlationID,
			Metadata: map[string]any{"providerKey": providerKey, "via": via},
		})
	})
}

// UnlinkExternalIdentity removes the link and revokes the User's sessions of that method in the same transaction.
func (r *Repository) UnlinkExternalIdentity(ctx context.Context, actor audit.Actor, correlationID, providerKey, subject, via string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `DELETE FROM organization.external_identities WHERE provider_key = $1 AND external_subject = $2 RETURNING user_id::text`,
			providerKey, subject).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrEntraNotLinked
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now() WHERE user_id = $1::uuid AND auth_method = 'entra' AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "organization.external_identity.unlinked", TargetType: "user", TargetID: userID, Actor: actor, CorrelationID: correlationID,
			Metadata: map[string]any{"providerKey": providerKey, "via": via},
		})
	})
}
