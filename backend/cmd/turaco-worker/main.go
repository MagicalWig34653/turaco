package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

var version = "dev"

// directorySyncMargin covers run bookkeeping beyond the fetch/apply timeout.
const directorySyncMargin = 2 * time.Minute

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	ldapCfg, err := config.LoadLDAP(cfg.Environment)
	if err != nil {
		logger.Error("load directory configuration", "error", err)
		os.Exit(1)
	}
	opts := jobs.RunnerOptions{}
	if ldapCfg.Enabled() {
		// A job must not outlive its lock, or another worker would reclaim it.
		if minLock := ldapCfg.SyncTimeout + directorySyncMargin + 5*time.Minute; minLock > 30*time.Minute {
			opts.LockTimeout = minLock
		}
	}
	runner := jobs.NewRunner(pool, opts, logger)

	if ldapCfg.Enabled() {
		if err := registerDirectorySync(runner, pool, ldapCfg, logger); err != nil {
			logger.Error("configure directory sync", "error", err)
			os.Exit(1)
		}
		logger.Info("directory sync enabled", "provider_key", ldapCfg.ProviderKey, "interval", ldapCfg.SyncInterval.String())
	}

	// Consumers are registered here as modules add them (ADR-0024). Events
	// without a consumer are acknowledged, so the outbox does not grow.
	dispatcher := events.NewDispatcher(pool, events.DispatcherOptions{}, logger)
	smtpCfg, err := config.LoadSMTP(cfg.Environment)
	if err != nil {
		logger.Error("load email configuration", "error", err)
		os.Exit(1)
	}
	if smtpCfg.Enabled() {
		if err := registerEmail(runner, pool, smtpCfg); err != nil {
			logger.Error("configure email notifications", "error", err)
			os.Exit(1)
		}
		logger.Info("email notifications enabled", "host", smtpCfg.Host, "port", smtpCfg.Port, "security", smtpCfg.Security)
	}
	if err := registerRecurrence(runner, pool); err != nil {
		logger.Error("configure recurring tasks", "error", err)
		os.Exit(1)
	}
	if err := registerConsumers(dispatcher, pool, smtpCfg.Enabled()); err != nil {
		logger.Error("register outbox consumers", "error", err)
		os.Exit(1)
	}
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		if err := dispatcher.Run(ctx); err != nil {
			logger.Error("outbox dispatcher stopped", "error", err)
		}
	}()

	logger.Info("turaco-worker started", "version", version)
	err = runner.Run(ctx)
	stop()
	<-dispatcherDone
	if err != nil {
		logger.Error("job runner stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("turaco-worker stopped")
}

func registerDirectorySync(runner *jobs.Runner, pool *pgxpool.Pool, cfg config.LDAPConfig, logger *slog.Logger) error {
	password, err := ldap.ReadPasswordFile(cfg.BindPasswordFile)
	if err != nil {
		return err
	}
	src, err := ldap.NewSource(cfg, password, logger)
	if err != nil {
		return err
	}
	sync := orgpublic.NewDirectorySync(orgrepository.New(pool),
		orgpublic.DirectorySyncConfig{MaxMissingPercent: cfg.MaxMissingPercent, RunTimeout: cfg.SyncTimeout},
		nil, logger)
	if err := runner.Register(orgpublic.DirectorySyncJobType, cfg.SyncTimeout+directorySyncMargin, directorySyncHandler(sync, src, logger)); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{
		JobType:     orgpublic.DirectorySyncJobType,
		DedupeKey:   orgpublic.DirectorySyncDedupeKey(src.ProviderKey()),
		Payload:     orgpublic.DirectorySyncJobPayload{ProviderKey: src.ProviderKey(), Trigger: orgpublic.SyncTriggerScheduled},
		Interval:    cfg.SyncInterval,
		MaxAttempts: 3,
	})
}

func registerConsumers(d *events.Dispatcher, pool *pgxpool.Pool, email bool) error {
	notifier := notifications.NewService(pool)
	if email {
		notifier = notifier.WithEmail()
	}
	taskConsumers := tasksapp.NewConsumers(
		tasksrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
	if err := d.Register("TaskAssigned", "tasks.notify-assigned", taskConsumers.OnTaskAssigned); err != nil {
		return err
	}
	return d.Register("TaskCompleted", "tasks.notify-completed", taskConsumers.OnTaskCompleted)
}

// orgContacts adapts the Organization work directory to the email sender.
type orgContacts struct{ dir *orgpublic.WorkDirectory }

func (o orgContacts) EmailContact(ctx context.Context, userID string) (string, string, bool, error) {
	contacts, err := o.dir.Contacts(ctx, []string{userID})
	if err != nil {
		return "", "", false, err
	}
	c, ok := contacts[userID]
	return c.Email, c.DisplayName, ok && c.Active && c.Email != "", nil
}

func registerEmail(runner *jobs.Runner, pool *pgxpool.Pool, cfg config.SMTPConfig) error {
	mailerCfg := smtp.Config{
		Host: cfg.Host, Port: cfg.Port, Security: smtp.Security(cfg.Security), Username: cfg.Username,
		From: cfg.From, Timeout: cfg.Timeout,
	}
	if cfg.PasswordFile != "" {
		password, err := readSecretFile(cfg.PasswordFile)
		if err != nil {
			return fmt.Errorf("read SMTP password file: %w", err)
		}
		mailerCfg.Password = password
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return fmt.Errorf("read SMTP CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("SMTP_CA_FILE contains no PEM certificate")
		}
		mailerCfg.RootCAs = pool
	}
	mailer, err := smtp.New(mailerCfg)
	if err != nil {
		return err
	}
	sender := notifications.NewEmailSender(pool, mailer,
		orgContacts{orgpublic.NewWorkDirectory(orgrepository.New(pool))}, cfg.BaseURL, cfg.DefaultLocale)
	return runner.Register(notifications.EmailJobType, notifications.EmailJobTimeout, sender.Handle)
}

// readSecretFile reads a one-line secret, trimming only the trailing newline.
func readSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", errors.New("file is empty")
	}
	return secret, nil
}

// registerRecurrence schedules the generation of tasks from Recurring Task
// Definitions. The schedule's dedupe key keeps several workers from
// enqueueing it twice; the generation itself is idempotent per run.
func registerRecurrence(runner *jobs.Runner, pool *pgxpool.Pool) error {
	svc := tasksapp.NewRecurrenceService(tasksrepository.NewDefinitions(pool),
		orgpublic.NewWorkDirectory(orgrepository.New(pool)), nil)
	if err := runner.Register(tasksapp.GenerateJobType, tasksapp.GenerateJobTimeout, svc.HandleGenerate); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{
		JobType: tasksapp.GenerateJobType, DedupeKey: tasksapp.GenerateJobType,
		Interval: tasksapp.GenerateInterval, MaxAttempts: 3,
	})
}
