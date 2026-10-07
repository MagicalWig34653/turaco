package main

import (
	"io"
	"log/slog"
	"testing"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// TestWorkerRegistersEveryJob builds the real runner options and registers every job of main() with every
// optional feature switched on. A Register error (for example a timeout not shorter than the lock timeout) keeps
// the worker from starting, so it must fail here.
func TestWorkerRegistersEveryJob(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	categories, err := notifications.NewRegistry(allCategories()...)
	if err != nil {
		t.Fatal(err)
	}
	lock := runnerLockTimeout(false, 0)
	runner := jobs.NewRunner(nil, jobs.RunnerOptions{LockTimeout: lock}, logger)
	dispatcher := events.NewDispatcher(nil, events.DispatcherOptions{}, logger)
	deps := jobDeps{
		SMTP:       config.SMTPConfig{Host: "localhost", Port: 25, Security: "none", From: "turaco@example.test", Timeout: 1, BaseURL: "https://turaco.example.test", DefaultLocale: "en"},
		Categories: categories, Logger: logger,
		SoftwareProviderSync: true, SoftwareDeployWrite: true, AutotaskSync: true,
	}
	if err := registerJobs(runner, dispatcher, nil, deps); err != nil {
		t.Fatalf("registerJobs: %v", err)
	}
	registered := runner.RegisteredJobs()
	for _, want := range []string{endpointsapp.DeploymentTickJobType, endpointsapp.DeploymentCorrelationJobType, securityapp.AdvisorySyncJobType, notifications.EmailJobType, servicedeskapp.PushJobType} {
		if _, ok := registered[want]; !ok {
			t.Errorf("job %s is not registered", want)
		}
	}
	if len(registered) < 8 {
		t.Fatalf("only %d jobs registered: %v", len(registered), registered)
	}
	for jobType, timeout := range registered {
		if timeout <= 0 || timeout >= lock {
			t.Errorf("job %s timeout %s must be positive and shorter than the lock timeout %s", jobType, timeout, lock)
		}
	}
}
