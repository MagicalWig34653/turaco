// Package repository implements the Assets store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
)

// Repository stores assets and their assignments.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = `id::text, reference, product_id::text, serial_number, asset_tag, status, status_reason, provisioning_status,
	ownership_type, supplier_id::text, source_type, source_id::text, purchased_at, warranty_until, location_id::text, notes,
	version, created_at, updated_at`

func scan(row pgx.Row) (application.Asset, error) {
	var a application.Asset
	err := row.Scan(&a.ID, &a.Reference, &a.ProductID, &a.SerialNumber, &a.AssetTag, &a.Status, &a.StatusReason, &a.ProvisioningStatus,
		&a.OwnershipType, &a.SupplierID, &a.SourceType, &a.SourceID, &a.PurchasedAt, &a.WarrantyUntil, &a.LocationID, &a.Notes,
		&a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, n application.NewAsset) (application.Asset, error) {
	a, err := scan(tx.QueryRow(ctx, `
		INSERT INTO assets.assets (product_id, serial_number, asset_tag, status, ownership_type, supplier_id, source_type, source_id,
			purchased_at, warranty_until, location_id, notes)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8::uuid, $9, $10, $11::uuid, $12)
		RETURNING `+columns,
		n.ProductID, n.SerialNumber, n.AssetTag, n.Status, n.OwnershipType, n.SupplierID, n.SourceType, n.SourceID,
		n.PurchasedAt, n.WarrantyUntil, n.LocationID, n.Notes))
	if pgCode(err) == "23505" {
		return application.Asset{}, application.ErrConflict
	}
	if err != nil {
		return application.Asset{}, fmt.Errorf("insert asset: %w", err)
	}
	return a, nil
}

func (r *Repository) LockTx(ctx context.Context, tx pgx.Tx, id string) (application.Asset, error) {
	if !validUUID(id) {
		return application.Asset{}, application.ErrNotFound
	}
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM assets.assets WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Asset{}, application.ErrNotFound
	}
	if err != nil {
		return application.Asset{}, fmt.Errorf("lock asset: %w", err)
	}
	return a, nil
}

func (r *Repository) UpdateTx(ctx context.Context, tx pgx.Tx, a application.Asset) (application.Asset, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE assets.assets SET serial_number = $2, asset_tag = $3, status = $4, status_reason = $5, provisioning_status = $6,
			ownership_type = $7, purchased_at = $8, warranty_until = $9, location_id = $10::uuid, notes = $11,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+columns,
		a.ID, a.SerialNumber, a.AssetTag, a.Status, a.StatusReason, a.ProvisioningStatus,
		a.OwnershipType, a.PurchasedAt, a.WarrantyUntil, a.LocationID, a.Notes))
	if pgCode(err) == "23505" {
		return application.Asset{}, application.ErrConflict
	}
	if err != nil {
		return application.Asset{}, fmt.Errorf("update asset: %w", err)
	}
	return out, nil
}

const assignmentColumns = `id::text, asset_id::text, assignee_type, assignee_id::text, assigned_at, assigned_by::text, returned_at, note`

func scanAssignment(row pgx.Row) (application.Assignment, error) {
	var a application.Assignment
	err := row.Scan(&a.ID, &a.AssetID, &a.AssigneeType, &a.AssigneeID, &a.AssignedAt, &a.AssignedBy, &a.ReturnedAt, &a.Note)
	return a, err
}

func (r *Repository) ActiveAssignmentTx(ctx context.Context, tx pgx.Tx, assetID string) (*application.Assignment, error) {
	a, err := scanAssignment(tx.QueryRow(ctx, `SELECT `+assignmentColumns+` FROM assets.asset_assignments WHERE asset_id = $1::uuid AND returned_at IS NULL`, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("active assignment: %w", err)
	}
	return &a, nil
}

func (r *Repository) OpenAssignmentTx(ctx context.Context, tx pgx.Tx, n application.Assignment) (application.Assignment, error) {
	a, err := scanAssignment(tx.QueryRow(ctx, `
		INSERT INTO assets.asset_assignments (asset_id, assignee_type, assignee_id, assigned_at, assigned_by, note)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5::uuid, $6) RETURNING `+assignmentColumns,
		n.AssetID, n.AssigneeType, n.AssigneeID, n.AssignedAt, n.AssignedBy, n.Note))
	if pgCode(err) == "23505" {
		return application.Assignment{}, application.ErrConflict
	}
	if err != nil {
		return application.Assignment{}, fmt.Errorf("open assignment: %w", err)
	}
	return a, nil
}

func (r *Repository) CloseAssignmentTx(ctx context.Context, tx pgx.Tx, assetID string, at time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE assets.asset_assignments SET returned_at = greatest($2, assigned_at) WHERE asset_id = $1::uuid AND returned_at IS NULL`, assetID, at)
	if err != nil {
		return false, fmt.Errorf("close assignment: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Asset, error) {
	if !validUUID(id) {
		return application.Asset{}, application.ErrNotFound
	}
	a, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM assets.assets WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Asset{}, application.ErrNotFound
	}
	if err != nil {
		return application.Asset{}, fmt.Errorf("get asset: %w", err)
	}
	return a, nil
}

func (r *Repository) Lookup(ctx context.Context, code string) (application.Asset, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+columns+` FROM assets.assets
		WHERE lower(asset_tag) = lower($1) OR lower(serial_number) = lower($1) OR reference = upper($1)
		ORDER BY id LIMIT 3`, code)
	if err != nil {
		return application.Asset{}, fmt.Errorf("lookup asset: %w", err)
	}
	defer rows.Close()
	var found []application.Asset
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return application.Asset{}, fmt.Errorf("lookup asset: scan: %w", err)
		}
		found = append(found, a)
	}
	if err := rows.Err(); err != nil {
		return application.Asset{}, fmt.Errorf("lookup asset: %w", err)
	}
	switch len(found) {
	case 0:
		return application.Asset{}, application.ErrNotFound
	case 1:
		return found[0], nil
	}
	return application.Asset{}, application.ErrConflict
}

func (r *Repository) BySerial(ctx context.Context, serial string) ([]application.Asset, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM assets.assets WHERE lower(serial_number) = lower($1) ORDER BY id LIMIT 2`, serial)
	if err != nil {
		return nil, fmt.Errorf("assets by serial: %w", err)
	}
	defer rows.Close()
	out := []application.Asset{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("assets by serial: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func prefixPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

func (r *Repository) List(ctx context.Context, f application.Filter) (application.Result, error) {
	page := f.Page.Normalize()
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("a.status = $%d", f.Status)
	}
	if validUUID(f.ProductID) {
		add("a.product_id = $%d::uuid", f.ProductID)
	} else if f.ProductID != "" {
		return application.Result{Items: []application.Asset{}}, nil
	}
	if validUUID(f.LocationID) {
		add("a.location_id = $%d::uuid", f.LocationID)
	} else if f.LocationID != "" {
		return application.Result{Items: []application.Asset{}}, nil
	}
	if f.AssigneeID != "" {
		if !validUUID(f.AssigneeID) {
			return application.Result{Items: []application.Asset{}}, nil
		}
		add("EXISTS (SELECT 1 FROM assets.asset_assignments s WHERE s.asset_id = a.id AND s.returned_at IS NULL AND s.assignee_id = $%d::uuid)", f.AssigneeID)
	}
	if f.AssignedToUser != "" {
		if !validUUID(f.AssignedToUser) {
			return application.Result{Items: []application.Asset{}}, nil
		}
		add("EXISTS (SELECT 1 FROM assets.asset_assignments s WHERE s.asset_id = a.id AND s.returned_at IS NULL AND s.assignee_type = 'user' AND s.assignee_id = $%d::uuid)", f.AssignedToUser)
	}
	if f.Query != "" {
		args = append(args, prefixPattern(strings.ToLower(f.Query)))
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(lower(a.reference) LIKE $%[1]d OR lower(a.serial_number) LIKE $%[1]d OR lower(a.asset_tag) LIKE $%[1]d)`, n))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		add("a.id > $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM assets.assets a%s ORDER BY a.id LIMIT $%d`, prefixColumns("a."), where, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()
	items := make([]application.Asset, 0, page.Limit+1)
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list assets: scan: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list assets: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// prefixColumns qualifies the column list with a table alias.
func prefixColumns(alias string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func (r *Repository) Assignments(ctx context.Context, assetID string) ([]application.Assignment, error) {
	if !validUUID(assetID) {
		return []application.Assignment{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+assignmentColumns+` FROM assets.asset_assignments WHERE asset_id = $1::uuid ORDER BY assigned_at DESC, id DESC LIMIT 200`, assetID)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()
	out := []application.Assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("list assignments: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}

// validUUIDs drops malformed ids so a bad id never turns into a cast error for the whole lookup.
func validUUIDs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		if validUUID(id) {
			out = append(out, id)
		}
	}
	return out
}

// MaxHeldAssetsRows bounds AssetsHeldByUsers whatever the caller asks for.
const MaxHeldAssetsRows = 5000

func (r *Repository) UserHolders(ctx context.Context, assetIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if assetIDs = validUUIDs(assetIDs); len(assetIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT asset_id::text, assignee_id::text FROM assets.asset_assignments
		WHERE asset_id = ANY($1::uuid[]) AND returned_at IS NULL AND assignee_type = 'user'`, assetIDs)
	if err != nil {
		return nil, fmt.Errorf("user holders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var asset, user string
		if err := rows.Scan(&asset, &user); err != nil {
			return nil, fmt.Errorf("user holders: scan: %w", err)
		}
		out[asset] = user
	}
	return out, rows.Err()
}

func (r *Repository) AssetsHeldByUsers(ctx context.Context, userIDs []string, limit int) (map[string][]string, error) {
	out := map[string][]string{}
	if userIDs = validUUIDs(userIDs); len(userIDs) == 0 || limit <= 0 {
		return out, nil
	}
	limit = min(limit, MaxHeldAssetsRows)
	rows, err := r.pool.Query(ctx, `
		SELECT assignee_id::text, asset_id::text FROM assets.asset_assignments
		WHERE assignee_id = ANY($1::uuid[]) AND returned_at IS NULL AND assignee_type = 'user'
		ORDER BY assignee_id, asset_id LIMIT $2`, userIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("assets held by users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var user, asset string
		if err := rows.Scan(&user, &asset); err != nil {
			return nil, fmt.Errorf("assets held by users: scan: %w", err)
		}
		out[user] = append(out[user], asset)
	}
	return out, rows.Err()
}
