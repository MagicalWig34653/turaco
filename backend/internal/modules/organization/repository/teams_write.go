package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

var _ application.TeamStore = (*Repository)(nil)

const teamTarget = "team"

type teamState struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func (r *Repository) recordTeam(ctx context.Context, tx pgx.Tx, c application.Caller, action, teamID string, before, after any, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: teamTarget, TargetID: teamID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

// lockTeam locks the team row FOR UPDATE. Membership changes and state
// changes of one team are serialized through it.
func lockTeam(ctx context.Context, tx pgx.Tx, id string) (application.Team, error) {
	u, ok := parseID(id)
	if !ok {
		return application.Team{}, application.ErrNotFound
	}
	t, err := scanTeam(tx.QueryRow(ctx, `
		SELECT id::text, name, active, updated_at FROM organization.teams WHERE id = $1 FOR UPDATE`, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Team{}, application.ErrNotFound
	}
	if err != nil {
		return application.Team{}, fmt.Errorf("lock team: %w", err)
	}
	return t, nil
}

func (r *Repository) CreateTeam(ctx context.Context, c application.Caller, name string) (application.Team, error) {
	var out application.Team
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanTeam(tx.QueryRow(ctx, `
			INSERT INTO organization.teams(name) VALUES ($1)
			RETURNING id::text, name, active, updated_at`, name))
		if isUnique(err) {
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("insert team: %w", err)
		}
		return r.recordTeam(ctx, tx, c, "organization.team.created", out.ID, nil, teamState{out.Name, out.Active}, nil)
	})
	return finish(out, err, "create team")
}

func (r *Repository) RenameTeam(ctx context.Context, c application.Caller, id, name string) (application.Team, error) {
	var out application.Team
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockTeam(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Name == name {
			out = before
			return nil
		}
		out, err = scanTeam(tx.QueryRow(ctx, `
			UPDATE organization.teams SET name = $2, updated_at = now() WHERE id = $1
			RETURNING id::text, name, active, updated_at`, id, name))
		if isUnique(err) {
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("rename team: %w", err)
		}
		return r.recordTeam(ctx, tx, c, "organization.team.renamed", id,
			teamState{before.Name, before.Active}, teamState{out.Name, out.Active}, nil)
	})
	return finish(out, err, "rename team")
}

func (r *Repository) SetTeamActive(ctx context.Context, c application.Caller, id string, active bool) (application.Team, error) {
	var out application.Team
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockTeam(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Active == active {
			return application.ErrConflict
		}
		out, err = scanTeam(tx.QueryRow(ctx, `
			UPDATE organization.teams SET active = $2, updated_at = now() WHERE id = $1
			RETURNING id::text, name, active, updated_at`, id, active))
		if isUnique(err) { // reactivating a name an active team now uses
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("set team active: %w", err)
		}
		action := "organization.team.deactivated"
		if active {
			action = "organization.team.activated"
		}
		return r.recordTeam(ctx, tx, c, action, id, teamState{before.Name, before.Active}, teamState{out.Name, out.Active}, nil)
	})
	return finish(out, err, "set team active")
}

func (r *Repository) AddTeamMember(ctx context.Context, c application.Caller, teamID, userID string, role *string) (application.TeamMember, error) {
	var out application.TeamMember
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		team, err := lockTeam(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if !team.Active {
			return application.ErrTeamInactive
		}
		uid, ok := parseID(userID)
		if !ok {
			return application.ErrNotFound
		}
		// No row lock on the user: a lock would wait for a whole directory sync
		// transaction while holding the team lock, and a later deactivation
		// leaves the same state anyway. The membership insert's foreign key
		// still prevents deleting the user.
		var name, status string
		err = tx.QueryRow(ctx, `SELECT display_name, status FROM organization.users WHERE id = $1`, uid).Scan(&name, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if status != "active" {
			return application.ErrUserNotActive
		}
		out = application.TeamMember{UserID: userID, DisplayName: name, Role: role, Source: "platform"}
		err = tx.QueryRow(ctx, `
			INSERT INTO organization.team_memberships(team_id, user_id, role, source, valid_from)
			VALUES ($1, $2, $3, 'platform', clock_timestamp()) RETURNING valid_from`, teamID, uid, role).Scan(&out.ValidFrom)
		if isUnique(err) {
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("insert membership: %w", err)
		}
		return r.recordTeam(ctx, tx, c, "organization.team.member_added", teamID, nil,
			map[string]any{"userId": userID, "role": role}, nil)
	})
	return finish(out, err, "add team member")
}

func (r *Repository) RemoveTeamMember(ctx context.Context, c application.Caller, teamID, userID string) error {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := lockTeam(ctx, tx, teamID); err != nil {
			return err
		}
		uid, ok := parseID(userID)
		if !ok {
			return application.ErrNotFound
		}
		var role *string
		err := tx.QueryRow(ctx, `
			UPDATE organization.team_memberships SET valid_until = greatest(valid_from, clock_timestamp())
			WHERE team_id = $1 AND user_id = $2 AND valid_until IS NULL
			RETURNING role`, teamID, uid).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("end membership: %w", err)
		}
		return r.recordTeam(ctx, tx, c, "organization.team.member_removed", teamID,
			map[string]any{"userId": userID, "role": role}, nil, nil)
	})
	if errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrConflict) {
		return err
	}
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	return nil
}

// finish passes domain errors through unchanged and wraps everything else.
func finish[T any](v T, err error, what string) (T, error) {
	var zero T
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, application.ErrNotFound), errors.Is(err, application.ErrConflict),
		errors.Is(err, application.ErrUserNotActive), errors.Is(err, application.ErrTeamInactive):
		return zero, err
	default:
		return zero, fmt.Errorf("%s: %w", what, err)
	}
}
