package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// directorySyncRunner is the part of orgpublic.DirectorySync the handler uses.
type directorySyncRunner interface {
	Run(ctx context.Context, src orgpublic.DirectorySource, trigger orgpublic.SyncTrigger) (orgpublic.SyncRunResult, error)
}

// directorySyncHandler runs one directory sync job against the configured
// source. Jobs for another provider key or with an unknown trigger are
// rejected permanently; retry semantics otherwise come from Run's errors.
func directorySyncHandler(sync directorySyncRunner, src orgpublic.DirectorySource, logger *slog.Logger) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p orgpublic.DirectorySyncJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return jobs.Permanent(fmt.Errorf("directory sync job: invalid payload: %w", err))
		}
		if p.ProviderKey != src.ProviderKey() {
			return jobs.Permanent(fmt.Errorf("directory sync job: provider %q is not configured", p.ProviderKey))
		}
		if p.Trigger != orgpublic.SyncTriggerScheduled && p.Trigger != orgpublic.SyncTriggerManual {
			return jobs.Permanent(fmt.Errorf("directory sync job: invalid trigger %q", p.Trigger))
		}
		res, err := sync.Run(ctx, src, p.Trigger)
		logger.InfoContext(ctx, "directory sync finished",
			"job_id", job.ID, "provider_key", p.ProviderKey, "trigger", p.Trigger,
			"run_id", res.RunID, "outcome", res.Outcome, "counts", res.Counts,
			"conflict_count", res.ConflictCount, "error", err != nil)
		return err
	}
}
