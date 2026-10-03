// Package repository implements the Infrastructure store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
)

// Repository stores buildings, rooms, racks, rack placements and virtual machines.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func pgErr(err error) (code, constraint string) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code, pg.ConstraintName
	}
	return "", ""
}

func isUnique(err error) bool { c, _ := pgErr(err); return c == "23505" }

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

// one runs a single-row query and maps no rows to ErrNotFound and a unique
// violation to ErrConflict.
func one[T any](row pgx.Row, scan func(pgx.Row) (T, error), what string) (T, error) {
	v, err := scan(row)
	var zero T
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return zero, application.ErrNotFound
	case isUnique(err):
		return zero, application.ErrConflict
	case err != nil:
		return zero, fmt.Errorf("%s: %w", what, err)
	}
	return v, nil
}

func lockByID[T any](ctx context.Context, tx pgx.Tx, id, table, cols string, scan func(pgx.Row) (T, error)) (T, error) {
	if !validUUID(id) {
		var zero T
		return zero, application.ErrNotFound
	}
	return one(tx.QueryRow(ctx, `SELECT `+cols+` FROM `+table+` WHERE id = $1::uuid FOR UPDATE`, id), scan, "lock "+table)
}

func getByID[T any](ctx context.Context, r *Repository, id, table, cols string, scan func(pgx.Row) (T, error)) (T, error) {
	if !validUUID(id) {
		var zero T
		return zero, application.ErrNotFound
	}
	return one(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM `+table+` WHERE id = $1::uuid`, id), scan, "get "+table)
}

func count(ctx context.Context, tx pgx.Tx, sql string, id string) (int, error) {
	var n int
	if err := tx.QueryRow(ctx, sql, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// listPage is keyset pagination over a UUIDv7 id column, ascending.
func listPage[T any](ctx context.Context, r *Repository, table, columns string, conds []string, args []any, page application.Page,
	scan func(pgx.Row) (T, error), id func(T) string) (application.Result[T], error) {
	page = page.Normalize()
	conds = append([]string(nil), conds...)
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
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s%s ORDER BY id ASC LIMIT $%d`, columns, table, where, len(args)), args...)
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

// ---- buildings ----

const buildingCols = `id::text, site_location_id::text, name, address_note, active, version, created_at, updated_at`

func scanBuilding(row pgx.Row) (application.Building, error) {
	var b application.Building
	err := row.Scan(&b.ID, &b.SiteLocationID, &b.Name, &b.AddressNote, &b.Active, &b.Version, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (r *Repository) InsertBuildingTx(ctx context.Context, tx pgx.Tx, siteLocationID, name string, addressNote *string) (application.Building, error) {
	return one(tx.QueryRow(ctx, `INSERT INTO infrastructure.buildings(site_location_id, name, address_note) VALUES ($1::uuid, $2, $3) RETURNING `+buildingCols,
		siteLocationID, name, addressNote), scanBuilding, "insert building")
}

func (r *Repository) LockBuildingTx(ctx context.Context, tx pgx.Tx, id string) (application.Building, error) {
	return lockByID(ctx, tx, id, "infrastructure.buildings", buildingCols, scanBuilding)
}

func (r *Repository) UpdateBuildingTx(ctx context.Context, tx pgx.Tx, b application.Building) (application.Building, error) {
	return one(tx.QueryRow(ctx, `UPDATE infrastructure.buildings SET name = $2, address_note = $3, active = $4, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+buildingCols, b.ID, b.Name, b.AddressNote, b.Active), scanBuilding, "update building")
}

func (r *Repository) GetBuilding(ctx context.Context, id string) (application.Building, error) {
	return getByID(ctx, r, id, "infrastructure.buildings", buildingCols, scanBuilding)
}

func (r *Repository) ListBuildings(ctx context.Context, siteLocationID string, includeArchived bool, page application.Page) (application.Result[application.Building], error) {
	var conds []string
	var args []any
	if siteLocationID != "" {
		if !validUUID(siteLocationID) {
			return application.Result[application.Building]{Items: []application.Building{}}, nil
		}
		args = append(args, siteLocationID)
		conds = append(conds, "site_location_id = $1::uuid")
	}
	if !includeArchived {
		conds = append(conds, "active")
	}
	return listPage(ctx, r, "infrastructure.buildings", buildingCols, conds, args, page, scanBuilding, func(b application.Building) string { return b.ID })
}

func (r *Repository) CountActiveRoomsTx(ctx context.Context, tx pgx.Tx, buildingID string) (int, error) {
	return count(ctx, tx, `SELECT count(*) FROM infrastructure.rooms WHERE building_id = $1::uuid AND active`, buildingID)
}

// ---- rooms ----

const roomCols = `id::text, building_id::text, name, floor, active, version, created_at, updated_at`

func scanRoom(row pgx.Row) (application.Room, error) {
	var x application.Room
	err := row.Scan(&x.ID, &x.BuildingID, &x.Name, &x.Floor, &x.Active, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (r *Repository) InsertRoomTx(ctx context.Context, tx pgx.Tx, buildingID, name string, floor *string) (application.Room, error) {
	return one(tx.QueryRow(ctx, `INSERT INTO infrastructure.rooms(building_id, name, floor) VALUES ($1::uuid, $2, $3) RETURNING `+roomCols,
		buildingID, name, floor), scanRoom, "insert room")
}

func (r *Repository) LockRoomTx(ctx context.Context, tx pgx.Tx, id string) (application.Room, error) {
	return lockByID(ctx, tx, id, "infrastructure.rooms", roomCols, scanRoom)
}

func (r *Repository) UpdateRoomTx(ctx context.Context, tx pgx.Tx, x application.Room) (application.Room, error) {
	return one(tx.QueryRow(ctx, `UPDATE infrastructure.rooms SET name = $2, floor = $3, active = $4, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+roomCols, x.ID, x.Name, x.Floor, x.Active), scanRoom, "update room")
}

func (r *Repository) GetRoom(ctx context.Context, id string) (application.Room, error) {
	return getByID(ctx, r, id, "infrastructure.rooms", roomCols, scanRoom)
}

func (r *Repository) ListRooms(ctx context.Context, buildingID string, includeArchived bool, page application.Page) (application.Result[application.Room], error) {
	if !validUUID(buildingID) {
		return application.Result[application.Room]{Items: []application.Room{}}, nil
	}
	conds := []string{"building_id = $1::uuid"}
	if !includeArchived {
		conds = append(conds, "active")
	}
	return listPage(ctx, r, "infrastructure.rooms", roomCols, conds, []any{buildingID}, page, scanRoom, func(x application.Room) string { return x.ID })
}

func (r *Repository) CountActiveRacksTx(ctx context.Context, tx pgx.Tx, roomID string) (int, error) {
	return count(ctx, tx, `SELECT count(*) FROM infrastructure.racks WHERE room_id = $1::uuid AND active`, roomID)
}

// ---- racks ----

const rackCols = `id::text, room_id::text, name, height_u, active, version, created_at, updated_at`

func scanRack(row pgx.Row) (application.Rack, error) {
	var x application.Rack
	err := row.Scan(&x.ID, &x.RoomID, &x.Name, &x.HeightU, &x.Active, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (r *Repository) InsertRackTx(ctx context.Context, tx pgx.Tx, roomID, name string, heightU int) (application.Rack, error) {
	return one(tx.QueryRow(ctx, `INSERT INTO infrastructure.racks(room_id, name, height_u) VALUES ($1::uuid, $2, $3) RETURNING `+rackCols,
		roomID, name, heightU), scanRack, "insert rack")
}

func (r *Repository) LockRackTx(ctx context.Context, tx pgx.Tx, id string) (application.Rack, error) {
	return lockByID(ctx, tx, id, "infrastructure.racks", rackCols, scanRack)
}

func (r *Repository) UpdateRackTx(ctx context.Context, tx pgx.Tx, x application.Rack) (application.Rack, error) {
	return one(tx.QueryRow(ctx, `UPDATE infrastructure.racks SET name = $2, active = $3, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+rackCols, x.ID, x.Name, x.Active), scanRack, "update rack")
}

func (r *Repository) GetRack(ctx context.Context, id string) (application.Rack, error) {
	return getByID(ctx, r, id, "infrastructure.racks", rackCols, scanRack)
}

func (r *Repository) ListRacks(ctx context.Context, roomID string, includeArchived bool, page application.Page) (application.Result[application.Rack], error) {
	if !validUUID(roomID) {
		return application.Result[application.Rack]{Items: []application.Rack{}}, nil
	}
	conds := []string{"room_id = $1::uuid"}
	if !includeArchived {
		conds = append(conds, "active")
	}
	return listPage(ctx, r, "infrastructure.racks", rackCols, conds, []any{roomID}, page, scanRack, func(x application.Rack) string { return x.ID })
}

func (r *Repository) CountActivePlacementsTx(ctx context.Context, tx pgx.Tx, rackID string) (int, error) {
	return count(ctx, tx, `SELECT count(*) FROM infrastructure.rack_placements WHERE rack_id = $1::uuid AND removed_at IS NULL`, rackID)
}

// ---- placements ----

const placementCols = `id::text, rack_id::text, asset_id::text, u_position, height_u, face, placed_by::text, placed_at,
	removed_at, removed_by::text, removal_reason, previous_placement_id::text, version`

func scanPlacement(row pgx.Row) (application.Placement, error) {
	var p application.Placement
	err := row.Scan(&p.ID, &p.RackID, &p.AssetID, &p.UPosition, &p.HeightU, &p.Face, &p.PlacedBy, &p.PlacedAt,
		&p.RemovedAt, &p.RemovedBy, &p.RemovalReason, &p.PreviousID, &p.Version)
	return p, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repository) InsertPlacementTx(ctx context.Context, tx pgx.Tx, in application.PlacementInput, placedBy string, previousID *string) (application.Placement, error) {
	p, err := scanPlacement(tx.QueryRow(ctx, `
		INSERT INTO infrastructure.rack_placements(rack_id, asset_id, u_position, height_u, face, placed_by, previous_placement_id, rack_height_u)
		SELECT k.id, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid, k.height_u FROM infrastructure.racks k WHERE k.id = $1::uuid
		RETURNING `+placementCols,
		in.RackID, in.AssetID, in.UPosition, in.HeightU, in.Face, nilIfEmpty(placedBy), previousID))
	if isUnique(err) {
		return application.Placement{}, application.ErrAssetPlaced
	}
	if err != nil {
		return application.Placement{}, fmt.Errorf("insert placement: %w", err)
	}
	// One row per unit: the primary key rejects any unit that is already taken.
	_, err = tx.Exec(ctx, `
		INSERT INTO infrastructure.rack_unit_occupancy(rack_id, face, u, placement_id)
		SELECT $1::uuid, $2, g, $3::uuid FROM generate_series($4::int, $5::int) AS g`,
		p.RackID, p.Face, p.ID, p.UPosition, p.Top())
	if isUnique(err) {
		return application.Placement{}, application.ErrOccupied
	}
	if err != nil {
		return application.Placement{}, fmt.Errorf("occupy units: %w", err)
	}
	return p, nil
}

func (r *Repository) LockPlacementTx(ctx context.Context, tx pgx.Tx, id string) (application.Placement, error) {
	return lockByID(ctx, tx, id, "infrastructure.rack_placements", placementCols, scanPlacement)
}

func (r *Repository) ClosePlacementTx(ctx context.Context, tx pgx.Tx, id, reason, removedBy string) (application.Placement, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM infrastructure.rack_unit_occupancy WHERE placement_id = $1::uuid`, id); err != nil {
		return application.Placement{}, fmt.Errorf("free units: %w", err)
	}
	return one(tx.QueryRow(ctx, `UPDATE infrastructure.rack_placements SET removed_at = now(), removed_by = $3::uuid, removal_reason = $2, version = version + 1
		WHERE id = $1::uuid AND removed_at IS NULL RETURNING `+placementCols, id, reason, nilIfEmpty(removedBy)), scanPlacement, "close placement")
}

func (r *Repository) GetPlacement(ctx context.Context, id string) (application.Placement, error) {
	return getByID(ctx, r, id, "infrastructure.rack_placements", placementCols, scanPlacement)
}

// ActivePlacements returns the active placements of a rack ordered by unit
// (bounded by the 60 units of a rack and two faces).
func (r *Repository) ActivePlacements(ctx context.Context, rackID string) ([]application.Placement, error) {
	if !validUUID(rackID) {
		return []application.Placement{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+placementCols+` FROM infrastructure.rack_placements
		WHERE rack_id = $1::uuid AND removed_at IS NULL ORDER BY u_position, face, id LIMIT 200`, rackID)
	if err != nil {
		return nil, fmt.Errorf("active placements: %w", err)
	}
	defer rows.Close()
	out := []application.Placement{}
	for rows.Next() {
		p, err := scanPlacement(rows)
		if err != nil {
			return nil, fmt.Errorf("active placements: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivePlacementsAfter returns up to limit active placements with id > afterID
// (all when empty) in id order, with their rack names.
func (r *Repository) ActivePlacementsAfter(ctx context.Context, afterID string, limit int) ([]application.PlacementRef, error) {
	if afterID == "" {
		afterID = "00000000-0000-0000-0000-000000000000"
	}
	if !validUUID(afterID) {
		return nil, application.ErrInvalidCursor
	}
	rows, err := r.pool.Query(ctx, `SELECT `+prefixed("p", placementCols)+`, k.name
		FROM infrastructure.rack_placements p JOIN infrastructure.racks k ON k.id = p.rack_id
		WHERE p.removed_at IS NULL AND p.id > $1::uuid ORDER BY p.id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("active placements after: %w", err)
	}
	defer rows.Close()
	out := []application.PlacementRef{}
	for rows.Next() {
		var p application.Placement
		var name string
		if err := rows.Scan(&p.ID, &p.RackID, &p.AssetID, &p.UPosition, &p.HeightU, &p.Face, &p.PlacedBy, &p.PlacedAt,
			&p.RemovedAt, &p.RemovedBy, &p.RemovalReason, &p.PreviousID, &p.Version, &name); err != nil {
			return nil, fmt.Errorf("active placements after: scan: %w", err)
		}
		out = append(out, application.PlacementRef{Placement: p, RackName: name})
	}
	return out, rows.Err()
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func (r *Repository) ListPlacements(ctx context.Context, rackID string, includeRemoved bool, page application.Page) (application.Result[application.Placement], error) {
	if !validUUID(rackID) {
		return application.Result[application.Placement]{Items: []application.Placement{}}, nil
	}
	conds := []string{"rack_id = $1::uuid"}
	if !includeRemoved {
		conds = append(conds, "removed_at IS NULL")
	}
	return listPage(ctx, r, "infrastructure.rack_placements", placementCols, conds, []any{rackID}, page, scanPlacement, func(p application.Placement) string { return p.ID })
}

func (r *Repository) WhereIs(ctx context.Context, assetID string) (application.AssetLocation, error) {
	if !validUUID(assetID) {
		return application.AssetLocation{}, application.ErrNotFound
	}
	var l application.AssetLocation
	err := r.pool.QueryRow(ctx, `
		SELECT p.id::text, k.id::text, k.name, m.id::text, m.name, m.floor, b.id::text, b.name, b.site_location_id::text,
		       p.u_position, p.height_u, p.face, p.placed_at
		FROM infrastructure.rack_placements p
		JOIN infrastructure.racks k ON k.id = p.rack_id
		JOIN infrastructure.rooms m ON m.id = k.room_id
		JOIN infrastructure.buildings b ON b.id = m.building_id
		WHERE p.asset_id = $1::uuid AND p.removed_at IS NULL`, assetID).
		Scan(&l.PlacementID, &l.RackID, &l.RackName, &l.RoomID, &l.RoomName, &l.Floor, &l.BuildingID, &l.BuildingName, &l.SiteLocationID,
			&l.UPosition, &l.HeightU, &l.Face, &l.PlacedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AssetLocation{}, application.ErrNotFound
	}
	if err != nil {
		return application.AssetLocation{}, fmt.Errorf("where is asset: %w", err)
	}
	return l, nil
}

// ---- virtual machines ----

const vmCols = `id::text, name, state, hypervisor_asset_id::text, vcpu, memory_mb, management_address, network_note, notes,
	decommission_reason, decommissioned_at, version, created_by::text, created_at, updated_at`

func scanVM(row pgx.Row) (application.VirtualMachine, error) {
	var v application.VirtualMachine
	err := row.Scan(&v.ID, &v.Name, &v.State, &v.HypervisorAssetID, &v.VCPU, &v.MemoryMB, &v.ManagementAddress, &v.NetworkNote, &v.Notes,
		&v.DecommissionReason, &v.DecommissionedAt, &v.Version, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (r *Repository) InsertVMTx(ctx context.Context, tx pgx.Tx, v application.VirtualMachine) (application.VirtualMachine, error) {
	return one(tx.QueryRow(ctx, `
		INSERT INTO infrastructure.virtual_machines(name, state, hypervisor_asset_id, vcpu, memory_mb, management_address, network_note, notes, created_by)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9::uuid) RETURNING `+vmCols,
		v.Name, v.State, v.HypervisorAssetID, v.VCPU, v.MemoryMB, v.ManagementAddress, v.NetworkNote, v.Notes, v.CreatedBy), scanVM, "insert virtual machine")
}

func (r *Repository) LockVMTx(ctx context.Context, tx pgx.Tx, id string) (application.VirtualMachine, error) {
	return lockByID(ctx, tx, id, "infrastructure.virtual_machines", vmCols, scanVM)
}

func (r *Repository) UpdateVMTx(ctx context.Context, tx pgx.Tx, v application.VirtualMachine) (application.VirtualMachine, error) {
	return one(tx.QueryRow(ctx, `
		UPDATE infrastructure.virtual_machines SET name = $2, state = $3, hypervisor_asset_id = $4::uuid, vcpu = $5, memory_mb = $6,
			management_address = $7, network_note = $8, notes = $9, decommission_reason = $10,
			decommissioned_at = CASE WHEN $3 = 'decommissioned' THEN coalesce(decommissioned_at, now()) END,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+vmCols,
		v.ID, v.Name, v.State, v.HypervisorAssetID, v.VCPU, v.MemoryMB, v.ManagementAddress, v.NetworkNote, v.Notes, v.DecommissionReason), scanVM, "update virtual machine")
}

func (r *Repository) GetVM(ctx context.Context, id string) (application.VirtualMachine, error) {
	return getByID(ctx, r, id, "infrastructure.virtual_machines", vmCols, scanVM)
}

func (r *Repository) VMsByIDs(ctx context.Context, ids []string) ([]application.VirtualMachine, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+vmCols+` FROM infrastructure.virtual_machines WHERE id = ANY($1::uuid[])`, valid)
	if err != nil {
		return nil, fmt.Errorf("virtual machines by id: %w", err)
	}
	defer rows.Close()
	var out []application.VirtualMachine
	for rows.Next() {
		v, err := scanVM(rows)
		if err != nil {
			return nil, fmt.Errorf("virtual machines by id: scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListVMs filters by state and hypervisor with the keyset order of the primary
// key. The name search is an unindexed ILIKE scan: Virtual Machines are
// hand-entered and bounded in the hundreds to low thousands, so the scan is
// accepted; pg_trgm is not installed and would be a new dependency.
func (r *Repository) ListVMs(ctx context.Context, f application.VMFilter) (application.Result[application.VirtualMachine], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.HypervisorAssetID != "" {
		add("hypervisor_asset_id = $%d::uuid", f.HypervisorAssetID)
	}
	if f.Query != "" {
		add(`name ILIKE $%d ESCAPE '\'`, "%"+likeEscape(f.Query)+"%")
	}
	return listPage(ctx, r, "infrastructure.virtual_machines", vmCols, conds, args, f.Page, scanVM, func(v application.VirtualMachine) string { return v.ID })
}

// ---- tree ----

// Tree counts buildings, rooms, racks and placed assets per site. It returns
// at most MaxTreeSites sites (ordered by id) and at most MaxTreeSites*20
// buildings; Truncated reports that more exist. Counts are GROUP BY aggregates
// over the selected sites only.
func (r *Repository) Tree(ctx context.Context, includeArchived bool) (application.TreeResult, error) {
	var sites []string
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT site_location_id::text FROM infrastructure.buildings
		WHERE active OR $1::boolean ORDER BY 1 LIMIT $2`, includeArchived, application.MaxTreeSites+1)
	if err != nil {
		return application.TreeResult{}, fmt.Errorf("infrastructure tree: sites: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return application.TreeResult{}, fmt.Errorf("infrastructure tree: scan site: %w", err)
		}
		sites = append(sites, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return application.TreeResult{}, fmt.Errorf("infrastructure tree: sites: %w", err)
	}
	res := application.TreeResult{Sites: []application.SiteSummary{}}
	if len(sites) > application.MaxTreeSites {
		sites, res.Truncated = sites[:application.MaxTreeSites], true
	}
	if len(sites) == 0 {
		return res, nil
	}
	maxBuildings := application.MaxTreeSites * 20
	brows, err := r.pool.Query(ctx, `
		WITH sel AS (
			SELECT id, site_location_id, name, active, version FROM infrastructure.buildings
			WHERE site_location_id = ANY($2::uuid[]) AND (active OR $1::boolean)
			ORDER BY site_location_id, id LIMIT $3
		), room_counts AS (
			SELECT m.building_id, count(*) AS n FROM infrastructure.rooms m
			WHERE m.building_id IN (SELECT id FROM sel) AND (m.active OR $1::boolean) GROUP BY m.building_id
		), rack_counts AS (
			SELECT m.building_id, count(*) AS n FROM infrastructure.racks k JOIN infrastructure.rooms m ON m.id = k.room_id
			WHERE m.building_id IN (SELECT id FROM sel) AND (k.active OR $1::boolean) GROUP BY m.building_id
		), placed_counts AS (
			SELECT m.building_id, count(*) AS n FROM infrastructure.rack_placements p
			JOIN infrastructure.racks k ON k.id = p.rack_id JOIN infrastructure.rooms m ON m.id = k.room_id
			WHERE m.building_id IN (SELECT id FROM sel) AND p.removed_at IS NULL GROUP BY m.building_id
		)
		SELECT s.site_location_id::text, s.id::text, s.name, s.active, s.version,
		       coalesce(rc.n, 0), coalesce(kc.n, 0), coalesce(pc.n, 0)
		FROM sel s
		LEFT JOIN room_counts rc ON rc.building_id = s.id
		LEFT JOIN rack_counts kc ON kc.building_id = s.id
		LEFT JOIN placed_counts pc ON pc.building_id = s.id
		ORDER BY s.site_location_id, s.id`, includeArchived, sites, maxBuildings+1)
	if err != nil {
		return application.TreeResult{}, fmt.Errorf("infrastructure tree: %w", err)
	}
	defer brows.Close()
	idx := map[string]int{}
	n := 0
	for brows.Next() {
		var site string
		var b application.BuildingSummary
		if err := brows.Scan(&site, &b.ID, &b.Name, &b.Active, &b.Version, &b.Rooms, &b.Racks, &b.Placed); err != nil {
			return application.TreeResult{}, fmt.Errorf("infrastructure tree: scan: %w", err)
		}
		if n++; n > maxBuildings {
			res.Truncated = true
			break
		}
		b.SiteID = site
		i, ok := idx[site]
		if !ok {
			res.Sites = append(res.Sites, application.SiteSummary{LocationID: site})
			i = len(res.Sites) - 1
			idx[site] = i
		}
		res.Sites[i].Buildings = append(res.Sites[i].Buildings, b)
	}
	return res, brows.Err()
}
