package roles

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Evaluator evaluates the effective permissions of a User per request
// (no cache): active role assignments of the User and of the User's Directory
// Groups (transitive, via GroupResolver). The built-in administrator role
// yields every registered permission; other roles their stored permissions
// intersected with the registry. It satisfies authentication.PermissionLoader.
type Evaluator struct {
	pool   *pgxpool.Pool
	groups GroupResolver
}

// NewEvaluator creates the evaluator.
func NewEvaluator(pool *pgxpool.Pool, groups GroupResolver) *Evaluator {
	return &Evaluator{pool: pool, groups: groups}
}

// Permissions returns the effective permission set of userID.
func (e *Evaluator) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	if !uuidPattern.MatchString(userID) {
		return out, nil
	}
	groupIDs, err := e.groups.GroupIDsOfUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve groups: %w", err)
	}
	if groupIDs == nil {
		groupIDs = []string{}
	}
	rows, err := e.pool.Query(ctx, `
		SELECT r.built_in,
		       COALESCE((SELECT array_agg(rp.permission) FROM platform.role_permissions rp WHERE rp.role_id = r.id), '{}'::text[])
		FROM platform.roles r
		WHERE r.deleted_at IS NULL AND r.id IN (
			SELECT a.role_id FROM platform.role_assignments a
			WHERE a.revoked_at IS NULL AND a.scope = 'global' AND (
				(a.subject_type = 'user' AND a.subject_id = $1::uuid)
				OR (a.subject_type = 'directory_group' AND a.subject_id = ANY($2::text[]::uuid[])))
		)`, userID, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("load role permissions: %w", err)
	}
	defer rows.Close()
	known := map[string]struct{}{}
	for _, p := range permissions.Registry {
		known[p.Name] = struct{}{}
	}
	for rows.Next() {
		var builtIn bool
		var perms []string
		if err := rows.Scan(&builtIn, &perms); err != nil {
			return nil, fmt.Errorf("load role permissions: scan: %w", err)
		}
		if builtIn {
			for p := range known {
				out[p] = struct{}{}
			}
			continue
		}
		for _, p := range perms {
			if _, ok := known[p]; ok {
				out[p] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load role permissions: %w", err)
	}
	return out, nil
}
