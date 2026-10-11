package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// firstAdminLockKey serializes concurrent "admin create-first" runs (and nothing else).
const firstAdminLockKey = "turaco-admin:create-first"

type firstAdminArgs struct {
	user, tenant, object string
	force                bool
}

// runAdmin implements the "admin" command group: the documented production path for the first platform
// administrator (ADR-0034, ADR-0035). It grants the built-in platform-administrator role to a User that signs in
// through a directory or Microsoft Entra, never to a local account.
func runAdmin(ctx context.Context, e env, command string, args []string) error {
	if command != "create-first" {
		return errUsage
	}
	a, err := parseFirstAdminArgs(args)
	if err != nil {
		return err
	}
	return createFirstAdmin(ctx, e, a)
}

func parseFirstAdminArgs(args []string) (firstAdminArgs, error) {
	var a firstAdminArgs
	fs := newFlagSet("admin create-first")
	fs.StringVar(&a.user, "user", "", "")
	fs.StringVar(&a.tenant, "tenant", "", "")
	fs.StringVar(&a.object, "object", "", "")
	fs.BoolVar(&a.force, "force", false, "")
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	switch {
	case fs.NArg() != 0:
		return a, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	case a.user == "":
		return a, fmt.Errorf("%w: --user is required", errUsage)
	case (a.tenant == "") != (a.object == ""):
		return a, fmt.Errorf("%w: --tenant and --object are given together", errUsage)
	case a.tenant != "" && (!entraGUIDPattern.MatchString(a.tenant) || !entraGUIDPattern.MatchString(a.object)):
		return a, fmt.Errorf("%w: --tenant and --object must be GUIDs (copy them from the Entra portal)", errUsage)
	}
	return a, nil
}

func createFirstAdmin(ctx context.Context, e env, a firstAdminArgs) error {
	// One session-level advisory lock makes the "no administrator yet" check and the grant atomic against a
	// parallel run; the lock is released with the connection.
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, firstAdminLockKey); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, firstAdminLockKey)
	}()

	repo := orgrepo.New(e.pool)
	subjects := orgpublic.NewAuthorizationSubjects(repo)
	svc := roles.NewService(e.pool, subjects)
	role, err := svc.GetRoleByKey(ctx, roles.AdministratorRoleKey)
	if err != nil {
		return fmt.Errorf("load administrator role (are migrations applied?): %w", err)
	}

	var existing int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments
		WHERE role_id = $1::uuid AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, role.ID).Scan(&existing); err != nil {
		return fmt.Errorf("count administrators: %w", err)
	}
	if existing > 0 && !a.force {
		return fmt.Errorf("a platform administrator already exists (%d active assignment(s)); use the web UI or 'role grant', or pass --force", existing)
	}

	userID, found, err := subjects.FindUser(ctx, a.user)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("user %q not found or ambiguous; wait for the first directory sync, or use the user's UUID", a.user)
	}
	var local bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local')`, userID).Scan(&local); err != nil {
		return fmt.Errorf("check local account: %w", err)
	}
	if !local {
		// A local account without a credential yet (not invited) is still local: classify by the immutable origin too.
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization.users WHERE id = $1::uuid AND origin = 'local')`, userID).Scan(&local); err != nil {
			return fmt.Errorf("check account origin: %w", err)
		}
	}
	if local {
		return errors.New("local accounts cannot be platform administrators; use a directory or Entra user (ADR-0034)")
	}

	if a.tenant != "" {
		provider := orgpublic.EntraProviderKey(a.tenant)
		if err := entraMessage(repo.LinkExternalIdentity(ctx, e.auditActor(), "cli-first-admin-link", strings.ToLower(userID), provider, strings.ToLower(a.object), "cli")); err != nil {
			return err
		}
	}

	correlation := e.correlationID()
	assignment, err := svc.AssignRole(ctx, e.auditActor(), correlation, roles.AssignInput{
		RoleID: role.ID, SubjectType: roles.SubjectUser, SubjectID: userID,
	})
	switch {
	case errors.Is(err, roles.ErrDuplicateAssignment):
		return errors.New("this user is already a platform administrator")
	case errors.Is(err, roles.ErrLocalAccountHighRisk):
		return errors.New("local accounts cannot be platform administrators; use a directory or Entra user (ADR-0034)")
	case errors.Is(err, roles.ErrSubjectNotFound):
		return fmt.Errorf("user %q does not exist", a.user)
	case err != nil:
		return err
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{
			Action: "authorization.first_administrator.created", TargetType: "role_assignment", TargetID: assignment.ID,
			Actor: e.auditActor(), CorrelationID: correlation,
			Metadata: map[string]any{"userId": userID, "forced": a.force && existing > 0, "existingAdministrators": existing, "entraLinked": a.tenant != ""},
		})
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	fmt.Fprintf(e.stdout, "user %s is now a platform administrator (assignment %s)\n", userID, assignment.ID)
	return nil
}
