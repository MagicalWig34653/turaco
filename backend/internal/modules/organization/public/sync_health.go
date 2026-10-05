package public

import (
	"context"
	"time"
)

type DirectorySyncStatus struct {
	Provider         string
	LastSuccessAt    *time.Time
	LastFailureAt    *time.Time
	LastErrorCode    string
	LastErrorSummary string
}

// SyncScope selects diagnostic text. Its zero value returns only a reason code.
type SyncScope struct{ IncludeErrorSummary bool }
type syncHealthReader interface {
	DirectorySyncStatus(context.Context, SyncScope) ([]DirectorySyncStatus, error)
}
type SyncHealth struct{ repo syncHealthReader }

func NewSyncHealth(repo syncHealthReader) *SyncHealth { return &SyncHealth{repo: repo} }

// DirectorySyncStatus returns at most 21 unhealthy providers and a code rather than raw errors.
func (h *SyncHealth) DirectorySyncStatus(ctx context.Context, scope SyncScope) ([]DirectorySyncStatus, error) {
	return h.repo.DirectorySyncStatus(ctx, scope)
}
