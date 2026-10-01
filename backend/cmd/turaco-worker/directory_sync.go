package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// directorySyncRunner is the part of orgpublic.DirectorySync the handler uses.
type directorySyncRunner interface {
	Run(ctx context.Context, src orgpublic.DirectorySource, trigger orgpublic.SyncTrigger, jobID string) (orgpublic.SyncRunResult, error)
}

// directorySyncHandler runs one directory sync job against the configured
// source. Jobs for another provider key are rejected permanently. Run returns
// plain errors; this handler decides retry semantics: an invalid request or
// snapshot and a changed provider key are permanent (retrying cannot help),
// everything else is retried by the runner. A withheld sweep is a successful
// run (nil error): the next scheduled run re-evaluates.
func directorySyncHandler(sync directorySyncRunner, src orgpublic.DirectorySource, logger *slog.Logger) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		var p orgpublic.DirectorySyncJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return jobs.Permanent(fmt.Errorf("directory sync job: invalid payload: %w", err))
		}
		if p.ProviderKey != src.ProviderKey() {
			return jobs.Permanent(fmt.Errorf("directory sync job: provider %q is not configured", p.ProviderKey))
		}
		res, err := sync.Run(ctx, src, p.Trigger, job.ID)
		logger.InfoContext(ctx, "directory sync finished",
			"job_id", job.ID, "provider_key", p.ProviderKey, "trigger", p.Trigger,
			"run_id", res.RunID, "outcome", res.Outcome, "counts", res.Counts,
			"conflict_count", res.ConflictCount, "error", err != nil)
		if errors.Is(err, orgpublic.ErrInvalidRequest) || errors.Is(err, orgpublic.ErrInvalidSnapshot) || errors.Is(err, orgpublic.ErrProviderKeyChanged) {
			return jobs.Permanent(err)
		}
		return err
	}
}
