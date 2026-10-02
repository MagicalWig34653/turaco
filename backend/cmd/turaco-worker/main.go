package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
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
	if err := registerConsumers(dispatcher, pool); err != nil {
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

func registerConsumers(d *events.Dispatcher, pool *pgxpool.Pool) error {
	taskConsumers := tasksapp.NewConsumers(
		tasksrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifications.NewService(pool))
	if err := d.Register("TaskAssigned", "tasks.notify-assigned", taskConsumers.OnTaskAssigned); err != nil {
		return err
	}
	return d.Register("TaskCompleted", "tasks.notify-completed", taskConsumers.OnTaskCompleted)
}
