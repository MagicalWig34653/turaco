package public

import (
	"log/slog"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// This file is the integration and wiring contract of directory
// synchronization. The use case itself lives in application; the types are
// re-exported so integrations (backend/internal/integrations/*) and the
// worker depend only on organization/public.

// Snapshot contract implemented by directory sources.
type (
	DirectorySnapshot = application.DirectorySnapshot
	DirectoryUser     = application.SnapshotUser
	DirectoryGroup    = application.SnapshotGroup
	DirectorySource   = application.DirectorySource
	SyncTrigger       = application.SyncTrigger
)

// Use case types needed to wire and call the synchronization.
type (
	DirectorySync       = application.DirectorySync
	DirectorySyncConfig = application.DirectorySyncConfig
	DirectorySyncStore  = application.DirectorySyncStore
	SyncRunResult       = application.SyncRunResult
)

const (
	SyncTriggerScheduled = application.SyncTriggerScheduled
	SyncTriggerManual    = application.SyncTriggerManual

	SyncOutcomeSucceeded     = application.SyncOutcomeSucceeded
	SyncOutcomeFailed        = application.SyncOutcomeFailed
	SyncOutcomeSweepWithheld = application.SyncOutcomeSweepWithheld
)

// Errors returned by DirectorySync.Run. Run returns plain errors; the worker
// maps ErrInvalidRequest, ErrInvalidSnapshot and ErrProviderKeyChanged to
// permanent job failures and retries the rest.
var (
	ErrSyncAlreadyRunning = application.ErrSyncAlreadyRunning
	ErrInvalidSnapshot    = application.ErrInvalidSnapshot
	ErrProviderKeyChanged = application.ErrProviderKeyChanged
	ErrInvalidRequest     = application.ErrInvalidRequest
)

// NewDirectorySync creates the directory synchronization use case on top of
// the Organization repository. now and logger may be nil.
func NewDirectorySync(store DirectorySyncStore, cfg DirectorySyncConfig, now func() time.Time, logger *slog.Logger) *DirectorySync {
	return application.NewDirectorySync(store, cfg, now, logger)
}

// DirectorySyncJobType is the platform job type that runs one directory sync.
// Its payload is DirectorySyncJobPayload.
const DirectorySyncJobType = "organization.directory_sync"

// DirectorySyncJobPayload is the JSON payload of a DirectorySyncJobType job.
type DirectorySyncJobPayload struct {
	ProviderKey string      `json:"providerKey"`
	Trigger     SyncTrigger `json:"trigger"`
}

// DirectorySyncDedupeKey collapses scheduled and manual requests for one provider.
func DirectorySyncDedupeKey(providerKey string) string {
	return DirectorySyncJobType + ":" + providerKey
}
