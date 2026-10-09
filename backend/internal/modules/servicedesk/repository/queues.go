package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.QueueStore = (*Repository)(nil)

const queueColumns = `q.id::text, q.key, q.prefix, q.name, COALESCE(q.description, ''), COALESCE(q.public_label, ''), q.status, q.visibility,
	q.routing_mode, q.default_priority, q.default_team_id::text, q.default_for_intake, q.number_padding, q.version, q.created_at, q.updated_at, q.archived_at`

func scanQueue(row pgx.Row, extra ...any) (application.Queue, error) {
	var q application.Queue
	var padding int16
	dest := append([]any{&q.ID, &q.Key, &q.Prefix, &q.Name, &q.Description, &q.PublicLabel, &q.Status, &q.Visibility, &q.RoutingMode,
		&q.DefaultPriority, &q.DefaultTeamID, &q.DefaultIntake, &padding, &q.Version, &q.CreatedAt, &q.UpdatedAt, &q.ArchivedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return q, err
	}
	q.NumberPadding = int(padding)
	return q, nil
}

// mapQueueError translates the database errors of the Queue triggers and functions.
func mapQueueError(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return nil
	}
	switch pe.Code {
	case "SD404":
		return application.ErrQueueArchived
	case "23505":
		switch pe.ConstraintName {
		case "queues_key_unique":
			return application.ErrQueueKeyTaken
		case "queues_prefix_unique":
			return application.ErrQueuePrefixTaken
		}
	}
	return nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const accessSQL = `
	SELECT ` + queueColumns + `,
	       COALESCE(max(CASE g.level WHEN 'manage' THEN 4 WHEN 'work' THEN 3 WHEN 'view' THEN 2 WHEN 'create' THEN 1 END), 0)
	FROM servicedesk.queues q
	LEFT JOIN servicedesk.queue_grants g ON g.queue_id = q.id AND (
		(g.subject_type = 'user' AND g.subject_id = $1::uuid)
		OR (g.subject_type = 'team' AND g.subject_id = ANY($2::text[]::uuid[]))
		OR (g.subject_type = 'role' AND g.subject_id = ANY($3::text[]::uuid[])))
	GROUP BY q.id`

func accessRows(ctx context.Context, q querier, userID string, teamIDs, roleIDs []string) ([]application.QueueAccessRow, error) {
	if !validUUID(userID) {
		return []application.QueueAccessRow{}, nil
	}
	rows, err := q.Query(ctx, accessSQL, userID, nonNil(teamIDs), nonNil(roleIDs))
	if err != nil {
		return nil, fmt.Errorf("queue access: %w", err)
	}
	defer rows.Close()
	out := []application.QueueAccessRow{}
	for rows.Next() {
		var level int
		queue, err := scanQueue(rows, &level)
		if err != nil {
			return nil, fmt.Errorf("queue access: scan: %w", err)
		}
		out = append(out, application.QueueAccessRow{Queue: queue, Level: level})
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// QueueAccess implements application.QueueStore.
func (r *Repository) QueueAccess(ctx context.Context, userID string, teamIDs, roleIDs []string) ([]application.QueueAccessRow, error) {
	return accessRows(ctx, r.pool, userID, teamIDs, roleIDs)
}

// QueueAccessTx implements application.QueueStore.
func (r *Repository) QueueAccessTx(ctx context.Context, tx pgx.Tx, userID string, teamIDs, roleIDs []string) ([]application.QueueAccessRow, error) {
	return accessRows(ctx, tx, userID, teamIDs, roleIDs)
}

func (r *Repository) one(ctx context.Context, where string, args ...any) (application.Queue, error) {
	q, err := scanQueue(r.pool.QueryRow(ctx, `SELECT `+queueColumns+` FROM servicedesk.queues q WHERE `+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Queue{}, application.ErrQueueNotFound
	}
	if err != nil {
		return application.Queue{}, fmt.Errorf("get queue: %w", err)
	}
	return q, nil
}

// GetQueue implements application.QueueStore.
func (r *Repository) GetQueue(ctx context.Context, id string) (application.Queue, error) {
	if !validUUID(id) {
		return application.Queue{}, application.ErrQueueNotFound
	}
	return r.one(ctx, `q.id = $1::uuid`, id)
}

// QueueByKey implements application.QueueStore.
func (r *Repository) QueueByKey(ctx context.Context, key string) (application.Queue, error) {
	return r.one(ctx, `q.key = $1`, key)
}

// DefaultQueue implements application.QueueStore.
func (r *Repository) DefaultQueue(ctx context.Context) (application.Queue, error) {
	return r.one(ctx, `q.default_for_intake`)
}

// ListQueues implements application.QueueStore.
func (r *Repository) ListQueues(ctx context.Context) ([]application.Queue, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+queueColumns+` FROM servicedesk.queues q ORDER BY q.id`)
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	defer rows.Close()
	out := []application.Queue{}
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, fmt.Errorf("list queues: scan: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// LockQueuesTx implements application.QueueStore. Rows are locked in id order, so two transactions that lock the
// same Queues cannot deadlock.
func (r *Repository) LockQueuesTx(ctx context.Context, tx pgx.Tx, ids []string) (map[string]application.Queue, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if validUUID(id) {
			valid = append(valid, strings.ToLower(id))
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+queueColumns+` FROM servicedesk.queues q WHERE q.id = ANY($1::text[]::uuid[]) ORDER BY q.id FOR UPDATE`, valid)
	if err != nil {
		return nil, fmt.Errorf("lock queues: %w", err)
	}
	defer rows.Close()
	out := map[string]application.Queue{}
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, fmt.Errorf("lock queues: scan: %w", err)
		}
		out[q.ID] = q
	}
	return out, rows.Err()
}

// InsertQueueTx implements application.QueueStore.
func (r *Repository) InsertQueueTx(ctx context.Context, tx pgx.Tx, q application.Queue) (application.Queue, error) {
	out, err := scanQueue(tx.QueryRow(ctx, `
		INSERT INTO servicedesk.queues AS q (key, prefix, name, description, public_label, visibility, routing_mode, default_priority, default_team_id, number_padding)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9::uuid, $10)
		RETURNING `+queueColumns, q.Key, q.Prefix, q.Name, q.Description, q.PublicLabel, q.Visibility, q.RoutingMode, q.DefaultPriority, q.DefaultTeamID, q.NumberPadding))
	if err != nil {
		if mapped := mapQueueError(err); mapped != nil {
			return application.Queue{}, mapped
		}
		return application.Queue{}, fmt.Errorf("insert queue: %w", err)
	}
	return out, nil
}

// UpdateQueueTx implements application.QueueStore.
func (r *Repository) UpdateQueueTx(ctx context.Context, tx pgx.Tx, q application.Queue) (application.Queue, error) {
	if q.DefaultIntake {
		if _, err := tx.Exec(ctx, `UPDATE servicedesk.queues SET default_for_intake = false, version = version + 1, updated_at = now() WHERE default_for_intake AND id <> $1::uuid`, q.ID); err != nil {
			return application.Queue{}, fmt.Errorf("clear intake queue: %w", err)
		}
	}
	out, err := scanQueue(tx.QueryRow(ctx, `
		UPDATE servicedesk.queues AS q SET name = $2, description = NULLIF($3, ''), public_label = NULLIF($4, ''), status = $5, visibility = $6,
			routing_mode = $7, default_priority = $8, default_team_id = $9::uuid, default_for_intake = $10, archived_at = $11,
			version = q.version + 1, updated_at = now()
		WHERE q.id = $1::uuid RETURNING `+queueColumns,
		q.ID, q.Name, q.Description, q.PublicLabel, q.Status, q.Visibility, q.RoutingMode, q.DefaultPriority, q.DefaultTeamID, q.DefaultIntake, q.ArchivedAt))
	if err != nil {
		return application.Queue{}, fmt.Errorf("update queue: %w", err)
	}
	return out, nil
}

// CountOpenTicketsTx implements application.QueueStore.
func (r *Repository) CountOpenTicketsTx(ctx context.Context, tx pgx.Tx, queueID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM servicedesk.tickets WHERE queue_id = $1::uuid AND status IN ('new', 'open', 'in_progress', 'waiting')`, queueID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count open tickets: %w", err)
	}
	return n, nil
}

// Grants implements application.QueueStore.
func (r *Repository) Grants(ctx context.Context, queueID string) ([]application.QueueGrant, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT subject_type, subject_id::text, level, COALESCE(granted_by::text, ''), granted_at
		FROM servicedesk.queue_grants WHERE queue_id = $1::uuid ORDER BY subject_type, subject_id, level`, queueID)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()
	out := []application.QueueGrant{}
	for rows.Next() {
		var g application.QueueGrant
		if err := rows.Scan(&g.SubjectType, &g.SubjectID, &g.Level, &g.GrantedBy, &g.GrantedAt); err != nil {
			return nil, fmt.Errorf("list grants: scan: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceGrantsTx implements application.QueueStore.
func (r *Repository) ReplaceGrantsTx(ctx context.Context, tx pgx.Tx, queueID string, grants []application.QueueGrant) error {
	if _, err := tx.Exec(ctx, `DELETE FROM servicedesk.queue_grants WHERE queue_id = $1::uuid`, queueID); err != nil {
		return fmt.Errorf("clear grants: %w", err)
	}
	for _, g := range grants {
		if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.queue_grants (queue_id, subject_type, subject_id, level, granted_by) VALUES ($1::uuid, $2, $3::uuid, $4, NULLIF($5, '')::uuid)`,
			queueID, g.SubjectType, g.SubjectID, g.Level, g.GrantedBy); err != nil {
			return fmt.Errorf("insert grant: %w", err)
		}
	}
	return nil
}

// MoveTx implements application.QueueStore.
func (r *Repository) MoveTx(ctx context.Context, tx pgx.Tx, t application.Ticket) (application.Ticket, error) {
	out, err := scan(tx.QueryRow(ctx, `
		WITH n AS (SELECT o_number, o_reference FROM servicedesk.issue_reference($2::uuid))
		UPDATE servicedesk.tickets AS tk SET queue_id = $2::uuid, number = n.o_number, reference = n.o_reference, queue_team_id = $3::uuid,
			assignee_user_id = $4::uuid, status = $5, version = tk.version + 1, updated_at = now()
		FROM n WHERE tk.id = $1::uuid
		RETURNING tk.id::text, tk.reference, tk.kind, tk.title, tk.description, tk.status, tk.waiting_reason, tk.status_reason, tk.resolution, tk.priority,
			tk.reporter_user_id::text, tk.affected_user_id::text, tk.queue_team_id::text, tk.assignee_user_id::text, tk.asset_id::text, tk.major_incident_id::text, tk.device_snapshot,
			tk.resolved_at, tk.closed_at, tk.version, tk.created_at, tk.updated_at, tk.queue_id::text, tk.number, tk.patient_impact, tk.affected_location_id::text, tk.duplicate_of_ticket_id::text, tk.reported_impact`,
		t.ID, t.QueueID, t.QueueTeamID, t.AssigneeID, t.Status))
	if err != nil {
		if mapped := mapQueueError(err); mapped != nil {
			return application.Ticket{}, mapped
		}
		return application.Ticket{}, fmt.Errorf("move ticket: %w", err)
	}
	return out, nil
}

// References implements application.QueueStore.
func (r *Repository) References(ctx context.Context, ticketIDs []string) (map[string][]application.RefEntry, error) {
	valid := make([]string, 0, len(ticketIDs))
	for _, id := range ticketIDs {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT ticket_id::text, reference, queue_id::text, kind, issued_at
		FROM servicedesk.reference_registry WHERE ticket_id = ANY($1::text[]::uuid[]) ORDER BY ticket_id, issued_at, reference`, valid)
	if err != nil {
		return nil, fmt.Errorf("list references: %w", err)
	}
	defer rows.Close()
	out := map[string][]application.RefEntry{}
	for rows.Next() {
		var id string
		var e application.RefEntry
		if err := rows.Scan(&id, &e.Reference, &e.QueueID, &e.Kind, &e.IssuedAt); err != nil {
			return nil, fmt.Errorf("list references: scan: %w", err)
		}
		out[id] = append(out[id], e)
	}
	return out, rows.Err()
}

// ResolveReference implements application.QueueStore.
func (r *Repository) ResolveReference(ctx context.Context, reference string) (string, bool, bool, error) {
	var id string
	var alias bool
	err := r.pool.QueryRow(ctx, `SELECT ticket_id::text, kind = 'alias' FROM servicedesk.reference_registry WHERE reference = $1`, reference).Scan(&id, &alias)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("resolve reference: %w", err)
	}
	return id, alias, true, nil
}
