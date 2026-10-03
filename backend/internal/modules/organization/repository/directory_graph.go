package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

var _ application.DirectoryGraphStore = (*Repository)(nil)

// Every list method asks for limit+1 rows: the extra row only tells the caller that the result was cut.

func (r *Repository) groupRefs(ctx context.Context, where string, providerKey string, arg []string, limit int) ([]application.DirectoryGroupRef, bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, provider_key, external_id, display_name, last_observed_at
		FROM organization.directory_groups WHERE `+where+` AND provider_key = $2 AND deleted_observed_at IS NULL
		ORDER BY external_id, last_observed_at DESC, id LIMIT $3`, arg, providerKey, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("directory groups: %w", err)
	}
	defer rows.Close()
	var out []application.DirectoryGroupRef
	for rows.Next() {
		var g application.DirectoryGroupRef
		if err := rows.Scan(&g.ID, &g.ProviderKey, &g.ExternalID, &g.DisplayName, &g.LastObservedAt); err != nil {
			return nil, false, fmt.Errorf("directory groups: scan: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	out, truncated := cut(out, limit)
	return out, truncated, nil
}

func cut[T any](in []T, limit int) ([]T, bool) {
	if len(in) > limit {
		return in[:limit], true
	}
	return in, false
}

func (r *Repository) GroupsByExternalIDs(ctx context.Context, providerKey string, externalIDs []string, limit int) ([]application.DirectoryGroupRef, bool, error) {
	return r.groupRefs(ctx, `external_id = ANY($1::text[])`, providerKey, externalIDs, limit)
}

func (r *Repository) GroupsByIDs(ctx context.Context, providerKey string, ids []string, limit int) ([]application.DirectoryGroupRef, bool, error) {
	return r.groupRefs(ctx, `id = ANY($1::text[]::uuid[])`, providerKey, ids, limit)
}

// nesting walks the current nesting edges from the groups. UNION makes the recursion stop on cycles, the depth
// column caps it at MaxNestingDepth (a row one level deeper only proves that the walk was cut), and the LIMIT on
// the outer query lets PostgreSQL stop producing rows early. Rows are sorted and deduplicated in Go.
func (r *Repository) nesting(ctx context.Context, up bool, providerKey string, groupIDs []string, limit int) ([]application.GroupNestingEdge, bool, error) {
	from, next := "child_group_id", "parent"
	if !up {
		from, next = "parent_group_id", "child"
	}
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH RECURSIVE walk(child_id, parent_id, depth) AS (
			SELECT n.child_group_id, n.parent_group_id, 1
			FROM organization.directory_group_nesting n
			JOIN organization.directory_groups c ON c.id = n.child_group_id AND c.deleted_observed_at IS NULL AND c.provider_key = $3
			JOIN organization.directory_groups p ON p.id = n.parent_group_id AND p.deleted_observed_at IS NULL AND p.provider_key = $3
			WHERE n.observed_until IS NULL AND n.%[1]s = ANY($1::text[]::uuid[])
			UNION
			SELECT n.child_group_id, n.parent_group_id, w.depth + 1
			FROM walk w
			JOIN organization.directory_group_nesting n ON n.%[1]s = w.%[2]s_id AND n.observed_until IS NULL
			JOIN organization.directory_groups c ON c.id = n.child_group_id AND c.deleted_observed_at IS NULL AND c.provider_key = $3
			JOIN organization.directory_groups p ON p.id = n.parent_group_id AND p.deleted_observed_at IS NULL AND p.provider_key = $3
			WHERE w.depth <= $4
		)
		SELECT child_id::text, parent_id::text, depth FROM walk LIMIT $2`,
		from, next), groupIDs, limit+1, providerKey, application.MaxNestingDepth)
	if err != nil {
		return nil, false, fmt.Errorf("group nesting: %w", err)
	}
	defer rows.Close()
	seen := map[application.GroupNestingEdge]bool{}
	var out []application.GroupNestingEdge
	var deeper []application.GroupNestingEdge
	truncated, raw := false, 0
	for rows.Next() {
		var e application.GroupNestingEdge
		var depth int
		if err := rows.Scan(&e.ChildID, &e.ParentID, &depth); err != nil {
			return nil, false, fmt.Errorf("group nesting: scan: %w", err)
		}
		raw++
		switch {
		case depth > application.MaxNestingDepth:
			deeper = append(deeper, e)
		case !seen[e]:
			seen[e] = true
			out = append(out, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	// A cycle revisits known edges beyond the depth cap; only an edge not seen within the cap proves a cut walk.
	for _, e := range deeper {
		if !seen[e] {
			truncated = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChildID != out[j].ChildID {
			return out[i].ChildID < out[j].ChildID
		}
		return out[i].ParentID < out[j].ParentID
	})
	out, cutRows := cut(out, limit)
	return out, truncated || cutRows || raw > limit, nil
}

func (r *Repository) NestingUp(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]application.GroupNestingEdge, bool, error) {
	return r.nesting(ctx, true, providerKey, groupIDs, limit)
}

func (r *Repository) NestingDown(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]application.GroupNestingEdge, bool, error) {
	return r.nesting(ctx, false, providerKey, groupIDs, limit)
}

func scanGroupUserMemberships(rows pgx.Rows, what string, limit int) ([]application.GroupUserMembership, bool, error) {
	defer rows.Close()
	var out []application.GroupUserMembership
	for rows.Next() {
		var m application.GroupUserMembership
		if err := rows.Scan(&m.UserID, &m.GroupID, &m.LastObservedAt); err != nil {
			return nil, false, fmt.Errorf("%s: scan: %w", what, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("%s: %w", what, err)
	}
	out, truncated := cut(out, limit)
	return out, truncated, nil
}

func (r *Repository) UserMemberships(ctx context.Context, providerKey string, userIDs []string, limit int) ([]application.GroupUserMembership, bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.user_id::text, m.group_id::text, m.last_observed_at
		FROM organization.directory_group_memberships m
		JOIN organization.directory_groups g ON g.id = m.group_id AND g.deleted_observed_at IS NULL AND g.provider_key = $2
		WHERE m.user_id = ANY($1::text[]::uuid[]) AND m.observed_until IS NULL
		ORDER BY m.user_id, m.group_id LIMIT $3`, userIDs, providerKey, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("user memberships: %w", err)
	}
	return scanGroupUserMemberships(rows, "user memberships", limit)
}

// GroupMembers is ordered by (group id, user id), the order of the current-membership unique index.
func (r *Repository) GroupMembers(ctx context.Context, providerKey string, groupIDs []string, limit int) ([]application.GroupUserMembership, bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.user_id::text, m.group_id::text, m.last_observed_at
		FROM organization.directory_group_memberships m
		JOIN organization.directory_groups g ON g.id = m.group_id AND g.deleted_observed_at IS NULL AND g.provider_key = $2
		WHERE m.group_id = ANY($1::text[]::uuid[]) AND m.observed_until IS NULL
		ORDER BY m.group_id, m.user_id LIMIT $3`, groupIDs, providerKey, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("group members: %w", err)
	}
	return scanGroupUserMemberships(rows, "group members", limit)
}

func (r *Repository) UsersWithIdentity(ctx context.Context, providerKey string, userIDs []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT user_id::text FROM organization.external_identities
		WHERE user_id = ANY($1::text[]::uuid[]) AND provider_key = $2 AND deleted_observed_at IS NULL`, userIDs, providerKey)
	if err != nil {
		return nil, fmt.Errorf("users with identity: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("users with identity: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
