package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

var _ application.AuthorizationSubjectStore = (*Repository)(nil)

// GroupIDsOfUser expands the user's currently observed memberships of
// non-deleted groups over currently observed nesting edges to parent groups.
// UNION (not UNION ALL) makes the recursion terminate on cycles.
func (r *Repository) GroupIDsOfUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE g(id) AS (
			SELECT m.group_id
			FROM organization.directory_group_memberships m
			JOIN organization.directory_groups dg ON dg.id = m.group_id
			WHERE m.user_id = $1 AND m.observed_until IS NULL AND dg.deleted_observed_at IS NULL
			UNION
			SELECT n.parent_group_id
			FROM g
			JOIN organization.directory_group_nesting n ON n.child_group_id = g.id AND n.observed_until IS NULL
			JOIN organization.directory_groups pg ON pg.id = n.parent_group_id AND pg.deleted_observed_at IS NULL
		)
		SELECT id::text FROM g ORDER BY 1`, userID)
	if err != nil {
		return nil, fmt.Errorf("group ids of user: %w", err)
	}
	return collectStrings(rows, "group ids of user")
}

// GroupMemberUserIDs expands the groups over currently observed nesting to child groups and returns the
// currently observed members, ordered by id.
func (r *Repository) GroupMemberUserIDs(ctx context.Context, groupIDs []string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE g(id) AS (
			SELECT dg.id FROM organization.directory_groups dg
			WHERE dg.id = ANY($1::text[]::uuid[]) AND dg.deleted_observed_at IS NULL
			UNION
			SELECT n.child_group_id
			FROM g
			JOIN organization.directory_group_nesting n ON n.parent_group_id = g.id AND n.observed_until IS NULL
			JOIN organization.directory_groups cg ON cg.id = n.child_group_id AND cg.deleted_observed_at IS NULL
		)
		SELECT DISTINCT m.user_id::text
		FROM organization.directory_group_memberships m JOIN g ON g.id = m.group_id
		WHERE m.observed_until IS NULL
		ORDER BY 1 LIMIT $2`, groupIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("group member user ids: %w", err)
	}
	return collectStrings(rows, "group member user ids")
}

func collectStrings(rows pgx.Rows, what string) ([]string, error) {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

func (r *Repository) UserExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.users WHERE id = $1)`, id).Scan(&ok); err != nil {
		return false, fmt.Errorf("user exists: %w", err)
	}
	return ok, nil
}

func (r *Repository) DirectoryGroupObserved(ctx context.Context, id string) (bool, error) {
	var ok bool
	if err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM organization.directory_groups WHERE id = $1 AND deleted_observed_at IS NULL)`, id).Scan(&ok); err != nil {
		return false, fmt.Errorf("directory group observed: %w", err)
	}
	return ok, nil
}

func (r *Repository) DisplayNames(ctx context.Context, userIDs, groupIDs []string) (map[string]string, error) {
	if userIDs == nil {
		userIDs = []string{}
	}
	if groupIDs == nil {
		groupIDs = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, display_name FROM organization.users WHERE id = ANY($1::text[]::uuid[])
		UNION ALL
		SELECT id::text, display_name FROM organization.directory_groups WHERE id = ANY($2::text[]::uuid[])`, userIDs, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("display names: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("display names: scan: %w", err)
		}
		out[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("display names: %w", err)
	}
	return out, nil
}

func (r *Repository) ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM organization.users WHERE id = ANY($1::text[]::uuid[]) AND status = 'active'`, ids)
	if err != nil {
		return nil, fmt.Errorf("active users: %w", err)
	}
	active, err := collectStrings(rows, "active users")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(active))
	for _, id := range active {
		out[id] = true
	}
	return out, nil
}

func (r *Repository) UsersByUsername(ctx context.Context, username string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT user_id::text FROM organization.external_identities
		WHERE lower(username) = lower($1) AND deleted_observed_at IS NULL ORDER BY 1 LIMIT 2`, username)
	if err != nil {
		return nil, fmt.Errorf("users by username: %w", err)
	}
	return collectStrings(rows, "users by username")
}

func (r *Repository) UserByEmail(ctx context.Context, email string) (string, bool, error) {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT id::text FROM organization.users WHERE lower(primary_email) = lower($1)`, email).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("user by email: %w", err)
	}
	return id, true, nil
}

// ActiveEmployees returns id -> true for each id that is an active internal employee account (not external, not the
// emergency account). Used where a person may name another person without directory permissions (on-behalf tickets).
func (r *Repository) ActiveEmployees(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM organization.users WHERE id = ANY($1::text[]::uuid[]) AND status = 'active' AND account_kind = 'employee' AND origin <> 'emergency'`, ids)
	if err != nil {
		return nil, fmt.Errorf("active employees: %w", err)
	}
	active, err := collectStrings(rows, "active employees")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(active))
	for _, id := range active {
		out[id] = true
	}
	return out, nil
}

// PrimaryLocationIDs returns user id -> primary Location id for Users that have one.
func (r *Repository) PrimaryLocationIDs(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, primary_location_id::text FROM organization.users WHERE id = ANY($1::text[]::uuid[]) AND primary_location_id IS NOT NULL`, ids)
	if err != nil {
		return nil, fmt.Errorf("primary locations: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, loc string
		if err := rows.Scan(&id, &loc); err != nil {
			return nil, fmt.Errorf("primary locations: scan: %w", err)
		}
		out[id] = loc
	}
	return out, rows.Err()
}

// SearchUserIDs returns the ids of up to limit Users (any status) whose display name or e-mail contains text, by
// name. The text is escaped; the trigram indexes serve texts of 3 or more characters.
func (r *Repository) SearchUserIDs(ctx context.Context, text string, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM organization.users
		WHERE display_name ILIKE '%' || $1 || '%' OR primary_email ILIKE '%' || $1 || '%' ORDER BY display_name, id LIMIT $2`, prefixPattern(text), limit)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	return collectStrings(rows, "search users")
}
