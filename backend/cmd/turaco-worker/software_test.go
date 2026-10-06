package main

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

func TestSoftwarePackageSyncJobIsRegistered(t *testing.T) {
	pool := dbtest.Pool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, enabled := range []bool{false, true} {
		runner := jobs.NewRunner(pool, jobs.RunnerOptions{}, logger)
		if err := registerSoftwarePackageSync(runner, pool, enabled); err != nil {
			t.Fatalf("register (enabled=%v): %v", enabled, err)
		}
		// A second registration of the same job type is refused, which proves the first one happened.
		if err := runner.Register(endpointsapp.SoftwarePackageSyncJobType, endpointsapp.SoftwarePackageSyncJobTimeout, func(context.Context, jobs.Job) error { return nil }); err == nil {
			t.Fatalf("job type not registered (enabled=%v)", enabled)
		}
	}
}

func TestSoftwareApprovalCategoryIsRegistered(t *testing.T) {
	cats := allCategories()
	if !slices.ContainsFunc(cats, func(c notifications.Category) bool { return c.Name == endpointsapp.SoftwareApprovalCategory }) {
		t.Fatal("software.approval_requested is not among the worker's categories")
	}
	if _, err := notifications.NewRegistry(cats...); err != nil {
		t.Fatal(err)
	}
}
