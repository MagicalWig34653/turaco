package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
// inclusive and To exclusive. TargetID requires TargetType (the target index
// is (target_type, target_id, ...)). ActionPrefix is a literal prefix, matched
// as a byte range on the action text.
type Filter struct {
	TargetType    string
	TargetID      string
	Action        string
	ActionPrefix  string
	ActorID       string
	CorrelationID string
	From, To      *time.Time
}

// Page is keyset pagination, newest first: events are ordered by
// (occurred_at DESC, id DESC). Cursor is the opaque NextCursor of the previous
// page; it encodes both columns and must not be interpreted by callers.
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
	if f.TargetID != "" && f.TargetType == "" {
		return ErrInvalidFilter
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
		// A range predicate on the action text (bytewise, like text_pattern_ops)
		// instead of LIKE: no wildcard escaping and the planner can use the
		// (action text_pattern_ops, ...) index with a generic plan.
		add("action ~>=~ ?", f.ActionPrefix)
		if upper, ok := prefixUpperBound(f.ActionPrefix); ok {
			add("action ~<~ ?", upper)
		}
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
		at, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return Result{}, err
		}
		args = append(args, at, id)
		conds = append(conds, fmt.Sprintf("(occurred_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	sql := `SELECT id::text, occurred_at, actor_id::text, action, target_type, target_id, correlation_id, before_data, after_data, metadata
		FROM platform.audit_events`
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, p.Limit+1)
	sql += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args))
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
		res.NextCursor = encodeCursor(res.Items[p.Limit-1])
	}
	return res, nil
}

// prefixUpperBound returns the smallest string greater than every string with
// the given prefix, by incrementing the last rune (UTF-8 byte order equals
// code point order). A trailing U+10FFFF is dropped first. ok is false when no
// bound exists (an empty or all-U+10FFFF prefix).
func prefixUpperBound(prefix string) (string, bool) {
	runes := []rune(prefix)
	for len(runes) > 0 {
		last := runes[len(runes)-1]
		if last == utf8.MaxRune {
			runes = runes[:len(runes)-1]
			continue
		}
		last++
		if last >= 0xD800 && last <= 0xDFFF {
			last = 0xE000 // skip surrogates
		}
		runes[len(runes)-1] = last
		return string(runes), true
	}
	return "", false
}

// encodeCursor returns the opaque cursor of the last event of a page:
// base64url("<occurred_at in unix microseconds>.<id>").
func encodeCursor(e Event) string {
	raw := strconv.FormatInt(e.OccurredAt.UnixMicro(), 10) + "." + e.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok || !uuidPattern.MatchString(id) {
		return time.Time{}, "", ErrInvalidCursor
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	// Years 1 to 9999, the range timestamptz and RFC 3339 share.
	if err != nil || n < -62135596800*1e6 || n > 253402300799*1e6 {
		return time.Time{}, "", ErrInvalidCursor
	}
	return time.UnixMicro(n).UTC(), strings.ToLower(id), nil
}
