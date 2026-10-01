package authorization

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AssignInput identifies the role and subject of a new assignment.
type AssignInput struct {
	RoleID      string
	SubjectType string
	SubjectID   string
}

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
	       a.created_at, a.created_by->>'userId', a.revoked_at, a.revoked_by->>'userId'
	FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id`

func scanAssignment(row pgx.Row) (Assignment, error) {
	var a Assignment
	var createdBy, revokedBy *string
	if err := row.Scan(&a.ID, &a.RoleID, &a.RoleKey, &a.SubjectType, &a.SubjectID, &a.Scope,
		&a.CreatedAt, &createdBy, &a.RevokedAt, &revokedBy); err != nil {
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
	return map[string]any{"roleId": a.RoleID, "roleKey": a.RoleKey, "subjectType": a.SubjectType, "subjectId": a.SubjectID, "scope": a.Scope}
}

// AssignRole grants a role to an existing User or non-deleted Directory Group.
func (s *Service) AssignRole(ctx context.Context, actor Actor, in AssignInput) (Assignment, error) {
	if err := actor.validate(); err != nil {
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
	var out Assignment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadRole(ctx, tx, "r.id = $1", in.RoleID, " FOR SHARE OF r"); err != nil {
			return err
		}
		var exists bool
		var err error
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
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO platform.role_assignments(role_id, subject_type, subject_id, created_by)
			VALUES ($1,$2,$3,$4) RETURNING id::text`,
			in.RoleID, in.SubjectType, in.SubjectID, []byte(actor.ref())).Scan(&id)
		if isUnique(err) {
			return ErrDuplicateAssignment
		}
		if err != nil {
			return fmt.Errorf("insert assignment: %w", err)
		}
		if out, err = scanAssignment(tx.QueryRow(ctx, assignmentSelect+` WHERE a.id = $1`, id)); err != nil {
			return fmt.Errorf("reload assignment: %w", err)
		}
		return s.audit(ctx, tx, actor, "authorization.role.assigned", "role_assignment", id, nil, assignmentState(out), nil)
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
func (s *Service) RevokeAssignment(ctx context.Context, actor Actor, id string, bypassLastAdministratorGuard bool) (Assignment, RevokeOutcome, error) {
	var outcome RevokeOutcome
	if err := actor.validate(); err != nil {
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
		extra := map[string]any{}
		if roleKey == AdministratorRoleKey && before.SubjectType == SubjectUser {
			var others int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM platform.role_assignments
				WHERE role_id = $1 AND subject_type = 'user' AND revoked_at IS NULL AND id <> $2`, roleID, id).Scan(&others); err != nil {
				return fmt.Errorf("count administrators: %w", err)
			}
			if others == 0 {
				if !bypassLastAdministratorGuard {
					return ErrLastAdministrator
				}
				outcome.LastAdministratorBypassed = true
				extra["lastAdministratorGuardBypassed"] = true
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = $2 WHERE id = $1`, id, []byte(actor.ref())); err != nil {
			return fmt.Errorf("revoke assignment: %w", err)
		}
		if out, err = scanAssignment(tx.QueryRow(ctx, assignmentSelect+` WHERE a.id = $1`, id)); err != nil {
			return fmt.Errorf("reload assignment: %w", err)
		}
		return s.audit(ctx, tx, actor, "authorization.role.assignment_revoked", "role_assignment", id,
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
		conds = append(conds, "a.revoked_at IS NULL")
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
