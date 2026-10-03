package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/autotask"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	requestsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
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
	categories, err := notifications.NewRegistry(allCategories()...)
	if err != nil {
		logger.Error("register notification categories", "error", err)
		os.Exit(1)
	}
	if smtpCfg.Enabled() {
		if err := registerEmail(runner, pool, smtpCfg, categories); err != nil {
			logger.Error("configure email notifications", "error", err)
			os.Exit(1)
		}
		logger.Info("email notifications enabled", "host", smtpCfg.Host, "port", smtpCfg.Port, "security", smtpCfg.Security)
	}
	if err := registerRecurrence(runner, pool); err != nil {
		logger.Error("configure recurring tasks", "error", err)
		os.Exit(1)
	}
	if cfg.AutotaskSync {
		if err := registerExternalSync(runner, dispatcher, pool); err != nil {
			logger.Error("configure Autotask synchronization", "error", err)
			os.Exit(1)
		}
		logger.Warn("Autotask synchronization is on, but the REST client is not implemented: pushes fail with a visible \"not configured\" state")
	}
	if err := registerConsumers(dispatcher, pool, categories, smtpCfg.Enabled()); err != nil {
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

func registerConsumers(d *events.Dispatcher, pool *pgxpool.Pool, categories *notifications.Registry, email bool) error {
	orgReader := orgrepository.New(pool)
	return registerConsumersWith(d, pool, categories, email, roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(orgReader)))
}

// registerConsumersWith registers the outbox consumers; perms decides which
// Users hold task permissions and may therefore be notified about tasks.
func registerConsumersWith(d *events.Dispatcher, pool *pgxpool.Pool, categories *notifications.Registry, email bool, perms tasksapp.PermissionResolver) error {
	notifier := notifications.NewService(pool, categories)
	if email {
		notifier = notifier.WithEmail()
	}
	taskConsumers := tasksapp.NewConsumers(
		tasksrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier, perms)
	if err := d.Register("TaskAssigned", "tasks.notify-assigned", taskConsumers.OnTaskAssigned); err != nil {
		return err
	}
	if err := d.Register("TaskCompleted", "tasks.notify-completed", taskConsumers.OnTaskCompleted); err != nil {
		return err
	}
	approvalConsumers := approvalsapp.NewConsumers(approvalsrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
	if err := d.Register("ApprovalRequested", "approvals.notify-requested", approvalConsumers.OnApprovalRequested); err != nil {
		return err
	}
	sdStore := servicedeskrepository.New(pool)
	sdConsumers := servicedeskapp.NewConsumers(sdStore, orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
	majorConsumers := servicedeskapp.NewMajorConsumers(sdStore, orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
	for _, r := range []struct {
		event, name string
		fn          events.Consumer
	}{
		{"MajorIncidentUpdated", "servicedesk.notify-major", majorConsumers.OnMajorIncidentUpdated},
		{"TicketAssigned", "servicedesk.notify-assigned", sdConsumers.OnTicketAssigned},
		{"TicketResolved", "servicedesk.notify-resolved", sdConsumers.OnTicketResolved},
		{"TicketCommentAdded", "servicedesk.notify-comment", sdConsumers.OnCommentAdded},
	} {
		if err := d.Register(r.event, r.name, r.fn); err != nil {
			return err
		}
	}
	runbooks := wiring.Runbooks(pool)
	for _, event := range []string{"TaskCompleted", "TaskCancelled"} {
		if err := d.Register(event, "knowledge.runbook-task-finished", runbooks.OnTaskEvent); err != nil {
			return err
		}
	}
	procurementSvc := wiring.Procurement(pool)
	assetConsumers := assetsapp.NewConsumers(assetsrepository.New(pool), orgpublic.NewWorkDirectory(orgrepository.New(pool)), notifier)
	if err := d.Register("AssetAssigned", "assets.notify-assigned", assetConsumers.OnAssetAssigned); err != nil {
		return err
	}
	if err := d.Register("ApprovalDecided", "procurement.order-approval", procurementSvc.OnApprovalDecided); err != nil {
		return err
	}
	return registerRequestConsumers(d, wiring.Requests(pool), notifier)
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

func registerEmail(runner *jobs.Runner, pool *pgxpool.Pool, cfg config.SMTPConfig, categories *notifications.Registry) error {
	mailerCfg := smtp.Config{
		Host: cfg.Host, Port: cfg.Port, Security: smtp.Security(cfg.Security), Username: cfg.Username,
		From: cfg.From, Timeout: cfg.Timeout,
	}
	if cfg.PasswordFile != "" {
		password, err := config.ReadSecretFile(cfg.PasswordFile)
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
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("SMTP_CA_FILE contains no PEM certificate")
		}
		mailerCfg.RootCAs = pool
	}
	mailer, err := smtp.New(mailerCfg)
	if err != nil {
		return err
	}
	sender := notifications.NewEmailSender(pool, smtpMailer{mailer},
		orgContacts{orgpublic.NewWorkDirectory(orgrepository.New(pool))}, categories, cfg.BaseURL, cfg.DefaultLocale)
	return runner.Register(notifications.EmailJobType, notifications.EmailJobTimeout, sender.Handle)
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

// smtpMailer adapts the SMTP integration to the notification service's
// Mailer port and maps its permanent errors.
type smtpMailer struct{ m smtp.Mailer }

func (a smtpMailer) Send(ctx context.Context, msg notifications.EmailMessage) error {
	err := a.m.Send(ctx, smtp.Message{To: msg.To, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML})
	if err != nil && smtp.IsPermanent(err) {
		return &notifications.PermanentEmailError{Err: err}
	}
	return err
}

// allCategories lists the notification categories of every module that creates notifications.
func allCategories() []notifications.Category {
	out := tasksapp.NotificationCategories()
	out = append(out, approvalsapp.NotificationCategories()...)
	out = append(out, requestsapp.NotificationCategories()...)
	out = append(out, assetsapp.NotificationCategories()...)
	return append(out, servicedeskapp.NotificationCategories()...)
}

// registerRequestConsumers registers the workflow and notification consumers of the Requests module.
func registerRequestConsumers(d *events.Dispatcher, svc *requestsapp.Service, notifier *notifications.Service) error {
	c := requestsapp.NewConsumers(svc, notifier)
	for _, r := range []struct {
		event, name string
		fn          events.Consumer
	}{
		{"ApprovalDecided", "requests.advance-approval", c.OnApprovalDecided},
		{"TaskCompleted", "requests.task-finished", c.OnTaskFinished},
		{"TaskCancelled", "requests.task-finished", c.OnTaskFinished},
		{"ServiceRequestApproved", "requests.notify-approved", c.OnRequestApproved},
		{"ServiceRequestRejected", "requests.notify-rejected", c.OnRequestRejected},
		{"ServiceRequestCompleted", "requests.notify-completed", c.OnRequestCompleted},
	} {
		if err := d.Register(r.event, r.name, r.fn); err != nil {
			return err
		}
	}
	return nil
}

// registerExternalSync wires the ticket synchronization with Autotask: the outbox consumers that
// request a push and the job that performs it. The gateway is the placeholder until a REST client exists.
func registerExternalSync(runner *jobs.Runner, d *events.Dispatcher, pool *pgxpool.Pool) error {
	sync := wiring.ExternalSync(pool, autotask.NotConfigured{}, true)
	for _, event := range []string{"TicketCreated", "TicketAssigned", "TicketResolved"} {
		if err := d.Register(event, "servicedesk.external-sync", sync.OnTicketChange); err != nil {
			return err
		}
	}
	return runner.Register(servicedeskapp.PushJobType, servicedeskapp.PushJobTimeout, sync.HandlePush)
}
