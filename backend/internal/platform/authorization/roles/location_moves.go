package roles

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Location moves are access-relevant (ADR-0040, F16 design L10, slice L-C). Organization owns the Location tree and
// the move operation; it asks the role service, through these narrow reads on Guards, how many scoped assignments a
// move touches and who the platform administrators are. Organization computes which Locations are affected; the
// role service never reads the tree for this.

// maxAffectedLocations bounds one ScopedAssignmentsAffectedBy call. A move touches the moved subtree plus the
// ancestors of the old and the new position, a few dozen Locations in a four-level tree.
const maxAffectedLocations = 10000

// ScopedAssignmentsAffectedBy counts the active, unexpired Location-scoped role assignments whose scope Location is
// one of locationIDs (malformed ids are ignored). It runs in the caller's transaction; the caller holds the Location
// tree lock, which every scoped assignment and revocation takes in shared mode before it commits (LockLocationPath),
// so the count includes every assignment that could still be in flight.
func (g *Guards) ScopedAssignmentsAffectedBy(ctx context.Context, tx pgx.Tx, locationIDs []string) (int, error) {
	ids := make([]string, 0, len(locationIDs))
	for _, id := range locationIDs {
		if uuidPattern.MatchString(id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > maxAffectedLocations {
		return 0, fmt.Errorf("count scoped assignments: %d Locations exceed the limit of %d", len(ids), maxAffectedLocations)
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM platform.role_assignments
		WHERE scope = 'location' AND scope_location_id = ANY($1::text[]::uuid[]) AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`, ids).Scan(&n); err != nil {
		return 0, fmt.Errorf("count scoped assignments: %w", err)
	}
	return n, nil
}

// maxAdministratorRecipients bounds the administrators AdministratorUserIDs returns.
const maxAdministratorRecipients = 500

// AdministratorUserIDs returns the active Users who hold the built-in administrator role through a global, active,
// unexpired assignment, directly or through a Directory Group they currently belong to, ordered by id and at most
// maxAdministratorRecipients. External accounts are left out (the evaluator never makes them administrators). It is
// the recipient list of administrator notifications; it grants nothing.
func (g *Guards) AdministratorUserIDs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.subject_type, a.subject_id::text
		FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id AND r.built_in AND r.deleted_at IS NULL
		WHERE r.key = $1 AND a.scope = 'global' AND a.revoked_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > now())
		ORDER BY a.subject_id`, AdministratorRoleKey)
	if err != nil {
		return nil, fmt.Errorf("list administrators: %w", err)
	}
	var users, groups []string
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list administrators: scan: %w", err)
		}
		if typ == SubjectUser {
			users = append(users, id)
		} else {
			groups = append(groups, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list administrators: %w", err)
	}
	if len(groups) > 0 {
		members, err := g.subjects.GroupMemberUserIDs(ctx, groups, maxAdministratorRecipients)
		if err != nil {
			return nil, fmt.Errorf("list administrator group members: %w", err)
		}
		users = append(users, members...)
	}
	slices.Sort(users)
	users = slices.Compact(users)
	active, err := g.subjects.ActiveUsers(ctx, users)
	if err != nil {
		return nil, fmt.Errorf("check active administrators: %w", err)
	}
	out := []string{}
	for _, id := range users {
		if !active[id] {
			continue
		}
		external, err := g.subjects.IsExternalAccount(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("check administrator account kind: %w", err)
		}
		if external {
			continue
		}
		out = append(out, id)
		if len(out) == maxAdministratorRecipients {
			break
		}
	}
	return out, nil
}
