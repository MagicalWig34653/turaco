// Package repository implements the approvals store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/approvals/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

type Repository struct{ pool *pgxpool.Pool }

var (
	_ application.Store    = (*Repository)(nil)
	_ application.TxReader = (*Repository)(nil)
)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) CountPending(ctx context.Context, userID string, teamIDs []string) (int, error) {
	if !validUUID(userID) {
		return 0, nil
	}
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM approvals.approvals WHERE status='pending'
 AND (approver_user_id=$1::uuid OR approver_team_id=ANY($2::text[]::uuid[]))
 AND NOT ($1::uuid=ANY(excluded_user_ids))`, userID, teamIDs).Scan(&n)
	return n, err
}

const columns = `id::text, subject_type, subject_id::text, subject_label, step_index, status,
	approver_user_id::text, approver_team_id::text, coalesce(excluded_user_ids::text[], '{}'), requested_by_user_id::text,
	decided_by_user_id::text, decided_at, decision_comment, version, created_at, updated_at`

func scan(row pgx.Row) (application.Approval, error) {
	var a application.Approval
	err := row.Scan(&a.ID, &a.SubjectType, &a.SubjectID, &a.SubjectLabel, &a.StepIndex, &a.Status,
		&a.ApproverUserID, &a.ApproverTeamID, &a.ExcludedUserIDs, &a.RequestedByID,
		&a.DecidedByUserID, &a.DecidedAt, &a.DecisionComment, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// auditState holds no label and no comment text beyond what the action needs.
func auditState(a application.Approval) map[string]any {
	return map[string]any{
		"subjectType": a.SubjectType, "subjectId": a.SubjectID, "stepIndex": a.StepIndex, "status": a.Status,
		"approverUserId": a.ApproverUserID, "approverTeamId": a.ApproverTeamID, "version": a.Version,
	}
}

func actorID(c application.Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	id := c.Actor.UserID
	return &id
}

func publish(ctx context.Context, tx pgx.Tx, c application.Caller, e application.Event) error {
	return events.Publish(ctx, tx, events.Publication{Type: e.Type, ActorID: actorID(c), CorrelationID: c.CorrelationID, Payload: e.Payload})
}

func (r *Repository) InsertTx(ctx context.Context, tx pgx.Tx, c application.Caller, n application.NewApproval) (application.Approval, error) {
	excl := n.ExcludedUserIDs
	if excl == nil {
		excl = []string{}
	}
	a, err := scan(tx.QueryRow(ctx, `
		INSERT INTO approvals.approvals(subject_type, subject_id, subject_label, step_index, approver_user_id, approver_team_id,
		                                excluded_user_ids, requested_by_user_id)
		VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6::uuid, $7::text[]::uuid[], $8::uuid)
		RETURNING `+columns, n.SubjectType, n.SubjectID, n.SubjectLabel, n.StepIndex, n.ApproverUserID, n.ApproverTeamID, excl, n.RequestedBy))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return application.Approval{}, application.ErrConflict
		}
		return application.Approval{}, fmt.Errorf("insert approval: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Change{
		Action: "approvals.approval.requested", TargetType: "approval", TargetID: a.ID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, After: auditState(a),
	}); err != nil {
		return application.Approval{}, err
	}
	if err := publish(ctx, tx, c, application.Event{Type: "ApprovalRequested", Payload: map[string]any{
		"approvalId": a.ID, "subjectType": a.SubjectType, "subjectId": a.SubjectID, "stepIndex": a.StepIndex,
	}}); err != nil {
		return application.Approval{}, err
	}
	return a, nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}

// GetTx reads an approval inside the caller's transaction (outbox consumers).
func (r *Repository) GetTx(ctx context.Context, tx pgx.Tx, id string) (application.Approval, error) {
	if !validUUID(id) {
		return application.Approval{}, application.ErrNotFound
	}
	a, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM approvals.approvals WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Approval{}, application.ErrNotFound
	}
	if err != nil {
		return application.Approval{}, fmt.Errorf("get approval: %w", err)
	}
	return a, nil
}

func (r *Repository) Get(ctx context.Context, id string) (application.Approval, error) {
	if !validUUID(id) {
		return application.Approval{}, application.ErrNotFound
	}
	a, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM approvals.approvals WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Approval{}, application.ErrNotFound
	}
	if err != nil {
		return application.Approval{}, fmt.Errorf("get approval: %w", err)
	}
	return a, nil
}

func (r *Repository) Decide(ctx context.Context, c application.Caller, id string, decide func(application.Approval) (application.Approval, []application.Event, error)) (application.Approval, error) {
	if !validUUID(id) {
		return application.Approval{}, application.ErrNotFound
	}
	var out application.Approval
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM approvals.approvals WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock approval: %w", err)
		}
		next, evs, err := decide(cur)
		if err != nil {
			return err
		}
		out, err = scan(tx.QueryRow(ctx, `
			UPDATE approvals.approvals SET status = $2, decided_by_user_id = $3::uuid, decided_at = $4, decision_comment = $5,
				version = version + 1, updated_at = now()
			WHERE id = $1::uuid RETURNING `+columns, id, next.Status, next.DecidedByUserID, next.DecidedAt, next.DecisionComment))
		if err != nil {
			return fmt.Errorf("update approval: %w", err)
		}
		meta := map[string]any{}
		if out.DecisionComment != nil {
			meta["comment"] = *out.DecisionComment
		}
		if err := audit.Record(ctx, tx, audit.Change{
			Action: "approvals.approval." + out.Status, TargetType: "approval", TargetID: id, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Before: auditState(cur), After: auditState(out), Metadata: meta,
		}); err != nil {
			return err
		}
		for _, e := range evs {
			if err := publish(ctx, tx, c, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return application.Approval{}, err
	}
	return out, nil
}

func (r *Repository) CancelBySubjectTx(ctx context.Context, tx pgx.Tx, c application.Caller, subjectType, subjectID string) (int, error) {
	if !validUUID(subjectID) {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM approvals.approvals
		WHERE subject_type = $1 AND subject_id = $2::uuid AND status = 'pending' ORDER BY id FOR UPDATE`, subjectType, subjectID)
	if err != nil {
		return 0, fmt.Errorf("select pending approvals: %w", err)
	}
	var pending []application.Approval
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("select pending approvals: scan: %w", err)
		}
		pending = append(pending, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("select pending approvals: %w", err)
	}
	for _, cur := range pending {
		out, err := scan(tx.QueryRow(ctx, `UPDATE approvals.approvals SET status = 'cancelled', version = version + 1, updated_at = now()
			WHERE id = $1::uuid RETURNING `+columns, cur.ID))
		if err != nil {
			return 0, fmt.Errorf("cancel approval: %w", err)
		}
		if err := audit.Record(ctx, tx, audit.Change{
			Action: "approvals.approval.cancelled", TargetType: "approval", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Before: auditState(cur), After: auditState(out),
		}); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

func (r *Repository) ForSubject(ctx context.Context, subjectType, subjectID string) ([]application.Approval, error) {
	if !validUUID(subjectID) {
		return []application.Approval{}, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM approvals.approvals WHERE subject_type = $1 AND subject_id = $2::uuid ORDER BY step_index`, subjectType, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list approvals of subject: %w", err)
	}
	defer rows.Close()
	out := []application.Approval{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list approvals of subject: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) Inbox(ctx context.Context, q application.InboxQuery) (application.Result, error) {
	if !validUUID(q.UserID) {
		return application.Result{Items: []application.Approval{}}, nil
	}
	page := q.Page.Normalize()
	args := []any{q.UserID}
	var conds []string
	if q.Status == "decided" {
		conds = append(conds, "decided_by_user_id = $1::uuid")
	} else {
		args = append(args, q.TeamIDs)
		conds = append(conds, `status = 'pending' AND (approver_user_id = $1::uuid OR approver_team_id = ANY($2::text[]::uuid[]))
			AND NOT ($1::uuid = ANY(excluded_user_ids))`)
	}
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		conds = append(conds, fmt.Sprintf("id < $%d::uuid", len(args)))
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM approvals.approvals WHERE %s ORDER BY id DESC LIMIT $%d`,
		columns, strings.Join(conds, " AND "), len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("approval inbox: %w", err)
	}
	defer rows.Close()
	items := make([]application.Approval, 0, page.Limit+1)
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("approval inbox: scan: %w", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("approval inbox: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) IsApproverFor(ctx context.Context, subjectType, subjectID, userID string, teamIDs []string) (bool, error) {
	if !validUUID(subjectID) || !validUUID(userID) {
		return false, nil
	}
	var ok bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM approvals.approvals
			WHERE subject_type = $1 AND subject_id = $2::uuid
			  AND (approver_user_id = $3::uuid OR approver_team_id = ANY($4::text[]::uuid[]) OR decided_by_user_id = $3::uuid))`,
		subjectType, subjectID, userID, teamIDs).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("is approver: %w", err)
	}
	return ok, nil
}
