package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

var _ application.ProblemStore = (*Repository)(nil)

const problemCols = `p.id::text, p.reference, p.title, p.description, p.status, p.cause, p.workaround, p.resolution, p.owner_user_id::text,
	p.created_by::text, p.resolved_at, p.closed_at, (SELECT count(*) FROM servicedesk.problem_tickets t WHERE t.problem_id = p.id),
	p.version, p.created_at, p.updated_at`

func scanProblem(row pgx.Row) (application.Problem, error) {
	var p application.Problem
	err := row.Scan(&p.ID, &p.Reference, &p.Title, &p.Description, &p.Status, &p.Cause, &p.Workaround, &p.Resolution, &p.OwnerID,
		&p.CreatedBy, &p.ResolvedAt, &p.ClosedAt, &p.Tickets, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (r *Repository) InsertProblemTx(ctx context.Context, tx pgx.Tx, p application.Problem) (application.Problem, error) {
	out, err := scanProblem(tx.QueryRow(ctx, `
		INSERT INTO servicedesk.problems AS p (title, description, status, owner_user_id, created_by) VALUES ($1, $2, $3, $4::uuid, $5::uuid) RETURNING `+problemCols,
		p.Title, p.Description, p.Status, p.OwnerID, p.CreatedBy))
	if err != nil {
		return application.Problem{}, fmt.Errorf("insert problem: %w", err)
	}
	return out, nil
}

func (r *Repository) LockProblemTx(ctx context.Context, tx pgx.Tx, id string) (application.Problem, error) {
	if !validUUID(id) {
		return application.Problem{}, application.ErrNotFound
	}
	p, err := scanProblem(tx.QueryRow(ctx, `SELECT `+problemCols+` FROM servicedesk.problems p WHERE p.id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Problem{}, application.ErrNotFound
	}
	if err != nil {
		return application.Problem{}, fmt.Errorf("lock problem: %w", err)
	}
	return p, nil
}

func (r *Repository) UpdateProblemTx(ctx context.Context, tx pgx.Tx, p application.Problem) (application.Problem, error) {
	out, err := scanProblem(tx.QueryRow(ctx, `
		UPDATE servicedesk.problems AS p SET status = $2, cause = $3, workaround = $4, resolution = $5, owner_user_id = $6::uuid,
			resolved_at = $7, closed_at = $8, version = p.version + 1, updated_at = now()
		WHERE p.id = $1::uuid RETURNING `+problemCols, p.ID, p.Status, p.Cause, p.Workaround, p.Resolution, p.OwnerID, p.ResolvedAt, p.ClosedAt))
	if err != nil {
		return application.Problem{}, fmt.Errorf("update problem: %w", err)
	}
	return out, nil
}

func (r *Repository) GetProblem(ctx context.Context, id string) (application.Problem, error) {
	if !validUUID(id) {
		return application.Problem{}, application.ErrNotFound
	}
	p, err := scanProblem(r.pool.QueryRow(ctx, `SELECT `+problemCols+` FROM servicedesk.problems p WHERE p.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Problem{}, application.ErrNotFound
	}
	if err != nil {
		return application.Problem{}, fmt.Errorf("get problem: %w", err)
	}
	return p, nil
}

func (r *Repository) ListProblems(ctx context.Context, status string, page application.Page) (application.ProblemResult, error) {
	page = page.Normalize()
	args := []any{}
	cond := "TRUE"
	if status != "" {
		args = append(args, status)
		cond = fmt.Sprintf("p.status = $%d", len(args))
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.ProblemResult{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond += fmt.Sprintf(" AND p.id < $%d::uuid", len(args))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM servicedesk.problems p WHERE %s ORDER BY p.id DESC LIMIT $%d`, problemCols, cond, len(args)), args...)
	if err != nil {
		return application.ProblemResult{}, fmt.Errorf("list problems: %w", err)
	}
	defer rows.Close()
	items := make([]application.Problem, 0, page.Limit+1)
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return application.ProblemResult{}, fmt.Errorf("list problems: scan: %w", err)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return application.ProblemResult{}, fmt.Errorf("list problems: %w", err)
	}
	res := application.ProblemResult{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) LinkProblemTicketTx(ctx context.Context, tx pgx.Tx, problemID, ticketID, by string) (bool, error) {
	if !validUUID(ticketID) {
		return false, application.ErrNotFound
	}
	var byArg any
	if validUUID(by) {
		byArg = by
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO servicedesk.problem_tickets(problem_id, ticket_id, linked_by)
		SELECT $1::uuid, t.id, $3::uuid FROM servicedesk.tickets t WHERE t.id = $2::uuid
		ON CONFLICT DO NOTHING`, problemID, ticketID, byArg)
	if err != nil {
		return false, fmt.Errorf("link problem ticket: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM servicedesk.tickets WHERE id = $1::uuid)`, ticketID).Scan(&exists); err != nil {
			return false, fmt.Errorf("link problem ticket: %w", err)
		}
		if !exists {
			return false, application.ErrNotFound
		}
		return false, nil // already linked
	}
	return true, nil
}

func (r *Repository) UnlinkProblemTicketTx(ctx context.Context, tx pgx.Tx, problemID, ticketID string) error {
	if !validUUID(ticketID) {
		return application.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM servicedesk.problem_tickets WHERE problem_id = $1::uuid AND ticket_id = $2::uuid`, problemID, ticketID); err != nil {
		return fmt.Errorf("unlink problem ticket: %w", err)
	}
	return nil
}

func (r *Repository) ProblemTickets(ctx context.Context, problemID string) ([]application.Ticket, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM servicedesk.tickets WHERE id IN (SELECT ticket_id FROM servicedesk.problem_tickets WHERE problem_id = $1::uuid) ORDER BY id DESC LIMIT 200`, problemID)
	if err != nil {
		return nil, fmt.Errorf("list problem tickets: %w", err)
	}
	defer rows.Close()
	out := []application.Ticket{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list problem tickets: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Repository) KnownErrorsOfTicket(ctx context.Context, ticketID string) ([]application.Problem, error) {
	if !validUUID(ticketID) {
		return []application.Problem{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+problemCols+` FROM servicedesk.problems p
		WHERE p.status IN ('known_error', 'resolution_planned') AND p.id IN (SELECT problem_id FROM servicedesk.problem_tickets WHERE ticket_id = $1::uuid)
		ORDER BY p.id DESC LIMIT 20`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("known errors of ticket: %w", err)
	}
	defer rows.Close()
	out := []application.Problem{}
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, fmt.Errorf("known errors of ticket: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// VisibleProblemCounts implements application.ProblemStore.
func (r *Repository) VisibleProblemCounts(ctx context.Context, problemIDs []string, scope application.TicketScope) (map[string]int, error) {
	out := map[string]int{}
	ids := validUUIDs(problemIDs)
	if len(ids) == 0 {
		return out, nil
	}
	all, queues, user := scopeArgs(scope)
	rows, err := r.pool.Query(ctx, `SELECT pt.problem_id::text, count(*) FROM servicedesk.problem_tickets pt
		JOIN servicedesk.tickets t ON t.id = pt.ticket_id
		WHERE pt.problem_id = ANY($1::text[]::uuid[]) AND `+visibleTicketCond+` GROUP BY 1`, ids, all, queues, user)
	if err != nil {
		return nil, fmt.Errorf("count visible problem tickets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("count visible problem tickets: scan: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
