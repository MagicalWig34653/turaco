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

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/kerberos"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/ldap"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	orgtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/transport"
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
	authentication.Register(mux, sessions, sessionAuth, cfg.SessionCookieSecure, logger)

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
	notificationstransport.Register(mux, notifications.NewService(pool), sessionAuth, logger)
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
