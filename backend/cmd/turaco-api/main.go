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

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	orgtransport "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
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

	// Browser sessions authenticate requests. Until F1 slice 5 evaluates real
	// permissions, NoPermissions gives sessions none, so permission-guarded
	// routes such as Organization still answer 403.
	orgReader := orgrepository.New(pool)
	sessions := authentication.NewService(pool, authentication.Config{IdleTimeout: cfg.SessionIdleTimeout, AbsoluteTimeout: cfg.SessionAbsoluteTimeout}, nil)
	sessionAuth := authentication.NewSessionAuthenticator(sessions, orgpublic.NewUserAccess(orgReader), authentication.NoPermissions{}, cfg.SessionCookieSecure)
	authentication.Register(mux, sessions, sessionAuth, cfg.SessionCookieSecure, logger)
	// Manual directory sync requests need a configured provider; the worker
	// performs the sync itself, the API only enqueues it and never needs the
	// directory credentials.
	orgtransport.Register(mux, orgReader, orgReader, cfg.DirectoryProviderKey, sessionAuth, logger)

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
