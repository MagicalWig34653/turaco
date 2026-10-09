package repository

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var _ application.QueryStore = (*Repository)(nil)

func (r *Repository) QueryUsers(ctx context.Context, plan *query.Plan) (query.Page[application.User], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: prefixed("u", userColumns)},
		func(rows pgx.Rows, extra []any) (application.User, error) {
			var u application.User
			err := rows.Scan(append([]any{&u.ID, &u.DisplayName, &u.GivenName, &u.FamilyName, &u.PrimaryEmail, &u.Status,
				&u.DepartmentID, &u.PrimaryLocationID, &u.ManagerUserID, &u.UpdatedAt,
				&u.EmployeeNumber, &u.StatusSource, &u.AccountKind, &u.Origin, &u.AccessExpiresAt, &u.Version}, extra...)...)
			return u, err
		})
}

func (r *Repository) QueryTeams(ctx context.Context, plan *query.Plan) (query.Page[application.Team], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: prefixed("t", teamColumns)},
		func(rows pgx.Rows, extra []any) (application.Team, error) {
			var t application.Team
			err := rows.Scan(append([]any{&t.ID, &t.Name, &t.Active, &t.UpdatedAt, &t.Description, &t.Version}, extra...)...)
			return t, err
		})
}

func (r *Repository) QueryLocations(ctx context.Context, plan *query.Plan) (query.Page[application.Location], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: prefixed("l", locationColumns)},
		func(rows pgx.Rows, extra []any) (application.Location, error) {
			var l application.Location
			err := rows.Scan(append([]any{&l.ID, &l.Name, &l.ExternalKey, &l.Active, &l.UpdatedAt, &l.Kind, &l.ParentID, &l.Code, &l.Description, &l.Version}, extra...)...)
			return l, err
		})
}

func (r *Repository) QueryDepartments(ctx context.Context, plan *query.Plan) (query.Page[application.Department], error) {
	return query.Run(ctx, r.pool, plan, query.Select{Columns: prefixed("d", departmentColumns)},
		func(rows pgx.Rows, extra []any) (application.Department, error) {
			var d application.Department
			err := rows.Scan(append([]any{&d.ID, &d.Name, &d.Code, &d.ParentID, &d.ExternalKey, &d.Active, &d.UpdatedAt, &d.Version}, extra...)...)
			return d, err
		})
}

// prefixed qualifies every column of a comma separated column list with the table alias, so the query engine's
// generated FROM clause (organization.users u) resolves them. The lists contain only plain columns and casts.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}
