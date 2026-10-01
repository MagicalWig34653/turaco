package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
)

// runRole implements the "role" command group. Every change uses the same
// audited application operations as the API, with the CLI actor.
func runRole(ctx context.Context, e env, command string, args []string) error {
	subjects := orgpublic.NewAuthorizationSubjects(orgrepo.New(e.pool))
	svc := roles.NewService(e.pool, subjects)
	switch command {
	case "list":
		if len(args) != 0 {
			return fmt.Errorf("%w: role list takes no arguments", errUsage)
		}
		return roleList(ctx, e, svc)
	case "grant":
		a, err := parseGrantArgs(args)
		if err != nil {
			return err
		}
		return roleGrant(ctx, e, svc, subjects, a)
	case "revoke":
		a, err := parseRevokeArgs(args)
		if err != nil {
			return err
		}
		return roleRevoke(ctx, e, svc, a)
	default:
		return errUsage
	}
}

// auditActor is the audit actor of this invocation: the system actor "cli"
// with the (informational) OS user from e.actor.
func (e env) auditActor() audit.Actor {
	var marker struct {
		OSUser string `json:"osUser"`
	}
	_ = json.Unmarshal(e.actor, &marker)
	return audit.CLIActor(marker.OSUser)
}

type grantArgs struct {
	role  string
	user  string
	group string
}

type revokeArgs struct {
	assignment string
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseGrantArgs(args []string) (grantArgs, error) {
	var a grantArgs
	fs := newFlagSet("role grant")
	fs.StringVar(&a.role, "role", "", "")
	fs.StringVar(&a.user, "user", "", "")
	fs.StringVar(&a.group, "group", "", "")
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	switch {
	case fs.NArg() != 0:
		return a, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	case a.role == "":
		return a, fmt.Errorf("%w: --role is required", errUsage)
	case (a.user == "") == (a.group == ""):
		return a, fmt.Errorf("%w: exactly one of --user or --group is required", errUsage)
	}
	return a, nil
}

func parseRevokeArgs(args []string) (revokeArgs, error) {
	var a revokeArgs
	fs := newFlagSet("role revoke")
	fs.StringVar(&a.assignment, "assignment", "", "")
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	switch {
	case fs.NArg() != 0:
		return a, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	case a.assignment == "":
		return a, fmt.Errorf("%w: --assignment is required", errUsage)
	}
	return a, nil
}

func roleList(ctx context.Context, e env, svc *roles.Service) error {
	list, err := svc.ListRoles(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tNAME\tBUILT-IN\tACTIVE ASSIGNMENTS")
	for _, r := range list {
		fmt.Fprintf(tw, "%s\t%s\t%t\t%d\n", r.Key, r.Name, r.BuiltIn, r.ActiveAssignments)
	}
	return tw.Flush()
}

func roleGrant(ctx context.Context, e env, svc *roles.Service, subjects *orgpublic.AuthorizationSubjects, a grantArgs) error {
	role, err := svc.GetRoleByKey(ctx, a.role)
	if errors.Is(err, roles.ErrNotFound) {
		return fmt.Errorf("role %q not found (see: turaco-admin role list)", a.role)
	}
	if err != nil {
		return err
	}
	in := roles.AssignInput{RoleID: role.ID}
	if a.user != "" {
		id, found, err := subjects.FindUser(ctx, a.user)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("user %q not found or ambiguous; use the user's UUID", a.user)
		}
		in.SubjectType, in.SubjectID = roles.SubjectUser, id
	} else {
		in.SubjectType, in.SubjectID = roles.SubjectDirectoryGroup, a.group
	}
	assignment, err := svc.AssignRole(ctx, e.auditActor(), e.correlationID(), in)
	switch {
	case errors.Is(err, roles.ErrSubjectNotFound):
		return fmt.Errorf("%s %q does not exist (or the directory group is deleted)", in.SubjectType, in.SubjectID)
	case errors.Is(err, roles.ErrDuplicateAssignment):
		return fmt.Errorf("role %q is already assigned to this %s", a.role, in.SubjectType)
	case err != nil:
		return err
	}
	name := assignment.SubjectDisplayName
	if name == "" {
		name = assignment.SubjectID
	}
	fmt.Fprintf(e.stdout, "granted role %s to %s %s (assignment %s)\n", role.Key, assignment.SubjectType, name, assignment.ID)
	return nil
}

func roleRevoke(ctx context.Context, e env, svc *roles.Service, a revokeArgs) error {
	// The CLI is the recovery path, so it may revoke the last administrator.
	assignment, outcome, err := svc.RevokeAssignment(ctx, e.auditActor(), e.correlationID(), a.assignment, true)
	if errors.Is(err, roles.ErrNotFound) {
		return fmt.Errorf("assignment %q not found", a.assignment)
	}
	if err != nil {
		return err
	}
	if outcome.AlreadyRevoked {
		fmt.Fprintf(e.stdout, "assignment %s was already revoked\n", assignment.ID)
		return nil
	}
	if outcome.LastAdministratorBypassed {
		fmt.Fprintln(e.stdout, "warning: this was the last active platform-administrator assignment of a user; nobody can administer Turaco until another administrator is granted")
	}
	fmt.Fprintf(e.stdout, "revoked assignment %s (role %s)\n", assignment.ID, assignment.RoleKey)
	return nil
}
