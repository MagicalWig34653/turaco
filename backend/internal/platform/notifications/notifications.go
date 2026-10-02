// Package notifications is the platform Notification service (ADR-0024).
// Modules emit a notification intent; this package stores one in-app
// Notification per recipient. Texts are localized by the client from the
// category and params, so no UI text lives here. Reading and marking
// notifications is limited to the recipient: ownership is the authorization.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxParamsBytes = 4096
	// MaxUnreadCount caps the unread counter so the query stays cheap.
	MaxUnreadCount = 100
	DefaultLimit   = 50
	MaxLimit       = 200
)

var (
	ErrNotFound      = errors.New("notifications: not found")
	ErrInvalidCursor = errors.New("notifications: invalid cursor")
)

// Intent is a request to notify one User.
type Intent struct {
	RecipientUserID string
	Category        string
	// Params are shown by the client (for example a task title); no secrets.
	Params map[string]any
	// LinkType and LinkID name the record the notification is about; both or neither.
	LinkType string
	LinkID   string
	// DedupeKey makes creation idempotent per recipient (for example outbox
	// event id plus recipient). Required.
	DedupeKey string
	// SuppressWithin, when positive and a link is set, skips the notification
	// if the recipient already received one of the same category for the same
	// link within this duration, so repeated changes cannot flood an inbox.
	SuppressWithin time.Duration
}

// Notification is a stored in-app notification.
type Notification struct {
	ID        string
	Category  string
	Params    map[string]any
	LinkType  *string
	LinkID    *string
	CreatedAt time.Time
	ReadAt    *time.Time
}

// Page is cursor pagination, newest first; Cursor is the last id of the previous page.
type Page struct {
	Limit  int
	Cursor string
}

func (p Page) normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// Result is one page; NextCursor is empty on the last page.
type Result struct {
	Items      []Notification
	NextCursor string
}

// Service stores and reads notifications and, when the email channel is
// enabled, schedules their email delivery.
type Service struct {
	pool       *pgxpool.Pool
	categories *Registry
	email      bool
}

// NewService creates a Service without the email channel (turaco-api).
func NewService(pool *pgxpool.Pool, categories *Registry) *Service {
	return &Service{pool: pool, categories: categories}
}

// WithEmail returns a Service that also creates an email delivery (and its
// job) for every new notification whose recipient has not opted out of email
// for the category. Only turaco-worker, which sends the mail, enables it.
func (s *Service) WithEmail() *Service {
	return &Service{pool: s.pool, categories: s.categories, email: true}
}

// Create stores the notification inside the caller's transaction (typically
// an outbox consumer's) and reports whether a row was created; a repeated
// DedupeKey for the same recipient is a no-op.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, in Intent) (bool, error) {
	switch {
	case !validUUID(in.RecipientUserID):
		return false, errors.New("notifications: recipient must be a user id")
	case !s.categories.Valid(in.Category):
		return false, fmt.Errorf("notifications: unregistered category %q", in.Category)
	case in.DedupeKey == "":
		return false, errors.New("notifications: dedupe key is required")
	case (in.LinkType == "") != (in.LinkID == ""):
		return false, errors.New("notifications: link type and id go together")
	case in.LinkID != "" && !validUUID(in.LinkID):
		return false, errors.New("notifications: link id must be a uuid")
	}
	params := []byte(`{}`)
	if in.Params != nil {
		var err error
		if params, err = json.Marshal(in.Params); err != nil {
			return false, fmt.Errorf("notifications: marshal params: %w", err)
		}
		if len(params) > maxParamsBytes {
			return false, errors.New("notifications: params too large")
		}
	}
	var linkType, linkID *string
	if in.LinkType != "" {
		linkType, linkID = &in.LinkType, &in.LinkID
	}
	if in.SuppressWithin > 0 && in.LinkID != "" {
		var recent bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM platform.notifications
				WHERE recipient_user_id = $1::uuid AND category = $2 AND link_type = $3 AND link_id = $4::uuid
				  AND created_at > now() - make_interval(secs => $5))`,
			in.RecipientUserID, in.Category, in.LinkType, in.LinkID, in.SuppressWithin.Seconds()).Scan(&recent); err != nil {
			return false, fmt.Errorf("check recent notification: %w", err)
		}
		if recent {
			return false, nil
		}
	}
	var notificationID string
	err := tx.QueryRow(ctx, `
		INSERT INTO platform.notifications(recipient_user_id, category, params, link_type, link_id, dedupe_key)
		VALUES ($1::uuid, $2, $3, $4, $5::uuid, $6)
		ON CONFLICT (recipient_user_id, dedupe_key) DO NOTHING
		RETURNING id::text`,
		in.RecipientUserID, in.Category, params, linkType, linkID, in.DedupeKey).Scan(&notificationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // already created by an earlier delivery of the same event
	}
	if err != nil {
		return false, fmt.Errorf("create notification: %w", err)
	}
	if s.email {
		if err := scheduleEmail(ctx, tx, notificationID, in.RecipientUserID, in.Category); err != nil {
			return false, err
		}
	}
	return true, nil
}

// List returns the recipient's notifications, newest first.
func (s *Service) List(ctx context.Context, userID string, unreadOnly bool, p Page) (Result, error) {
	p = p.normalize()
	if !validUUID(userID) {
		return Result{Items: []Notification{}}, nil
	}
	args := []any{userID, unreadOnly, p.Limit + 1}
	cursor := ""
	if p.Cursor != "" {
		if !validUUID(p.Cursor) {
			return Result{}, ErrInvalidCursor
		}
		args = append(args, p.Cursor)
		cursor = " AND id < $4::uuid"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, category, params, link_type, link_id::text, created_at, read_at
		FROM platform.notifications
		WHERE recipient_user_id = $1::uuid AND (NOT $2::boolean OR read_at IS NULL)`+cursor+`
		ORDER BY id DESC LIMIT $3`, args...)
	if err != nil {
		return Result{}, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	items := make([]Notification, 0, p.Limit+1)
	for rows.Next() {
		var n Notification
		var raw []byte
		if err := rows.Scan(&n.ID, &n.Category, &raw, &n.LinkType, &n.LinkID, &n.CreatedAt, &n.ReadAt); err != nil {
			return Result{}, fmt.Errorf("list notifications: scan: %w", err)
		}
		if err := json.Unmarshal(raw, &n.Params); err != nil {
			return Result{}, fmt.Errorf("list notifications: params: %w", err)
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("list notifications: %w", err)
	}
	res := Result{Items: items}
	if len(items) > p.Limit {
		res.Items = items[:p.Limit]
		res.NextCursor = res.Items[p.Limit-1].ID
	}
	return res, nil
}

// UnreadCount returns the number of unread notifications, at most MaxUnreadCount.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	if !validUUID(userID) {
		return 0, nil
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT 1 FROM platform.notifications
			WHERE recipient_user_id = $1::uuid AND read_at IS NULL LIMIT $2
		) unread`, userID, MaxUnreadCount).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("unread count: %w", err)
	}
	return n, nil
}

// MarkRead marks one of the recipient's notifications read (idempotent).
// Another User's or an unknown id is ErrNotFound.
func (s *Service) MarkRead(ctx context.Context, userID, id string) error {
	if !validUUID(userID) || !validUUID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE platform.notifications SET read_at = COALESCE(read_at, now())
		WHERE id = $1::uuid AND recipient_user_id = $2::uuid`, id, userID)
	if err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead marks all of the recipient's unread notifications read and
// returns how many changed.
func (s *Service) MarkAllRead(ctx context.Context, userID string) (int, error) {
	if !validUUID(userID) {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE platform.notifications SET read_at = now()
		WHERE recipient_user_id = $1::uuid AND read_at IS NULL`, userID)
	if err != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", err)
	}
	return int(tag.RowsAffected()), nil
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
