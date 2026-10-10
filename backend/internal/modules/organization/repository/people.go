package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

var _ application.PeopleStore = (*Repository)(nil)

// Lock order of the people operations: the Organization row (user, then location or department, then team), then
// platform.roles (administrator role) inside the access guards. Tree changes additionally hold a transaction-level
// advisory lock per tree so two concurrent moves cannot create a cycle.
const (
	treeLockLocations   = "organization.locations.tree"
	treeLockDepartments = "organization.departments.tree"
	treeLockManagers    = "organization.users.managers"
)

func lockTree(ctx context.Context, tx pgx.Tx, name string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
		return fmt.Errorf("lock %s: %w", name, err)
	}
	return nil
}

func actorUserID(c application.Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	id := c.Actor.UserID
	return &id
}

func (r *Repository) record(ctx context.Context, tx pgx.Tx, c application.Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func lockUser(ctx context.Context, tx pgx.Tx, id string) (application.User, error) {
	u, ok := parseID(id)
	if !ok {
		return application.User{}, application.ErrNotFound
	}
	out, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM organization.users WHERE id = $1 FOR UPDATE`, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.User{}, application.ErrNotFound
	}
	if err != nil {
		return application.User{}, fmt.Errorf("lock user: %w", err)
	}
	return out, nil
}

func reloadUser(ctx context.Context, tx pgx.Tx, id string) (application.User, error) {
	return scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM organization.users WHERE id = $1::uuid`, id))
}

func (r *Repository) requireGuards() error {
	if r.guards == nil || r.sessions == nil {
		return application.ErrNoGuards
	}
	return nil
}

// finishPeople passes domain errors through unchanged and wraps everything else.
func finishPeople[T any](v T, err error, what string) (T, error) {
	var zero T
	var owned *application.FieldDirectoryOwnedError
	var impact *application.ImpactError
	var inv *application.InvalidInputError
	switch {
	case err == nil:
		return v, nil
	case errors.As(err, &owned), errors.As(err, &impact), errors.As(err, &inv),
		errors.Is(err, application.ErrNotFound), errors.Is(err, application.ErrConflict), errors.Is(err, application.ErrVersionConflict),
		errors.Is(err, application.ErrDirectoryUser), errors.Is(err, application.ErrEmergencyAccount), errors.Is(err, application.ErrLastAdministrator),
		errors.Is(err, application.ErrSelfOperation), errors.Is(err, application.ErrTeamMembershipSelf), errors.Is(err, application.ErrDominanceRequired),
		errors.Is(err, application.ErrDirectoryIdentityDisabled), errors.Is(err, application.ErrWrongState), errors.Is(err, application.ErrHierarchy),
		errors.Is(err, application.ErrTargetInactive), errors.Is(err, application.ErrExternalNeedsPermission), errors.Is(err, application.ErrNoGuards),
		errors.Is(err, application.ErrUserNotActive), errors.Is(err, application.ErrTeamInactive),
		errors.Is(err, application.ErrMailNotConfigured), errors.Is(err, application.ErrBaseURLNotConfigured), errors.Is(err, application.ErrMailFailed),
		errors.Is(err, application.ErrNoEmail):
		return zero, err
	default:
		return zero, fmt.Errorf("%s: %w", what, err)
	}
}

// dominance maps the guard's error to the module error.
func (r *Repository) dominance(ctx context.Context, tx pgx.Tx, c application.Caller, targetID string) error {
	if err := r.requireGuards(); err != nil {
		return err
	}
	if err := r.guards.RequireDominance(ctx, tx, c.Actor, targetID); err != nil {
		if errors.Is(err, application.ErrDominanceRequired) || strings.Contains(err.Error(), "dominate") {
			return application.ErrDominanceRequired
		}
		return err
	}
	return nil
}

// ---- users ----

func (r *Repository) CreateLocalUser(ctx context.Context, c application.Caller, in application.NewUserInput) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if in.DepartmentID != nil {
			if err := requireActive(ctx, tx, "departments", *in.DepartmentID); err != nil {
				return err
			}
		}
		if in.LocationID != nil {
			if err := requireActive(ctx, tx, "locations", *in.LocationID); err != nil {
				return err
			}
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO organization.users (display_name, given_name, family_name, primary_email, employee_number, status, status_source,
				origin, account_kind, department_id, primary_location_id)
			VALUES ($1, $2, $3, $4, $5, 'active', 'platform', 'local', 'employee', $6::uuid, $7::uuid) RETURNING id::text`,
			in.DisplayName, in.GivenName, in.FamilyName, in.PrimaryEmail, in.EmployeeNumber, in.DepartmentID, in.LocationID).Scan(&id)
		if isUnique(err) {
			return application.ErrConflict
		}
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.user.created_local", "user", id, nil,
			map[string]any{"status": out.Status, "statusSource": out.StatusSource, "origin": out.Origin, "accountKind": out.AccountKind,
				"departmentId": out.DepartmentID, "primaryLocationId": out.PrimaryLocationID}, nil)
	})
	return finishPeople(out, err, "create local user")
}

// requireActive checks that the referenced department or location exists and is active (table is a constant).
func requireActive(ctx context.Context, tx pgx.Tx, table, id string) error {
	u, ok := parseID(id)
	if !ok {
		return application.ErrNotFound
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM organization.`+table+` WHERE id = $1 FOR SHARE`, u).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check %s: %w", table, err)
	}
	if !active {
		return application.ErrTargetInactive
	}
	return nil
}

func (r *Repository) UpdateProfile(ctx context.Context, c application.Caller, id string, in application.ProfileChange) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if before.Version != in.ExpectedVersion {
			return application.ErrVersionConflict
		}
		if before.Origin == application.OriginDirectory {
			var owned []string
			for _, f := range []string{"displayName", "givenName", "familyName", "primaryEmail", "employeeNumber"} {
				if slices.Contains(profileFields(in), f) {
					owned = append(owned, f)
				}
			}
			if len(owned) > 0 {
				return &application.FieldDirectoryOwnedError{Fields: owned}
			}
		}
		next := before
		var changed []string
		apply := func(set bool, name string, cur *string, val *string, assign func(*string)) {
			if set && !equalPtr(cur, val) {
				assign(val)
				changed = append(changed, name)
			}
		}
		if in.DisplayName.Set && in.DisplayName.Value != nil && *in.DisplayName.Value != before.DisplayName {
			next.DisplayName = *in.DisplayName.Value
			changed = append(changed, "displayName")
		}
		apply(in.GivenName.Set, "givenName", before.GivenName, in.GivenName.Value, func(v *string) { next.GivenName = v })
		apply(in.FamilyName.Set, "familyName", before.FamilyName, in.FamilyName.Value, func(v *string) { next.FamilyName = v })
		apply(in.EmployeeNumber.Set, "employeeNumber", before.EmployeeNumber, in.EmployeeNumber.Value, func(v *string) { next.EmployeeNumber = v })
		emailChanged := in.PrimaryEmail.Set && !equalPtrFold(before.PrimaryEmail, in.PrimaryEmail.Value)
		if emailChanged {
			// Account-takeover capable (review rule R1): the new address receives future resets.
			if err := r.dominance(ctx, tx, c, id); err != nil {
				return err
			}
			next.PrimaryEmail = in.PrimaryEmail.Value
			changed = append(changed, "primaryEmail")
		}
		if len(changed) == 0 {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE organization.users SET display_name = $2, given_name = $3, family_name = $4, primary_email = $5, employee_number = $6,
				version = version + 1, updated_at = now() WHERE id = $1::uuid`,
			id, next.DisplayName, next.GivenName, next.FamilyName, next.PrimaryEmail, next.EmployeeNumber); err != nil {
			if isUnique(err) {
				return application.ErrConflict
			}
			return fmt.Errorf("update profile: %w", err)
		}
		meta := map[string]any{"changed": changed}
		if emailChanged {
			// Open invitation and reset tokens were issued for the old address: they die with it.
			n, err := r.sessions.RevokeCredentialTokens(ctx, tx, id, "email_changed", c.Actor, c.CorrelationID, time.Now())
			if err != nil {
				return err
			}
			meta["credentialTokensRevoked"] = n
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.user.profile_changed", "user", id, nil, map[string]any{"version": out.Version}, meta)
	})
	return finishPeople(out, err, "update profile")
}

func profileFields(in application.ProfileChange) []string {
	var out []string
	if in.DisplayName.Set {
		out = append(out, "displayName")
	}
	if in.GivenName.Set {
		out = append(out, "givenName")
	}
	if in.FamilyName.Set {
		out = append(out, "familyName")
	}
	if in.PrimaryEmail.Set {
		out = append(out, "primaryEmail")
	}
	if in.EmployeeNumber.Set {
		out = append(out, "employeeNumber")
	}
	return out
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalPtrFold(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return strings.EqualFold(*a, *b)
}

// setUserLink sets department, primary location or manager (column and audit action are constants).
func (r *Repository) setUserLink(ctx context.Context, c application.Caller, id string, version int, column, action string, target *string,
	check func(ctx context.Context, tx pgx.Tx, before application.User, target string) error, current func(application.User) *string) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if target != nil {
			if err := check(ctx, tx, before, *target); err != nil {
				return err
			}
		}
		if equalPtr(current(before), target) {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET `+column+` = $2::uuid, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, target); err != nil {
			return fmt.Errorf("set %s: %w", column, err)
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, action, "user", id, map[string]any{column: current(before)}, map[string]any{column: target, "version": out.Version}, nil)
	})
	return finishPeople(out, err, "set "+column)
}

func (r *Repository) SetDepartment(ctx context.Context, c application.Caller, id string, version int, departmentID *string) (application.User, error) {
	return r.setUserLink(ctx, c, id, version, "department_id", "organization.user.department_set", departmentID,
		func(ctx context.Context, tx pgx.Tx, _ application.User, t string) error {
			return requireActive(ctx, tx, "departments", t)
		},
		func(u application.User) *string { return u.DepartmentID })
}

func (r *Repository) SetPrimaryLocation(ctx context.Context, c application.Caller, id string, version int, locationID *string) (application.User, error) {
	return r.setUserLink(ctx, c, id, version, "primary_location_id", "organization.user.location_set", locationID,
		func(ctx context.Context, tx pgx.Tx, _ application.User, t string) error {
			return requireActive(ctx, tx, "locations", t)
		},
		func(u application.User) *string { return u.PrimaryLocationID })
}

func (r *Repository) SetManager(ctx context.Context, c application.Caller, id string, version int, managerID *string) (application.User, error) {
	if managerID != nil {
		// The cycle check below reads the whole manager chain.
		if _, ok := parseID(*managerID); !ok {
			return application.User{}, application.ErrNotFound
		}
	}
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockTree(ctx, tx, treeLockManagers); err != nil {
			return err
		}
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if before.Origin == application.OriginDirectory {
			return &application.FieldDirectoryOwnedError{Fields: []string{"manager"}}
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if managerID != nil {
			if strings.EqualFold(*managerID, id) {
				return application.ErrHierarchy
			}
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM organization.users WHERE id = $1::uuid`, *managerID).Scan(&status)
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("check manager: %w", err)
			}
			if status != application.StatusActive {
				return application.ErrTargetInactive
			}
			var cycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE up(id, depth) AS (
					SELECT $1::uuid, 0
					UNION ALL
					SELECT u.manager_user_id, up.depth + 1 FROM organization.users u JOIN up ON u.id = up.id
					WHERE u.manager_user_id IS NOT NULL AND up.depth < 100)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, *managerID, id).Scan(&cycle); err != nil {
				return fmt.Errorf("check manager cycle: %w", err)
			}
			if cycle {
				return application.ErrHierarchy
			}
		}
		if equalPtr(before.ManagerUserID, managerID) {
			out = before
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE organization.users SET manager_user_id = $2::uuid, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, managerID); err != nil {
			return fmt.Errorf("set manager: %w", err)
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		return r.record(ctx, tx, c, "organization.user.manager_set", "user", id,
			map[string]any{"managerUserId": before.ManagerUserID}, map[string]any{"managerUserId": managerID, "version": out.Version}, nil)
	})
	return finishPeople(out, err, "set manager")
}

func (r *Repository) ChangeStatus(ctx context.Context, c application.Caller, id string, version int, op, reason string) (application.User, error) {
	var out application.User
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		before, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.Origin == application.OriginEmergency {
			return application.ErrEmergencyAccount
		}
		if c.Actor.UserID != "" && strings.EqualFold(c.Actor.UserID, id) {
			return application.ErrSelfOperation
		}
		if before.Version != version {
			return application.ErrVersionConflict
		}
		if err := r.dominance(ctx, tx, c, id); err != nil {
			return err
		}
		var to, action, event string
		leavesActive := false
		switch op {
		case application.OpDeactivate:
			if before.Status != application.StatusActive {
				return application.ErrWrongState
			}
			to, action, event, leavesActive = application.StatusInactive, "organization.user.deactivated", "UserDeactivated", true
		case application.OpMarkDeparted:
			if before.Status != application.StatusActive && before.Status != application.StatusInactive {
				return application.ErrWrongState
			}
			to, action, event, leavesActive = application.StatusDeparted, "organization.user.departed", "UserDeparted", before.Status == application.StatusActive
		case application.OpReactivate:
			if before.Status != application.StatusInactive && before.Status != application.StatusDeparted {
				return application.ErrWrongState
			}
			if before.Origin == application.OriginDirectory {
				var enabled bool
				if err := tx.QueryRow(ctx, `
					SELECT EXISTS (SELECT 1 FROM organization.external_identities WHERE user_id = $1::uuid AND enabled AND deleted_observed_at IS NULL)`, id).Scan(&enabled); err != nil {
					return fmt.Errorf("check directory identity: %w", err)
				}
				if !enabled {
					return application.ErrDirectoryIdentityDisabled
				}
			}
			to, action = application.StatusActive, "organization.user.reactivated"
		default:
			return fmt.Errorf("change status: unknown operation %q", op)
		}
		if leavesActive {
			lost, err := r.guards.WouldLoseLastAdministrator(ctx, tx, id)
			if err != nil {
				return err
			}
			if lost {
				return application.ErrLastAdministrator
			}
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `
			UPDATE organization.users SET status = $2, status_source = 'platform', version = version + 1, updated_at = $3 WHERE id = $1::uuid`, id, to, now); err != nil {
			return fmt.Errorf("change status: %w", err)
		}
		meta := map[string]any{"reasonCode": reason}
		if leavesActive {
			n, err := r.sessions.RevokeSessions(ctx, tx, id, "user_deactivated", c.Actor, c.CorrelationID, now)
			if err != nil {
				return err
			}
			meta["sessionsRevoked"] = n
			// Any outstanding invitation or reset must not outlive the account's active period.
			if _, err := r.sessions.RevokeCredentialTokens(ctx, tx, id, "user_deactivated", c.Actor, c.CorrelationID, now); err != nil {
				return err
			}
		}
		if out, err = reloadUser(ctx, tx, id); err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		if err := r.record(ctx, tx, c, action, "user", id,
			map[string]any{"status": before.Status, "statusSource": before.StatusSource},
			map[string]any{"status": out.Status, "statusSource": out.StatusSource, "version": out.Version}, meta); err != nil {
			return err
		}
		if event != "" {
			return events.Publish(ctx, tx, events.Publication{Type: event, ActorID: actorUserID(c), CorrelationID: c.CorrelationID,
				Payload: map[string]any{"userId": id, "reasonCode": reason}})
		}
		return nil
	})
	return finishPeople(out, err, "change user status")
}

// IssueCredentialLink issues an invitation (never-activated local account) or reset (activated local account).
// Both are takeover-capable: they need the dominance rule (R1). The link is built from the configured base URL
// only (R7). It is mailed to the stored primary address; without a mail channel only the invitation of a
// never-activated account is shown, once, and only to a platform administrator.
func (r *Repository) IssueCredentialLink(ctx context.Context, c application.Caller, id, purpose string) (application.CredentialLink, error) {
	var out application.CredentialLink
	if r.issuer == nil || r.mailer == nil {
		return out, application.ErrNoGuards
	}
	if !r.mailer.BaseURLConfigured() {
		return out, application.ErrBaseURLNotConfigured
	}
	if purpose == application.CredentialReset && !r.mailer.MailConfigured() {
		return out, application.ErrMailNotConfigured
	}
	if !r.mailer.MailConfigured() && !c.PlatformAdmin {
		// Without mail the only delivery is showing the link, which only an administrator may see (ADR-0034).
		return out, application.ErrMailNotConfigured
	}
	var token, email, name string
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		u, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		switch u.Origin {
		case application.OriginEmergency:
			return application.ErrEmergencyAccount
		case application.OriginDirectory:
			return application.ErrDirectoryUser
		}
		if u.Status != application.StatusActive {
			return application.ErrWrongState
		}
		if r.mailer.MailConfigured() && (u.PrimaryEmail == nil || *u.PrimaryEmail == "") {
			return application.ErrNoEmail
		}
		if err := r.dominance(ctx, tx, c, id); err != nil {
			return err
		}
		activated, err := r.issuer.EnsureLocalCredential(ctx, tx, id)
		if err != nil {
			return err
		}
		if (purpose == application.CredentialInvitation) == activated {
			// An invitation is for a never-activated account, a reset for an activated one.
			return application.ErrWrongState
		}
		token, out.ExpiresAt, err = r.issuer.IssueToken(ctx, tx, id, purpose, c.Actor, c.CorrelationID)
		if err != nil {
			return err
		}
		if u.PrimaryEmail != nil {
			email = *u.PrimaryEmail
		}
		name = u.DisplayName
		return nil
	})
	if err != nil {
		return finishPeople(application.CredentialLink{}, err, "issue credential link")
	}
	link := r.mailer.Link(token, purpose)
	if r.mailer.MailConfigured() {
		if err := r.mailer.Send(ctx, email, name, purpose, link); err != nil {
			return application.CredentialLink{}, application.ErrMailFailed
		}
		out.Mailed = true
		return out, nil
	}
	if purpose == application.CredentialInvitation && c.PlatformAdmin {
		out.Link = link
	}
	return out, nil
}
