// Package repository implements the briefing store on PostgreSQL.
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Repository stores briefing items in briefing.items.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const target = "briefing_item"

const columns = `id::text, title, body, severity, status, valid_until, author_user_id::text,
	published_at, published_by_user_id::text, withdrawn_at, withdrawn_by_user_id::text,
	version, created_at, updated_at`

func scan(row pgx.Row) (application.Item, error) {
	var it application.Item
	err := row.Scan(&it.ID, &it.Title, &it.Body, &it.Severity, &it.Status, &it.ValidUntil, &it.AuthorUserID,
		&it.PublishedAt, &it.PublishedByUserID, &it.WithdrawnAt, &it.WithdrawnByUserID,
		&it.Version, &it.CreatedAt, &it.UpdatedAt)
	return it, err
}

// auditState never contains the title or body.
func auditState(it application.Item) map[string]any {
	return map[string]any{"status": it.Status, "severity": it.Severity, "validUntil": it.ValidUntil, "version": it.Version}
}

func record(ctx context.Context, tx pgx.Tx, c application.Caller, action, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: target, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func (r *Repository) Insert(ctx context.Context, c application.Caller, n application.NewItem) (application.Item, error) {
	var out application.Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scan(tx.QueryRow(ctx, `
			INSERT INTO briefing.items(title, body, severity, valid_until, author_user_id)
			VALUES ($1, $2, $3, $4, $5::uuid) RETURNING `+columns,
			n.Title, n.Body, n.Severity, n.ValidUntil, n.Author))
		if err != nil {
			return fmt.Errorf("insert briefing item: %w", err)
		}
		return record(ctx, tx, c, "briefing.item.created", out.ID, nil, auditState(out), nil)
	})
	return out, err
}

func (r *Repository) Get(ctx context.Context, id string) (application.Item, error) {
	if !validUUID(id) {
		return application.Item{}, application.ErrNotFound
	}
	it, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM briefing.items WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Item{}, application.ErrNotFound
	}
	if err != nil {
		return application.Item{}, fmt.Errorf("get briefing item: %w", err)
	}
	return it, nil
}

func (r *Repository) Change(ctx context.Context, c application.Caller, id string, decide func(application.Item) (application.Change, error)) (application.Item, error) {
	if !validUUID(id) {
		return application.Item{}, application.ErrNotFound
	}
	var out application.Item
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM briefing.items WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock briefing item: %w", err)
		}
		ch, err := decide(cur)
		if err != nil {
			return err
		}
		if ch.NoChange {
			out = cur
			return nil
		}
		n := ch.Next
		out, err = scan(tx.QueryRow(ctx, `
			UPDATE briefing.items SET
				title = $2, body = $3, severity = $4, status = $5, valid_until = $6,
				published_at = $7, published_by_user_id = $8::uuid, withdrawn_at = $9, withdrawn_by_user_id = $10::uuid,
				version = version + 1, updated_at = now()
			WHERE id = $1::uuid RETURNING `+columns,
			id, n.Title, n.Body, n.Severity, n.Status, n.ValidUntil, n.PublishedAt, n.PublishedByUserID,
			n.WithdrawnAt, n.WithdrawnByUserID))
		if err != nil {
			return fmt.Errorf("update briefing item: %w", err)
		}
		if err := record(ctx, tx, c, ch.Action, id, auditState(cur), auditState(out), ch.Metadata); err != nil {
			return err
		}
		var actor *string
		if c.Actor.UserID != "" {
			a := c.Actor.UserID
			actor = &a
		}
		for _, e := range ch.Events {
			if err := events.Publish(ctx, tx, events.Publication{Type: e.Type, ActorID: actor, CorrelationID: c.CorrelationID, Payload: e.Payload}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return application.Item{}, err
	}
	return out, nil
}

func (r *Repository) Delete(ctx context.Context, c application.Caller, id string, decide func(application.Item) error) error {
	if !validUUID(id) {
		return application.ErrNotFound
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM briefing.items WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock briefing item: %w", err)
		}
		if err := decide(cur); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM briefing.items WHERE id = $1::uuid`, id); err != nil {
			return fmt.Errorf("delete briefing item: %w", err)
		}
		return record(ctx, tx, c, "briefing.item.deleted", id, auditState(cur), nil, nil)
	})
}

// ---- listing ----

// Items are ordered newest first by publication time (drafts by creation
// time), then id. The cursor carries the sort key of the last returned item.
const sortKey = `coalesce(published_at, created_at)`

type cursor struct {
	At time.Time `json:"t"`
	ID string    `json:"i"`
}

func encodeCursor(it application.Item) string {
	at := it.CreatedAt
	if it.PublishedAt != nil {
		at = *it.PublishedAt
	}
	raw, _ := json.Marshal(cursor{At: at.UTC(), ID: it.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, application.ErrInvalidCursor
	}
	var c cursor
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || !validUUID(c.ID) || c.At.IsZero() {
		return cursor{}, application.ErrInvalidCursor
	}
	return c, nil
}

func (r *Repository) List(ctx context.Context, q application.ListQuery) (application.Result, error) {
	page := q.Page.Normalize()
	var (
		conds []string
		args  []any
	)
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if q.PublishedOnly {
		conds = append(conds, `status = 'published' AND (valid_until IS NULL OR valid_until > now())`)
	} else if q.Status != "" {
		add(`status = ?`, q.Status)
	}
	if page.Cursor != "" {
		c, err := decodeCursor(page.Cursor)
		if err != nil {
			return application.Result{}, err
		}
		args = append(args, c.At, c.ID)
		conds = append(conds, fmt.Sprintf(`(%s, id) < ($%d::timestamptz, $%d::uuid)`, sortKey, len(args)-1, len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM briefing.items`+where+
		fmt.Sprintf(` ORDER BY %s DESC, id DESC LIMIT $%d`, sortKey, len(args)), args...)
	if err != nil {
		return application.Result{}, fmt.Errorf("list briefing items: %w", err)
	}
	defer rows.Close()
	items := make([]application.Item, 0, page.Limit+1)
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return application.Result{}, fmt.Errorf("list briefing items: scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return application.Result{}, fmt.Errorf("list briefing items: %w", err)
	}
	res := application.Result{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = encodeCursor(res.Items[page.Limit-1])
	}
	return res, nil
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
