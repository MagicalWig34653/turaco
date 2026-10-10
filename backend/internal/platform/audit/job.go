package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

const (
	// PurgeJobType is the retention job. It is scheduled only when AUDIT_RETENTION_DAYS is set.
	PurgeJobType = "platform.audit.purge"
	// PurgeJobTimeout bounds one run.
	PurgeJobTimeout = 10 * time.Minute
	// PurgeInterval is how often the retention job runs.
	PurgeInterval = 6 * time.Hour

	purgeBatch      = 5000
	purgeMaxBatches = 40
)

// NewPurgeHandler deletes events older than retentionDays (never less than MinRetentionDays: the database function
// clamps again) in batches of 5000, at most 40 batches per run; a backlog is worked off by the following runs.
func NewPurgeHandler(pool *pgxpool.Pool, retentionDays int) jobs.Handler {
	reader := NewReader(pool)
	return func(ctx context.Context, job jobs.Job) error {
		if retentionDays <= 0 {
			return nil
		}
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		for i := 0; i < purgeMaxBatches; i++ {
			res, err := reader.PurgeBefore(ctx, cutoff, purgeBatch, fmt.Sprintf("audit-purge-%s", job.ID))
			if err != nil {
				return err
			}
			if res.Deleted == 0 {
				return nil
			}
		}
		return nil
	}
}
