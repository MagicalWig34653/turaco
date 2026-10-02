// Package repository implements the Products store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Repository stores manufacturers, categories and products.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func pgCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

// mapWrite turns constraint violations into domain errors.
func mapWrite(err error, what string) error {
	switch pgCode(err) {
	case "23505":
		return application.ErrConflict
	case "23503":
		return application.ErrReferenceNotFound
	case "":
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func record(ctx context.Context, tx pgx.Tx, c application.Caller, action, target, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: target, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

// change locks one row, applies decide and stores the new state with its audit event.
func change[T any](ctx context.Context, r *Repository, c application.Caller, id, target, selectSQL, updateSQL string,
	scan func(pgx.Row) (T, error), args func(T) []any, state func(T) any,
	decide func(T) (application.Change[T], error)) (T, error) {
	var zero, out T
	if !validUUID(id) {
		return zero, application.ErrNotFound
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, selectSQL+` WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock %s: %w", target, err)
		}
		ch, err := decide(cur)
		if err != nil {
			return err
		}
		if ch.NoChange {
			out = cur
			return nil
		}
		out, err = scan(tx.QueryRow(ctx, updateSQL, append([]any{id}, args(ch.Next)...)...))
		if err != nil {
			return mapWrite(err, "update "+target)
		}
		return record(ctx, tx, c, ch.Action, target, id, state(cur), state(out), ch.Metadata)
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

func listPage[T any](ctx context.Context, r *Repository, table, columns string, conds []string, args []any, page application.Page,
	scan func(pgx.Row) (T, error), id func(T) string) (application.Result[T], error) {
	page = page.Normalize()
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[T]{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds = append(conds, fmt.Sprintf("id > $%d::uuid", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY id LIMIT $%d`, columns, table, where, len(args)), args...)
	if err != nil {
		return application.Result[T]{}, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()
	items := make([]T, 0, page.Limit+1)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return application.Result[T]{}, fmt.Errorf("list %s: scan: %w", table, err)
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return application.Result[T]{}, fmt.Errorf("list %s: %w", table, err)
	}
	res := application.Result[T]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = id(res.Items[page.Limit-1])
	}
	return res, nil
}

func prefixPattern(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
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

// ---- manufacturers ----

const manufacturerCols = `id::text, name, version, created_at, updated_at`

func scanManufacturer(row pgx.Row) (application.Manufacturer, error) {
	var m application.Manufacturer
	err := row.Scan(&m.ID, &m.Name, &m.Version, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (r *Repository) InsertManufacturer(ctx context.Context, c application.Caller, name string) (application.Manufacturer, error) {
	var out application.Manufacturer
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanManufacturer(tx.QueryRow(ctx, `INSERT INTO products.manufacturers(name) VALUES ($1) RETURNING `+manufacturerCols, name))
		if err != nil {
			return mapWrite(err, "insert manufacturer")
		}
		return record(ctx, tx, c, "products.manufacturer.created", "manufacturer", out.ID, nil, map[string]any{"version": out.Version}, nil)
	})
	return out, err
}

func (r *Repository) ChangeManufacturer(ctx context.Context, c application.Caller, id string, decide func(application.Manufacturer) (application.Change[application.Manufacturer], error)) (application.Manufacturer, error) {
	return change(ctx, r, c, id, "manufacturer", `SELECT `+manufacturerCols+` FROM products.manufacturers`,
		`UPDATE products.manufacturers SET name = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid RETURNING `+manufacturerCols,
		scanManufacturer, func(m application.Manufacturer) []any { return []any{m.Name} },
		func(m application.Manufacturer) any { return map[string]any{"version": m.Version} }, decide)
}

func (r *Repository) ListManufacturers(ctx context.Context, prefix string, p application.Page) (application.Result[application.Manufacturer], error) {
	var conds []string
	var args []any
	if prefix != "" {
		args = append(args, prefixPattern(prefix))
		conds = append(conds, "lower(name) LIKE lower($1) || '%'")
	}
	return listPage(ctx, r, "products.manufacturers", manufacturerCols, conds, args, p, scanManufacturer, func(m application.Manufacturer) string { return m.ID })
}

// ---- categories ----

const categoryCols = `id::text, name, parent_category_id::text, version, created_at, updated_at`

func scanCategory(row pgx.Row) (application.Category, error) {
	var c application.Category
	err := row.Scan(&c.ID, &c.Name, &c.ParentID, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (r *Repository) InsertCategory(ctx context.Context, c application.Caller, name string, parentID *string) (application.Category, error) {
	if parentID != nil && !validUUID(*parentID) {
		return application.Category{}, application.ErrReferenceNotFound
	}
	var out application.Category
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanCategory(tx.QueryRow(ctx, `INSERT INTO products.categories(name, parent_category_id) VALUES ($1, $2::uuid) RETURNING `+categoryCols, name, parentID))
		if err != nil {
			return mapWrite(err, "insert category")
		}
		return record(ctx, tx, c, "products.category.created", "product_category", out.ID, nil, map[string]any{"parentId": out.ParentID, "version": out.Version}, nil)
	})
	return out, err
}

func (r *Repository) ChangeCategory(ctx context.Context, c application.Caller, id string, decide func(application.Category) (application.Change[application.Category], error)) (application.Category, error) {
	return change(ctx, r, c, id, "product_category", `SELECT `+categoryCols+` FROM products.categories`,
		`UPDATE products.categories SET name = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid RETURNING `+categoryCols,
		scanCategory, func(v application.Category) []any { return []any{v.Name} },
		func(v application.Category) any { return map[string]any{"parentId": v.ParentID, "version": v.Version} }, decide)
}

func (r *Repository) ListCategories(ctx context.Context, prefix string, p application.Page) (application.Result[application.Category], error) {
	var conds []string
	var args []any
	if prefix != "" {
		args = append(args, prefixPattern(prefix))
		conds = append(conds, "lower(name) LIKE lower($1) || '%'")
	}
	return listPage(ctx, r, "products.categories", categoryCols, conds, args, p, scanCategory, func(v application.Category) string { return v.ID })
}

// ---- products ----

const productCols = `id::text, name, manufacturer_id::text, category_id::text, manufacturer_part_number, internal_part_number,
	serialized, stock_managed, asset_managed, active, version, created_at, updated_at`

func scanProduct(row pgx.Row) (application.Product, error) {
	var p application.Product
	err := row.Scan(&p.ID, &p.Name, &p.ManufacturerID, &p.CategoryID, &p.ManufacturerPartNumber, &p.InternalPartNumber,
		&p.Serialized, &p.StockManaged, &p.AssetManaged, &p.Active, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// productState is audited; names and part numbers are product master data,
// not personal data, but only ids and flags are kept for symmetry with the
// other modules.
func productState(p application.Product) any {
	return map[string]any{
		"manufacturerId": p.ManufacturerID, "categoryId": p.CategoryID, "serialized": p.Serialized,
		"stockManaged": p.StockManaged, "assetManaged": p.AssetManaged, "active": p.Active, "version": p.Version,
	}
}

func (r *Repository) InsertProduct(ctx context.Context, c application.Caller, n application.NewProduct) (application.Product, error) {
	for _, ref := range []*string{n.ManufacturerID, n.CategoryID} {
		if ref != nil && !validUUID(*ref) {
			return application.Product{}, application.ErrReferenceNotFound
		}
	}
	var out application.Product
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProduct(tx.QueryRow(ctx, `
			INSERT INTO products.products(name, manufacturer_id, category_id, manufacturer_part_number, internal_part_number,
			                              serialized, stock_managed, asset_managed)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8) RETURNING `+productCols,
			n.Name, n.ManufacturerID, n.CategoryID, n.ManufacturerPartNumber, n.InternalPartNumber, n.Serialized, n.StockManaged, n.AssetManaged))
		if err != nil {
			return mapWrite(err, "insert product")
		}
		return record(ctx, tx, c, "products.product.created", "product", out.ID, nil, productState(out), nil)
	})
	return out, err
}

func (r *Repository) GetProduct(ctx context.Context, id string) (application.Product, error) {
	if !validUUID(id) {
		return application.Product{}, application.ErrNotFound
	}
	p, err := scanProduct(r.pool.QueryRow(ctx, `SELECT `+productCols+` FROM products.products WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Product{}, application.ErrNotFound
	}
	if err != nil {
		return application.Product{}, fmt.Errorf("get product: %w", err)
	}
	return p, nil
}

func (r *Repository) ChangeProduct(ctx context.Context, c application.Caller, id string, decide func(application.Product) (application.Change[application.Product], error)) (application.Product, error) {
	return change(ctx, r, c, id, "product", `SELECT `+productCols+` FROM products.products`,
		`UPDATE products.products SET name = $2, manufacturer_id = $3::uuid, category_id = $4::uuid,
			manufacturer_part_number = $5, internal_part_number = $6, serialized = $7, stock_managed = $8, asset_managed = $9,
			active = $10, version = version + 1, updated_at = now() WHERE id = $1::uuid RETURNING `+productCols,
		scanProduct, func(p application.Product) []any {
			return []any{p.Name, p.ManufacturerID, p.CategoryID, p.ManufacturerPartNumber, p.InternalPartNumber,
				p.Serialized, p.StockManaged, p.AssetManaged, p.Active}
		}, productState, decide)
}

func (r *Repository) ListProducts(ctx context.Context, f application.ProductFilter) (application.Result[application.Product], error) {
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.TitlePrefix != "" {
		add("lower(name) LIKE lower(?) || '%'", prefixPattern(f.TitlePrefix))
	}
	for _, ref := range []struct{ col, val string }{{"category_id", f.CategoryID}, {"manufacturer_id", f.ManufacturerID}} {
		if ref.val == "" {
			continue
		}
		if !validUUID(ref.val) {
			return application.Result[application.Product]{Items: []application.Product{}}, nil
		}
		add(ref.col+" = ?::uuid", ref.val)
	}
	if f.Active != nil {
		add("active = ?", *f.Active)
	}
	return listPage(ctx, r, "products.products", productCols, conds, args, f.Page, scanProduct, func(p application.Product) string { return p.ID })
}

func (r *Repository) CategoriesByIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	out := map[string]bool{}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM products.categories WHERE id = ANY($1::text[]::uuid[])`, valid)
	if err != nil {
		return nil, fmt.Errorf("categories by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("categories by ids: scan: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}
