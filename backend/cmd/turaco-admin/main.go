// Command turaco-admin is the operator CLI for tasks that must work without a
// web login: granting the first administrator role and managing the local
// emergency (break-glass) account. It talks to PostgreSQL directly using
// DATABASE_URL; every change is audited with the actor {"actor":"cli"}.
//
//	turaco-admin role list
//	turaco-admin role grant  --role <key> (--user <username|email|uuid> | --group <uuid>)
//	turaco-admin role revoke --assignment <uuid>
//	turaco-admin emergency create --login <name> --display-name <text> [--password-stdin]
//	turaco-admin emergency set-password --login <name> [--password-stdin]
//	turaco-admin emergency enable|disable --login <name>
//	turaco-admin security import < advisories.json
//	turaco-admin security sync-feeds [--source nvd|cisa_kev] [--since YYYY-MM-DD]
//	turaco-admin demo seed   (APP_ENV=development only)
//	turaco-admin demo seed-hospital   (APP_ENV=development only; hospital IT simulation)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
)

// errUsage marks command-line mistakes; main prints usage and exits 2.
var errUsage = errors.New("usage")

// env is what every command receives.
type env struct {
	pool   *pgxpool.Pool
	cfg    config.Config
	stdin  io.Reader
	stdout io.Writer
	logger *slog.Logger
	// actor is the audit metadata identifying this CLI invocation.
	actor json.RawMessage
}

const usage = `usage:
  turaco-admin role list
  turaco-admin role grant  --role <key> (--user <username|email|uuid> | --group <uuid>)
  turaco-admin role revoke --assignment <uuid>
  turaco-admin emergency create --login <name> --display-name <text> [--password-stdin]
  turaco-admin emergency set-password --login <name> [--password-stdin]
  turaco-admin emergency enable  --login <name>
  turaco-admin emergency disable --login <name>
  turaco-admin entra link   --user <username|email|uuid> --tenant <guid> --object <guid>
  turaco-admin entra unlink --tenant <guid> --object <guid>
  turaco-admin security import < advisories.json
  turaco-admin security sync-feeds [--source nvd|cisa_kev] [--since YYYY-MM-DD]
  turaco-admin demo seed   (development only)
  turaco-admin demo seed-hospital   (development only; hospital IT simulation)`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "turaco-admin: load configuration:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "turaco-admin: open database:", err)
		return 1
	}
	defer pool.Close()

	e := env{pool: pool, cfg: cfg, stdin: stdin, stdout: stdout, logger: logger, actor: cliActor()}
	group, command, rest := args[0], args[1], args[2:]
	switch group {
	case "role":
		err = runRole(ctx, e, command, rest)
	case "emergency":
		err = runEmergency(ctx, e, command, rest)
	case "demo":
		err = runDemo(ctx, e, command, rest)
	case "security":
		err = runSecurity(ctx, e, command, rest)
	case "entra":
		err = runEntra(ctx, e, command, rest)
	default:
		err = errUsage
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, usage)
		return 2
	default:
		fmt.Fprintln(stderr, "turaco-admin:", err)
		return 1
	}
}

// cliActor returns the audit actor metadata for this invocation. The OS user
// is informational only; the database credentials are the real authority.
func cliActor() json.RawMessage {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	raw, _ := json.Marshal(map[string]string{"actor": "cli", "osUser": name})
	return raw
}
