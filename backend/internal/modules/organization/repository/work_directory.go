package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

var _ application.WorkDirectoryStore = (*Repository)(nil)

func (r *Repository) idNameMap(ctx context.Context, sql, what string, ids []string) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, sql, ids)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

func (r *Repository) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	return r.idNameMap(ctx, `SELECT id::text, display_name FROM organization.users WHERE id = ANY($1::text[]::uuid[])`, "user names", ids)
}

func (r *Repository) TeamNames(ctx context.Context, ids []string) (map[string]string, error) {
	return r.idNameMap(ctx, `SELECT id::text, name FROM organization.teams WHERE id = ANY($1::text[]::uuid[])`, "team names", ids)
}

func (r *Repository) ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM organization.teams WHERE id = ANY($1::text[]::uuid[]) AND active`, ids)
	if err != nil {
		return nil, fmt.Errorf("active teams: %w", err)
	}
	active, err := collectStrings(rows, "active teams")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(active))
	for _, id := range active {
		out[id] = true
	}
	return out, nil
}

func (r *Repository) ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM organization.locations WHERE id = ANY($1::text[]::uuid[]) AND active`, ids)
	if err != nil {
		return nil, fmt.Errorf("active locations: %w", err)
	}
	active, err := collectStrings(rows, "active locations")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(active))
	for _, id := range active {
		out[id] = true
	}
	return out, nil
}

func (r *Repository) LocationNames(ctx context.Context, ids []string) (map[string]string, error) {
	return r.idNameMap(ctx, `SELECT id::text, name FROM organization.locations WHERE id = ANY($1::text[]::uuid[])`, "location names", ids)
}

func (r *Repository) CurrentTeamIDs(ctx context.Context, userID string) ([]string, error) {
	var rows pgx.Rows
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT tm.team_id::text
		FROM organization.team_memberships tm
		JOIN organization.teams t ON t.id = tm.team_id AND t.active
		WHERE tm.user_id = $1::uuid AND tm.valid_from <= now() AND (tm.valid_until IS NULL OR tm.valid_until > now())
		ORDER BY 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("current team ids: %w", err)
	}
	return collectStrings(rows, "current team ids")
}

func (r *Repository) CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT tm.user_id::text
		FROM organization.team_memberships tm
		JOIN organization.teams t ON t.id = tm.team_id AND t.active
		WHERE tm.team_id = $1::uuid AND tm.valid_from <= now() AND (tm.valid_until IS NULL OR tm.valid_until > now())
		ORDER BY 1 LIMIT $2`, teamID, application.MaxTeamMembers)
	if err != nil {
		return nil, fmt.Errorf("current member ids: %w", err)
	}
	return collectStrings(rows, "current member ids")
}

func (r *Repository) Contacts(ctx context.Context, ids []string) (map[string]application.Contact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, display_name, coalesce(primary_email, ''), status = 'active'
		FROM organization.users WHERE id = ANY($1::text[]::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	defer rows.Close()
	out := map[string]application.Contact{}
	for rows.Next() {
		var id string
		var c application.Contact
		if err := rows.Scan(&id, &c.DisplayName, &c.Email, &c.Active); err != nil {
			return nil, fmt.Errorf("contacts: scan: %w", err)
		}
		out[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("contacts: %w", err)
	}
	return out, nil
}

func (r *Repository) ManagerIDs(ctx context.Context, userIDs []string) (map[string]string, error) {
	return r.idNameMap(ctx, `SELECT id::text, manager_user_id::text FROM organization.users WHERE id = ANY($1::text[]::uuid[]) AND manager_user_id IS NOT NULL`, "manager ids", userIDs)
}
