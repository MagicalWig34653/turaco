package modules

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// ErrorCodeDisabled is the API error code of a request to a switched-off module (HTTP 404).
const ErrorCodeDisabled = "platform.module_disabled"

// Gate wraps the API handler. A request below /api/v1 that belongs to a module that is not effectively on is
// answered 404 platform.module_disabled and never reaches the module. Callers who are not signed in still get 401,
// so the switch state is not revealed to anonymous clients. Paths of core modules and unknown paths pass through.
func (s *Service) Gate(auth authorization.Authenticator, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, owned := s.ix.ForPath(r.URL.Path)
		if !owned {
			next.ServeHTTP(w, r)
			return
		}
		on, err := s.Enabled(r.Context(), key)
		if err != nil {
			logger.ErrorContext(r.Context(), "module gate failed", "module", key, "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
			return
		}
		if on {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok, aerr := auth.Authenticate(r); aerr != nil {
			logger.ErrorContext(r.Context(), "module gate authentication failed", "module", key, "error", aerr)
			httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
			return
		} else if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "platform.unauthenticated", "Authentication is required.")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteError(w, http.StatusNotFound, ErrorCodeDisabled, "This module is not enabled.")
	})
}

// JobGate decides what happens to a claimed background job (jobs.RunnerOptions.Gate). Jobs of an enabled module and
// always-run jobs (safety and retention paths) run; a disposable scheduled tick of a disabled module is dropped; durable
// work of a disabled module is deferred and stays pending until the module is enabled. A database error is returned so
// the runner retries later instead of running or dropping the job blindly.
func (s *Service) JobGate(ctx context.Context, jobType string) (jobs.GateDecision, error) {
	key, policy, owned := s.ix.ForJob(jobType)
	if !owned {
		return jobs.GateRun, nil
	}
	on, err := s.Enabled(ctx, key)
	if err != nil || on {
		return jobs.GateRun, err
	}
	if policy == JobDrop {
		return jobs.GateDrop, nil
	}
	return jobs.GateDefer, nil
}
