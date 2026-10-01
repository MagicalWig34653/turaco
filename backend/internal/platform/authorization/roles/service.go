package roles

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Service implements role and role-assignment operations. Every mutation runs
// in one transaction together with its audit event; actors are audit.Actor
// values (audit.UserActor for sessions, audit.CLIActor for turaco-admin) and
// every mutation takes the correlation id of its request or invocation.
type Service struct {
	pool     *pgxpool.Pool
	subjects SubjectDirectory
}

// NewService creates a Service over pool; subjects validates and names
// assignment subjects.
func NewService(pool *pgxpool.Pool, subjects SubjectDirectory) *Service {
	return &Service{pool: pool, subjects: subjects}
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ListPermissions returns the registry sorted by name.
func (s *Service) ListPermissions() []permissions.Permission {
	out := append([]permissions.Permission(nil), permissions.Registry...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func registryNames() []string {
	names := make([]string, 0, len(permissions.Registry))
	for _, p := range permissions.Registry {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

// normalizePermissions validates names against the registry, removes
// duplicates and sorts.
func normalizePermissions(in []string) ([]string, error) {
	known := map[string]struct{}{}
	for _, p := range permissions.Registry {
		known[p.Name] = struct{}{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	var unknown []string
	for _, p := range in {
		if _, ok := known[p]; !ok {
			unknown = append(unknown, p)
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		if len(unknown) > 10 {
			unknown = unknown[:10]
		}
		return nil, &UnknownPermissionError{Names: unknown}
	}
	sort.Strings(out)
	return out, nil
}

const roleSelect = `
	SELECT r.id::text, r.key, r.name, r.description, r.built_in, r.created_at, r.updated_at,
	       COALESCE((SELECT array_agg(rp.permission ORDER BY rp.permission) FROM platform.role_permissions rp WHERE rp.role_id = r.id), '{}'::text[]),
	       (SELECT count(*) FROM platform.role_assignments a WHERE a.role_id = r.id AND a.revoked_at IS NULL)
	FROM platform.roles r`

func scanRole(row pgx.Row) (Role, error) {
	var r Role
	var perms []string
	var n int64
	if err := row.Scan(&r.ID, &r.Key, &r.Name, &r.Description, &r.BuiltIn, &r.CreatedAt, &r.UpdatedAt, &perms, &n); err != nil {
		return Role{}, err
	}
	r.ActiveAssignments = int(n)
	if r.BuiltIn {
		r.Permissions = registryNames()
	} else {
		// Stored permissions that are no longer registered are ignored.
		eff, err := intersectRegistry(perms)
		if err != nil {
			return Role{}, err
		}
		r.Permissions = eff
	}
	return r, nil
}

func intersectRegistry(perms []string) ([]string, error) {
	known := map[string]struct{}{}
	for _, p := range permissions.Registry {
		known[p.Name] = struct{}{}
	}
	out := []string{}
	for _, p := range perms {
		if _, ok := known[p]; ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func loadRole(ctx context.Context, q querier, where string, arg any) (Role, error) {
	// Deleted roles are kept for assignment history but are invisible here.
	r, err := scanRole(q.QueryRow(ctx, roleSelect+" WHERE r.deleted_at IS NULL AND "+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("load role: %w", err)
	}
	return r, nil
}

// lockRole locks the live role row in its own statement and returns
// ErrNotFound for unknown or deleted roles. Callers load the role (and its
// assignment count) in a later statement: under READ COMMITTED each statement
// takes a fresh snapshot, so what is read after the lock includes everything
// committed by transactions that held the lock before. A single
// "SELECT ... FOR UPDATE" would count assignments from the statement's
// pre-lock snapshot and miss an AssignRole that committed while it waited.
func lockRole(ctx context.Context, tx pgx.Tx, id, mode string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM platform.roles WHERE id = $1 AND deleted_at IS NULL FOR `+mode, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock role: %w", err)
	}
	return nil
}

// lockAndLoadRole takes the exclusive role lock, then loads the role.
func lockAndLoadRole(ctx context.Context, tx pgx.Tx, id string) (Role, error) {
	if err := lockRole(ctx, tx, id, "UPDATE"); err != nil {
		return Role{}, err
	}
	return loadRole(ctx, tx, "r.id = $1", id)
}

// ListRoles returns all roles sorted by name.
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx, roleSelect+` WHERE r.deleted_at IS NULL ORDER BY lower(r.name), r.id`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, fmt.Errorf("list roles: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return out, nil
}

// GetRole returns one role; ErrNotFound for unknown or malformed ids.
func (s *Service) GetRole(ctx context.Context, id string) (Role, error) {
	if !uuidPattern.MatchString(id) {
		return Role{}, ErrNotFound
	}
	return loadRole(ctx, s.pool, "r.id = $1", id)
}

// GetRoleByKey returns one role by its key.
func (s *Service) GetRoleByKey(ctx context.Context, key string) (Role, error) {
	return loadRole(ctx, s.pool, "r.key = $1", key)
}

// CreateRoleInput are the fields of a new custom role.
type CreateRoleInput struct {
	Key         string
	Name        string
	Description string
	Permissions []string
}

// CreateRole creates a custom role.
func (s *Service) CreateRole(ctx context.Context, actor audit.Actor, correlationID string, in CreateRoleInput) (Role, error) {
	if err := validateActor(actor, correlationID); err != nil {
		return Role{}, err
	}
	if !keyPattern.MatchString(in.Key) {
		return Role{}, invalid("key must match ^[a-z0-9][a-z0-9-]{1,62}$")
	}
	name, err := validateName(in.Name)
	if err != nil {
		return Role{}, err
	}
	if err := validateDescription(in.Description); err != nil {
		return Role{}, err
	}
	perms, err := normalizePermissions(in.Permissions)
	if err != nil {
		return Role{}, err
	}
	var out Role
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO platform.roles(key, name, description) VALUES ($1,$2,$3) RETURNING id::text`,
			in.Key, name, in.Description).Scan(&id)
		if isUnique(err) {
			return ErrDuplicateKey
		}
		if err != nil {
			return fmt.Errorf("insert role: %w", err)
		}
		for _, p := range perms {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(role_id, permission) VALUES ($1,$2)`, id, p); err != nil {
				return fmt.Errorf("insert role permission: %w", err)
			}
		}
		if out, err = loadRole(ctx, tx, "r.id = $1", id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.created", "role", id, nil, roleState(out), nil)
	})
	if err != nil {
		return Role{}, wrap("create role", err)
	}
	return out, nil
}

// UpdateRoleInput changes name and/or description; nil fields stay unchanged.
type UpdateRoleInput struct {
	Name        *string
	Description *string
}

// UpdateRole renames or redescribes a custom role.
func (s *Service) UpdateRole(ctx context.Context, actor audit.Actor, correlationID, id string, in UpdateRoleInput) (Role, error) {
	if err := validateActor(actor, correlationID); err != nil {
		return Role{}, err
	}
	if !uuidPattern.MatchString(id) {
		return Role{}, ErrNotFound
	}
	if in.Name == nil && in.Description == nil {
		return Role{}, invalid("name or description is required")
	}
	if in.Name != nil {
		n, err := validateName(*in.Name)
		if err != nil {
			return Role{}, err
		}
		in.Name = &n
	}
	if in.Description != nil {
		if err := validateDescription(*in.Description); err != nil {
			return Role{}, err
		}
	}
	var out Role
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := lockAndLoadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.BuiltIn {
			return ErrBuiltInRole
		}
		if _, err := tx.Exec(ctx, `
			UPDATE platform.roles SET name = COALESCE($2, name), description = COALESCE($3, description), updated_at = now()
			WHERE id = $1`, id, in.Name, in.Description); err != nil {
			return fmt.Errorf("update role: %w", err)
		}
		if out, err = loadRole(ctx, tx, "r.id = $1", id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.updated", "role", id, roleState(before), roleState(out), nil)
	})
	if err != nil {
		return Role{}, wrap("update role", err)
	}
	return out, nil
}

// SetRolePermissions replaces the permissions of a custom role.
func (s *Service) SetRolePermissions(ctx context.Context, actor audit.Actor, correlationID, id string, perms []string) (Role, error) {
	if err := validateActor(actor, correlationID); err != nil {
		return Role{}, err
	}
	if !uuidPattern.MatchString(id) {
		return Role{}, ErrNotFound
	}
	norm, err := normalizePermissions(perms)
	if err != nil {
		return Role{}, err
	}
	var out Role
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := lockAndLoadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.BuiltIn {
			return ErrBuiltInRole
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.role_permissions WHERE role_id = $1`, id); err != nil {
			return fmt.Errorf("clear role permissions: %w", err)
		}
		for _, p := range norm {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(role_id, permission) VALUES ($1,$2)`, id, p); err != nil {
				return fmt.Errorf("insert role permission: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.roles SET updated_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("touch role: %w", err)
		}
		if out, err = loadRole(ctx, tx, "r.id = $1", id); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.permissions_changed", "role", id,
			map[string]any{"permissions": before.Permissions}, map[string]any{"permissions": out.Permissions}, nil)
	})
	if err != nil {
		return Role{}, wrap("set role permissions", err)
	}
	return out, nil
}

// DeleteRole soft-deletes a custom role that has no active assignments. The
// role row and its revoked assignments are kept as history (they reference
// the role, and the role key becomes free for a new role); the audit log
// records authorization.role.deleted.
func (s *Service) DeleteRole(ctx context.Context, actor audit.Actor, correlationID, id string) error {
	if err := validateActor(actor, correlationID); err != nil {
		return err
	}
	if !uuidPattern.MatchString(id) {
		return ErrNotFound
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := lockAndLoadRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.BuiltIn {
			return ErrBuiltInRole
		}
		if before.ActiveAssignments > 0 {
			return ErrRoleInUse
		}
		// Soft delete: the role and its revoked assignments stay as history;
		// the key becomes free for a new role.
		if _, err := tx.Exec(ctx, `UPDATE platform.roles SET deleted_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete role: %w", err)
		}
		return s.record(ctx, tx, actor, correlationID, "authorization.role.deleted", "role", id, roleState(before), nil, nil)
	})
	return wrap("delete role", err)
}

func roleState(r Role) map[string]any {
	return map[string]any{"key": r.Key, "name": r.Name, "description": r.Description, "permissions": r.Permissions}
}

// record writes the audit event of a mutation inside tx.
func (s *Service) record(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, action, targetType, targetID string, before, after any, extra map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID, Actor: actor, CorrelationID: correlationID,
		Before: before, After: after, Metadata: extra,
	})
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// wrap adds context to unexpected errors and passes domain errors through.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	var inv *InvalidError
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrSubjectNotFound), errors.Is(err, ErrUnknownPermission),
		errors.Is(err, ErrDuplicateKey), errors.Is(err, ErrBuiltInRole), errors.Is(err, ErrRoleInUse),
		errors.Is(err, ErrDuplicateAssignment), errors.Is(err, ErrLastAdministrator), errors.Is(err, ErrInvalidCursor),
		errors.As(err, &inv):
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
