package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

// The emergency (break-glass) account lifecycle. Accounts are created
// disabled; enabling is an explicit, separately audited step. Passwords are
// stored as argon2id hashes only. docs/security/identity-access-design.md §7.

const (
	generatedPasswordLength   = 24
	generatedPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
)

// runEmergency implements the "emergency" command group.
func runEmergency(ctx context.Context, e env, command string, args []string) error {
	switch command {
	case "create":
		return emergencyCreate(ctx, e, args)
	case "set-password":
		return emergencySetPassword(ctx, e, args)
	case "enable":
		return emergencySetEnabled(ctx, e, args, true)
	case "disable":
		return emergencySetEnabled(ctx, e, args, false)
	default:
		return errUsage
	}
}

type emergencyFlags struct {
	login         string
	displayName   string
	passwordStdin bool
}

func parseEmergencyFlags(command string, args []string, withDisplayName, withPassword bool) (emergencyFlags, error) {
	var f emergencyFlags
	fs := flag.NewFlagSet("emergency "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.login, "login", "", "")
	if withDisplayName {
		fs.StringVar(&f.displayName, "display-name", "", "")
	}
	if withPassword {
		fs.BoolVar(&f.passwordStdin, "password-stdin", false, "")
	}
	if err := fs.Parse(args); err != nil {
		return f, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	f.login = strings.TrimSpace(f.login)
	if f.login == "" {
		return f, fmt.Errorf("%w: --login is required", errUsage)
	}
	if !authentication.ValidLoginName(f.login) {
		return f, fmt.Errorf("%w: --login must match [a-z0-9][a-z0-9._-]{1,62}", errUsage)
	}
	if withDisplayName && strings.TrimSpace(f.displayName) == "" {
		return f, fmt.Errorf("%w: --display-name is required", errUsage)
	}
	return f, nil
}

// passwordFor returns the password to set: the first line of stdin with
// --password-stdin, otherwise a generated one. generated tells the caller to
// print it once.
func passwordFor(e env, fromStdin bool) (password string, generated bool, err error) {
	if !fromStdin {
		pw, err := generatePassword()
		return pw, true, err
	}
	line, err := bufio.NewReader(io.LimitReader(e.stdin, authentication.MaxPasswordLength+3)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read password from stdin: %w", err)
	}
	password = strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", false, errors.New("no password on stdin")
	}
	return password, false, nil
}

func generatePassword() (string, error) {
	out := make([]byte, generatedPasswordLength)
	max := big.NewInt(int64(len(generatedPasswordAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = generatedPasswordAlphabet[n.Int64()]
	}
	return string(out), nil
}

func (e env) correlationID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("cli-%x", b)
}

func (e env) credentialAudit() authentication.LocalCredentialAudit {
	return authentication.LocalCredentialAudit{Actor: e.actor, CorrelationID: e.correlationID(), At: time.Now()}
}

func emergencyCreate(ctx context.Context, e env, args []string) error {
	f, err := parseEmergencyFlags("create", args, true, true)
	if err != nil {
		return err
	}
	password, generated, err := passwordFor(e, f.passwordStdin)
	if err != nil {
		return err
	}
	hash, err := authentication.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	accounts := public.NewLoginAccounts(repository.New(e.pool), nil)
	audit := e.credentialAudit()
	var userID string
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		if userID, err = accounts.CreateEmergencyUser(ctx, tx, f.displayName, audit.CorrelationID, e.actor); err != nil {
			return err
		}
		return authentication.CreateLocalCredential(ctx, tx, userID, f.login, hash, audit)
	})
	switch {
	case errors.Is(err, authentication.ErrLocalCredentialExist):
		return fmt.Errorf("an emergency account with login %q already exists", f.login)
	case errors.Is(err, public.ErrInvalidLocalUser):
		return fmt.Errorf("%w: %v", errUsage, err)
	case err != nil:
		return fmt.Errorf("create emergency account: %w", err)
	}
	fmt.Fprintf(e.stdout, "Created emergency account %q (user %s). It is disabled; run `turaco-admin emergency enable --login %s` to enable it.\n", f.login, userID, f.login)
	printGenerated(e, generated, password)
	return nil
}

func emergencySetPassword(ctx context.Context, e env, args []string) error {
	f, err := parseEmergencyFlags("set-password", args, false, true)
	if err != nil {
		return err
	}
	password, generated, err := passwordFor(e, f.passwordStdin)
	if err != nil {
		return err
	}
	hash, err := authentication.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	audit := e.credentialAudit()
	revoked := 0
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		userID, err := authentication.SetLocalPassword(ctx, tx, f.login, hash, audit)
		if err != nil {
			return err
		}
		// Sessions opened with the old password end with it.
		revoked, err = authentication.RevokeUserSessions(ctx, tx, userID, "emergency_password_changed", "cli", audit.CorrelationID, audit.At)
		return err
	})
	if errors.Is(err, authentication.ErrLocalCredentialNone) {
		return fmt.Errorf("no emergency account with login %q", f.login)
	}
	if err != nil {
		return fmt.Errorf("set emergency password: %w", err)
	}
	fmt.Fprintf(e.stdout, "Password of emergency account %q changed (%d session(s) revoked).\n", f.login, revoked)
	printGenerated(e, generated, password)
	return nil
}

func emergencySetEnabled(ctx context.Context, e env, args []string, enabled bool) error {
	command := "disable"
	if enabled {
		command = "enable"
	}
	f, err := parseEmergencyFlags(command, args, false, false)
	if err != nil {
		return err
	}
	audit := e.credentialAudit()
	var changed bool
	revoked := 0
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		userID, ch, err := authentication.SetLocalEnabled(ctx, tx, f.login, enabled, audit)
		if err != nil {
			return err
		}
		changed = ch
		if !enabled {
			// Disabling always ends the account's sessions, even when it
			// was already disabled.
			revoked, err = authentication.RevokeUserSessions(ctx, tx, userID, "emergency_account_disabled", "cli", audit.CorrelationID, audit.At)
		}
		return err
	})
	if errors.Is(err, authentication.ErrLocalCredentialNone) {
		return fmt.Errorf("no emergency account with login %q", f.login)
	}
	if err != nil {
		return fmt.Errorf("%s emergency account: %w", command, err)
	}
	state := map[bool]string{true: "enabled", false: "disabled"}[enabled]
	if !changed {
		fmt.Fprintf(e.stdout, "Emergency account %q was already %s.", f.login, state)
	} else {
		fmt.Fprintf(e.stdout, "Emergency account %q is now %s.", f.login, state)
	}
	if !enabled {
		fmt.Fprintf(e.stdout, " %d session(s) revoked.", revoked)
	}
	fmt.Fprintln(e.stdout)
	return nil
}

func printGenerated(e env, generated bool, password string) {
	if generated {
		fmt.Fprintf(e.stdout, "Generated password (shown once, store it securely): %s\n", password)
	}
}
