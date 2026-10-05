package repository

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/externalrefs"
	"time"
)

// BriefingReadScope selects incident details. Its zero value exposes references and state only.
type BriefingReadScope struct{ IncludeDetails bool }
type BriefingMajorIncident struct {
	ID            string
	Reference     string
	Title         string
	Status        string
	StartedAt     time.Time
	LinkedTickets int
}
type BriefingSyncStatus struct {
	Failed          int
	Pending         int
	OldestFailureAt *time.Time
}

// OpenMajorIncidents returns at most 21 records, so callers can show 20 and a truncation flag.
// Service Desk has no major-incident severity or affected-user count; linked tickets are counted.
func (b *Repository) OpenMajorIncidents(ctx context.Context, scope BriefingReadScope) ([]BriefingMajorIncident, error) {
	rows, err := b.pool.Query(ctx, `SELECT m.id::text,m.reference,m.title,m.status,m.created_at,
 (SELECT count(*) FROM servicedesk.tickets t WHERE t.major_incident_id=m.id)
 FROM servicedesk.major_incidents m WHERE m.status NOT IN ('resolved','closed') ORDER BY m.created_at DESC,m.id DESC LIMIT 21`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BriefingMajorIncident{}
	for rows.Next() {
		var v BriefingMajorIncident
		if err = rows.Scan(&v.ID, &v.Reference, &v.Title, &v.Status, &v.StartedAt, &v.LinkedTickets); err != nil {
			return nil, err
		}
		if !scope.IncludeDetails {
			v.Title = ""
			v.LinkedTickets = 0
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UnassignedOpenTickets counts open tickets without an assigned user. It contains no ticket details.
func (b *Repository) UnassignedOpenTickets(ctx context.Context) (int, error) {
	var n int
	err := b.pool.QueryRow(ctx, `SELECT count(*) FROM servicedesk.tickets WHERE assignee_user_id IS NULL AND status IN ('new','open','in_progress','waiting')`).Scan(&n)
	return n, err
}

// SyncHealth reports Autotask ticket pushes by state, without exposing external errors.
func (b *Repository) SyncHealth(ctx context.Context) (BriefingSyncStatus, error) {
	h, err := externalrefs.SyncHealth(ctx, b.pool, "autotask", "ticket")
	return BriefingSyncStatus{Failed: h.Failed, Pending: h.Pending, OldestFailureAt: h.OldestFailureAt}, err
}
