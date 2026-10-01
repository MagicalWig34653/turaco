package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

var _ application.DirectorySyncRequester = (*Repository)(nil)

// RequestDirectorySync enqueues a deduplicated manual sync job and writes the
// audit event in the same transaction. correlationID (the HTTP request ID)
// correlates the audit event with the request; jobID is recorded in the
// metadata and used when no request ID is available.
func (r *Repository) RequestDirectorySync(ctx context.Context, actorUserID, providerKey, correlationID string) (string, bool, error) {
	if _, ok := parseID(actorUserID); !ok {
		return "", false, errors.New("request directory sync: actor must be a user id")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("request directory sync: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobID string
	var created bool
	// A job that finishes between the conflict and the lookup yields an empty
	// id; one retry resolves that race.
	for attempt := 0; attempt < 2 && jobID == ""; attempt++ {
		jobID, created, err = jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{
			Type:        public.DirectorySyncJobType,
			Payload:     public.DirectorySyncJobPayload{ProviderKey: providerKey, Trigger: public.SyncTriggerManual},
			DedupeKey:   public.DirectorySyncDedupeKey(providerKey),
			MaxAttempts: 3,
		})
		if err != nil {
			return "", false, err
		}
	}
	if jobID == "" {
		return "", false, errors.New("request directory sync: no active job after retry")
	}
	if correlationID == "" {
		correlationID = jobID
	}
	if err := audit.Record(ctx, tx, audit.Change{
		Action: "organization.directory_sync.requested", TargetType: "directory_provider", TargetID: providerKey,
		Actor: audit.UserActor(actorUserID), CorrelationID: correlationID,
		Metadata: map[string]any{"jobId": jobID, "created": created},
	}); err != nil {
		return "", false, fmt.Errorf("request directory sync: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("request directory sync: commit: %w", err)
	}
	return jobID, created, nil
}
