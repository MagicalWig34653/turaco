package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

var (
	// ErrInvalidFilter marks a malformed filter value.
	ErrInvalidFilter = errors.New("audit: invalid filter")
	// ErrInvalidCursor marks a malformed pagination cursor.
	ErrInvalidCursor = errors.New("audit: invalid cursor")
	// ErrInvalidLimit marks a non-positive limit.
	ErrInvalidLimit = errors.New("audit: invalid limit")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Filter narrows an audit query. Empty fields do not filter. From is
// inclusive and To exclusive.
type Filter struct {
	TargetType    string
	TargetID      string
	Action        string
	ActionPrefix  string
	ActorID       string
	CorrelationID string
	From, To      *time.Time
}

// Page is keyset pagination, newest first (descending id). Cursor is the last
// id of the previous page.
type Page struct {
	Limit  int
	Cursor string
}

// Result is one page of events; NextCursor is empty on the last page.
type Result struct {
	Items      []Event
	NextCursor string
}

// Event is a stored audit event as returned by queries.
type Event struct {
	ID            string
	OccurredAt    time.Time
	ActorID       *string
	Action        string
	TargetType    string
	TargetID      string
	CorrelationID string
	Before        json.RawMessage
	After         json.RawMessage
	Metadata      json.RawMessage
}

// Reader queries platform.audit_events.
type Reader struct {
	pool *pgxpool.Pool
}

func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

func (f Filter) validate() error {
	for _, c := range []struct {
		v   string
		max int
	}{{f.TargetType, 100}, {f.TargetID, 200}, {f.Action, 200}, {f.ActionPrefix, 200}, {f.CorrelationID, 200}} {
		if utf8.RuneCountInString(c.v) > c.max || !utf8.ValidString(c.v) || strings.ContainsRune(c.v, 0) {
			return ErrInvalidFilter
		}
	}
	if f.ActorID != "" && !uuidPattern.MatchString(f.ActorID) {
		return ErrInvalidFilter
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return ErrInvalidFilter
	}
	return nil
}

// List returns audit events matching f, newest first.
func (r *Reader) List(ctx context.Context, f Filter, p Page) (Result, error) {
	if err := f.validate(); err != nil {
		return Result{}, err
	}
	if p.Limit < 0 {
		return Result{}, ErrInvalidLimit
	}
	if p.Limit == 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.TargetType != "" {
		add("target_type = ?", f.TargetType)
	}
	if f.TargetID != "" {
		add("target_id = ?", f.TargetID)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.ActionPrefix != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.ActionPrefix)
		add(`action LIKE ? || '%' ESCAPE '\'`, esc)
	}
	if f.ActorID != "" {
		add("actor_id = ?", f.ActorID)
	}
	if f.CorrelationID != "" {
		add("correlation_id = ?", f.CorrelationID)
	}
	if f.From != nil {
		add("occurred_at >= ?", f.From.UTC())
	}
	if f.To != nil {
		add("occurred_at < ?", f.To.UTC())
	}
	if p.Cursor != "" {
		if !uuidPattern.MatchString(p.Cursor) {
			return Result{}, ErrInvalidCursor
		}
		add("id < ?", p.Cursor)
	}
	sql := `SELECT id::text, occurred_at, actor_id::text, action, target_type, target_id, correlation_id, before_data, after_data, metadata
		FROM platform.audit_events`
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, p.Limit+1)
	sql += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return Result{}, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()
	res := Result{Items: []Event{}}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.ActorID, &e.Action, &e.TargetType, &e.TargetID, &e.CorrelationID, &e.Before, &e.After, &e.Metadata); err != nil {
			return Result{}, fmt.Errorf("query audit events: scan: %w", err)
		}
		res.Items = append(res.Items, e)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("query audit events: %w", err)
	}
	if len(res.Items) > p.Limit {
		res.Items = res.Items[:p.Limit]
		res.NextCursor = res.Items[p.Limit-1].ID
	}
	return res, nil
}
