package repository

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/externalrefs"
	"time"
)

// BriefingReadScope selects incident details. Its zero value exposes references and state only.
type BriefingReadScope struct{ IncludeDetails bool }
type BriefingMajorIncident struct {
	ID        string
	Reference string
	Title     string
	Status    string
	StartedAt time.Time
	// LinkedTicketsTotal includes resolved, closed and cancelled links.
	LinkedTicketsTotal int
	// OpenLinkedTickets includes new, open, in_progress and waiting Tickets.
	OpenLinkedTickets int
}
type BriefingSyncStatus struct {
	Failed          int
	Pending         int
	OldestFailureAt *time.Time
}

// OpenMajorIncidents returns at most 21 records, so callers can show 20 and a truncation flag. Exercises are excluded.
// Service Desk has no major-incident severity or affected-user count; linked tickets are counted.
func (b *Repository) OpenMajorIncidents(ctx context.Context, scope BriefingReadScope) ([]BriefingMajorIncident, error) {
	rows, err := b.pool.Query(ctx, `SELECT m.id::text,m.reference,m.title,m.status,m.created_at,
 (SELECT count(*) FROM servicedesk.tickets t WHERE t.major_incident_id=m.id),
 (SELECT count(*) FROM servicedesk.tickets t WHERE t.major_incident_id=m.id AND t.status IN ('new','open','in_progress','waiting'))
 FROM servicedesk.major_incidents m WHERE m.status NOT IN ('resolved','closed') AND NOT m.is_exercise ORDER BY m.created_at DESC,m.id DESC LIMIT 21`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BriefingMajorIncident{}
	for rows.Next() {
		var v BriefingMajorIncident
		if err = rows.Scan(&v.ID, &v.Reference, &v.Title, &v.Status, &v.StartedAt, &v.LinkedTicketsTotal, &v.OpenLinkedTickets); err != nil {
			return nil, err
		}
		if !scope.IncludeDetails {
			v.Title = ""
			v.LinkedTicketsTotal = 0
			v.OpenLinkedTickets = 0
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UnassignedOpenTickets counts open tickets without an assigned user. It contains no ticket details.
// A zero scope hides the count.
func (b *Repository) UnassignedOpenTickets(ctx context.Context, scope BriefingReadScope) (int, error) {
	if !scope.IncludeDetails {
		return 0, nil
	}
	var n int
	err := b.pool.QueryRow(ctx, `SELECT count(*) FROM servicedesk.tickets WHERE assignee_user_id IS NULL AND status IN ('new','open','in_progress','waiting')`).Scan(&n)
	return n, err
}

// SyncHealth reports Autotask ticket pushes by state, without exposing external errors.
func (b *Repository) SyncHealth(ctx context.Context) (BriefingSyncStatus, error) {
	h, err := externalrefs.SyncHealth(ctx, b.pool, "autotask", "ticket")
	return BriefingSyncStatus{Failed: h.Failed, Pending: h.Pending, OldestFailureAt: h.OldestFailureAt}, err
}

// PublicIncident is the public status of an open, non-exercise Major Incident, the same text every signed-in User can read.
type PublicIncident struct {
	ID            string
	Reference     string
	Title         string
	Summary       string
	Status        string
	NextUpdateDue *time.Time
	UpdatedAt     time.Time
}

// PublicOpenIncidents returns at most 20 open, non-exercise incidents with their public status only
// (no owner, tickets or counts).
func (b *Repository) PublicOpenIncidents(ctx context.Context) ([]PublicIncident, error) {
	rows, err := b.pool.Query(ctx, `SELECT m.id::text,m.reference,m.title,m.summary,m.status,m.next_update_due,m.updated_at
 FROM servicedesk.major_incidents m WHERE m.status NOT IN ('resolved','closed') AND NOT m.is_exercise ORDER BY m.created_at DESC,m.id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicIncident{}
	for rows.Next() {
		var v PublicIncident
		if err = rows.Scan(&v.ID, &v.Reference, &v.Title, &v.Summary, &v.Status, &v.NextUpdateDue, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
