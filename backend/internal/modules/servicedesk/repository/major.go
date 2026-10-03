package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.MajorStore = (*Repository)(nil)

const majorCols = `m.id::text, m.reference, m.title, m.summary, m.status, m.declared_by::text, m.resolved_at, m.closed_at,
	m.version, m.created_at, m.updated_at`

// majorView adds the viewer's subscription flag and the number of linked tickets.
const majorView = majorCols + `,
	EXISTS (SELECT 1 FROM servicedesk.major_incident_subscriptions s WHERE s.major_incident_id = m.id AND s.user_id = $1::uuid),
	(SELECT count(*) FROM servicedesk.tickets t WHERE t.major_incident_id = m.id)`

func scanMajor(row pgx.Row, view bool) (application.MajorIncident, error) {
	var m application.MajorIncident
	dst := []any{&m.ID, &m.Reference, &m.Title, &m.Summary, &m.Status, &m.DeclaredBy, &m.ResolvedAt, &m.ClosedAt, &m.Version, &m.CreatedAt, &m.UpdatedAt}
	if view {
		dst = append(dst, &m.Subscribed, &m.Tickets)
	}
	err := row.Scan(dst...)
	return m, err
}

func (r *Repository) InsertMajorTx(ctx context.Context, tx pgx.Tx, m application.MajorIncident) (application.MajorIncident, error) {
	out, err := scanMajor(tx.QueryRow(ctx, `
		INSERT INTO servicedesk.major_incidents AS m (title, summary, status, declared_by) VALUES ($1, $2, $3, $4::uuid) RETURNING `+majorCols,
		m.Title, m.Summary, m.Status, m.DeclaredBy), false)
	if err != nil {
		return application.MajorIncident{}, fmt.Errorf("insert major incident: %w", err)
	}
	return out, nil
}

func (r *Repository) LockMajorTx(ctx context.Context, tx pgx.Tx, id string) (application.MajorIncident, error) {
	if !validUUID(id) {
		return application.MajorIncident{}, application.ErrNotFound
	}
	m, err := scanMajor(tx.QueryRow(ctx, `SELECT `+majorCols+` FROM servicedesk.major_incidents m WHERE m.id = $1::uuid FOR UPDATE`, id), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MajorIncident{}, application.ErrNotFound
	}
	if err != nil {
		return application.MajorIncident{}, fmt.Errorf("lock major incident: %w", err)
	}
	return m, nil
}

func (r *Repository) UpdateMajorTx(ctx context.Context, tx pgx.Tx, m application.MajorIncident) (application.MajorIncident, error) {
	out, err := scanMajor(tx.QueryRow(ctx, `
		UPDATE servicedesk.major_incidents AS m SET summary = $2, status = $3, resolved_at = $4, closed_at = $5, version = m.version + 1, updated_at = now()
		WHERE m.id = $1::uuid RETURNING `+majorCols, m.ID, m.Summary, m.Status, m.ResolvedAt, m.ClosedAt), false)
	if err != nil {
		return application.MajorIncident{}, fmt.Errorf("update major incident: %w", err)
	}
	return out, nil
}

func (r *Repository) AddUpdateTx(ctx context.Context, tx pgx.Tx, id string, u application.MajorUpdate) (application.MajorUpdate, error) {
	err := tx.QueryRow(ctx, `
		INSERT INTO servicedesk.major_incident_updates(major_incident_id, author_user_id, status, body) VALUES ($1::uuid, $2::uuid, $3, $4)
		RETURNING id::text, created_at`, id, u.AuthorID, u.Status, u.Body).Scan(&u.ID, &u.CreatedAt)
	if err != nil {
		return application.MajorUpdate{}, fmt.Errorf("insert major incident update: %w", err)
	}
	return u, nil
}

func (r *Repository) GetMajor(ctx context.Context, id, viewer string) (application.MajorIncident, error) {
	if !validUUID(id) || !validUUID(viewer) {
		return application.MajorIncident{}, application.ErrNotFound
	}
	m, err := scanMajor(r.pool.QueryRow(ctx, `SELECT `+majorView+` FROM servicedesk.major_incidents m WHERE m.id = $2::uuid`, viewer, id), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MajorIncident{}, application.ErrNotFound
	}
	if err != nil {
		return application.MajorIncident{}, fmt.Errorf("get major incident: %w", err)
	}
	return m, nil
}

func (r *Repository) ListMajor(ctx context.Context, viewer string, activeOnly bool, page application.Page) (application.MajorResult, error) {
	page = page.Normalize()
	if !validUUID(viewer) {
		return application.MajorResult{Items: []application.MajorIncident{}}, nil
	}
	args := []any{viewer}
	cond := "TRUE"
	if activeOnly {
		cond = "m.status NOT IN ('resolved', 'closed')"
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.MajorResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond += fmt.Sprintf(" AND m.id < $%d::uuid", len(args))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM servicedesk.major_incidents m WHERE %s ORDER BY m.id DESC LIMIT $%d`, majorView, cond, len(args)), args...)
	if err != nil {
		return application.MajorResult{}, fmt.Errorf("list major incidents: %w", err)
	}
	defer rows.Close()
	items := make([]application.MajorIncident, 0, page.Limit+1)
	for rows.Next() {
		m, err := scanMajor(rows, true)
		if err != nil {
			return application.MajorResult{}, fmt.Errorf("list major incidents: scan: %w", err)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return application.MajorResult{}, fmt.Errorf("list major incidents: %w", err)
	}
	res := application.MajorResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) Updates(ctx context.Context, id string) ([]application.MajorUpdate, error) {
	if !validUUID(id) {
		return []application.MajorUpdate{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT * FROM (SELECT id::text, author_user_id::text, status, body, created_at FROM servicedesk.major_incident_updates
		WHERE major_incident_id = $1::uuid ORDER BY id DESC LIMIT 500) newest ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("list major incident updates: %w", err)
	}
	defer rows.Close()
	out := []application.MajorUpdate{}
	for rows.Next() {
		var u application.MajorUpdate
		if err := rows.Scan(&u.ID, &u.AuthorID, &u.Status, &u.Body, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("list major incident updates: scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Repository) SubscribeTx(ctx context.Context, tx pgx.Tx, id, userID string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO servicedesk.major_incident_subscriptions(major_incident_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, id, userID); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	return nil
}

func (r *Repository) UnsubscribeTx(ctx context.Context, tx pgx.Tx, id, userID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM servicedesk.major_incident_subscriptions WHERE major_incident_id = $1::uuid AND user_id = $2::uuid`, id, userID); err != nil {
		return fmt.Errorf("unsubscribe: %w", err)
	}
	return nil
}

func (r *Repository) Subscribers(ctx context.Context, tx pgx.Tx, id, after string, limit int) ([]string, error) {
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := tx.Query(ctx, `SELECT user_id::text FROM servicedesk.major_incident_subscriptions
		WHERE major_incident_id = $1::uuid AND user_id > $2::uuid ORDER BY user_id LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list subscribers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("list subscribers: scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Repository) LinkTicketTx(ctx context.Context, tx pgx.Tx, majorID, ticketID string) (string, string, error) {
	if !validUUID(ticketID) {
		return "", "", application.ErrNotFound
	}
	var reporter, affected string
	err := tx.QueryRow(ctx, `
		UPDATE servicedesk.tickets SET major_incident_id = $1::uuid, version = version + 1, updated_at = now()
		WHERE id = $2::uuid AND status NOT IN ('closed', 'cancelled') AND major_incident_id IS NULL
		RETURNING reporter_user_id::text, affected_user_id::text`, majorID, ticketID).Scan(&reporter, &affected)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already linked to this incident is a no-op; anything else is not linkable.
		var current *string
		qerr := tx.QueryRow(ctx, `SELECT major_incident_id::text, reporter_user_id::text, affected_user_id::text FROM servicedesk.tickets WHERE id = $1::uuid AND status NOT IN ('closed', 'cancelled')`, ticketID).Scan(&current, &reporter, &affected)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return "", "", application.ErrNotFound
		}
		if qerr != nil {
			return "", "", fmt.Errorf("link ticket: %w", qerr)
		}
		if current != nil && *current == majorID {
			return reporter, affected, application.ErrAlreadyLinked
		}
		return "", "", &application.InvalidTransitionError{Operation: "link_ticket", From: "linked_elsewhere"}
	}
	if err != nil {
		return "", "", fmt.Errorf("link ticket: %w", err)
	}
	return reporter, affected, nil
}

func (r *Repository) MajorTitle(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var title string
	err := tx.QueryRow(ctx, `SELECT reference || ' · ' || title FROM servicedesk.major_incidents WHERE id = $1::uuid`, id).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", application.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("major incident title: %w", err)
	}
	return title, nil
}
