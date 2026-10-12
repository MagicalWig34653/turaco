package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
		var err error
		out, err = r.createLocationTx(ctx, tx, c, in)
		return err
	})
	return finishPeople(out, err, "create location")
}

// createLocationTx runs the operation inside the caller's transaction (single operations, imports and bulk operations share it).
func (r *Repository) createLocationTx(ctx context.Context, tx pgx.Tx, c application.Caller, in application.NewLocationInput) (application.Location, error) {
	var out application.Location
	err := func() error {
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
	}()
	return out, err
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
		var err error
		out, err = r.updateLocationTx(ctx, tx, c, id, in)
		return err
	})
	return finishPeople(out, err, "update location")
}

// updateLocationTx runs the operation inside the caller's transaction (single operations, imports and bulk operations share it).
func (r *Repository) updateLocationTx(ctx context.Context, tx pgx.Tx, c application.Caller, id string, in application.LocationChange) (application.Location, error) {
	var out application.Location
	err := func() error {
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
	}()
	return out, err
}

// locationMovePlan is the validated outcome of a requested move inside the move's transaction.
type locationMovePlan struct {
	before application.Location
	// noop: the Location already has the requested parent.
	noop     bool
	preview  application.LocationMovePreview
	affected int // scoped assignments the move affects (the audit value; the preview hides it from non-administrators)
}

// planLocationMove takes the tree lock, locks and validates the move and runs the Location move guard (ADR-0040, F16
// design L10, slice L-C). Re-parenting changes which Locations a Location-scoped role assignment covers, so the guard
// counts the active scoped assignments the move affects through the role service: those at the moved Location and
// its descendants, and those at the ancestors of the old or the new position that are not ancestors of both (a
// common ancestor covers the subtree before and after). A non-zero count makes the move administrator-only: a
// holder of organization.locations.manage cannot pull another Site's area below a Site where someone (or they) hold
// a scoped role. Scoped role assignments and revocations take the same tree lock in shared mode, so none is in flight
// while the count runs. A move without guards fails closed.
//
// A dry run (preview) must not block scoped assignments or revocations: it takes the tree lock in shared mode, which
// they share, and reads the Location without a row lock. A concurrent move still excludes it, so the answer reflects
// a consistent tree; it can only be stale by the time the real move runs, which re-checks.
func (r *Repository) planLocationMove(ctx context.Context, tx pgx.Tx, c application.Caller, id string, version int, parentID *string, preview bool) (locationMovePlan, error) {
	if r.guards == nil {
		return locationMovePlan{}, application.ErrNoGuards
	}
	var before application.Location
	var err error
	if preview {
		if err := lockTreeShared(ctx, tx, treeLockLocations); err != nil {
			return locationMovePlan{}, err
		}
		u, ok := parseID(id)
		if !ok {
			return locationMovePlan{}, application.ErrNotFound
		}
		if before, err = scanLocation(tx.QueryRow(ctx, `SELECT `+locationColumns+` FROM organization.locations WHERE id = $1`, u)); errors.Is(err, pgx.ErrNoRows) {
			return locationMovePlan{}, application.ErrNotFound
		} else if err != nil {
			return locationMovePlan{}, fmt.Errorf("read location: %w", err)
		}
	} else {
		if err := lockTree(ctx, tx, treeLockLocations); err != nil {
			return locationMovePlan{}, err
		}
		if before, err = lockLocation(ctx, tx, id); err != nil {
			return locationMovePlan{}, err
		}
	}
	plan := locationMovePlan{before: before, preview: application.LocationMovePreview{Allowed: true}}
	if before.Version != version {
		return plan, application.ErrVersionConflict
	}
	// A site is a root and an area always has a parent: a move never changes the kind.
	if (before.Kind == application.LocationSite) != (parentID == nil) {
		return plan, application.ErrHierarchy
	}
	if equalPtr(before.ParentID, parentID) {
		plan.noop = true
		return plan, nil
	}
	if parentID != nil {
		if err := requireActive(ctx, tx, "locations", *parentID); err != nil {
			return plan, err
		}
		var cycle bool
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE up(id, depth) AS (
				SELECT $1::uuid, 0
				UNION ALL
				SELECT l.parent_location_id, up.depth + 1 FROM organization.locations l JOIN up ON l.id = up.id
				WHERE l.parent_location_id IS NOT NULL AND up.depth < 32)
			SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, *parentID, id).Scan(&cycle); err != nil {
			return plan, fmt.Errorf("check location cycle: %w", err)
		}
		if cycle {
			return plan, application.ErrHierarchy
		}
		depth, err := locationDepth(ctx, tx, *parentID)
		if err != nil {
			return plan, fmt.Errorf("location depth: %w", err)
		}
		height, err := locationHeight(ctx, tx, id)
		if err != nil {
			return plan, fmt.Errorf("location height: %w", err)
		}
		if depth+height > application.MaxLocationDepth {
			return plan, application.ErrHierarchy
		}
	}
	affected, err := moveAffectedLocations(ctx, tx, id, before.ParentID, parentID)
	if err != nil {
		return plan, err
	}
	if len(affected) > application.MaxMoveAffectedLocations {
		return plan, application.ErrLocationMoveTooLarge
	}
	n, err := r.guards.ScopedAssignmentsAffectedBy(ctx, tx, affected)
	if err != nil {
		return plan, fmt.Errorf("count scoped role assignments: %w", err)
	}
	plan.affected = n
	admin := true // the CLI (no User) is exempt
	if c.Actor.UserID != "" {
		if admin, err = r.guards.IsAdministrator(ctx, tx, c.Actor.UserID); err != nil {
			return plan, fmt.Errorf("check administrator: %w", err)
		}
	}
	if n > 0 {
		plan.preview.AdministratorRequired = true
		plan.preview.Allowed = admin
	}
	if admin {
		plan.preview.ScopedAssignmentsAffected = &n
	}
	return plan, nil
}

// moveAffectedLocations returns the Locations whose scoped role assignments a move of id from oldParent to newParent
// affects: the subtree of id and the symmetric difference of the ancestor-or-self chains of both parents. It runs
// under the tree lock, so the tree cannot change underneath.
func moveAffectedLocations(ctx context.Context, tx pgx.Tx, id string, oldParent, newParent *string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE down(id, depth) AS (
			SELECT id, 1 FROM organization.locations WHERE id = $1::uuid
			UNION ALL
			SELECT l.id, down.depth + 1 FROM organization.locations l JOIN down ON l.parent_location_id = down.id WHERE down.depth < 32)
		SELECT id::text FROM down`, id)
	if err != nil {
		return nil, fmt.Errorf("location subtree: %w", err)
	}
	out, err := collectStrings(rows, "location subtree")
	if err != nil {
		return nil, err
	}
	oldChain, err := ancestorsOrSelf(ctx, tx, oldParent)
	if err != nil {
		return nil, err
	}
	newChain, err := ancestorsOrSelf(ctx, tx, newParent)
	if err != nil {
		return nil, err
	}
	for _, a := range oldChain {
		if !slices.Contains(newChain, a) {
			out = append(out, a)
		}
	}
	for _, a := range newChain {
		if !slices.Contains(oldChain, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// ancestorsOrSelf returns the Location and its ancestors; nothing for nil.
func ancestorsOrSelf(ctx context.Context, tx pgx.Tx, id *string) ([]string, error) {
	if id == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE up(id, parent, depth) AS (
			SELECT id, parent_location_id, 1 FROM organization.locations WHERE id = $1::uuid
			UNION ALL
			SELECT l.id, l.parent_location_id, up.depth + 1 FROM organization.locations l JOIN up ON l.id = up.parent WHERE up.depth < 32)
		SELECT id::text FROM up`, *id)
	if err != nil {
		return nil, fmt.Errorf("location ancestors: %w", err)
	}
	return collectStrings(rows, "location ancestors")
}

// MoveLocation re-parents a Location after the move guard (planLocationMove). The audit record carries the number of
// affected Location-scoped role assignments; when it is not zero, ScopedLocationMoved tells the other platform
// administrators (consumer in turaco-worker).
func (r *Repository) MoveLocation(ctx context.Context, c application.Caller, id string, version int, parentID *string) (application.Location, error) {
	var out application.Location
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		plan, err := r.planLocationMove(ctx, tx, c, id, version, parentID, false)
		if err != nil {
			return err
		}
		if plan.noop {
			out = plan.before
			return nil
		}
		if !plan.preview.Allowed {
			return application.ErrLocationMoveRequiresAdministrator
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
		affected := plan.affected
		if err := r.record(ctx, tx, c, "organization.location.moved", "location", id,
			map[string]any{"parentId": plan.before.ParentID}, map[string]any{"parentId": out.ParentID, "version": out.Version},
			map[string]any{"scopedAssignmentsAffected": affected}); err != nil {
			return err
		}
		if err := events.Publish(ctx, tx, events.Publication{Type: "LocationChanged", ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
			Payload: map[string]any{"locationId": id, "operation": "moved"}}); err != nil {
			return err
		}
		if affected == 0 {
			return nil
		}
		return events.Publish(ctx, tx, events.Publication{Type: application.EventScopedLocationMoved, ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
			Payload: map[string]any{"locationId": id, "fromParentId": plan.before.ParentID, "toParentId": out.ParentID, "scopedAssignmentsAffected": affected}})
	})
	return finishPeople(out, err, "move location")
}

var _ application.LocationMoveStore = (*Repository)(nil)

// LocationNameTx implements application.LocationMoveStore.
func (r *Repository) LocationNameTx(ctx context.Context, tx pgx.Tx, id string) (string, bool, error) {
	u, ok := parseID(id)
	if !ok {
		return "", false, nil
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM organization.locations WHERE id = $1`, u).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("location name: %w", err)
	}
	return name, true, nil
}

// PreviewLocationMove implements application.PeopleStore: planLocationMove in a transaction that changes nothing.
func (r *Repository) PreviewLocationMove(ctx context.Context, c application.Caller, id string, version int, parentID *string) (application.LocationMovePreview, error) {
	var out application.LocationMovePreview
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		plan, err := r.planLocationMove(ctx, tx, c, id, version, parentID, true)
		out = plan.preview
		return err
	})
	return finishPeople(out, err, "preview location move")
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
		var err error
		out, err = r.createDepartmentTx(ctx, tx, c, in)
		return err
	})
	return finishPeople(out, err, "create department")
}

// createDepartmentTx runs the operation inside the caller's transaction (single operations, imports and bulk operations share it).
func (r *Repository) createDepartmentTx(ctx context.Context, tx pgx.Tx, c application.Caller, in application.NewDepartmentInput) (application.Department, error) {
	var out application.Department
	err := func() error {
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
	}()
	return out, err
}

func (r *Repository) UpdateDepartment(ctx context.Context, c application.Caller, id string, in application.DepartmentChange) (application.Department, error) {
	var out application.Department
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = r.updateDepartmentTx(ctx, tx, c, id, in)
		return err
	})
	return finishPeople(out, err, "update department")
}

// updateDepartmentTx runs the operation inside the caller's transaction (single operations, imports and bulk operations share it).
func (r *Repository) updateDepartmentTx(ctx context.Context, tx pgx.Tx, c application.Caller, id string, in application.DepartmentChange) (application.Department, error) {
	var out application.Department
	err := func() error {
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
	}()
	return out, err
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
