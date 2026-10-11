package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// External Parties (F14 slice A-G). Every write locks the party row, checks the version, authorizes inside the
// transaction and audits in the same transaction. The sponsor must be an active internal employee.

var _ application.ExternalPartyStore = (*Repository)(nil)

const partyColumns = `p.id::text, p.kind, p.name, p.sponsor_user_id::text, s.display_name, p.active, p.notes, p.version, p.created_at, p.updated_at,
	(SELECT count(*) FROM organization.users u WHERE u.external_party_id = p.id)`

func scanParty(row pgx.Row) (application.ExternalParty, error) {
	var p application.ExternalParty
	err := row.Scan(&p.ID, &p.Kind, &p.Name, &p.SponsorUserID, &p.SponsorName, &p.Active, &p.Notes, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.AccountCount)
	return p, err
}

func (r *Repository) ListExternalParties(ctx context.Context) ([]application.ExternalParty, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+partyColumns+`
		FROM organization.external_parties p JOIN organization.users s ON s.id = p.sponsor_user_id
		ORDER BY lower(p.name), p.id`)
	if err != nil {
		return nil, fmt.Errorf("list external parties: %w", err)
	}
	defer rows.Close()
	out := []application.ExternalParty{}
	for rows.Next() {
		p, err := scanParty(rows)
		if err != nil {
			return nil, fmt.Errorf("list external parties: scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list external parties: %w", err)
	}
	return out, nil
}

// maxPartyAccounts bounds the accounts shown with one party.
const maxPartyAccounts = 500

func (r *Repository) GetExternalParty(ctx context.Context, id string) (application.ExternalParty, []application.PartyAccount, error) {
	u, ok := parseID(id)
	if !ok {
		return application.ExternalParty{}, nil, application.ErrNotFound
	}
	p, err := scanParty(r.pool.QueryRow(ctx, `
		SELECT `+partyColumns+`
		FROM organization.external_parties p JOIN organization.users s ON s.id = p.sponsor_user_id WHERE p.id = $1`, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ExternalParty{}, nil, application.ErrNotFound
	}
	if err != nil {
		return application.ExternalParty{}, nil, fmt.Errorf("get external party: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, display_name, status, access_expires_at FROM organization.users
		WHERE external_party_id = $1 ORDER BY lower(display_name), id LIMIT $2`, u, maxPartyAccounts)
	if err != nil {
		return application.ExternalParty{}, nil, fmt.Errorf("list party accounts: %w", err)
	}
	defer rows.Close()
	accounts := []application.PartyAccount{}
	for rows.Next() {
		var a application.PartyAccount
		if err := rows.Scan(&a.UserID, &a.DisplayName, &a.Status, &a.AccessExpiresAt); err != nil {
			return application.ExternalParty{}, nil, fmt.Errorf("list party accounts: scan: %w", err)
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		return application.ExternalParty{}, nil, fmt.Errorf("list party accounts: %w", err)
	}
	return p, accounts, nil
}

// requireSponsor checks that the user is an active internal employee and keeps the row stable until the
// transaction ends.
func requireSponsor(ctx context.Context, tx pgx.Tx, id string) error {
	var status, kind, origin string
	err := tx.QueryRow(ctx, `SELECT status, account_kind, origin FROM organization.users WHERE id = $1::uuid FOR SHARE`, id).Scan(&status, &kind, &origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrSponsorInvalid
	}
	if err != nil {
		return fmt.Errorf("check sponsor: %w", err)
	}
	if status != application.StatusActive || kind != application.AccountKindEmployee || origin == application.OriginEmergency {
		return application.ErrSponsorInvalid
	}
	return nil
}

func partyState(p application.ExternalParty) map[string]any {
	return map[string]any{"kind": p.Kind, "sponsorUserId": p.SponsorUserID, "active": p.Active, "version": p.Version}
}

func (r *Repository) CreateExternalParty(ctx context.Context, c application.Caller, in application.NewExternalPartyInput) (application.ExternalParty, error) {
	var out application.ExternalParty
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := requireSponsor(ctx, tx, in.SponsorUserID); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO organization.external_parties (kind, name, sponsor_user_id, notes)
			VALUES ($1, $2, $3::uuid, $4) RETURNING id::text`, in.Kind, in.Name, in.SponsorUserID, in.Notes).Scan(&id)
		if isUnique(err) {
			return application.ErrPartyNameTaken
		}
		if err != nil {
			return fmt.Errorf("insert external party: %w", err)
		}
		if out, err = scanParty(tx.QueryRow(ctx, `
			SELECT `+partyColumns+` FROM organization.external_parties p JOIN organization.users s ON s.id = p.sponsor_user_id WHERE p.id = $1::uuid`, id)); err != nil {
			return fmt.Errorf("reload external party: %w", err)
		}
		return r.record(ctx, tx, c, "organization.external_party.created", "external_party", id, nil, partyState(out), nil)
	})
	if err != nil {
		return application.ExternalParty{}, wrapParty(err, "create external party")
	}
	return out, nil
}

func (r *Repository) UpdateExternalParty(ctx context.Context, c application.Caller, id string, in application.ExternalPartyChange) (application.ExternalParty, error) {
	u, ok := parseID(id)
	if !ok {
		return application.ExternalParty{}, application.ErrNotFound
	}
	var out application.ExternalParty
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var before application.ExternalParty
		err := tx.QueryRow(ctx, `
			SELECT id::text, kind, name, sponsor_user_id::text, active, notes, version FROM organization.external_parties WHERE id = $1 FOR UPDATE`, u).
			Scan(&before.ID, &before.Kind, &before.Name, &before.SponsorUserID, &before.Active, &before.Notes, &before.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock external party: %w", err)
		}
		if before.Version != in.ExpectedVersion {
			return application.ErrVersionConflict
		}
		after := before
		var fields []string
		if in.Kind != nil && *in.Kind != before.Kind {
			after.Kind = *in.Kind
			fields = append(fields, "kind")
		}
		if in.Name != nil && *in.Name != before.Name {
			after.Name = *in.Name
			fields = append(fields, "name")
		}
		if in.SponsorUserID != nil && !strings.EqualFold(*in.SponsorUserID, before.SponsorUserID) {
			if err := requireSponsor(ctx, tx, *in.SponsorUserID); err != nil {
				return err
			}
			after.SponsorUserID = strings.ToLower(*in.SponsorUserID)
			fields = append(fields, "sponsorUserId")
		}
		if in.Notes != nil && *in.Notes != before.Notes {
			after.Notes = *in.Notes
			fields = append(fields, "notes")
		}
		if in.Active != nil && *in.Active != before.Active {
			after.Active = *in.Active
			fields = append(fields, "active")
		}
		if len(fields) == 0 {
			out, err = scanParty(tx.QueryRow(ctx, `
				SELECT `+partyColumns+` FROM organization.external_parties p JOIN organization.users s ON s.id = p.sponsor_user_id WHERE p.id = $1`, u))
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE organization.external_parties
			SET kind = $2, name = $3, sponsor_user_id = $4::uuid, notes = $5, active = $6, version = version + 1, updated_at = now()
			WHERE id = $1`, u, after.Kind, after.Name, after.SponsorUserID, after.Notes, after.Active); err != nil {
			if isUnique(err) {
				return application.ErrPartyNameTaken
			}
			return fmt.Errorf("update external party: %w", err)
		}
		if out, err = scanParty(tx.QueryRow(ctx, `
			SELECT `+partyColumns+` FROM organization.external_parties p JOIN organization.users s ON s.id = p.sponsor_user_id WHERE p.id = $1`, u)); err != nil {
			return fmt.Errorf("reload external party: %w", err)
		}
		if err := r.record(ctx, tx, c, "organization.external_party.updated", "external_party", id, partyState(before), partyState(out),
			map[string]any{"fields": fields}); err != nil {
			return err
		}
		// Participant notifications (sponsor hand-over, deactivation) are made by consumers of these events.
		if after.SponsorUserID != before.SponsorUserID {
			if err := events.Publish(ctx, tx, events.Publication{Type: "ExternalPartySponsorChanged", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
				Payload: map[string]any{"partyId": id, "sponsorUserId": after.SponsorUserID, "previousSponsorUserId": before.SponsorUserID}}); err != nil {
				return err
			}
		}
		if before.Active && !after.Active {
			return events.Publish(ctx, tx, events.Publication{Type: "ExternalPartyDeactivated", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
				Payload: map[string]any{"partyId": id}})
		}
		return nil
	})
	if err != nil {
		return application.ExternalParty{}, wrapParty(err, "update external party")
	}
	return out, nil
}

// wrapParty keeps the module errors intact and wraps everything else with the operation name.
func wrapParty(err error, what string) error {
	for _, known := range []error{application.ErrNotFound, application.ErrVersionConflict, application.ErrSponsorInvalid, application.ErrPartyNameTaken} {
		if errors.Is(err, known) {
			return err
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// requirePartyActive locks the party row FOR SHARE and checks that it exists and is active.
func requirePartyActive(ctx context.Context, tx pgx.Tx, id string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM organization.external_parties WHERE id = $1::uuid FOR SHARE`, id).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check external party: %w", err)
	}
	if !active {
		return application.ErrPartyInactive
	}
	return nil
}

// lockedPartySponsor reads sponsor and activity under one row lock for sponsor authorization.
func lockedPartySponsor(ctx context.Context, tx pgx.Tx, partyID string) (string, bool, error) {
	var sponsor string
	var active bool
	err := tx.QueryRow(ctx, `SELECT sponsor_user_id::text, active FROM organization.external_parties WHERE id = $1::uuid FOR SHARE`, partyID).Scan(&sponsor, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, application.ErrForbidden
	}
	if err != nil {
		return "", false, fmt.Errorf("load sponsor: %w", err)
	}
	return sponsor, active, nil
}
