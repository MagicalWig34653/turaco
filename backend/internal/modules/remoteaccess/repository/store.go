// Package repository implements the Remote Access store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
)

// Repository stores sessions, transitions, launch handles and peer mappings.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F'):
			return false
		}
	}
	return true
}

func constraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

const cols = `id::text, reference, device_id::text, ticket_id::text, provider, peer_id, mode, status, status_reason,
	initiated_by::text, approval_id::text, excluded_user_ids::text[], consent, consent_recorded_by::text, consent_recorded_at,
	mismatch_reason, launched_at, closed_at, expires_at, note, observed_connected_at, observed_ended_at, observed_source,
	observed_at, version, created_at, updated_at`

func scan(row pgx.Row) (application.Session, error) {
	var s application.Session
	err := row.Scan(&s.ID, &s.Reference, &s.DeviceID, &s.TicketID, &s.Provider, &s.PeerID, &s.Mode, &s.Status, &s.StatusReason,
		&s.InitiatedBy, &s.ApprovalID, &s.ExcludedUserIDs, &s.Consent, &s.ConsentRecordedBy, &s.ConsentRecordedAt,
		&s.MismatchReason, &s.LaunchedAt, &s.ClosedAt, &s.ExpiresAt, &s.Note, &s.ObservedConnectedAt, &s.ObservedEndedAt, &s.ObservedSource,
		&s.ObservedAt, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func ids(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *Repository) InsertSessionTx(ctx context.Context, tx pgx.Tx, s application.Session) (application.Session, error) {
	out, err := scan(tx.QueryRow(ctx, `
		INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, excluded_user_ids,
			consent, mismatch_reason, expires_at, note)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, $8::uuid[], $9, $10, $11, $12) RETURNING `+cols,
		s.DeviceID, s.TicketID, s.Provider, s.PeerID, s.Mode, s.Status, s.InitiatedBy, ids(s.ExcludedUserIDs), s.Consent, s.MismatchReason,
		s.ExpiresAt, s.Note))
	if constraint(err) == "sessions_open_per_device" {
		return application.Session{}, &application.RefusedError{Code: application.RefSessionOpen}
	}
	if err != nil {
		return application.Session{}, fmt.Errorf("insert session: %w", err)
	}
	return out, nil
}

func (r *Repository) LockSessionTx(ctx context.Context, tx pgx.Tx, id string) (application.Session, error) {
	if !validUUID(id) {
		return application.Session{}, application.ErrNotFound
	}
	s, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM remoteaccess.sessions WHERE id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Session{}, application.ErrNotFound
	}
	if err != nil {
		return application.Session{}, fmt.Errorf("lock session: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSessionTx(ctx context.Context, tx pgx.Tx, s application.Session) (application.Session, error) {
	out, err := scan(tx.QueryRow(ctx, `
		UPDATE remoteaccess.sessions SET status = $2, status_reason = $3, approval_id = $4::uuid, consent = $5,
			consent_recorded_by = $6::uuid, consent_recorded_at = $7, launched_at = $8, closed_at = $9, expires_at = $10,
			observed_connected_at = $11, observed_ended_at = $12, observed_source = $13, observed_at = $14,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+cols,
		s.ID, s.Status, s.StatusReason, s.ApprovalID, s.Consent, s.ConsentRecordedBy, s.ConsentRecordedAt, s.LaunchedAt, s.ClosedAt, s.ExpiresAt,
		s.ObservedConnectedAt, s.ObservedEndedAt, s.ObservedSource, s.ObservedAt))
	if err != nil {
		return application.Session{}, fmt.Errorf("update session: %w", err)
	}
	return out, nil
}

func (r *Repository) GetSession(ctx context.Context, id string) (application.Session, error) {
	if !validUUID(id) {
		return application.Session{}, application.ErrNotFound
	}
	s, err := scan(r.pool.QueryRow(ctx, `SELECT `+cols+` FROM remoteaccess.sessions WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Session{}, application.ErrNotFound
	}
	if err != nil {
		return application.Session{}, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

// ListSessions is keyset pagination over the UUIDv7 id, newest first.
func (r *Repository) ListSessions(ctx context.Context, f application.Filter) (application.Result[application.Session], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.DeviceID != "" {
		add("device_id = $%d::uuid", f.DeviceID)
	}
	if f.TicketID != "" {
		add("ticket_id = $%d::uuid", f.TicketID)
	}
	if f.InitiatedBy != "" {
		add("initiated_by = $%d::uuid", f.InitiatedBy)
	}
	if f.OnlyInitiator != "" {
		add("initiated_by = $%d::uuid", f.OnlyInitiator)
	}
	page := f.Page.Normalize()
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Session]{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM remoteaccess.sessions%s ORDER BY id DESC LIMIT $%d`, cols, where, len(args)), args...)
	if err != nil {
		return application.Result[application.Session]{}, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	items := make([]application.Session, 0, page.Limit+1)
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return application.Result[application.Session]{}, fmt.Errorf("list sessions: scan: %w", err)
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Session]{}, fmt.Errorf("list sessions: %w", err)
	}
	res := application.Result[application.Session]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) LockUserTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('remoteaccess.rate:' || $1::text, 0))`, userID)
	if err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	return nil
}

func (r *Repository) CountAttemptsTx(ctx context.Context, tx pgx.Tx, userID string, since time.Time) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM remoteaccess.request_attempts WHERE user_id = $1::uuid AND created_at > $2`, userID, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count request attempts: %w", err)
	}
	return n, nil
}

func (r *Repository) InsertAttemptTx(ctx context.Context, tx pgx.Tx, userID string, at time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO remoteaccess.request_attempts(user_id, created_at) VALUES ($1::uuid, $2)`, userID, at); err != nil {
		return fmt.Errorf("insert request attempt: %w", err)
	}
	return nil
}

func (r *Repository) PruneAttempts(ctx context.Context, before time.Time) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM remoteaccess.request_attempts WHERE created_at < $1`, before); err != nil {
		return fmt.Errorf("prune request attempts: %w", err)
	}
	return nil
}

func (r *Repository) OpenSessionsOfDeviceTx(ctx context.Context, tx pgx.Tx, deviceID, provider string) ([]application.Session, error) {
	rows, err := tx.Query(ctx, `SELECT `+cols+` FROM remoteaccess.sessions WHERE device_id = $1::uuid AND provider = $2
		AND status IN ('requested', 'pending_approval', 'authorized', 'launched') ORDER BY id FOR UPDATE`, deviceID, provider)
	if err != nil {
		return nil, fmt.Errorf("lock open sessions: %w", err)
	}
	defer rows.Close()
	out := []application.Session{}
	for rows.Next() {
		sess, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("lock open sessions: scan: %w", err)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (r *Repository) listWhere(ctx context.Context, where string, args ...any) ([]application.Session, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+cols+` FROM remoteaccess.sessions WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	out := []application.Session{}
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions: scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) DueSessions(ctx context.Context, now time.Time, limit int) ([]application.Session, error) {
	return r.listWhere(ctx, `status IN ('pending_approval', 'authorized', 'launched') AND expires_at <= $1 ORDER BY expires_at, id LIMIT $2`, now, limit)
}

func (r *Repository) CandidateSessions(ctx context.Context, provider, peerID string, started time.Time) ([]application.Session, error) {
	return r.listWhere(ctx, `provider = $1 AND peer_id = $2 AND status IN ('launched', 'closed') AND launched_at <= $3
		AND (closed_at IS NULL OR closed_at + make_interval(secs => $4) >= $3) ORDER BY id LIMIT 5`,
		provider, peerID, started, application.ObservationSlack.Seconds())
}

// ---- provider records ----

const recCols = `id::text, provider, provider_session_id, peer_id, started_at, ended_at, operator, source, observed_at, session_id::text, flag, first_seen_at`

func scanRecord(row pgx.Row) (application.ProviderRecord, error) {
	var x application.ProviderRecord
	err := row.Scan(&x.ID, &x.Provider, &x.ProviderSessionID, &x.PeerID, &x.StartedAt, &x.EndedAt, &x.Operator, &x.Source, &x.ObservedAt,
		&x.SessionID, &x.Flag, &x.FirstSeenAt)
	return x, err
}

func (r *Repository) UpsertRecordTx(ctx context.Context, tx pgx.Tx, in application.ProviderRecord) (application.ProviderRecord, error) {
	out, err := scanRecord(tx.QueryRow(ctx, `
		INSERT INTO remoteaccess.provider_session_records(provider, provider_session_id, peer_id, started_at, ended_at, operator, source, observed_at, flag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'unattributed')
		ON CONFLICT (provider, provider_session_id) DO NOTHING RETURNING `+recCols,
		in.Provider, in.ProviderSessionID, in.PeerID, in.StartedAt, in.EndedAt, in.Operator, in.Source, in.ObservedAt))
	if err == nil {
		out.New = true
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return application.ProviderRecord{}, fmt.Errorf("insert provider record: %w", err)
	}
	cur, err := scanRecord(tx.QueryRow(ctx, `SELECT `+recCols+` FROM remoteaccess.provider_session_records
		WHERE provider = $1 AND provider_session_id = $2 FOR UPDATE`, in.Provider, in.ProviderSessionID))
	if err != nil {
		return application.ProviderRecord{}, fmt.Errorf("lock provider record: %w", err)
	}
	if cur.PeerID != in.PeerID || !cur.StartedAt.Equal(in.StartedAt.Truncate(time.Microsecond)) || cur.Source != in.Source {
		return application.ProviderRecord{}, application.ErrRecordConflict
	}
	// A later report may add the end or the operator; it never removes them.
	if in.EndedAt == nil {
		in.EndedAt = cur.EndedAt
	}
	if in.Operator == nil {
		in.Operator = cur.Operator
	}
	if equalTime(in.EndedAt, cur.EndedAt) && in.ObservedAt.Truncate(time.Microsecond).Equal(cur.ObservedAt) && equalStr(in.Operator, cur.Operator) {
		return cur, nil
	}
	cur, err = scanRecord(tx.QueryRow(ctx, `UPDATE remoteaccess.provider_session_records SET ended_at = $2, operator = $3, observed_at = $4
		WHERE id = $1::uuid RETURNING `+recCols, cur.ID, in.EndedAt, in.Operator, in.ObservedAt))
	if err != nil {
		return application.ProviderRecord{}, fmt.Errorf("update provider record: %w", err)
	}
	return cur, nil
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

func equalStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (r *Repository) SetRecordAttributionTx(ctx context.Context, tx pgx.Tx, id string, sessionID, flag *string) error {
	if _, err := tx.Exec(ctx, `UPDATE remoteaccess.provider_session_records SET session_id = $2::uuid, flag = $3 WHERE id = $1::uuid`, id, sessionID, flag); err != nil {
		return fmt.Errorf("set provider record attribution: %w", err)
	}
	return nil
}

func (r *Repository) HasAttributedRecordTx(ctx context.Context, tx pgx.Tx, sessionID, exceptRecordID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM remoteaccess.provider_session_records
		WHERE session_id = $1::uuid AND flag IS NULL AND id <> $2::uuid)`, sessionID, exceptRecordID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check attributed record: %w", err)
	}
	return ok, nil
}

func (r *Repository) RecordSummary(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT flag, count(*) FROM remoteaccess.provider_session_records
		WHERE flag IS NOT NULL AND first_seen_at >= $1 GROUP BY flag`, since)
	if err != nil {
		return nil, fmt.Errorf("summarize provider records: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var flag string
		var n int
		if err := rows.Scan(&flag, &n); err != nil {
			return nil, fmt.Errorf("summarize provider records: scan: %w", err)
		}
		out[flag] = n
	}
	return out, rows.Err()
}

// ---- transitions ----

func (r *Repository) InsertTransitionTx(ctx context.Context, tx pgx.Tx, t application.Transition) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO remoteaccess.session_transitions(session_id, from_status, to_status, operation, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		t.SessionID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("insert session transition: %w", err)
	}
	return nil
}

func (r *Repository) Transitions(ctx context.Context, sessionID string, page application.Page) (application.Result[application.Transition], error) {
	page = page.Normalize()
	if !validUUID(sessionID) {
		return application.Result[application.Transition]{Items: []application.Transition{}}, nil
	}
	args := []any{sessionID}
	cond := ""
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Transition]{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond = " AND id > $2::uuid"
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`SELECT id::text, session_id::text, from_status, to_status, operation, reason, actor_user_id::text, actor_system,
		correlation_id, created_at FROM remoteaccess.session_transitions WHERE session_id = $1::uuid%s ORDER BY id LIMIT $%d`, cond, len(args)), args...)
	if err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list session transitions: %w", err)
	}
	defer rows.Close()
	items := []application.Transition{}
	for rows.Next() {
		var t application.Transition
		if err := rows.Scan(&t.ID, &t.SessionID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.ActorUserID, &t.ActorSystem, &t.CorrelationID, &t.CreatedAt); err != nil {
			return application.Result[application.Transition]{}, fmt.Errorf("list session transitions: scan: %w", err)
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Transition]{}, err
	}
	res := application.Result[application.Transition]{Items: items}
	if len(items) > page.Limit {
		res.Items = items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

// ---- launch handles ----

func (r *Repository) InsertHandleTx(ctx context.Context, tx pgx.Tx, h application.Handle, tokenHash []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO remoteaccess.launch_handles(session_id, user_id, token_hash, expires_at) VALUES ($1::uuid, $2::uuid, $3, $4)`,
		h.SessionID, h.UserID, tokenHash, h.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert launch handle: %w", err)
	}
	return nil
}

func (r *Repository) LockHandleTx(ctx context.Context, tx pgx.Tx, tokenHash []byte) (application.Handle, error) {
	var h application.Handle
	err := tx.QueryRow(ctx, `SELECT id::text, session_id::text, user_id::text, expires_at, used_at FROM remoteaccess.launch_handles
		WHERE token_hash = $1 FOR UPDATE`, tokenHash).Scan(&h.ID, &h.SessionID, &h.UserID, &h.ExpiresAt, &h.UsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Handle{}, application.ErrNotFound
	}
	if err != nil {
		return application.Handle{}, fmt.Errorf("lock launch handle: %w", err)
	}
	return h, nil
}

func (r *Repository) MarkHandleUsedTx(ctx context.Context, tx pgx.Tx, id string, at time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE remoteaccess.launch_handles SET used_at = $2 WHERE id = $1::uuid AND used_at IS NULL`, id, at)
	if err != nil {
		return fmt.Errorf("mark launch handle used: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrHandleUsed
	}
	return nil
}

func (r *Repository) LiveHandlesTx(ctx context.Context, tx pgx.Tx, sessionID string, now time.Time) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM remoteaccess.launch_handles WHERE session_id = $1::uuid AND used_at IS NULL AND expires_at > $2`, sessionID, now).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count live launch handles: %w", err)
	}
	return n, nil
}

// ---- peer mappings ----

const mapCols = `id::text, device_id::text, provider, peer_id, source, reason, mapped_by::text, mapped_at, closed_at, closed_by::text, close_reason`

func scanMapping(row pgx.Row) (application.PeerMapping, error) {
	var m application.PeerMapping
	err := row.Scan(&m.ID, &m.DeviceID, &m.Provider, &m.PeerID, &m.Source, &m.Reason, &m.MappedBy, &m.MappedAt, &m.ClosedAt, &m.ClosedBy, &m.CloseReason)
	return m, err
}

func (r *Repository) ActiveMapping(ctx context.Context, deviceID, provider string) (application.PeerMapping, bool, error) {
	if !validUUID(deviceID) {
		return application.PeerMapping{}, false, nil
	}
	m, err := scanMapping(r.pool.QueryRow(ctx, `SELECT `+mapCols+` FROM remoteaccess.peer_mappings WHERE device_id = $1::uuid AND provider = $2 AND closed_at IS NULL`, deviceID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PeerMapping{}, false, nil
	}
	if err != nil {
		return application.PeerMapping{}, false, fmt.Errorf("active peer mapping: %w", err)
	}
	return m, true, nil
}

func (r *Repository) LockActiveMappingTx(ctx context.Context, tx pgx.Tx, deviceID, provider string) (application.PeerMapping, bool, error) {
	m, err := scanMapping(tx.QueryRow(ctx, `SELECT `+mapCols+` FROM remoteaccess.peer_mappings WHERE device_id = $1::uuid AND provider = $2 AND closed_at IS NULL FOR UPDATE`, deviceID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PeerMapping{}, false, nil
	}
	if err != nil {
		return application.PeerMapping{}, false, fmt.Errorf("lock peer mapping: %w", err)
	}
	return m, true, nil
}

func (r *Repository) Mappings(ctx context.Context, deviceID string, includeClosed bool) ([]application.PeerMapping, error) {
	if !validUUID(deviceID) {
		return []application.PeerMapping{}, nil
	}
	cond := " AND closed_at IS NULL"
	if includeClosed {
		cond = ""
	}
	rows, err := r.pool.Query(ctx, `SELECT `+mapCols+` FROM remoteaccess.peer_mappings WHERE device_id = $1::uuid`+cond+` ORDER BY id DESC LIMIT 200`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list peer mappings: %w", err)
	}
	defer rows.Close()
	out := []application.PeerMapping{}
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, fmt.Errorf("list peer mappings: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repository) InsertMappingTx(ctx context.Context, tx pgx.Tx, m application.PeerMapping) (application.PeerMapping, error) {
	out, err := scanMapping(tx.QueryRow(ctx, `
		INSERT INTO remoteaccess.peer_mappings(device_id, provider, peer_id, source, reason, mapped_by, mapped_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7) RETURNING `+mapCols,
		m.DeviceID, m.Provider, m.PeerID, m.Source, m.Reason, m.MappedBy, m.MappedAt))
	switch constraint(err) {
	case "peer_mappings_peer_active":
		return application.PeerMapping{}, application.ErrPeerTaken
	case "peer_mappings_device_active":
		return application.PeerMapping{}, application.ErrVersionConflict
	}
	if err != nil {
		return application.PeerMapping{}, fmt.Errorf("insert peer mapping: %w", err)
	}
	return out, nil
}

func (r *Repository) CloseMappingTx(ctx context.Context, tx pgx.Tx, id string, closedBy *string, reason string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE remoteaccess.peer_mappings SET closed_at = $2, closed_by = $3::uuid, close_reason = $4 WHERE id = $1::uuid AND closed_at IS NULL`,
		id, at, closedBy, reason)
	if err != nil {
		return fmt.Errorf("close peer mapping: %w", err)
	}
	return nil
}
