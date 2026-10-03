package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

var _ application.DirectoryGraphStore = (*Repository)(nil)

func (r *Repository) groupRefs(ctx context.Context, where string, arg []string) ([]application.DirectoryGroupRef, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, provider_key, external_id, display_name, last_observed_at
		FROM organization.directory_groups WHERE `+where+` AND deleted_observed_at IS NULL
		ORDER BY external_id, last_observed_at DESC, id`, arg)
	if err != nil {
		return nil, fmt.Errorf("directory groups: %w", err)
	}
	defer rows.Close()
	var out []application.DirectoryGroupRef
	for rows.Next() {
		var g application.DirectoryGroupRef
		if err := rows.Scan(&g.ID, &g.ProviderKey, &g.ExternalID, &g.DisplayName, &g.LastObservedAt); err != nil {
			return nil, fmt.Errorf("directory groups: scan: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) GroupsByExternalIDs(ctx context.Context, externalIDs []string) ([]application.DirectoryGroupRef, error) {
	return r.groupRefs(ctx, `external_id = ANY($1::text[])`, externalIDs)
}

func (r *Repository) GroupsByIDs(ctx context.Context, ids []string) ([]application.DirectoryGroupRef, error) {
	return r.groupRefs(ctx, `id = ANY($1::text[]::uuid[])`, ids)
}

// nesting walks the current nesting edges from the groups; UNION makes the recursion stop on cycles.
func (r *Repository) nesting(ctx context.Context, up bool, groupIDs []string, limit int) ([]application.GroupNestingEdge, error) {
	from := "child_group_id"
	if !up {
		from = "parent_group_id"
	}
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH RECURSIVE walk(child_id, parent_id) AS (
			SELECT n.child_group_id, n.parent_group_id
			FROM organization.directory_group_nesting n
			JOIN organization.directory_groups c ON c.id = n.child_group_id AND c.deleted_observed_at IS NULL
			JOIN organization.directory_groups p ON p.id = n.parent_group_id AND p.deleted_observed_at IS NULL
			WHERE n.observed_until IS NULL AND n.%[1]s = ANY($1::text[]::uuid[])
			UNION
			SELECT n.child_group_id, n.parent_group_id
			FROM walk w
			JOIN organization.directory_group_nesting n ON n.%[1]s = w.%[2]s_id AND n.observed_until IS NULL
			JOIN organization.directory_groups c ON c.id = n.child_group_id AND c.deleted_observed_at IS NULL
			JOIN organization.directory_groups p ON p.id = n.parent_group_id AND p.deleted_observed_at IS NULL
		)
		SELECT child_id::text, parent_id::text FROM walk ORDER BY 1, 2 LIMIT $2`,
		from, map[bool]string{true: "parent", false: "child"}[up]), groupIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("group nesting: %w", err)
	}
	defer rows.Close()
	var out []application.GroupNestingEdge
	for rows.Next() {
		var e application.GroupNestingEdge
		if err := rows.Scan(&e.ChildID, &e.ParentID); err != nil {
			return nil, fmt.Errorf("group nesting: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) NestingUp(ctx context.Context, groupIDs []string, limit int) ([]application.GroupNestingEdge, error) {
	return r.nesting(ctx, true, groupIDs, limit)
}

func (r *Repository) NestingDown(ctx context.Context, groupIDs []string, limit int) ([]application.GroupNestingEdge, error) {
	return r.nesting(ctx, false, groupIDs, limit)
}

func scanGroupUserMemberships(rows pgx.Rows, what string) ([]application.GroupUserMembership, error) {
	defer rows.Close()
	var out []application.GroupUserMembership
	for rows.Next() {
		var m application.GroupUserMembership
		if err := rows.Scan(&m.UserID, &m.GroupID, &m.LastObservedAt); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

func (r *Repository) UserMemberships(ctx context.Context, userIDs []string, limit int) ([]application.GroupUserMembership, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.user_id::text, m.group_id::text, m.last_observed_at
		FROM organization.directory_group_memberships m
		JOIN organization.directory_groups g ON g.id = m.group_id AND g.deleted_observed_at IS NULL
		WHERE m.user_id = ANY($1::text[]::uuid[]) AND m.observed_until IS NULL
		ORDER BY m.user_id, m.group_id LIMIT $2`, userIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("user memberships: %w", err)
	}
	return scanGroupUserMemberships(rows, "user memberships")
}

func (r *Repository) GroupMembers(ctx context.Context, groupIDs []string, limit int) ([]application.GroupUserMembership, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.user_id::text, m.group_id::text, m.last_observed_at
		FROM organization.directory_group_memberships m
		JOIN organization.directory_groups g ON g.id = m.group_id AND g.deleted_observed_at IS NULL
		WHERE m.group_id = ANY($1::text[]::uuid[]) AND m.observed_until IS NULL
		ORDER BY m.user_id, m.group_id LIMIT $2`, groupIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("group members: %w", err)
	}
	return scanGroupUserMemberships(rows, "group members")
}
