// Package public exposes bounded Service Desk reads to other modules.
package public

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReadScope selects incident details. Its zero value exposes references and state only.
type ReadScope = repository.BriefingReadScope
type MajorIncident = repository.BriefingMajorIncident
type SyncStatus = repository.BriefingSyncStatus
type Briefing struct{ repo *repository.Repository }

func NewBriefing(pool *pgxpool.Pool) *Briefing { return &Briefing{repo: repository.New(pool)} }

// OpenMajorIncidents returns at most 21 records; the caller may show 20 and truncation.
func (b *Briefing) OpenMajorIncidents(ctx context.Context, scope ReadScope) ([]MajorIncident, error) {
	return b.repo.OpenMajorIncidents(ctx, scope)
}

// UnassignedOpenTickets returns a count without ticket details. Zero ReadScope hides the count.
func (b *Briefing) UnassignedOpenTickets(ctx context.Context, scope ReadScope) (int, error) {
	return b.repo.UnassignedOpenTickets(ctx, scope)
}

// SyncHealth returns Autotask push counts without raw errors.
func (b *Briefing) SyncHealth(ctx context.Context) (SyncStatus, error) { return b.repo.SyncHealth(ctx) }
