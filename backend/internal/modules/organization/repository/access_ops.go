package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// LinkDirectoryIdentity attaches the directory identity of a synchronization conflict to a local account (ADR-0034
// review rule R5). Platform administrators only. In one transaction it checks that the User holds no role, deletes
// the local credential, ends every open credential token and session, attaches the identity and turns the origin of
// the User into "directory" (the database trigger accepts that change only when an identity exists and no
// credential is left). The identity's account data (distinguished name, attributes) arrives with the next
// synchronization run, which owns the profile attributes from then on.
func (r *Repository) LinkDirectoryIdentity(ctx context.Context, c application.Caller, id string, version int, runID, externalID string) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		if r.removers == nil {
			return application.ErrNoGuards
		}
		if !c.PlatformAdmin {
			return application.ErrAdminRequired
		}
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		switch before.Origin {
		case application.OriginEmergency:
			return application.ErrEmergencyAccount
		case application.OriginDirectory:
			return application.ErrDirectoryUser
		}
		if before.AccountKind != application.AccountKindEmployee {
			return application.ErrWrongState
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if err := r.dominance(ctx, tx, c, id); err != nil {
			return err
		}
		holds, err := r.guards.HoldsAnyRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if holds {
			return application.ErrDirectoryLinkRoles
		}
		var providerKey string
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT provider_key, conflicts FROM organization.directory_sync_runs WHERE id = $1::uuid`, runID).Scan(&providerKey, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load sync run: %w", err)
		}
		var conflicts []syncConflict
		if err := json.Unmarshal(raw, &conflicts); err != nil {
			return fmt.Errorf("decode run conflicts: %w", err)
		}
		var username string
		found := false
		for _, cf := range conflicts {
			if cf.ExternalID == externalID && cf.Kind == application.ConflictEmailInUse {
				username, found = cf.Username, true
				break
			}
		}
		if !found {
			return application.ErrNotFound
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.external_identities WHERE provider_key = $1 AND external_subject = $2)`,
			providerKey, externalID).Scan(&taken); err != nil {
			return fmt.Errorf("check identity: %w", err)
		}
		if taken {
			return application.ErrDirectoryIdentityInUse
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		credentials, err := r.removers.DeleteLocalCredential(ctx, tx, id)
		if err != nil {
			return err
		}
		tokens, err := r.sessions.RevokeCredentialTokens(ctx, tx, id, "directory_linked", c.Actor, c.CorrelationID, now)
		if err != nil {
			return err
		}
		sessions, err := r.sessions.RevokeSessions(ctx, tx, id, "directory_linked", c.Actor, c.CorrelationID, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization.external_identities (user_id, provider_key, external_subject, username, enabled)
			VALUES ($1::uuid, $2, $3, NULLIF($4, ''), true)`, id, providerKey, externalID, username); err != nil {
			if isUnique(err) {
				return application.ErrDirectoryIdentityInUse
			}
			return fmt.Errorf("insert identity: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET origin = 'directory', version = version + 1, updated_at = $2 WHERE id = $1::uuid`, id, now); err != nil {
			return fmt.Errorf("link origin: %w", err)
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.user.directory_linked", "user", id,
			map[string]any{"origin": before.Origin},
			map[string]any{"origin": out.Origin, "version": out.Version},
			map[string]any{"providerKey": providerKey, "credentialsDeleted": credentials, "credentialTokensRevoked": tokens, "sessionsRevoked": sessions})
	})
	return finishPeople(out, err, "link directory identity")
}

// ExtendAccess moves the access end of an external account to a later date. The only way to extend (ADR-0034
// point 4); the date is bounded by the caller of the application layer. The account stays inactive when the expiry
// job had already deactivated it: reactivation is its own operation with the dominance rule.
func (r *Repository) ExtendAccess(ctx context.Context, c application.Caller, id string, version int, until time.Time, reason string) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.AccountKind != application.AccountKindExternal || before.AccessExpiresAt == nil || before.Status == application.StatusDeparted {
			return application.ErrWrongState
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if !until.After(*before.AccessExpiresAt) {
			return &application.InvalidInputError{Message: "accessExpiresAt must be later than the current end of access"}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET access_expires_at = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, until); err != nil {
			return fmt.Errorf("extend access: %w", err)
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.user.access_extended", "user", id,
			map[string]any{"accessExpiresAt": before.AccessExpiresAt.UTC().Format(time.RFC3339)},
			map[string]any{"accessExpiresAt": until.UTC().Format(time.RFC3339), "version": out.Version},
			map[string]any{"reasonCode": reason})
	})
	return finishPeople(out, err, "extend access")
}
