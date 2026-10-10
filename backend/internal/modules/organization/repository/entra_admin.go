package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// entraProviderPrefix marks the External Identities of Microsoft Entra tenants (`entra:<tenant id>`).
const entraProviderPrefix = "entra:"

// LinkEntraIdentity links an Entra identity to a User on behalf of a platform administrator (ADR-0035, ADR-0034 R1
// and R5). In one transaction it checks the caller (platform administrator, not the target), the target (not the
// emergency account, an active employee, expected version, dominance), replaces a local credential atomically (the
// User must hold no role; the credential, open credential tokens and sessions end), inserts the identity, bumps the
// version and audits. An email address is never part of the decision.
func (r *Repository) LinkEntraIdentity(ctx context.Context, c application.Caller, userID string, version int, tenantID, objectID string) (application.EntraLinkResult, error) {
	var out application.EntraLinkResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		if !c.PlatformAdmin {
			return application.ErrAdminRequired
		}
		if c.Actor.UserID != "" && strings.EqualFold(c.Actor.UserID, userID) {
			return application.ErrSelfOperation
		}
		before, err := lockUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if before.Status != application.StatusActive || before.AccountKind != application.AccountKindEmployee {
			return application.ErrWrongState
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if err := r.dominance(ctx, tx, c, userID); err != nil {
			return err
		}
		providerKey := entraProviderPrefix + tenantID
		var sameTenant bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.external_identities WHERE user_id = $1::uuid AND provider_key = $2)`,
			userID, providerKey).Scan(&sameTenant); err != nil {
			return fmt.Errorf("check tenant identity: %w", err)
		}
		if sameTenant {
			return application.ErrEntraTenantAlreadyUsed
		}
		var hasCredential bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local')`,
			userID).Scan(&hasCredential); err != nil {
			return fmt.Errorf("check local credential: %w", err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		meta := map[string]any{"providerKey": providerKey, "via": "administrator", "subjectSuffix": suffix(objectID, 4)}
		// Open invitation or reset tokens could still set a password after the link, so they end in every case.
		tokens, err := r.sessions.RevokeCredentialTokens(ctx, tx, userID, "entra_linked", c.Actor, c.CorrelationID, now)
		if err != nil {
			return err
		}
		meta["credentialTokensRevoked"] = tokens
		if hasCredential {
			// ADR-0034 R5: a password that predates the link would stay usable by whoever set it.
			if r.removers == nil {
				return application.ErrNoGuards
			}
			holds, err := r.guards.HoldsAnyRole(ctx, tx, userID)
			if err != nil {
				return err
			}
			if holds {
				return application.ErrDirectoryLinkRoles
			}
			deleted, err := r.removers.DeleteLocalCredential(ctx, tx, userID)
			if err != nil {
				return err
			}
			sessions, err := r.sessions.RevokeSessions(ctx, tx, userID, "entra_linked", c.Actor, c.CorrelationID, now)
			if err != nil {
				return err
			}
			out.CredentialDeleted = deleted > 0
			meta["credentialsDeleted"], meta["sessionsRevoked"] = deleted, sessions
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject, linked_via)
			VALUES ($1::uuid, $2, $3, 'administrator')`, userID, providerKey, objectID); err != nil {
			if isUnique(err) {
				return application.ErrEntraIdentityTaken
			}
			return fmt.Errorf("insert entra identity: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET version = version + 1, updated_at = $2 WHERE id = $1::uuid`, userID, now); err != nil {
			return fmt.Errorf("bump version: %w", err)
		}
		if out.User, err = reloadUser(ctx, tx, userID); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.external_identity.linked", "user", userID, nil, map[string]any{"version": out.User.Version}, meta)
	})
	if err != nil {
		return application.EntraLinkResult{}, finishEntra(err, "link entra identity")
	}
	return out, nil
}

// UnlinkEntraIdentity removes one Entra identity of a User and revokes the User's Entra sessions in the same
// transaction. Only identities of Entra tenants can be removed here; a directory identity is never touched. The
// identity must belong to the User named in the path.
func (r *Repository) UnlinkEntraIdentity(ctx context.Context, c application.Caller, userID, identityID string) (application.EntraLinkResult, error) {
	var out application.EntraLinkResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		if !c.PlatformAdmin {
			return application.ErrAdminRequired
		}
		if c.Actor.UserID != "" && strings.EqualFold(c.Actor.UserID, userID) {
			return application.ErrSelfOperation
		}
		before, err := lockUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if err := r.dominance(ctx, tx, c, userID); err != nil {
			return err
		}
		id, ok := parseID(identityID)
		if !ok {
			return application.ErrNotFound
		}
		var providerKey, subject string
		err = tx.QueryRow(ctx, `DELETE FROM organization.external_identities
			WHERE id = $1 AND user_id = $2::uuid AND provider_key LIKE 'entra:%' RETURNING provider_key, external_subject`, id, userID).Scan(&providerKey, &subject)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete entra identity: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.sessions SET revoked_at = now() WHERE user_id = $1::uuid AND auth_method = 'entra' AND revoked_at IS NULL`, userID)
		if err != nil {
			return fmt.Errorf("revoke entra sessions: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET version = version + 1, updated_at = now() WHERE id = $1::uuid`, userID); err != nil {
			return fmt.Errorf("bump version: %w", err)
		}
		if out.User, err = reloadUser(ctx, tx, userID); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.external_identity.unlinked", "user", userID, nil, map[string]any{"version": out.User.Version},
			map[string]any{"providerKey": providerKey, "via": "administrator", "subjectSuffix": suffix(subject, 4), "sessionsRevoked": tag.RowsAffected()})
	})
	if err != nil {
		return application.EntraLinkResult{}, finishEntra(err, "unlink entra identity")
	}
	return out, nil
}

// finishEntra passes the domain errors through and wraps everything else.
func finishEntra(err error, what string) error {
	_, err = finishPeople(struct{}{}, err, what)
	return err
}

func suffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- sign-in time operations (hybrid source anchor, provisioning) ----

var loginActor = audit.SystemActor("login")

// LinkEntraBySourceAnchor implements the hybrid match of ADR-0035 point 3b. directoryProviderKey is the configured
// directory provider (ENTRA_LINK_DIRECTORY_PROVIDER_KEY); anchor is the objectGUID decoded from Graph. Exactly one
// enabled directory identity of an active, directory-owned employee may match; the Entra identity is then linked in
// the same transaction and audited with via source_anchor. Matching never uses email, UPN or names.
func (r *Repository) LinkEntraBySourceAnchor(ctx context.Context, directoryProviderKey, tenantID, objectID, anchor, correlationID string) (string, error) {
	var userID string
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT u.id::text, u.status, u.origin, u.account_kind, e.enabled
			FROM organization.external_identities e JOIN organization.users u ON u.id = e.user_id
			WHERE e.provider_key = $1 AND e.external_subject = $2
			ORDER BY u.id FOR UPDATE OF u`, directoryProviderKey, anchor)
		if err != nil {
			return fmt.Errorf("match source anchor: %w", err)
		}
		type match struct {
			id, status, origin, kind string
			enabled                  bool
		}
		var found []match
		for rows.Next() {
			var m match
			if err := rows.Scan(&m.id, &m.status, &m.origin, &m.kind, &m.enabled); err != nil {
				rows.Close()
				return fmt.Errorf("scan source anchor match: %w", err)
			}
			found = append(found, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read source anchor matches: %w", err)
		}
		switch len(found) {
		case 0:
			return application.ErrAnchorNoMatch
		case 1:
		default:
			return application.ErrAnchorAmbiguous
		}
		m := found[0]
		if !m.enabled || m.status != application.StatusActive || m.origin != application.OriginDirectory || m.kind != application.AccountKindEmployee {
			return application.ErrAnchorRefused
		}
		providerKey := entraProviderPrefix + tenantID
		var tenantUsed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.external_identities WHERE user_id = $1::uuid AND provider_key = $2)`,
			m.id, providerKey).Scan(&tenantUsed); err != nil {
			return fmt.Errorf("check tenant identity: %w", err)
		}
		if tenantUsed {
			return application.ErrAnchorRefused
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject, linked_via)
			VALUES ($1::uuid, $2, $3, 'source_anchor')`, m.id, providerKey, objectID); err != nil {
			if isUnique(err) {
				return application.ErrAnchorRefused
			}
			return fmt.Errorf("insert entra identity: %w", err)
		}
		userID = m.id
		return audit.Record(ctx, tx, audit.Change{
			Action: "organization.external_identity.linked", TargetType: "user", TargetID: m.id, Actor: loginActor, CorrelationID: correlationID,
			Metadata: map[string]any{"providerKey": providerKey, "via": "source_anchor", "directoryProviderKey": directoryProviderKey, "subjectSuffix": suffix(objectID, 4)},
		})
	})
	if err != nil {
		return "", err
	}
	return userID, nil
}

// ProvisionEntraEmployee creates an internal employee for a first sign-in (setting auth.entra_provisioning =
// auto_employee, members of the home tenant only; the caller enforces tenant and guest rules). The User has no roles
// and no Team memberships, origin directory (attributes owned by the provider entra:<tenant>), and is audited as
// organization.user.provisioned with via entra. An existing User with the same primary email is never linked: the
// attempt is refused with application.ErrProvisionEmailConflict and an audit event for administrators.
func (r *Repository) ProvisionEntraEmployee(ctx context.Context, tenantID, objectID, displayName, email, correlationID string) (string, error) {
	name, address, err := application.ValidateProvisionProfile(displayName, email)
	if err != nil {
		return "", err
	}
	providerKey := entraProviderPrefix + tenantID
	var userID string
	var conflictID string
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// Serialize first sign-ins of the same identity; the unique indexes below are the final arbiter.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, providerKey+"/"+objectID); err != nil {
			return fmt.Errorf("lock provisioning: %w", err)
		}
		var existing string
		err := tx.QueryRow(ctx, `SELECT user_id::text FROM organization.external_identities WHERE provider_key = $1 AND external_subject = $2`,
			providerKey, objectID).Scan(&existing)
		if err == nil {
			userID = existing // created by a concurrent sign-in of the same person
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check identity: %w", err)
		}
		err = tx.QueryRow(ctx, `SELECT id::text FROM organization.users WHERE lower(primary_email) = lower($1)`, address).Scan(&conflictID)
		if err == nil {
			return recordProvisionRefusal(ctx, tx, conflictID, providerKey, correlationID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check email: %w", err)
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO organization.users (display_name, primary_email, status, status_source, origin, account_kind)
			VALUES ($1, $2, 'active', 'platform', 'directory', 'employee') RETURNING id::text`, name, address).Scan(&id)
		if isUnique(err) {
			return application.ErrProvisionEmailConflict // a concurrent request took the address between the check and the insert
		}
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization.external_identities (user_id, provider_key, external_subject, linked_via)
			VALUES ($1::uuid, $2, $3, 'provisioning')`, id, providerKey, objectID); err != nil {
			return fmt.Errorf("insert entra identity: %w", err)
		}
		userID = id
		if err := audit.Record(ctx, tx, audit.Change{
			Action: "organization.user.provisioned", TargetType: "user", TargetID: id, Actor: loginActor, CorrelationID: correlationID,
			Metadata: map[string]any{"via": "entra", "providerKey": providerKey, "accountKind": application.AccountKindEmployee, "subjectSuffix": suffix(objectID, 4)},
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "organization.external_identity.linked", TargetType: "user", TargetID: id, Actor: loginActor, CorrelationID: correlationID,
			Metadata: map[string]any{"providerKey": providerKey, "via": "provisioning", "subjectSuffix": suffix(objectID, 4)},
		})
	})
	if err != nil {
		return "", err
	}
	if conflictID != "" {
		return "", application.ErrProvisionEmailConflict
	}
	return userID, nil
}

// recordProvisionRefusal writes the administrator-visible trace of a refused provisioning (the collision) and lets
// the transaction commit; the caller turns it into application.ErrProvisionEmailConflict.
func recordProvisionRefusal(ctx context.Context, tx pgx.Tx, conflictUserID, providerKey, correlationID string) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: "organization.user.provisioning_refused", TargetType: "user", TargetID: conflictUserID, Actor: loginActor, CorrelationID: correlationID,
		Metadata: map[string]any{"reason": "email_conflict", "via": "entra", "providerKey": providerKey},
	})
}
