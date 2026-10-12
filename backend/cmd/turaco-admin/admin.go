package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/setup"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

type firstAdminArgs struct {
	user, tenant, object string
	force                bool
}

// runAdmin implements the "admin" command group: the documented production path for the first platform
// administrator (ADR-0034, ADR-0035). It grants the built-in platform-administrator role to a User that signs in
// through a directory or Microsoft Entra, never to a local account.
func runAdmin(ctx context.Context, e env, command string, args []string) error {
	if command == "setup-token" {
		fs := newFlagSet("admin setup-token")
		ttl := fs.Duration("ttl", setup.DefaultTokenTTL, "")
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
			return errUsage
		}
		return issueSetupToken(ctx, e, *ttl)
	}
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
	res, err := wiring.NewFirstAdministrator(e.pool).Grant(ctx, setup.Grant{
		User: a.user, Tenant: a.tenant, Object: a.object, Force: a.force,
		Actor: e.auditActor(), CorrelationID: e.correlationID(), Via: "cli",
	})
	switch {
	case errors.Is(err, setup.ErrAdministratorExists):
		return errors.New("a platform administrator already exists; use the web UI or 'role grant', or pass --force")
	case errors.Is(err, setup.ErrUserNotFound):
		return fmt.Errorf("user %q not found or ambiguous; wait for the first directory sync, or use the user's UUID", a.user)
	case errors.Is(err, setup.ErrLocalAccount):
		return errors.New("local accounts cannot be platform administrators; use a directory or Entra user (ADR-0034)")
	case errors.Is(err, setup.ErrAlreadyAdministrator):
		return errors.New("this user is already a platform administrator")
	case errors.Is(err, setup.ErrEntraLink):
		return entraMessage(errors.Unwrap(err))
	case err != nil:
		return err
	}
	fmt.Fprintf(e.stdout, "user %s is now a platform administrator (assignment %s)\n", res.UserID, res.AssignmentID)
	return nil
}

// issueSetupToken prints a single-use token for the first-administrator web page. It refuses once an
// administrator exists, because the page is closed from then on.
func issueSetupToken(ctx context.Context, e env, ttl time.Duration) error {
	exists, err := wiring.NewFirstAdministrator(e.pool).AdministratorExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("a platform administrator already exists; the setup page is closed")
	}
	token, expires, err := setup.NewStore(e.pool).Issue(ctx, e.auditActor(), e.correlationID(), ttl)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "setup token (valid until %s, single use, shown once):\n%s\n", expires.Local().Format(time.RFC3339), token)
	return nil
}
