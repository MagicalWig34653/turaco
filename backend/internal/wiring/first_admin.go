package wiring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/setup"
)

// firstAdminLockKey serializes concurrent first-administrator grants from the CLI and the web page.
const firstAdminLockKey = "turaco-admin:create-first"

// FirstAdministrator is the one domain operation behind "turaco-admin admin create-first" and the setup page
// (ADR-0034, ADR-0035): it grants the built-in platform-administrator role to a directory or Entra User, never
// to a local account, and records one audit event.
type FirstAdministrator struct {
	pool *pgxpool.Pool
}

// NewFirstAdministrator creates the operation over the shared pool.
func NewFirstAdministrator(pool *pgxpool.Pool) *FirstAdministrator {
	return &FirstAdministrator{pool: pool}
}

// AdministratorExists reports whether an active assignment of the administrator role exists.
func (f *FirstAdministrator) AdministratorExists(ctx context.Context) (bool, error) {
	n, err := f.count(ctx, f.pool)
	return n > 0, err
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (f *FirstAdministrator) count(ctx context.Context, q queryRower) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments ra
		JOIN platform.roles r ON r.id = ra.role_id
		WHERE r.key = $1 AND ra.revoked_at IS NULL AND (ra.expires_at IS NULL OR ra.expires_at > now())`, roles.AdministratorRoleKey).Scan(&n)
	return n, err
}

// Grant runs the operation. Without Force it is refused while an administrator exists.
func (f *FirstAdministrator) Grant(ctx context.Context, g setup.Grant) (setup.Result, error) {
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return setup.Result{}, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, firstAdminLockKey); err != nil {
		return setup.Result{}, fmt.Errorf("lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, firstAdminLockKey)
	}()

	repo := orgrepo.New(f.pool)
	subjects := orgpublic.NewAuthorizationSubjects(repo)
	svc := roles.NewService(f.pool, subjects)
	role, err := svc.GetRoleByKey(ctx, roles.AdministratorRoleKey)
	if err != nil {
		return setup.Result{}, fmt.Errorf("load administrator role (are migrations applied?): %w", err)
	}
	existing, err := f.count(ctx, conn)
	if err != nil {
		return setup.Result{}, fmt.Errorf("count administrators: %w", err)
	}
	if existing > 0 && !g.Force {
		return setup.Result{}, setup.ErrAdministratorExists
	}
	userID, found, err := subjects.FindUser(ctx, g.User)
	if err != nil {
		return setup.Result{}, err
	}
	if !found {
		return setup.Result{}, setup.ErrUserNotFound
	}
	var local bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local')
		OR EXISTS (SELECT 1 FROM organization.users WHERE id = $1::uuid AND origin = 'local')`, userID).Scan(&local); err != nil {
		return setup.Result{}, fmt.Errorf("check local account: %w", err)
	}
	if local {
		return setup.Result{}, setup.ErrLocalAccount
	}
	if g.Tenant != "" {
		provider := orgpublic.EntraProviderKey(g.Tenant)
		if err := repo.LinkExternalIdentity(ctx, g.Actor, "first-admin-link:"+g.CorrelationID, strings.ToLower(userID), provider, strings.ToLower(g.Object), g.Via); err != nil {
			return setup.Result{}, fmt.Errorf("%w: %v", setup.ErrEntraLink, err)
		}
	}
	assignment, err := svc.AssignRole(ctx, g.Actor, g.CorrelationID, roles.AssignInput{
		RoleID: role.ID, SubjectType: roles.SubjectUser, SubjectID: userID,
	})
	switch {
	case errors.Is(err, roles.ErrDuplicateAssignment):
		return setup.Result{}, setup.ErrAlreadyAdministrator
	case errors.Is(err, roles.ErrLocalAccountHighRisk):
		return setup.Result{}, setup.ErrLocalAccount
	case errors.Is(err, roles.ErrSubjectNotFound):
		return setup.Result{}, setup.ErrUserNotFound
	case err != nil:
		return setup.Result{}, err
	}
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{
			Action: "authorization.first_administrator.created", TargetType: "role_assignment", TargetID: assignment.ID,
			Actor: g.Actor, CorrelationID: g.CorrelationID,
			Metadata: map[string]any{"userId": userID, "forced": g.Force && existing > 0, "existingAdministrators": existing, "entraLinked": g.Tenant != "", "via": g.Via},
		})
	}); err != nil {
		return setup.Result{}, fmt.Errorf("audit: %w", err)
	}
	return setup.Result{UserID: userID, AssignmentID: assignment.ID}, nil
}
