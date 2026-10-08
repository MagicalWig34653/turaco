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
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/repository"
	changesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	planningapp "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	remoteaccessapp "github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	requestsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedeskrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	servicesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	aiplatform "github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

var version = "dev"

// runnerLockTimeout is the job lock timeout of the runner. A job must not outlive its lock, or
// another worker would reclaim it: it covers the longest job registered on every worker (the
// advisory feed sync) and, when LDAP is enabled, the directory sync.
func runnerLockTimeout(ldapEnabled bool, ldapSyncTimeout time.Duration) time.Duration {
	lock := securityapp.AdvisorySyncJobTimeout + 5*time.Minute
	if ldapEnabled {
		if minLock := ldapSyncTimeout + directorySyncMargin + 5*time.Minute; minLock > lock {
			lock = minLock
		}
	}
	return lock
}

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
	lockTimeout := runnerLockTimeout(ldapCfg.Enabled(), ldapCfg.SyncTimeout)
	// Module switches (ADR-0032): jobs of a switched-off module are skipped; retention jobs still run.
	moduleSvc := wiring.Modules(pool, wiring.ModuleGates{PresenceEnabled: cfg.PresenceEnabled, AIEnabled: cfg.AIEnabled, RemoteAccessProviders: cfg.RemoteAccessProviders})
	opts := jobs.RunnerOptions{LockTimeout: lockTimeout, Gate: moduleSvc.JobGate}
	runner := jobs.NewRunner(pool, opts, logger)

	// Outbox consumers of a switched-off module hand their events to durable deferred-event jobs (ADR-0032).
	consumerGate := moduleSvc.ConsumerGate()
	dispatcher := events.NewDispatcher(pool, events.DispatcherOptions{WrapConsumer: consumerGate.Wrap}, logger)
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
	deps := jobDeps{
		LDAP: ldapCfg, SMTP: smtpCfg, Categories: categories, Logger: logger,
		SoftwareProviderSync: cfg.SoftwareProviderSync, SoftwareDeployWrite: cfg.SoftwareDeployWrite, AutotaskSync: cfg.AutotaskSync,
		RemoteAccessProviders: cfg.RemoteAccessProviders, RemoteAccessApprovalOwnership: cfg.RemoteAccessApprovalOwnership,
		Presence: presenceapp.Config{Enabled: cfg.PresenceEnabled, RetentionDays: cfg.PresenceRetentionDays, StaleAfter: cfg.PresenceSourceStaleAfter},
		Modules:  moduleSvc, ConsumerGate: consumerGate,
	}
	if err := registerJobsFn(runner, dispatcher, pool, deps); err != nil {
		logger.Error("register worker jobs", "error", err)
		os.Exit(1)
	}
	// The backfill is idempotent, so it is enqueued at every worker start; a dedupe key keeps several workers from queueing it twice.
	if err := servicesapp.EnqueueBackfill(ctx, pool); err != nil {
		logger.Error("enqueue service link backfill", "error", err)
		os.Exit(1)
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

// jobDeps is everything registerJobs needs besides the runner, dispatcher and pool.
type jobDeps struct {
	LDAP                 config.LDAPConfig
	SMTP                 config.SMTPConfig
	Categories           *notifications.Registry
	Logger               *slog.Logger
	SoftwareProviderSync bool
	SoftwareDeployWrite  bool
	AutotaskSync         bool
	// RemoteAccessProviders and RemoteAccessApprovalOwnership are REMOTE_ACCESS_PROVIDERS and
	// REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP; the jobs are registered either way and do nothing without providers.
	RemoteAccessProviders         []string
	RemoteAccessApprovalOwnership []string
	// Presence is the Workforce Presence configuration (PRESENCE_*); the purge job is registered either way so
	// retention keeps working after the module was switched off.
	Presence presenceapp.Config
	// Modules (optional) lets the deployment engine run only its safety sweep while Endpoints is switched off, and
	// registers the deferred-event jobs.
	Modules      *modules.Service
	ConsumerGate *modules.ConsumerGate
}

// registerJobsFn is what main calls; the startup smoke test calls the same variable, so main cannot stop registering
// the jobs without the test noticing.
var registerJobsFn = registerJobs

// registerJobs registers every job type of the worker and its schedules. It is separate from main so a
// startup smoke test builds the same registrations: one invalid timeout would otherwise keep the worker
// from starting at all.
func registerJobs(runner *jobs.Runner, dispatcher *events.Dispatcher, pool *pgxpool.Pool, d jobDeps) error {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"directory sync", func() error {
			if !d.LDAP.Enabled() {
				return nil
			}
			if err := registerDirectorySync(runner, pool, d.LDAP, d.Logger); err != nil {
				return err
			}
			d.Logger.Info("directory sync enabled", "provider_key", d.LDAP.ProviderKey, "interval", d.LDAP.SyncInterval.String())
			return nil
		}},
		{"email notifications", func() error {
			if !d.SMTP.Enabled() {
				return nil
			}
			if err := registerEmail(runner, pool, d.SMTP, d.Categories); err != nil {
				return err
			}
			d.Logger.Info("email notifications enabled", "host", d.SMTP.Host, "port", d.SMTP.Port, "security", d.SMTP.Security)
			return nil
		}},
		{"recurring tasks", func() error { return registerRecurrence(runner, pool) }},
		{"service link backfill", func() error { return registerServicesBackfill(runner, pool) }},
		{"change reminders", func() error {
			return registerChangeReminders(runner, pool, d.Categories, d.SMTP.Enabled(), d.Logger)
		}},
		{"security matching", func() error { return registerSecurityMatching(runner, pool) }},
		{"security risk reminders", func() error {
			return registerSecurityRiskReminders(runner, pool, d.Categories, d.SMTP.Enabled())
		}},
		{"advisory feed synchronization", func() error { return registerAdvisorySync(runner, pool) }},
		{"software package synchronization", func() error {
			return registerSoftwarePackageSync(runner, pool, d.SoftwareProviderSync)
		}},
		{"deployment engine", func() error { return registerDeploymentEngine(runner, pool, d.SoftwareDeployWrite, d.Modules) }},
		{"deferred module events", func() error {
			if d.ConsumerGate == nil {
				return nil
			}
			return d.ConsumerGate.RegisterJobs(runner)
		}},
		{"deployment correlation", func() error {
			return registerDeploymentCorrelation(runner, pool, d.Categories, d.SMTP.Enabled())
		}},
		{"presence", func() error { return registerPresence(runner, pool, d.Presence) }},
		{"ai cleanup", func() error { return registerAI(runner, pool) }},
		{"remote access", func() error {
			return registerRemoteAccess(runner, pool, d.RemoteAccessProviders, d.RemoteAccessApprovalOwnership)
		}},
		{"Autotask synchronization", func() error {
			if !d.AutotaskSync {
				return nil
			}
			if err := registerExternalSync(runner, dispatcher, pool); err != nil {
				return err
			}
			d.Logger.Warn("Autotask synchronization is on, but the REST client is not implemented: pushes fail with a visible \"not configured\" state")
			return nil
		}},
	}
	for _, step := range steps {
		if err := step.fn(); err != nil {
			return fmt.Errorf("configure %s: %w", step.name, err)
		}
	}
	return nil
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
	changeNotes := wiring.ChangeNotifications(pool, notifier, perms)
	for _, r := range []struct{ event, name string }{
		{"ChangeApproved", "changes.notify-state"}, {"ChangeRejected", "changes.notify-state"}, {"ChangeFailed", "changes.notify-state"},
	} {
		if err := d.Register(r.event, r.name, changeNotes.OnChangeState); err != nil {
			return err
		}
	}
	for _, event := range []string{"ChangeScheduled", changesapp.FanOutEventType} {
		if err := d.Register(event, "changes.notify-scheduled", changeNotes.OnChangeScheduled); err != nil {
			return err
		}
	}
	if err := d.Register("ApprovalDecided", "changes.approval", wiring.Changes(pool).OnApprovalDecided); err != nil {
		return err
	}
	if err := d.Register("ApprovalDecided", "planning.approval", wiring.Planning(pool).OnApprovalDecided); err != nil {
		return err
	}
	// Remote Access Session Approvals (F10): the decision authorizes or rejects a pending session. The consumers
	// need no provider connectors, so they run with an empty registry.
	if err := d.Register("ApprovalDecided", "remoteaccess.approval", wiring.RemoteAccess(pool, remoteaccess.NewRegistryOf(), nil).OnApprovalDecided); err != nil {
		return err
	}
	if err := d.Register(remoteaccessapp.EventLaunched, "remoteaccess.notify-launched", wiring.RemoteAccessNotifications(pool, notifier).OnSessionLaunched); err != nil {
		return err
	}
	// Deployment plan Approvals (F9 G2): the decision moves a pending plan to approved or back to draft.
	if err := d.Register("ApprovalDecided", "endpoints.deployment-approval",
		wiring.Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false).OnApprovalDecided); err != nil {
		return err
	}
	if err := d.Register(planningapp.EventStatusChanged, "planning.notify-state", wiring.PlanningNotifications(pool, notifier).OnStatusChanged); err != nil {
		return err
	}
	securityNotes := wiring.SecurityNotifications(pool, notifier)
	for _, event := range []string{securityapp.EventAdvisoryPublished, securityapp.EventAdvisoryPublishedFanOut} {
		if err := d.Register(event, "security.notify-advisory", securityNotes.OnAdvisoryPublished); err != nil {
			return err
		}
	}
	softwareNotes := wiring.SoftwareNotifications(pool, notifier)
	for _, event := range []string{endpointsapp.EventSoftwareVersionApprovalRequested, endpointsapp.EventSoftwareVersionApprovalRequestedFanOut} {
		if err := d.Register(event, "endpoints.notify-software-approval", softwareNotes.OnApprovalRequested); err != nil {
			return err
		}
	}
	if err := d.Register("VirtualMachineChanged", "services.sync-vm-hypervisor-link", wiring.ServiceVMLinks(pool).OnVirtualMachineChanged); err != nil {
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

// registerServicesBackfill registers the job that derives "VM RUNS_ON hypervisor"
// links for Virtual Machines that predate the link consumer, and enqueues it. It
// is idempotent, so it runs at every worker start; a dedupe key keeps several
// workers from queueing it twice.
func registerServicesBackfill(runner *jobs.Runner, pool *pgxpool.Pool) error {
	return runner.Register(servicesapp.BackfillJobType, servicesapp.BackfillJobTimeout, wiring.ServiceVMLinks(pool).HandleBackfill)
}

// registerChangeReminders registers the "starts soon" reminder job of scheduled Changes and
// schedules it. The job is idempotent per Change and maintenance window; the schedule's
// dedupe key keeps several workers from enqueueing it twice.
func registerChangeReminders(runner *jobs.Runner, pool *pgxpool.Pool, categories *notifications.Registry, email bool, logger *slog.Logger) error {
	notifier := notifications.NewService(pool, categories)
	if email {
		notifier = notifier.WithEmail()
	}
	perms := roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(orgrepository.New(pool)))
	notes := wiring.ChangeNotifications(pool, notifier, perms).WithLogger(logger)
	if err := runner.Register(changesapp.ReminderJobType, changesapp.ReminderJobTimeout, notes.HandleReminders); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{
		JobType: changesapp.ReminderJobType, DedupeKey: changesapp.ReminderJobType, Interval: changesapp.ReminderInterval, MaxAttempts: 3,
	})
}

// registerSoftwarePackageSync registers the Software Package synchronization job; it is scheduled only when
// SOFTWARE_PROVIDER_SYNC is on. The provider is a placeholder until a real client exists, so a run with the
// switch on reports "not configured".
func registerSoftwarePackageSync(runner *jobs.Runner, pool *pgxpool.Pool, enabled bool) error {
	svc := wiring.Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, enabled)
	if err := runner.Register(endpointsapp.SoftwarePackageSyncJobType, endpointsapp.SoftwarePackageSyncJobTimeout, svc.HandleSoftwarePackageSync); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return runner.AddSchedule(jobs.Schedule{JobType: endpointsapp.SoftwarePackageSyncJobType, DedupeKey: endpointsapp.SoftwarePackageSyncJobType,
		Interval: endpointsapp.SoftwarePackageSyncInterval, MaxAttempts: 3})
}

// registerDeploymentEngine registers the Deployment execution job and schedules it every minute. With
// SOFTWARE_DEPLOY_WRITE off the job only runs the kill-switch sweep. The writer is a placeholder until the Graph write client exists, so a ring that is
// started with the capability on halts with assignment_failed.
func registerDeploymentEngine(runner *jobs.Runner, pool *pgxpool.Pool, enabled bool, mods *modules.Service) error {
	svc := wiring.Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false).WithDeployWrite(enabled, intune.NotConfiguredWriter{})
	tick := svc.HandleDeploymentTick
	if mods != nil {
		// The tick is a safety path and always runs. While Endpoints is switched off it does only the kill-switch sweep
		// (pause, halt, queue clearing) and starts no new rollout work (ADR-0032).
		tick = func(ctx context.Context, job jobs.Job) error {
			on, err := mods.Enabled(ctx, "endpoints")
			if err != nil {
				return err
			}
			if !on {
				return svc.GateSweep(ctx, "job:"+job.ID)
			}
			return svc.HandleDeploymentTick(ctx, job)
		}
	}
	if err := runner.Register(endpointsapp.DeploymentTickJobType, endpointsapp.DeploymentTickJobTimeout, tick); err != nil {
		return err
	}
	// Scheduled even with the capability off: the tick then only runs the kill-switch sweep (pause, halt, queue clearing).
	return runner.AddSchedule(jobs.Schedule{JobType: endpointsapp.DeploymentTickJobType, DedupeKey: endpointsapp.DeploymentTickJobType,
		Interval: endpointsapp.DeploymentTickInterval, MaxAttempts: 1})
}

// registerDeploymentCorrelation registers the failure correlation and follow-up job of the Deployments and schedules it every
// ten minutes. It needs no capability: it only reads Deployment data and creates findings, Tasks and notifications.
func registerDeploymentCorrelation(runner *jobs.Runner, pool *pgxpool.Pool, categories *notifications.Registry, email bool) error {
	notifier := notifications.NewService(pool, categories)
	if email {
		notifier = notifier.WithEmail()
	}
	svc := wiring.DeploymentCorrelation(pool, notifier)
	if err := runner.Register(endpointsapp.DeploymentCorrelationJobType, endpointsapp.DeploymentCorrelationJobTimeout, svc.HandleDeploymentCorrelation); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{JobType: endpointsapp.DeploymentCorrelationJobType, DedupeKey: endpointsapp.DeploymentCorrelationJobType,
		Interval: endpointsapp.DeploymentCorrelationInterval, MaxAttempts: 1})
}

// registerRemoteAccess registers the Remote Access session expiry and provider observation jobs. Both are
// scheduled even without providers (expiry also ends sessions of a provider that was switched off since).
func registerRemoteAccess(runner *jobs.Runner, pool *pgxpool.Pool, providerKeys, approvalOwnership []string) error {
	providers, err := remoteaccess.NewRegistry(providerKeys)
	if err != nil {
		return err
	}
	svc := wiring.RemoteAccess(pool, providers, approvalOwnership)
	for _, j := range []struct {
		typ      string
		timeout  time.Duration
		interval time.Duration
		handler  jobs.Handler
	}{
		{remoteaccessapp.ExpireJobType, remoteaccessapp.ExpireJobTimeout, remoteaccessapp.ExpireInterval, svc.HandleExpire},
		{remoteaccessapp.ObserveJobType, remoteaccessapp.ObserveJobTimeout, remoteaccessapp.ObserveInterval, svc.HandleObserve},
	} {
		if err := runner.Register(j.typ, j.timeout, j.handler); err != nil {
			return err
		}
		if err := runner.AddSchedule(jobs.Schedule{JobType: j.typ, DedupeKey: j.typ, Interval: j.interval, MaxAttempts: 2}); err != nil {
			return err
		}
	}
	return nil
}

// registerPresence registers the daily Workforce Presence retention job (idempotent; counts-only audit summary).
func registerPresence(runner *jobs.Runner, pool *pgxpool.Pool, cfg presenceapp.Config) error {
	svc := wiring.Presence(pool, cfg)
	if err := runner.Register(presenceapp.PurgeJobType, presenceapp.PurgeJobTimeout, svc.HandlePurge); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{JobType: presenceapp.PurgeJobType, DedupeKey: presenceapp.PurgeJobType, Interval: presenceapp.PurgeInterval, MaxAttempts: 2})
}

// registerAdvisorySync registers the advisory feed synchronization job (NVD, CISA KEV); it is scheduled only
// when ADVISORY_SYNC is on. The dedupe key keeps several workers from queueing the same run.
func registerAdvisorySync(runner *jobs.Runner, pool *pgxpool.Pool) error {
	cfg, err := config.LoadAdvisoryFeeds()
	if err != nil {
		return err
	}
	service, err := wiring.SecurityWithFeeds(pool, cfg)
	if err != nil {
		return err
	}
	if err := runner.Register(securityapp.AdvisorySyncJobType, securityapp.AdvisorySyncJobTimeout, service.HandleAdvisorySync); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	return runner.AddSchedule(jobs.Schedule{JobType: securityapp.AdvisorySyncJobType, DedupeKey: securityapp.AdvisorySyncJobType,
		Interval: cfg.Interval, MaxAttempts: 2})
}

func registerSecurityMatching(runner *jobs.Runner, pool *pgxpool.Pool) error {
	service := wiring.Security(pool)
	if err := runner.Register(securityapp.MatchJobType, securityapp.MatchJobTimeout, service.HandleMatch); err != nil {
		return err
	}
	if err := runner.Register(securityapp.MatchAllJobType, securityapp.MatchAllJobTimeout, service.HandleMatchAll); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{JobType: securityapp.MatchAllJobType, DedupeKey: securityapp.MatchAllJobType,
		Interval: securityapp.MatchAllInterval, MaxAttempts: 3})
}

func registerSecurityRiskReminders(runner *jobs.Runner, pool *pgxpool.Pool, categories *notifications.Registry, email bool) error {
	notifier := notifications.NewService(pool, categories)
	if email {
		notifier = notifier.WithEmail()
	}
	notes := wiring.SecurityNotifications(pool, notifier)
	if err := runner.Register(securityapp.RiskReminderJobType, 2*time.Minute, notes.HandleRiskReminders); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{JobType: securityapp.RiskReminderJobType, DedupeKey: securityapp.RiskReminderJobType, Interval: securityapp.RiskReminderInterval, MaxAttempts: 3})
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
	out = append(out, changesapp.NotificationCategories()...)
	out = append(out, planningapp.NotificationCategories()...)
	out = append(out, remoteaccessapp.NotificationCategories()...)
	out = append(out, securityapp.NotificationCategories()...)
	out = append(out, endpointsapp.NotificationCategories()...)
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
	for _, event := range []string{"TicketCreated", "TicketAssigned", "TicketResolved", "TicketStatusChanged"} {
		if err := d.Register(event, "servicedesk.external-sync", sync.OnTicketChange); err != nil {
			return err
		}
	}
	return runner.Register(servicedeskapp.PushJobType, servicedeskapp.PushJobTimeout, sync.HandlePush)
}

// registerAI registers the Turaco AI cleanup jobs: expired conversation sessions every five minutes and retained
// conversations and old usage counters daily. They run whether or not AI is enabled, so data is always removed on time.
func registerAI(runner *jobs.Runner, pool *pgxpool.Pool) error {
	svc, err := wiring.AI(pool, wiring.AIConfig{})
	if err != nil {
		return err
	}
	if err := runner.Register(aiplatform.SessionsExpireJobType, aiplatform.SessionsExpireJobTimeout, svc.HandleSessionsExpire); err != nil {
		return err
	}
	if err := runner.Register(aiplatform.RetentionPurgeJobType, aiplatform.RetentionPurgeJobTimeout, svc.HandleRetentionPurge); err != nil {
		return err
	}
	if err := runner.AddSchedule(jobs.Schedule{JobType: aiplatform.SessionsExpireJobType, DedupeKey: aiplatform.SessionsExpireJobType, Interval: aiplatform.SessionsExpireInterval, MaxAttempts: 2}); err != nil {
		return err
	}
	return runner.AddSchedule(jobs.Schedule{JobType: aiplatform.RetentionPurgeJobType, DedupeKey: aiplatform.RetentionPurgeJobType, Interval: aiplatform.RetentionPurgeInterval, MaxAttempts: 2})
}
