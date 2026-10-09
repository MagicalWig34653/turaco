package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// ---- locations ----

func lockLocation(ctx context.Context, tx pgx.Tx, id string) (application.Location, error) {
	u, ok := parseID(id)
	if !ok {
		return application.Location{}, application.ErrNotFound
	}
	l, err := scanLocation(tx.QueryRow(ctx, `SELECT `+locationColumns+` FROM organization.locations WHERE id = $1 FOR UPDATE`, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Location{}, application.ErrNotFound
	}
	if err != nil {
		return application.Location{}, fmt.Errorf("lock location: %w", err)
	}
	return l, nil
}

func reloadLocation(ctx context.Context, tx pgx.Tx, id string) (application.Location, error) {
	return scanLocation(tx.QueryRow(ctx, `SELECT `+locationColumns+` FROM organization.locations WHERE id = $1::uuid`, id))
}

// treeError maps the tree trigger errors (ORG01 cycle, ORG02 depth) to the module error.
func treeError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "ORG01" || pg.Code == "ORG02") {
		return application.ErrHierarchy
	}
	return err
}

// locationDepth is the number of levels from the root down to and including id (a site has depth 1).
func locationDepth(ctx context.Context, tx pgx.Tx, id string) (int, error) {
	var d int
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE up(id, parent, depth) AS (
			SELECT id, parent_location_id, 1 FROM organization.locations WHERE id = $1::uuid
			UNION ALL
			SELECT l.id, l.parent_location_id, up.depth + 1 FROM organization.locations l JOIN up ON l.id = up.parent WHERE up.depth < 32)
		SELECT coalesce(max(depth), 0) FROM up`, id).Scan(&d)
	return d, err
}

// locationHeight is the number of levels of the subtree below and including id.
func locationHeight(ctx context.Context, tx pgx.Tx, id string) (int, error) {
	var h int
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE down(id, depth) AS (
			SELECT id, 1 FROM organization.locations WHERE id = $1::uuid
			UNION ALL
			SELECT l.id, down.depth + 1 FROM organization.locations l JOIN down ON l.parent_location_id = down.id WHERE down.depth < 32)
		SELECT coalesce(max(depth), 0) FROM down`, id).Scan(&h)
	return h, err
}

func (r *Repository) CreateLocation(ctx context.Context, c application.Caller, in application.NewLocationInput) (application.Location, error) {
	var out application.Location
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockLocations); err != nil {
			return err
		}
		if in.ParentID != nil {
			if err := requireActive(ctx, tx, "locations", *in.ParentID); err != nil {
				return err
			}
			d, err := locationDepth(ctx, tx, *in.ParentID)
			if err != nil {
				return fmt.Errorf("location depth: %w", err)
			}
			if d+1 > application.MaxLocationDepth {
				return application.ErrHierarchy
			}
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO organization.locations (name, kind, parent_location_id, code, description)
			VALUES ($1, $2, $3::uuid, $4, $5) RETURNING id::text`, in.Name, in.Kind, in.ParentID, in.Code, in.Description).Scan(&id)
		if isUnique(err) {
			return application.ErrConflict
		}
		if err = treeError(err); err != nil {
			return mapOr(err, "insert location")
		}
		if out, err = reloadLocation(ctx, tx, id); err != nil {
			return fmt.Errorf("reload location: %w", err)
		}
		return r.record(ctx, tx, c, "organization.location.created", "location", id, nil,
			map[string]any{"kind": out.Kind, "parentId": out.ParentID, "active": out.Active}, nil)
	})
	return finishPeople(out, err, "create location")
}

// mapOr returns domain errors unchanged and wraps others.
func mapOr(err error, what string) error {
	if errors.Is(err, application.ErrHierarchy) {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (r *Repository) UpdateLocation(ctx context.Context, c application.Caller, id string, in application.LocationChange) (application.Location, error) {
	var out application.Location
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockLocation(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != in.ExpectedVersion {
			return application.ErrVersionConflict
		}
		next := before
		var changed []string
		if in.Name != nil && *in.Name != before.Name {
			next.Name = *in.Name
			changed = append(changed, "name")
		}
		if in.Code.Set && !equalPtr(before.Code, in.Code.Value) {
			next.Code = in.Code.Value
			changed = append(changed, "code")
		}
		if in.Description != nil && *in.Description != before.Description {
			next.Description = *in.Description
			changed = append(changed, "description")
		}
		if len(changed) == 0 {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.locations SET name = $2, code = $3, description = $4, version = version + 1, updated_at = now() WHERE id = $1::uuid`,
			id, next.Name, next.Code, next.Description); err != nil {
			if isUnique(err) {
				return application.ErrConflict
			}
			return fmt.Errorf("update location: %w", err)
		}
		if out, err = reloadLocation(ctx, tx, id); err != nil {
			return fmt.Errorf("reload location: %w", err)
		}
		if err := r.record(ctx, tx, c, "organization.location.updated", "location", id, nil, map[string]any{"version": out.Version},
			map[string]any{"changed": changed}); err != nil {
			return err
		}
		return events.Publish(ctx, tx, events.Publication{Type: "LocationChanged", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
			Payload: map[string]any{"locationId": id, "operation": "updated"}})
	})
	return finishPeople(out, err, "update location")
}

func (r *Repository) MoveLocation(ctx context.Context, c application.Caller, id string, version int, parentID *string) (application.Location, error) {
	var out application.Location
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockLocations); err != nil {
			return err
		}
		before, err := lockLocation(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		// A site is a root and an area always has a parent: a move never changes the kind.
		if (before.Kind == application.LocationSite) != (parentID == nil) {
			return application.ErrHierarchy
		}
		if equalPtr(before.ParentID, parentID) {
			out = before
			return nil
		}
		if parentID != nil {
			if err := requireActive(ctx, tx, "locations", *parentID); err != nil {
				return err
			}
			var cycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE up(id, depth) AS (
					SELECT $1::uuid, 0
					UNION ALL
					SELECT l.parent_location_id, up.depth + 1 FROM organization.locations l JOIN up ON l.id = up.id
					WHERE l.parent_location_id IS NOT NULL AND up.depth < 32)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, *parentID, id).Scan(&cycle); err != nil {
				return fmt.Errorf("check location cycle: %w", err)
			}
			if cycle {
				return application.ErrHierarchy
			}
			depth, err := locationDepth(ctx, tx, *parentID)
			if err != nil {
				return fmt.Errorf("location depth: %w", err)
			}
			height, err := locationHeight(ctx, tx, id)
			if err != nil {
				return fmt.Errorf("location height: %w", err)
			}
			if depth+height > application.MaxLocationDepth {
				return application.ErrHierarchy
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.locations SET parent_location_id = $2::uuid, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, parentID); err != nil {
			if err = treeError(err); errors.Is(err, application.ErrHierarchy) {
				return err
			}
			return fmt.Errorf("move location: %w", err)
		}
		if out, err = reloadLocation(ctx, tx, id); err != nil {
			return fmt.Errorf("reload location: %w", err)
		}
		if err := r.record(ctx, tx, c, "organization.location.moved", "location", id,
			map[string]any{"parentId": before.ParentID}, map[string]any{"parentId": out.ParentID, "version": out.Version}, nil); err != nil {
			return err
		}
		return events.Publish(ctx, tx, events.Publication{Type: "LocationChanged", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
			Payload: map[string]any{"locationId": id, "operation": "moved"}})
	})
	return finishPeople(out, err, "move location")
}

func (r *Repository) SetLocationActive(ctx context.Context, c application.Caller, id string, version int, active, confirm bool) (application.Location, error) {
	var out application.Location
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockLocations); err != nil {
			return err
		}
		before, err := lockLocation(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if before.Active == active {
			return application.ErrConflict
		}
		action := "organization.location.deactivated"
		var counts map[string]int
		if active {
			action = "organization.location.activated"
			if before.ParentID != nil {
				if err := requireActive(ctx, tx, "locations", *before.ParentID); err != nil {
					return err
				}
			}
		} else {
			counts = map[string]int{}
			var users, children int
			if err := tx.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM organization.users WHERE primary_location_id = $1::uuid),
				       (SELECT count(*) FROM organization.locations WHERE parent_location_id = $1::uuid AND active)`, id).Scan(&users, &children); err != nil {
				return fmt.Errorf("count location references: %w", err)
			}
			add(counts, "users", users)
			add(counts, "areas", children)
			for _, cnt := range r.counters {
				n, err := cnt.CountLocationReferences(ctx, id)
				if err != nil {
					return fmt.Errorf("count %s: %w", cnt.Name(), err)
				}
				add(counts, cnt.Name(), n)
			}
			if len(counts) > 0 && !confirm {
				return &application.ImpactError{Counts: counts}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.locations SET active = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, active); err != nil {
			if isUnique(err) {
				return application.ErrConflict // the code is used by another active location now
			}
			return fmt.Errorf("set location active: %w", err)
		}
		if out, err = reloadLocation(ctx, tx, id); err != nil {
			return fmt.Errorf("reload location: %w", err)
		}
		meta := map[string]any{}
		if len(counts) > 0 {
			meta["impact"] = counts
		}
		if err := r.record(ctx, tx, c, action, "location", id, map[string]any{"active": before.Active}, map[string]any{"active": out.Active, "version": out.Version}, meta); err != nil {
			return err
		}
		return events.Publish(ctx, tx, events.Publication{Type: "LocationChanged", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
			Payload: map[string]any{"locationId": id, "operation": map[bool]string{true: "activated", false: "deactivated"}[active]}})
	})
	return finishPeople(out, err, "set location active")
}

func add(m map[string]int, key string, n int) {
	if n > 0 {
		m[key] = n
	}
}

// ---- departments ----

func lockDepartment(ctx context.Context, tx pgx.Tx, id string) (application.Department, error) {
	u, ok := parseID(id)
	if !ok {
		return application.Department{}, application.ErrNotFound
	}
	d, err := scanDepartment(tx.QueryRow(ctx, `SELECT `+departmentColumns+` FROM organization.departments WHERE id = $1 FOR UPDATE`, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Department{}, application.ErrNotFound
	}
	if err != nil {
		return application.Department{}, fmt.Errorf("lock department: %w", err)
	}
	return d, nil
}

func reloadDepartment(ctx context.Context, tx pgx.Tx, id string) (application.Department, error) {
	return scanDepartment(tx.QueryRow(ctx, `SELECT `+departmentColumns+` FROM organization.departments WHERE id = $1::uuid`, id))
}

func (r *Repository) CreateDepartment(ctx context.Context, c application.Caller, in application.NewDepartmentInput) (application.Department, error) {
	var out application.Department
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if in.ParentID != nil {
			if err := lockTree(ctx, tx, treeLockDepartments); err != nil {
				return err
			}
			if err := requireActive(ctx, tx, "departments", *in.ParentID); err != nil {
				return err
			}
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO organization.departments (name, code, parent_department_id) VALUES ($1, $2, $3::uuid) RETURNING id::text`,
			in.Name, in.Code, in.ParentID).Scan(&id)
		if isUnique(err) {
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("insert department: %w", err)
		}
		if out, err = reloadDepartment(ctx, tx, id); err != nil {
			return fmt.Errorf("reload department: %w", err)
		}
		return r.record(ctx, tx, c, "organization.department.created", "department", id, nil,
			map[string]any{"parentId": out.ParentID, "active": out.Active}, nil)
	})
	return finishPeople(out, err, "create department")
}

func (r *Repository) UpdateDepartment(ctx context.Context, c application.Caller, id string, in application.DepartmentChange) (application.Department, error) {
	var out application.Department
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockDepartment(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != in.ExpectedVersion {
			return application.ErrVersionConflict
		}
		next := before
		var changed []string
		if in.Name != nil && *in.Name != before.Name {
			next.Name = *in.Name
			changed = append(changed, "name")
		}
		if in.Code.Set && !equalPtr(before.Code, in.Code.Value) {
			next.Code = in.Code.Value
			changed = append(changed, "code")
		}
		if len(changed) == 0 {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.departments SET name = $2, code = $3, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, next.Name, next.Code); err != nil {
			if isUnique(err) {
				return application.ErrConflict
			}
			return fmt.Errorf("update department: %w", err)
		}
		if out, err = reloadDepartment(ctx, tx, id); err != nil {
			return fmt.Errorf("reload department: %w", err)
		}
		return r.record(ctx, tx, c, "organization.department.renamed", "department", id, nil, map[string]any{"version": out.Version}, map[string]any{"changed": changed})
	})
	return finishPeople(out, err, "update department")
}

func (r *Repository) MoveDepartment(ctx context.Context, c application.Caller, id string, version int, parentID *string) (application.Department, error) {
	var out application.Department
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockDepartments); err != nil {
			return err
		}
		before, err := lockDepartment(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if equalPtr(before.ParentID, parentID) {
			out = before
			return nil
		}
		if parentID != nil {
			if err := requireActive(ctx, tx, "departments", *parentID); err != nil {
				return err
			}
			var cycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE up(id, depth) AS (
					SELECT $1::uuid, 0
					UNION ALL
					SELECT d.parent_department_id, up.depth + 1 FROM organization.departments d JOIN up ON d.id = up.id
					WHERE d.parent_department_id IS NOT NULL AND up.depth < 64)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, *parentID, id).Scan(&cycle); err != nil {
				return fmt.Errorf("check department cycle: %w", err)
			}
			if cycle {
				return application.ErrHierarchy
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.departments SET parent_department_id = $2::uuid, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, parentID); err != nil {
			if err = treeError(err); errors.Is(err, application.ErrHierarchy) {
				return err
			}
			return fmt.Errorf("move department: %w", err)
		}
		if out, err = reloadDepartment(ctx, tx, id); err != nil {
			return fmt.Errorf("reload department: %w", err)
		}
		return r.record(ctx, tx, c, "organization.department.moved", "department", id,
			map[string]any{"parentId": before.ParentID}, map[string]any{"parentId": out.ParentID, "version": out.Version}, nil)
	})
	return finishPeople(out, err, "move department")
}

func (r *Repository) SetDepartmentActive(ctx context.Context, c application.Caller, id string, version int, active, confirm bool) (application.Department, error) {
	var out application.Department
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockDepartments); err != nil {
			return err
		}
		before, err := lockDepartment(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if before.Active == active {
			return application.ErrConflict
		}
		action := "organization.department.deactivated"
		counts := map[string]int{}
		if active {
			action = "organization.department.activated"
			if before.ParentID != nil {
				if err := requireActive(ctx, tx, "departments", *before.ParentID); err != nil {
					return err
				}
			}
		} else {
			var users, children int
			if err := tx.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM organization.users WHERE department_id = $1::uuid),
				       (SELECT count(*) FROM organization.departments WHERE parent_department_id = $1::uuid AND active)`, id).Scan(&users, &children); err != nil {
				return fmt.Errorf("count department references: %w", err)
			}
			add(counts, "users", users)
			add(counts, "departments", children)
			if len(counts) > 0 && !confirm {
				return &application.ImpactError{Counts: counts}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.departments SET active = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, active); err != nil {
			if isUnique(err) {
				return application.ErrConflict
			}
			return fmt.Errorf("set department active: %w", err)
		}
		if out, err = reloadDepartment(ctx, tx, id); err != nil {
			return fmt.Errorf("reload department: %w", err)
		}
		meta := map[string]any{}
		if len(counts) > 0 {
			meta["impact"] = counts
		}
		return r.record(ctx, tx, c, action, "department", id, map[string]any{"active": before.Active}, map[string]any{"active": out.Active, "version": out.Version}, meta)
	})
	return finishPeople(out, err, "set department active")
}
