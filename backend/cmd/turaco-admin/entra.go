package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
)

var entraGUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type entraArgs struct {
	user, tenant, object string
}

// runEntra implements the "entra" command group: linking a User to a Microsoft Entra identity (tenant id and object
// id copied from the Entra portal). It never searches Entra and never links by email. Every change is audited with
// the CLI actor in the same transaction (ADR-0035).
func runEntra(ctx context.Context, e env, command string, args []string) error {
	repo := orgrepo.New(e.pool)
	switch command {
	case "link", "unlink":
		a, err := parseEntraArgs(command, args)
		if err != nil {
			return err
		}
		provider := orgpublic.EntraProviderKey(a.tenant)
		subject := strings.ToLower(a.object)
		if command == "link" {
			userID := a.user
			if !entraGUIDPattern.MatchString(userID) {
				subjects := orgpublic.NewAuthorizationSubjects(repo)
				id, found, err := subjects.FindUser(ctx, a.user)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("user %q not found or ambiguous; use the user's UUID", a.user)
				}
				userID = id
			}
			err := repo.LinkExternalIdentity(ctx, e.auditActor(), "cli-entra-link", strings.ToLower(userID), provider, subject, "cli")
			if err := entraMessage(err); err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "linked user %s to %s / %s\n", userID, provider, subject)
			return nil
		}
		if err := entraMessage(repo.UnlinkExternalIdentity(ctx, e.auditActor(), "cli-entra-unlink", provider, subject, "cli")); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "unlinked %s / %s; Entra sessions of that user were revoked\n", provider, subject)
		return nil
	default:
		return errUsage
	}
}

func parseEntraArgs(command string, args []string) (entraArgs, error) {
	var a entraArgs
	fs := newFlagSet("entra " + command)
	fs.StringVar(&a.user, "user", "", "")
	fs.StringVar(&a.tenant, "tenant", "", "")
	fs.StringVar(&a.object, "object", "", "")
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	switch {
	case fs.NArg() != 0:
		return a, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	case command == "link" && a.user == "":
		return a, fmt.Errorf("%w: --user is required", errUsage)
	case !entraGUIDPattern.MatchString(a.tenant) || !entraGUIDPattern.MatchString(a.object):
		return a, fmt.Errorf("%w: --tenant and --object must be GUIDs (copy them from the Entra portal)", errUsage)
	}
	return a, nil
}

func entraMessage(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, orgrepo.ErrEntraUserNotFound):
		return errors.New("user not found")
	case errors.Is(err, orgrepo.ErrEntraUserNotLinkable):
		return errors.New("the user is inactive or an emergency account and cannot be linked")
	case errors.Is(err, orgrepo.ErrEntraLocalCredential):
		return errors.New("the user has a local credential; link directory or Entra users, or remove the local credential first")
	case errors.Is(err, orgrepo.ErrEntraIdentityTaken):
		return errors.New("this Entra identity is already linked to a user")
	case errors.Is(err, orgrepo.ErrEntraTenantAlreadyUsed):
		return errors.New("the user already has an identity of this tenant; unlink it first")
	case errors.Is(err, orgrepo.ErrEntraNotLinked):
		return errors.New("no such identity link")
	default:
		return err
	}
}
