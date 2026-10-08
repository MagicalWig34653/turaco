package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/autotask"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/kerberos"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	approvalsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	approvalsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/repository"
	approvalstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/transport"
	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	assetstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/transport"
	briefingapp "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	briefingrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/repository"
	briefingtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/transport"
	catalogapp "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	catalogrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/repository"
	catalogtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/transport"
	changesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	changestransport "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/transport"
	endpointstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/transport"
	infrastructuretransport "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/transport"
	inventorytransport "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/transport"
	knowledgetransport "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/transport"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	orgtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/transport"
	planningapp "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	planningtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/planning/transport"
	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	presencetransport "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/transport"
	procurementtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/transport"
	productsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/products/application"
	productspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/products/public"
	productsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/products/repository"
	productstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/products/transport"
	remoteaccessapp "github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	remoteaccesstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/transport"
	requestsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/application"
	requeststransport "github.com/MagicalWig34653/turaco/backend/internal/modules/requests/transport"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	securitytransport "github.com/MagicalWig34653/turaco/backend/internal/modules/security/transport"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	servicedesktransport "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/transport"
	servicestransport "github.com/MagicalWig34653/turaco/backend/internal/modules/services/transport"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	taskstransport "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	audittransport "github.com/MagicalWig34653/turaco/backend/internal/platform/audit/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	rolestransport "github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	notificationstransport "github.com/MagicalWig34653/turaco/backend/internal/platform/notifications/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

var version = "dev"

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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, httpx.ErrorEnvelope{Error: httpx.APIError{Code: "platform.database_unavailable", Message: "Database is unavailable."}})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"name": "Turaco", "version": version, "environment": cfg.Environment})
	})

	// Browser sessions authenticate requests; their permissions come from
	// role assignments to the User and its (transitive) Directory Groups.
	orgReader := orgrepository.New(pool)
	subjects := orgpublic.NewAuthorizationSubjects(orgReader)
	sessions := authentication.NewService(pool, authentication.Config{IdleTimeout: cfg.SessionIdleTimeout, AbsoluteTimeout: cfg.SessionAbsoluteTimeout}, nil)
	sessionAuth := authentication.NewSessionAuthenticator(sessions, orgpublic.NewUserAccess(orgReader), roles.NewEvaluator(pool, subjects), cfg.SessionCookieSecure)
	authentication.Register(mux, sessions, sessionAuth, orgpublic.NewUserAccess(orgReader), cfg.SessionCookieSecure, logger)

	// Login. Password login binds as the synced account, so the API needs the
	// directory connection settings but never the sync bind secret.
	loginAccounts := orgpublic.NewLoginAccounts(orgReader, nil)
	loginDeps := authentication.LoginDeps{
		Pool: pool, Sessions: sessions,
		Throttle: authentication.NewThrottle(pool, authentication.ThrottleConfig{}, nil),
		Users:    loginAccounts, Logger: logger,
	}
	loginCfg := authentication.LoginConfig{
		EmergencyEnabled: cfg.AuthEmergencyLoginEnabled,
		SecureCookie:     cfg.SessionCookieSecure,
		TrustedProxies:   cfg.TrustedProxies,
	}
	ldapConn, err := config.LoadLDAPConnection(cfg.Environment)
	if err != nil {
		logger.Error("load directory connection", "error", err)
		os.Exit(1)
	}
	if ldapConn.Enabled() {
		verifier, err := ldap.NewPasswordVerifier(ldapConn, logger)
		if err != nil {
			logger.Error("configure directory password login", "error", err)
			os.Exit(1)
		}
		loginDeps.Directory, loginDeps.Verifier = loginAccounts, verifier
		loginCfg.ProviderKey = ldapConn.ProviderKey
	}
	// Kerberos/SPNEGO login maps tickets to synced directory accounts, so it
	// needs the directory too (config.Load enforces this). The keytab is read
	// once here; a missing or wrong keytab stops the API instead of silently
	// disabling single sign-on.
	if cfg.Kerberos.Enabled() {
		if !ldapConn.Enabled() {
			logger.Error("configure kerberos login", "error", "KERBEROS_KEYTAB_FILE requires LDAP_URL")
			os.Exit(1)
		}
		validator, err := kerberos.NewValidator(cfg.Kerberos, logger)
		if err != nil {
			logger.Error("configure kerberos login", "error", err)
			os.Exit(1)
		}
		loginDeps.Kerberos = validator
	}
	authentication.RegisterLogin(mux, loginDeps, loginCfg)

	// Manual directory sync requests need a configured provider; the worker
	// performs the sync itself, the API only enqueues it.
	orgtransport.Register(mux, orgReader, orgReader, cfg.DirectoryProviderKey, sessionAuth, logger)
	orgtransport.RegisterTeams(mux, orgapp.NewTeams(orgReader), sessionAuth, logger)
	tasksSvc := tasksapp.NewService(tasksrepository.New(pool), orgpublic.NewWorkDirectory(orgReader), nil)
	taskstransport.Register(mux, tasksSvc, sessionAuth, logger)
	approvalstransport.Register(mux, approvalsapp.NewService(approvalsrepository.New(pool), orgpublic.NewWorkDirectory(orgReader), nil), sessionAuth, logger)
	productsRepo := productsrepository.New(pool)
	requeststransport.Register(mux, wiring.Requests(pool), sessionAuth, logger)
	assetstransport.Register(mux, wiring.Assets(pool), sessionAuth, logger)
	endpointstransport.Register(mux, wiring.Endpoints(pool, intune.NotConfigured{}, cfg.IntuneSync, softwaremgmt.NotConfigured{}, cfg.SoftwareProviderSync).WithDeployWrite(cfg.SoftwareDeployWrite, intune.NotConfiguredWriter{}), sessionAuth, logger)
	knowledgetransport.Register(mux, wiring.Knowledge(pool), sessionAuth, logger)
	knowledgetransport.RegisterRunbooks(mux, wiring.Runbooks(pool), sessionAuth, logger)
	servicedesktransport.Register(mux, wiring.ServiceDesk(pool), sessionAuth, logger)
	servicedesktransport.RegisterExternal(mux, wiring.ExternalSync(pool, autotask.NotConfigured{}, cfg.AutotaskSync), sessionAuth, logger)
	servicedesktransport.RegisterProblems(mux, wiring.Problems(pool), sessionAuth, logger)
	servicedesktransport.RegisterMajor(mux, wiring.MajorIncidents(pool), sessionAuth, logger)
	inventorytransport.Register(mux, wiring.Inventory(pool), sessionAuth, logger)
	infrastructuretransport.Register(mux, wiring.Infrastructure(pool), sessionAuth, logger)
	servicestransport.Register(mux, wiring.Services(pool), sessionAuth, logger)
	changestransport.Register(mux, wiring.Changes(pool), sessionAuth, logger)
	planningtransport.Register(mux, wiring.Planning(pool), sessionAuth, logger)
	securitytransport.Register(mux, wiring.Security(pool), sessionAuth, logger)
	providers, err := remoteaccess.NewRegistry(cfg.RemoteAccessProviders)
	if err != nil {
		logger.Error("configure remote access providers", "error", err)
		os.Exit(1)
	}
	remoteaccesstransport.Register(mux, wiring.RemoteAccess(pool, providers, cfg.RemoteAccessApprovalOwnership), sessionAuth, logger)
	presencetransport.Register(mux, wiring.Presence(pool, presenceapp.Config{Enabled: cfg.PresenceEnabled, RetentionDays: cfg.PresenceRetentionDays, StaleAfter: cfg.PresenceSourceStaleAfter}), sessionAuth, logger)
	procurementtransport.Register(mux, wiring.Procurement(pool), sessionAuth, logger)
	productstransport.Register(mux, productsapp.NewService(productsRepo), sessionAuth, logger)
	catalogtransport.Register(mux, catalogapp.NewService(catalogrepository.New(pool), orgpublic.NewWorkDirectory(orgReader),
		catalogpublic.NewProducts(productspublic.NewDirectory(productsRepo))), sessionAuth, logger)
	briefingService := briefingapp.NewService(briefingrepository.New(pool), nil)
	briefingtransport.Register(mux, briefingService, sessionAuth, logger, wiring.BriefingFeed(pool, briefingService))
	taskstransport.RegisterRecurrence(mux, tasksapp.NewRecurrenceService(tasksrepository.NewDefinitions(pool), orgpublic.NewWorkDirectory(orgReader), nil), sessionAuth, logger)
	categories, err := notifications.NewRegistry(allCategories()...)
	if err != nil {
		logger.Error("register notification categories", "error", err)
		os.Exit(1)
	}
	notificationstransport.Register(mux, notifications.NewService(pool, categories), sessionAuth, logger)
	rolestransport.Register(mux, roles.NewService(pool, subjects), sessionAuth, logger)
	audittransport.Register(mux, audit.NewReader(pool), sessionAuth, logger)

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		// Every unsafe request must be same-origin, whatever route it reaches.
		Handler:           httpx.Middleware(logger, authentication.RequireSameOrigin(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown http server", "error", err)
		}
	}()

	logger.Info("turaco-api starting", "addr", cfg.HTTPAddr, "version", version)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("http server failed", "error", err)
		os.Exit(1)
	}
}

// allCategories lists the notification categories of every module that creates notifications.
func allCategories() []notifications.Category {
	out := tasksapp.NotificationCategories()
	out = append(out, approvalsapp.NotificationCategories()...)
	out = append(out, requestsapp.NotificationCategories()...)
	out = append(out, assetsapp.NotificationCategories()...)
	out = append(out, changesapp.NotificationCategories()...)
	out = append(out, planningapp.NotificationCategories()...)
	out = append(out, securityapp.NotificationCategories()...)
	out = append(out, remoteaccessapp.NotificationCategories()...)
	return append(out, servicedeskapp.NotificationCategories()...)
}
