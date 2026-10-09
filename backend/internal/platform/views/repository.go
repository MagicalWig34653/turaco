package views

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is satisfied by pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// viewer is the identity a View's access is resolved for: the User, the Teams the User currently belongs to and the
// roles that apply to the User (directly or through Directory Groups). It is resolved per request, never cached, so
// membership and role changes apply immediately.
type viewer struct {
	userID string
	teams  []string
	roles  []string
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

const viewColumns = `v.id::text, v.resource, v.name, v.description, v.owner_user_id::text, v.definition, v.definition_hash,
	v.visibility, v.version, COALESCE(v.last_edited_by::text, ''), v.archived_at, v.created_at, v.updated_at`

// levelExpr computes the viewer's access to the row aliased v: 'owner', 'edit', 'use' or ” (none). Parameters u, t
// and r are the argument positions of the User id, the Team ids and the role ids.
func levelExpr(u, t, r int) string {
	match := fmt.Sprintf(`(s.subject_type = 'everyone'
		OR (s.subject_type = 'user' AND s.subject_id = $%[1]d::uuid)
		OR (s.subject_type = 'team' AND s.subject_id = ANY($%[2]d::text[]::uuid[]))
		OR (s.subject_type = 'role' AND s.subject_id = ANY($%[3]d::text[]::uuid[])))`, u, t, r)
	return fmt.Sprintf(`CASE WHEN v.owner_user_id = $%[1]d::uuid THEN 'owner'
		WHEN EXISTS (SELECT 1 FROM views.view_shares s WHERE s.view_id = v.id AND s.level = 'edit' AND %[2]s) THEN 'edit'
		WHEN EXISTS (SELECT 1 FROM views.view_shares s WHERE s.view_id = v.id AND %[2]s) THEN 'use'
		ELSE '' END`, u, match)
}

func scanView(row pgx.Row, level *string) (View, error) {
	var v View
	var def []byte
	dest := []any{&v.ID, &v.Resource, &v.Name, &v.Description, &v.OwnerID, &def, &v.Hash, &v.Visibility, &v.Version,
		&v.LastEditedBy, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt}
	if level != nil {
		dest = append(dest, level)
	}
	if err := row.Scan(dest...); err != nil {
		return View{}, err
	}
	d, err := decodeStored(def)
	if err != nil {
		return View{}, err
	}
	v.Definition = d
	return v, nil
}

// roleIDs returns the ids of the roles that apply to the User: active assignments to the User or to one of the
// User's Directory Groups. It reads the assignments read-only, as the permission evaluator does.
func roleIDs(ctx context.Context, q querier, userID string, groupIDs []string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT a.role_id::text
		FROM platform.role_assignments a
		JOIN platform.roles r ON r.id = a.role_id AND r.deleted_at IS NULL
		WHERE a.revoked_at IS NULL AND a.scope = 'global' AND (
			(a.subject_type = 'user' AND a.subject_id = $1::uuid)
			OR (a.subject_type = 'directory_group' AND a.subject_id = ANY($2::text[]::uuid[])))`, userID, nonNil(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("load role ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("load role ids: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// rolesExist returns which of the ids are active roles.
func rolesExist(ctx context.Context, q querier, ids []string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT id::text FROM platform.roles WHERE deleted_at IS NULL AND id = ANY($1::text[]::uuid[])`, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("check roles: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("check roles: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// loadView reads one View with the viewer's access level. lock takes a row lock (FOR UPDATE) for mutations.
func loadView(ctx context.Context, q querier, id string, vw viewer, lock bool) (View, string, error) {
	sql := `SELECT ` + viewColumns + `, ` + levelExpr(2, 3, 4) + ` FROM views.saved_views v WHERE v.id = $1::uuid`
	if lock {
		sql += ` FOR UPDATE OF v`
	}
	var level string
	v, err := scanView(q.QueryRow(ctx, sql, id, vw.userID, nonNil(vw.teams), nonNil(vw.roles)), &level)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, "", ErrNotFound
	}
	if err != nil {
		return View{}, "", fmt.Errorf("load view: %w", err)
	}
	return v, level, nil
}

// loadAccessBatch returns the active Views among ids that the viewer may use, with their level.
func loadAccessBatch(ctx context.Context, q querier, ids []string, vw viewer) (map[string]accessed, error) {
	if len(ids) == 0 {
		return map[string]accessed{}, nil
	}
	rows, err := q.Query(ctx, `SELECT `+viewColumns+`, `+levelExpr(2, 3, 4)+`
		FROM views.saved_views v WHERE v.id = ANY($1::text[]::uuid[]) AND v.archived_at IS NULL`,
		ids, vw.userID, nonNil(vw.teams), nonNil(vw.roles))
	if err != nil {
		return nil, fmt.Errorf("load views: %w", err)
	}
	defer rows.Close()
	out := map[string]accessed{}
	for rows.Next() {
		var level string
		v, err := scanView(rows, &level)
		if err != nil {
			return nil, fmt.Errorf("load views: scan: %w", err)
		}
		if level != "" {
			out[v.ID] = accessed{view: v, level: level}
		}
	}
	return out, rows.Err()
}

type accessed struct {
	view  View
	level string
}

func listShares(ctx context.Context, q querier, viewID string) ([]Share, error) {
	rows, err := q.Query(ctx, `
		SELECT subject_type, COALESCE(subject_id::text, ''), level, granted_by::text, granted_at
		FROM views.view_shares WHERE view_id = $1::uuid ORDER BY subject_type, subject_id`, viewID)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var s Share
		if err := rows.Scan(&s.SubjectType, &s.SubjectID, &s.Level, &s.GrantedBy, &s.GrantedAt); err != nil {
			return nil, fmt.Errorf("list shares: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listFilter is the repository side of GET /views.
type listFilter struct {
	resources []string // usable resources, never empty-meaning-all
	resource  string
	scope     string // mine | shared | all | admin
	archived  bool   // mine and admin only
	afterID   string
	limit     int
}

func listViews(ctx context.Context, q querier, vw viewer, f listFilter) ([]accessed, error) {
	args := []any{vw.userID, nonNil(vw.teams), nonNil(vw.roles), f.resources}
	var where []string
	add := func(cond string, arg any) {
		args = append(args, arg)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	where = append(where, `x.resource = ANY($4::text[])`)
	if f.resource != "" {
		add(`x.resource = ?`, f.resource)
	}
	switch f.scope {
	case "mine":
		where = append(where, `x.lvl = 'owner'`)
	case "shared":
		where = append(where, `x.lvl IN ('edit', 'use')`)
	case "admin":
	default: // all
		where = append(where, `x.lvl <> ''`)
	}
	if f.scope == "mine" || f.scope == "admin" {
		if f.archived {
			where = append(where, `x.archived_at IS NOT NULL`)
		} else {
			where = append(where, `x.archived_at IS NULL`)
		}
	} else {
		where = append(where, `x.archived_at IS NULL`)
	}
	if f.afterID != "" {
		add(`x.id < ?::text::uuid`, f.afterID)
	}
	add(`TRUE`, f.limit+1)
	limitArg := len(args)
	sql := fmt.Sprintf(`
		SELECT x.id::text, x.resource, x.name, x.description, x.owner_user_id::text, x.definition, x.definition_hash,
		       x.visibility, x.version, COALESCE(x.last_edited_by::text, ''), x.archived_at, x.created_at, x.updated_at, x.lvl
		FROM (SELECT v.*, %s AS lvl FROM views.saved_views v) x
		WHERE %s ORDER BY x.id DESC LIMIT $%d`, levelExpr(1, 2, 3), strings.Join(where, " AND "), limitArg)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	defer rows.Close()
	var out []accessed
	for rows.Next() {
		var level string
		v, err := scanView(rows, &level)
		if err != nil {
			return nil, fmt.Errorf("list views: scan: %w", err)
		}
		out = append(out, accessed{view: v, level: level})
	}
	return out, rows.Err()
}

func pinnedSet(ctx context.Context, q querier, userID string, ids []string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT view_id::text FROM views.pins WHERE user_id = $1::uuid AND hidden = false AND view_id = ANY($2::text[]::uuid[])`,
		userID, nonNil(ids))
	if err != nil {
		return nil, fmt.Errorf("load pins: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("load pins: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// purgeArchived hard-deletes Views archived before cutoff (shares, pins and rules cascade) and returns the
// deleted ids and resources, oldest first, at most purgeBatch.
func purgeArchived(ctx context.Context, q querier, cutoff time.Time) ([][2]string, error) {
	rows, err := q.Query(ctx, `
		DELETE FROM views.saved_views WHERE id IN (
			SELECT id FROM views.saved_views WHERE archived_at IS NOT NULL AND archived_at < $1 ORDER BY archived_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id::text, resource`, cutoff, purgeBatch)
	if err != nil {
		return nil, fmt.Errorf("purge archived views: %w", err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, fmt.Errorf("purge archived views: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
