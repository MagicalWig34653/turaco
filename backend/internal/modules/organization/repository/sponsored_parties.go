package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// The sponsor's own page (F14 A-G follow-up): the External Parties a User sponsors and their accounts. The sponsor
// filter is part of every query; there is no way to ask for another User's parties.

var (
	_ application.ExternalPartyStore     = (*Repository)(nil)
	_ application.PartyNotificationStore = (*Repository)(nil)
)

// maxSponsoredParties bounds the parties of one sponsor; maxPartyAccounts (external_parties.go) bounds the accounts.
const maxSponsoredParties = 100

// ListSponsoredParties implements application.ExternalPartyStore.
func (r *Repository) ListSponsoredParties(ctx context.Context, sponsorUserID string) ([]application.SponsoredParty, error) {
	u, ok := parseID(sponsorUserID)
	if !ok {
		return []application.SponsoredParty{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, kind, name, active FROM organization.external_parties
		WHERE sponsor_user_id = $1 ORDER BY lower(name), id LIMIT $2`, u, maxSponsoredParties)
	if err != nil {
		return nil, fmt.Errorf("list sponsored parties: %w", err)
	}
	out := []application.SponsoredParty{}
	for rows.Next() {
		var p application.SponsoredParty
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &p.Active); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list sponsored parties: scan: %w", err)
		}
		p.Accounts = []application.SponsoredAccount{}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sponsored parties: %w", err)
	}
	for i := range out {
		acc, err := r.pool.Query(ctx, `
			SELECT id::text, display_name, status, access_expires_at, version FROM organization.users
			WHERE external_party_id = $1::uuid AND account_kind = 'external' AND access_expires_at IS NOT NULL
			ORDER BY access_expires_at, lower(display_name), id LIMIT $2`, out[i].ID, maxPartyAccounts)
		if err != nil {
			return nil, fmt.Errorf("list sponsored accounts: %w", err)
		}
		for acc.Next() {
			var a application.SponsoredAccount
			if err := acc.Scan(&a.UserID, &a.DisplayName, &a.Status, &a.AccessExpiresAt, &a.Version); err != nil {
				acc.Close()
				return nil, fmt.Errorf("list sponsored accounts: scan: %w", err)
			}
			out[i].Accounts = append(out[i].Accounts, a)
		}
		acc.Close()
		if err := acc.Err(); err != nil {
			return nil, fmt.Errorf("list sponsored accounts: %w", err)
		}
	}
	return out, nil
}

// AccountContextTx implements application.PartyNotificationStore.
func (r *Repository) AccountContextTx(ctx context.Context, tx pgx.Tx, accountID string) (application.PartyNotificationContext, bool, error) {
	u, ok := parseID(accountID)
	if !ok {
		return application.PartyNotificationContext{}, false, nil
	}
	var c application.PartyNotificationContext
	err := tx.QueryRow(ctx, `
		SELECT u.display_name, p.name, p.sponsor_user_id::text
		FROM organization.users u JOIN organization.external_parties p ON p.id = u.external_party_id
		WHERE u.id = $1 AND u.account_kind = 'external'`, u).Scan(&c.AccountName, &c.PartyName, &c.SponsorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PartyNotificationContext{}, false, nil
	}
	if err != nil {
		return application.PartyNotificationContext{}, false, fmt.Errorf("load account notification context: %w", err)
	}
	return c, true, nil
}

// PartyContextTx implements application.PartyNotificationStore.
func (r *Repository) PartyContextTx(ctx context.Context, tx pgx.Tx, partyID string) (application.PartyNotificationContext, bool, error) {
	u, ok := parseID(partyID)
	if !ok {
		return application.PartyNotificationContext{}, false, nil
	}
	var c application.PartyNotificationContext
	err := tx.QueryRow(ctx, `SELECT name, sponsor_user_id::text, active FROM organization.external_parties WHERE id = $1`, u).Scan(&c.PartyName, &c.SponsorID, &c.PartyActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PartyNotificationContext{}, false, nil
	}
	if err != nil {
		return application.PartyNotificationContext{}, false, fmt.Errorf("load party notification context: %w", err)
	}
	return c, true, nil
}

// IsActiveEmployeeTx implements application.PartyNotificationStore.
func (r *Repository) IsActiveEmployeeTx(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	u, ok := parseID(userID)
	if !ok {
		return false, nil
	}
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT status = 'active' AND account_kind = 'employee' AND origin <> 'emergency' FROM organization.users WHERE id = $1`, u).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check recipient: %w", err)
	}
	return active, nil
}
