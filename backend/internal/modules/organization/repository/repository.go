// Package repository implements the Organization read port on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// Repository reads Organization data with explicit SQL.
type Repository struct {
	pool     *pgxpool.Pool
	guards   application.AccessGuards
	sessions application.SessionRevoker
	counters []application.ReferenceCounter
	issuer   application.CredentialIssuer
	mailer   application.CredentialMailer
}

// WithCredentials returns a copy that can issue invitation and reset links.
func (r *Repository) WithCredentials(i application.CredentialIssuer, m application.CredentialMailer) *Repository {
	c := *r
	c.issuer, c.mailer = i, m
	return &c
}

// WithGuards returns a copy of the repository whose people operations enforce the access guards (dominance,
// last administrator) and end sessions and credential tokens through revoker. Without them the operations that
// need them fail closed.
func (r *Repository) WithGuards(g application.AccessGuards, revoker application.SessionRevoker) *Repository {
	c := *r
	c.guards, c.sessions = g, revoker
	return &c
}

// WithReferenceCounters returns a copy that also counts references to Locations held by other modules (through
// their public contracts) when a Location is deactivated.
func (r *Repository) WithReferenceCounters(c ...application.ReferenceCounter) *Repository {
	cp := *r
	cp.counters = append([]application.ReferenceCounter(nil), c...)
	return &cp
}

var _ application.Reader = (*Repository)(nil)

// New creates a Repository over pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// parseID validates a UUID string and returns its canonical form.
func parseID(s string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if len(s) != 36 {
		return u, false
	}
	if err := u.Scan(s); err != nil || !u.Valid {
		return u, false
	}
	return u, true
}

func parseCursor(c string) (pgtype.UUID, error) {
	if c == "" {
		return pgtype.UUID{}, nil
	}
	u, ok := parseID(c)
	if !ok {
		return pgtype.UUID{}, application.ErrInvalidCursor
	}
	return u, nil
}

// prefixPattern escapes LIKE metacharacters so the query is a literal prefix.
func prefixPattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

// listQuery builds "<base> [AND ...] AND id > $n ORDER BY id LIMIT n" style suffixes.
type listQuery struct {
	conds []string
	args  []any
}

func (q *listQuery) add(cond string, arg any) {
	q.args = append(q.args, arg)
	q.conds = append(q.conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(q.args))))
}

func (q *listQuery) where() string {
	if len(q.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(q.conds, " AND ")
}

// keyset applies the cursor condition on column col and returns the limit argument index.
func (q *listQuery) keyset(col string, cursor pgtype.UUID, limit int) string {
	if cursor.Valid {
		q.add(col+" > ?", cursor)
	}
	q.args = append(q.args, limit+1)
	return fmt.Sprintf(" ORDER BY %s LIMIT $%d", col, len(q.args))
}

// collect scans rows and applies the limit+1 keyset rule.
func collect[T any](rows pgx.Rows, limit int, scan func(pgx.Rows) (T, string, error)) (application.Result[T], error) {
	defer rows.Close()
	var res application.Result[T]
	var lastID string
	extra := false
	for rows.Next() {
		if len(res.Items) == limit {
			extra = true
			break
		}
		item, id, err := scan(rows)
		if err != nil {
			return application.Result[T]{}, fmt.Errorf("scan row: %w", err)
		}
		res.Items = append(res.Items, item)
		lastID = id
	}
	if err := rows.Err(); err != nil {
		return application.Result[T]{}, fmt.Errorf("iterate rows: %w", err)
	}
	if extra {
		res.NextCursor = lastID
	}
	return res, nil
}

func getOne[T any](ctx context.Context, r *Repository, what, id, sql string, scan func(pgx.Row) (T, error)) (T, error) {
	var zero T
	u, ok := parseID(id)
	if !ok {
		return zero, application.ErrNotFound
	}
	v, err := scan(r.pool.QueryRow(ctx, sql, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrNotFound
	}
	if err != nil {
		return zero, fmt.Errorf("get %s: %w", what, err)
	}
	return v, nil
}

func (r *Repository) exists(ctx context.Context, sql string, id string) error {
	u, ok := parseID(id)
	if !ok {
		return application.ErrNotFound
	}
	var one int
	err := r.pool.QueryRow(ctx, sql, u).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	return nil
}

// ---- users ----

const userColumns = `id::text, display_name, given_name, family_name, primary_email, status,
	department_id::text, primary_location_id::text, manager_user_id::text, updated_at,
	employee_number, status_source, account_kind, origin, access_expires_at, version`

func scanUser(row pgx.Row) (application.User, error) {
	var u application.User
	err := row.Scan(&u.ID, &u.DisplayName, &u.GivenName, &u.FamilyName, &u.PrimaryEmail, &u.Status,
		&u.DepartmentID, &u.PrimaryLocationID, &u.ManagerUserID, &u.UpdatedAt,
		&u.EmployeeNumber, &u.StatusSource, &u.AccountKind, &u.Origin, &u.AccessExpiresAt, &u.Version)
	return u, err
}

func (r *Repository) ListUsers(ctx context.Context, f application.UserFilter) (application.Result[application.User], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.User]{}, err
	}
	var q listQuery
	if f.Query != "" {
		// Any part of the name or the e-mail address matches (surname, second given name, "Dr. Brandt"), not only the
		// start of the display name. Served by the trigram indexes users_display_name_trgm_idx and users_primary_email_trgm_idx.
		q.add(`(display_name ILIKE '%' || ? || '%' OR primary_email ILIKE '%' || ? || '%')`, prefixPattern(f.Query))
	}
	if f.Status != "" {
		q.add(`status = ?`, f.Status)
	}
	suffix := q.keyset("id", cur, p.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+userColumns+` FROM organization.users`+q.where()+suffix, q.args...)
	if err != nil {
		return application.Result[application.User]{}, fmt.Errorf("list users: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.User, string, error) {
		u, err := scanUser(rows)
		return u, u.ID, err
	})
}

func (r *Repository) GetUser(ctx context.Context, id string) (application.User, error) {
	return getOne(ctx, r, "user", id, `SELECT `+userColumns+` FROM organization.users WHERE id = $1`, scanUser)
}

// ---- teams ----

const teamColumns = `id::text, name, active, updated_at, description, version`

func scanTeam(row pgx.Row) (application.Team, error) {
	var t application.Team
	err := row.Scan(&t.ID, &t.Name, &t.Active, &t.UpdatedAt, &t.Description, &t.Version)
	return t, err
}

func (r *Repository) ListTeams(ctx context.Context, f application.NameFilter) (application.Result[application.Team], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.Team]{}, err
	}
	var q listQuery
	if f.Query != "" {
		q.add(`lower(name) LIKE lower(?) || '%'`, prefixPattern(f.Query))
	}
	suffix := q.keyset("id", cur, p.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+teamColumns+` FROM organization.teams`+q.where()+suffix, q.args...)
	if err != nil {
		return application.Result[application.Team]{}, fmt.Errorf("list teams: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.Team, string, error) {
		t, err := scanTeam(rows)
		return t, t.ID, err
	})
}

func (r *Repository) GetTeam(ctx context.Context, id string) (application.Team, error) {
	return getOne(ctx, r, "team", id, `SELECT `+teamColumns+` FROM organization.teams WHERE id = $1`, scanTeam)
}

// ListTeamMembers returns current members of a team, one row per user, paginated by user id.
// An unknown team yields ErrNotFound.
func (r *Repository) ListTeamMembers(ctx context.Context, teamID string, p application.Page) (application.Result[application.TeamMember], error) {
	p = p.Normalize()
	if err := r.exists(ctx, `SELECT 1 FROM organization.teams WHERE id = $1`, teamID); err != nil {
		return application.Result[application.TeamMember]{}, err
	}
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.TeamMember]{}, err
	}
	team, _ := parseID(teamID)
	// $1 = team; cursor and limit follow.
	args := []any{team}
	cursorCond := ""
	if cur.Valid {
		args = append(args, cur)
		cursorCond = " AND tm.user_id > $2"
	}
	args = append(args, p.Limit+1)
	sql := `SELECT tm.user_id::text, u.display_name, tm.role, tm.source, tm.valid_from
FROM (
	SELECT DISTINCT ON (user_id) user_id, role, source, valid_from
	FROM organization.team_memberships
	WHERE team_id = $1 AND valid_from <= now() AND (valid_until IS NULL OR valid_until > now())
	ORDER BY user_id, valid_from DESC
) tm
JOIN organization.users u ON u.id = tm.user_id
WHERE true` + cursorCond + fmt.Sprintf(` ORDER BY tm.user_id LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return application.Result[application.TeamMember]{}, fmt.Errorf("list team members: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.TeamMember, string, error) {
		var m application.TeamMember
		err := rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.Source, &m.ValidFrom)
		return m, m.UserID, err
	})
}

// ListTeamLeads returns the current leads of a Team ordered by user id (at most 50).
func (r *Repository) ListTeamLeads(ctx context.Context, teamID string) ([]application.TeamMember, error) {
	if err := r.exists(ctx, `SELECT 1 FROM organization.teams WHERE id = $1`, teamID); err != nil {
		return nil, err
	}
	team, _ := parseID(teamID)
	rows, err := r.pool.Query(ctx, `
		SELECT tm.user_id::text, u.display_name, tm.role, tm.source, tm.valid_from
		FROM organization.team_memberships tm JOIN organization.users u ON u.id = tm.user_id
		WHERE tm.team_id = $1 AND tm.role = 'lead' AND tm.valid_from <= now() AND (tm.valid_until IS NULL OR tm.valid_until > now())
		ORDER BY tm.user_id LIMIT 50`, team)
	if err != nil {
		return nil, fmt.Errorf("list team leads: %w", err)
	}
	defer rows.Close()
	out := []application.TeamMember{}
	for rows.Next() {
		var m application.TeamMember
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.Source, &m.ValidFrom); err != nil {
			return nil, fmt.Errorf("list team leads: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- locations ----

const locationColumns = `id::text, name, external_key, active, updated_at, kind, parent_location_id::text, code, description, version`

func scanLocation(row pgx.Row) (application.Location, error) {
	var l application.Location
	err := row.Scan(&l.ID, &l.Name, &l.ExternalKey, &l.Active, &l.UpdatedAt, &l.Kind, &l.ParentID, &l.Code, &l.Description, &l.Version)
	return l, err
}

const departmentColumns = `id::text, name, code, parent_department_id::text, external_key, active, updated_at, version`

func scanDepartment(row pgx.Row) (application.Department, error) {
	var d application.Department
	err := row.Scan(&d.ID, &d.Name, &d.Code, &d.ParentID, &d.ExternalKey, &d.Active, &d.UpdatedAt, &d.Version)
	return d, err
}

func (r *Repository) ListDepartments(ctx context.Context, f application.NameFilter) (application.Result[application.Department], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.Department]{}, err
	}
	var q listQuery
	if f.Query != "" {
		q.add(`lower(name) LIKE lower(?) || '%'`, prefixPattern(f.Query))
	}
	suffix := q.keyset("id", cur, p.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+departmentColumns+` FROM organization.departments`+q.where()+suffix, q.args...)
	if err != nil {
		return application.Result[application.Department]{}, fmt.Errorf("list departments: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.Department, string, error) {
		d, err := scanDepartment(rows)
		return d, d.ID, err
	})
}

func (r *Repository) GetDepartment(ctx context.Context, id string) (application.Department, error) {
	return getOne(ctx, r, "department", id, `SELECT `+departmentColumns+` FROM organization.departments WHERE id = $1`, scanDepartment)
}

func (r *Repository) ListLocations(ctx context.Context, f application.NameFilter) (application.Result[application.Location], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.Location]{}, err
	}
	var q listQuery
	if f.Query != "" {
		q.add(`lower(name) LIKE lower(?) || '%'`, prefixPattern(f.Query))
	}
	suffix := q.keyset("id", cur, p.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+locationColumns+` FROM organization.locations`+q.where()+suffix, q.args...)
	if err != nil {
		return application.Result[application.Location]{}, fmt.Errorf("list locations: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.Location, string, error) {
		l, err := scanLocation(rows)
		return l, l.ID, err
	})
}

func (r *Repository) GetLocation(ctx context.Context, id string) (application.Location, error) {
	return getOne(ctx, r, "location", id, `SELECT `+locationColumns+` FROM organization.locations WHERE id = $1`, scanLocation)
}

// ---- directory groups ----

const groupColumns = `id::text, provider_key, external_id, display_name, description,
	first_observed_at, last_observed_at, deleted_observed_at`

func scanGroup(row pgx.Row) (application.DirectoryGroup, error) {
	var g application.DirectoryGroup
	err := row.Scan(&g.ID, &g.ProviderKey, &g.ExternalID, &g.DisplayName, &g.Description,
		&g.FirstObservedAt, &g.LastObservedAt, &g.DeletedObservedAt)
	return g, err
}

func (r *Repository) ListDirectoryGroups(ctx context.Context, f application.NameFilter) (application.Result[application.DirectoryGroup], error) {
	p := f.Page.Normalize()
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.DirectoryGroup]{}, err
	}
	var q listQuery
	if f.Query != "" {
		q.add(`lower(display_name) LIKE lower(?) || '%'`, prefixPattern(f.Query))
	}
	suffix := q.keyset("id", cur, p.Limit)
	rows, err := r.pool.Query(ctx, `SELECT `+groupColumns+` FROM organization.directory_groups`+q.where()+suffix, q.args...)
	if err != nil {
		return application.Result[application.DirectoryGroup]{}, fmt.Errorf("list directory groups: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.DirectoryGroup, string, error) {
		g, err := scanGroup(rows)
		return g, g.ID, err
	})
}

func (r *Repository) GetDirectoryGroup(ctx context.Context, id string) (application.DirectoryGroup, error) {
	return getOne(ctx, r, "directory group", id, `SELECT `+groupColumns+` FROM organization.directory_groups WHERE id = $1`, scanGroup)
}

// ListDirectoryGroupMembers returns the currently observed members of a group (open intervals). An unknown group yields ErrNotFound.
func (r *Repository) ListDirectoryGroupMembers(ctx context.Context, groupID string, p application.Page) (application.Result[application.DirectoryGroupMember], error) {
	p = p.Normalize()
	if err := r.exists(ctx, `SELECT 1 FROM organization.directory_groups WHERE id = $1`, groupID); err != nil {
		return application.Result[application.DirectoryGroupMember]{}, err
	}
	cur, err := parseCursor(p.Cursor)
	if err != nil {
		return application.Result[application.DirectoryGroupMember]{}, err
	}
	group, _ := parseID(groupID)
	args := []any{group}
	cursorCond := ""
	if cur.Valid {
		args = append(args, cur)
		cursorCond = " AND m.user_id > $2"
	}
	args = append(args, p.Limit+1)
	sql := `SELECT m.user_id::text, u.display_name, m.observed_from, m.last_observed_at
FROM organization.directory_group_memberships m
JOIN organization.users u ON u.id = m.user_id
WHERE m.group_id = $1 AND m.observed_until IS NULL` + cursorCond + fmt.Sprintf(` ORDER BY m.user_id LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return application.Result[application.DirectoryGroupMember]{}, fmt.Errorf("list directory group members: %w", err)
	}
	return collect(rows, p.Limit, func(rows pgx.Rows) (application.DirectoryGroupMember, string, error) {
		var m application.DirectoryGroupMember
		err := rows.Scan(&m.UserID, &m.DisplayName, &m.ObservedFrom, &m.LastObservedAt)
		return m, m.UserID, err
	})
}
