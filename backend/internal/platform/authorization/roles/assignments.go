package roles

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// AssignInput identifies the role and subject of a new assignment.
type AssignInput struct {
	RoleID      string
	SubjectType string
	SubjectID   string
	// ExpiresAt optionally ends the assignment. It is forbidden for the built-in administrator role and at most
	// MaxHighRiskAssignmentDays away for a role that holds a high-risk permission.
	ExpiresAt       *time.Time
	Acknowledgement Acknowledgement
}

// MaxHighRiskAssignmentDays is the longest assignment of a role that holds a high-risk permission.
const MaxHighRiskAssignmentDays = 366

// RevokeOutcome reports what RevokeAssignment did.
type RevokeOutcome struct {
	// AlreadyRevoked: the assignment was revoked before; nothing changed and
	// no audit entry was written.
	AlreadyRevoked bool
	// LastAdministratorBypassed: the revoked assignment was the last active
	// platform-administrator assignment of a User and the guard was bypassed.
	LastAdministratorBypassed bool
}

const assignmentSelect = `
	SELECT a.id::text, a.role_id::text, r.key, a.subject_type, a.subject_id::text, a.scope,
	       a.created_at, a.created_by->>'userId', a.revoked_at, a.revoked_by->>'userId', a.expires_at
	FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id`

func scanAssignment(row pgx.Row) (Assignment, error) {
	var a Assignment
	var createdBy, revokedBy *string
	if err := row.Scan(&a.ID, &a.RoleID, &a.RoleKey, &a.SubjectType, &a.SubjectID, &a.Scope,
		&a.CreatedAt, &createdBy, &a.RevokedAt, &revokedBy, &a.ExpiresAt); err != nil {
		return Assignment{}, err
	}
	if createdBy != nil {
		a.CreatedBy = *createdBy
	}
	if revokedBy != nil {
		a.RevokedBy = *revokedBy
	}
	return a, nil
}

func assignmentState(a Assignment) map[string]any {
	m := map[string]any{"roleId": a.RoleID, "roleKey": a.RoleKey, "subjectType": a.SubjectType, "subjectId": a.SubjectID, "scope": a.Scope}
	if a.ExpiresAt != nil {
		m["expiresAt"] = a.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return m
}

// AssignRole grants a role to an existing User or non-deleted Directory Group. For a user actor:
//   - nobody assigns to themselves or to a Directory Group they belong to (access.self_assignment);
//   - the actor holds the role or is a platform administrator, because the role also hands over the queue grants
//     and View shares attached to it (access.role_not_held, review rule R2);
//   - the grant ceiling applies to every permission of the role, high-risk roles only by an administrator;
//
// for every actor: the administrator role has no expiry, a high-risk role is not given to a local account (R10) and
// expires within 366 days, and newly created separation-of-duties conflicts need an acknowledgement.
func (s *Service) AssignRole(ctx context.Context, actor audit.Actor, correlationID string, in AssignInput) (Assignment, error) {
	if err := validateActor(actor, correlationID); err != nil {
		return Assignment{}, err
	}
	if !uuidPattern.MatchString(in.RoleID) {
		return Assignment{}, invalid("roleId must be a UUID")
	}
	if in.SubjectType != SubjectUser && in.SubjectType != SubjectDirectoryGroup {
		return Assignment{}, invalid("subjectType must be user or directory_group")
	}
	if !uuidPattern.MatchString(in.SubjectID) {
		return Assignment{}, invalid("subjectId must be a UUID")
	}
	now := s.now().UTC()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return Assignment{}, invalid("expiresAt must be in the future")
	}
	var out Assignment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockRole(ctx, tx, in.RoleID, "SHARE"); err != nil {
			return err
		}
		role, err := loadRole(ctx, tx, "r.id = $1", in.RoleID)
		if err != nil {
			return err
		}
		if in.ExpiresAt != nil {
			if role.BuiltIn {
				return ErrAdminNoExpiry
			}
			if hasHighRisk(role.Permissions) && in.ExpiresAt.After(now.AddDate(0, 0, MaxHighRiskAssignmentDays)) {
				return invalid("a role with high-risk permissions is assigned for at most %d days", MaxHighRiskAssignmentDays)
			}
		}
		var exists bool
		if in.SubjectType == SubjectUser {
			exists, err = s.subjects.UserExists(ctx, in.SubjectID)
		} else {
			exists, err = s.subjects.DirectoryGroupObserved(ctx, in.SubjectID)
		}
		if err != nil {
			return fmt.Errorf("check subject: %w", err)
		}
		if !exists {
			return ErrSubjectNotFound
		}
		a, err := s.actorOf(ctx, tx, actor)
		if err != nil {
			return err
		}
		if err := a.checkNotSelf(in.SubjectType, in.SubjectID); err != nil {
			return err
		}
		if !a.exempt() && !a.holdsRole(in.RoleID) {
			return ErrRoleNotHeld
		}
		if err := a.checkGrant(role.Permissions); err != nil {
			return err
		}
		var target holdings
		if in.SubjectType == SubjectUser {
			if target, err = s.eval.holdingsOf(ctx, tx, in.SubjectID); err != nil {
				return fmt.Errorf("evaluate subject: %w", err)
			}
			local, err := userIsLocal(ctx, tx, in.SubjectID)
			if err != nil {
				return err
			}
			if local && hasHighRisk(role.Permissions) {
				return ErrLocalAccountHighRisk
			}
		}
		// A role the subject holds already (or an administrator) adds no new conflicts; the built-in role is the
		// administrators' own and exempt from separation-of-duties hygiene.
		if !role.BuiltIn && !target.admin {
			combined := permSet(role.Permissions)
			for p := range target.perms {
				combined[p] = struct{}{}
			}
			if err := s.requireAcknowledgement(ctx, tx, actor, correlationID, "role_assignment", in.SubjectID,
				newViolations(target.perms, combined), in.Acknowledgement); err != nil {
				return err
			}
		}
		// An assignment whose expiry passed is still "active" until the job revokes it; it must not block a new one.
		var expiredID string
		err = tx.QueryRow(ctx, `
			UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = $4
			WHERE role_id = $1 AND subject_type = $2 AND subject_id = $3 AND scope = 'global' AND revoked_at IS NULL
			  AND expires_at IS NOT NULL AND expires_at <= now()
			RETURNING id::text`, in.RoleID, in.SubjectType, in.SubjectID, []byte(actorRef(audit.SystemActor("access-expiry")))).Scan(&expiredID)
		switch {
		case err == nil:
			if err := s.record(ctx, tx, audit.SystemActor("access-expiry"), correlationID, "authorization.role.assignment_expired",
				"role_assignment", expiredID, nil, nil, map[string]any{"reason": "replaced_after_expiry"}); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("revoke expired assignment: %w", err)
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO platform.role_assignments(role_id, subject_type, subject_id, created_by, expires_at)
			VALUES ($1,$2,$3,$4,$5) RETURNING id::text`,
			in.RoleID, in.SubjectType, in.SubjectID, []byte(actorRef(actor)), in.ExpiresAt).Scan(&id)
		if isUnique(err) {
			return ErrDuplicateAssignment
		}
		if err != nil {
			return fmt.Errorf("insert assignment: %w", err)
		}
		if out, err = scanAssignment(tx.QueryRow(ctx, assignmentSelect+` WHERE a.id = $1`, id)); err != nil {
			return fmt.Errorf("reload assignment: %w", err)
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.assigned", "role_assignment", id, nil, assignmentState(out), nil)
	})
	if err != nil {
		return Assignment{}, wrap("assign role", err)
	}
	s.name(ctx, &out)
	return out, nil
}

// RevokeAssignment revokes an assignment. Revoking an already revoked
// assignment is a no-op. Unless bypassLastAdministratorGuard is set (CLI only),
// revoking the last active platform-administrator assignment of a User fails
// with ErrLastAdministrator.
func (s *Service) RevokeAssignment(ctx context.Context, actor audit.Actor, correlationID, id string, bypassLastAdministratorGuard bool) (Assignment, RevokeOutcome, error) {
	var outcome RevokeOutcome
	if err := validateActor(actor, correlationID); err != nil {
		return Assignment{}, outcome, err
	}
	if !uuidPattern.MatchString(id) {
		return Assignment{}, outcome, ErrNotFound
	}
	var out Assignment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock order: role first (serializes revocations per role), then the
		// assignment. AssignRole and DeleteRole lock the role first as well.
		var roleID string
		err := tx.QueryRow(ctx, `SELECT role_id::text FROM platform.role_assignments WHERE id = $1`, id).Scan(&roleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("find assignment: %w", err)
		}
		var roleKey string
		if err := tx.QueryRow(ctx, `SELECT key FROM platform.roles WHERE id = $1 FOR UPDATE`, roleID).Scan(&roleKey); err != nil {
			return fmt.Errorf("lock role: %w", err)
		}
		before, err := scanAssignment(tx.QueryRow(ctx, assignmentSelect+` WHERE a.id = $1 FOR UPDATE OF a`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock assignment: %w", err)
		}
		if before.RevokedAt != nil {
			out = before
			outcome.AlreadyRevoked = true
			return nil
		}
		a, err := s.actorOf(ctx, tx, actor)
		if err != nil {
			return err
		}
		if err := a.checkNotSelf(before.SubjectType, before.SubjectID); err != nil {
			return err
		}
		if !a.exempt() {
			role, err := loadRole(ctx, tx, "r.id = $1", roleID)
			if err != nil {
				return err
			}
			if err := a.checkRemoval(role.Permissions); err != nil {
				return err
			}
		}
		extra := map[string]any{}
		if roleKey == AdministratorRoleKey && before.SubjectType == SubjectUser {
			last, err := s.isLastActiveAdministrator(ctx, tx, roleID, before)
			if err != nil {
				return err
			}
			if last {
				if !bypassLastAdministratorGuard {
					return ErrLastAdministrator
				}
				outcome.LastAdministratorBypassed = true
				extra["lastAdministratorGuardBypassed"] = true
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = $2 WHERE id = $1`, id, []byte(actorRef(actor))); err != nil {
			return fmt.Errorf("revoke assignment: %w", err)
		}
		if out, err = scanAssignment(tx.QueryRow(ctx, assignmentSelect+` WHERE a.id = $1`, id)); err != nil {
			return fmt.Errorf("reload assignment: %w", err)
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.assignment_revoked", "role_assignment", id,
			assignmentState(before), assignmentState(out), extra)
	})
	if err != nil {
		return Assignment{}, RevokeOutcome{}, wrap("revoke assignment", err)
	}
	s.name(ctx, &out)
	return out, outcome, nil
}

// ListAssignments lists assignments newest first (descending id).
func (s *Service) ListAssignments(ctx context.Context, f AssignmentFilter) (AssignmentPage, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.RoleID != "" {
		if !uuidPattern.MatchString(f.RoleID) {
			return AssignmentPage{}, invalid("roleId must be a UUID")
		}
		add("a.role_id = $%d", f.RoleID)
	}
	if f.SubjectType != "" {
		if f.SubjectType != SubjectUser && f.SubjectType != SubjectDirectoryGroup {
			return AssignmentPage{}, invalid("subjectType must be user or directory_group")
		}
		add("a.subject_type = $%d", f.SubjectType)
	}
	if f.SubjectID != "" {
		if !uuidPattern.MatchString(f.SubjectID) {
			return AssignmentPage{}, invalid("subjectId must be a UUID")
		}
		add("a.subject_id = $%d", f.SubjectID)
	}
	if !f.IncludeRevoked {
		// An expired assignment grants nothing even before the job revokes it.
		conds = append(conds, "a.revoked_at IS NULL", "(a.expires_at IS NULL OR a.expires_at > now())")
	}
	if page.Cursor != "" {
		if !uuidPattern.MatchString(page.Cursor) {
			return AssignmentPage{}, ErrInvalidCursor
		}
		add("a.id < $%d", page.Cursor)
	}
	sql := assignmentSelect
	for i, c := range conds {
		if i == 0 {
			sql += " WHERE " + c
		} else {
			sql += " AND " + c
		}
	}
	args = append(args, page.Limit+1)
	sql += fmt.Sprintf(" ORDER BY a.id DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return AssignmentPage{}, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()
	var res AssignmentPage
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return AssignmentPage{}, fmt.Errorf("list assignments: scan: %w", err)
		}
		res.Items = append(res.Items, a)
	}
	if err := rows.Err(); err != nil {
		return AssignmentPage{}, fmt.Errorf("list assignments: %w", err)
	}
	if len(res.Items) > page.Limit {
		res.Items = res.Items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	if res.Items == nil {
		res.Items = []Assignment{}
	}
	s.nameAll(ctx, res.Items)
	return res, nil
}

func (s *Service) name(ctx context.Context, a *Assignment) {
	items := []Assignment{*a}
	s.nameAll(ctx, items)
	*a = items[0]
}

// nameAll fills SubjectDisplayName. A lookup failure leaves names empty (the
// field is documented as empty if unknown) rather than failing the request.
func (s *Service) nameAll(ctx context.Context, items []Assignment) {
	var users, groups []string
	for _, a := range items {
		if a.SubjectType == SubjectUser {
			users = append(users, a.SubjectID)
		} else {
			groups = append(groups, a.SubjectID)
		}
	}
	if len(items) == 0 {
		return
	}
	names, err := s.subjects.DisplayNames(ctx, users, groups)
	if err != nil {
		return
	}
	for i := range items {
		items[i].SubjectDisplayName = names[items[i].SubjectID]
	}
}

// isLastActiveAdministrator reports whether revoking target would remove the
// last active platform-administrator assignment of an active User. Only
// assignments whose User is active count: an inactive or departed User cannot
// sign in, so such an assignment does not keep the platform administrable.
// Group assignments never count. The role is locked by the caller, so the set
// of assignments cannot change concurrently.
func (s *Service) isLastActiveAdministrator(ctx context.Context, tx pgx.Tx, roleID string, target Assignment) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT subject_id::text FROM platform.role_assignments
		WHERE role_id = $1 AND subject_type = 'user' AND revoked_at IS NULL AND id <> $2`, roleID, target.ID)
	if err != nil {
		return false, fmt.Errorf("list administrators: %w", err)
	}
	defer rows.Close()
	ids := []string{target.SubjectID}
	var others []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, fmt.Errorf("list administrators: scan: %w", err)
		}
		others = append(others, id)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("list administrators: %w", err)
	}
	rows.Close()
	ids = append(ids, others...)
	active, err := s.subjects.ActiveUsers(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("check active administrators: %w", err)
	}
	if !active[target.SubjectID] {
		// Revoking an assignment of an inactive User loses no active administrator.
		return false, nil
	}
	for _, id := range others {
		if active[id] {
			return false, nil
		}
	}
	return true, nil
}
