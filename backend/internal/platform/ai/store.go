package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL storage of the ai schema. It is used only by this package.
type Store struct{ pool *pgxpool.Pool }

// NewStore returns the store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// ---- settings ----

// Settings is the single ai.settings row.
type Settings struct {
	Enabled                  bool
	RetainConversations      bool
	RetentionDays            int
	UserRequestsPerHour      int
	UserRequestsPerDay       int
	UserTokensPerDay         int
	InstallationTokensPerDay int
	MaxOutputTokens          int
	MaxToolIterations        int
	UpdatedBy                *string
	UpdatedAt                time.Time
	Version                  int
}

const settingsCols = `enabled, retain_conversations, retention_days, user_requests_per_hour, user_requests_per_day,
	user_tokens_per_day, installation_tokens_per_day, max_output_tokens, max_tool_iterations, updated_by::text, updated_at, version`

func scanSettings(row pgx.Row) (Settings, error) {
	var st Settings
	var days, iters int16
	err := row.Scan(&st.Enabled, &st.RetainConversations, &days, &st.UserRequestsPerHour, &st.UserRequestsPerDay,
		&st.UserTokensPerDay, &st.InstallationTokensPerDay, &st.MaxOutputTokens, &iters, &st.UpdatedBy, &st.UpdatedAt, &st.Version)
	st.RetentionDays, st.MaxToolIterations = int(days), int(iters)
	return st, err
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	st, err := scanSettings(s.pool.QueryRow(ctx, `SELECT `+settingsCols+` FROM ai.settings WHERE singleton`))
	if err != nil {
		return Settings{}, fmt.Errorf("get ai settings: %w", err)
	}
	return st, nil
}

func (s *Store) lockSettings(ctx context.Context, tx pgx.Tx) (Settings, error) {
	st, err := scanSettings(tx.QueryRow(ctx, `SELECT `+settingsCols+` FROM ai.settings WHERE singleton FOR UPDATE`))
	if err != nil {
		return Settings{}, fmt.Errorf("lock ai settings: %w", err)
	}
	return st, nil
}

func (s *Store) updateSettings(ctx context.Context, tx pgx.Tx, in Settings, actor string) (Settings, error) {
	st, err := scanSettings(tx.QueryRow(ctx, `UPDATE ai.settings SET enabled=$1, retain_conversations=$2, retention_days=$3,
		user_requests_per_hour=$4, user_requests_per_day=$5, user_tokens_per_day=$6, installation_tokens_per_day=$7,
		max_output_tokens=$8, max_tool_iterations=$9, updated_by=$10, updated_at=now(), version=version+1
		WHERE singleton RETURNING `+settingsCols,
		in.Enabled, in.RetainConversations, in.RetentionDays, in.UserRequestsPerHour, in.UserRequestsPerDay, in.UserTokensPerDay,
		in.InstallationTokensPerDay, in.MaxOutputTokens, in.MaxToolIterations, actor))
	if err != nil {
		return Settings{}, fmt.Errorf("update ai settings: %w", err)
	}
	return st, nil
}

// ---- providers ----

const providerCols = `id::text, kind, display_name, endpoint_url, model, local, allowed_data_classes, dpa_recorded_on::text,
	no_training_confirmed, region, secret_ref, enabled, price_in_per_mtok::float8, price_out_per_mtok::float8, version,
	created_at::text, updated_at::text`

func scanProvider(row pgx.Row) (ProviderRecord, error) {
	var p ProviderRecord
	var classes []string
	err := row.Scan(&p.ID, &p.Kind, &p.DisplayName, &p.EndpointURL, &p.Model, &p.Local, &classes, &p.DPARecordedOn,
		&p.NoTrainingConfirm, &p.Region, &p.SecretRef, &p.Enabled, &p.PriceInPerMTok, &p.PriceOutPerMTok, &p.Version,
		&p.CreatedAt, &p.UpdatedAt)
	for _, c := range classes {
		p.AllowedDataClasses = append(p.AllowedDataClasses, DataClass(c))
	}
	if p.AllowedDataClasses == nil {
		p.AllowedDataClasses = []DataClass{}
	}
	return p, err
}

func (s *Store) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+providerCols+` FROM ai.providers ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list ai providers: %w", err)
	}
	defer rows.Close()
	out := []ProviderRecord{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ai provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProvider(ctx context.Context, id string) (ProviderRecord, error) {
	p, err := scanProvider(s.pool.QueryRow(ctx, `SELECT `+providerCols+` FROM ai.providers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderRecord{}, ErrNotFound
	}
	return p, err
}

// ActiveProvider returns the enabled provider, or ErrNotFound.
func (s *Store) ActiveProvider(ctx context.Context) (ProviderRecord, error) {
	p, err := scanProvider(s.pool.QueryRow(ctx, `SELECT `+providerCols+` FROM ai.providers WHERE enabled`))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderRecord{}, ErrNotFound
	}
	return p, err
}

func classStrings(in []DataClass) []string {
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = string(c)
	}
	return out
}

func (s *Store) insertProvider(ctx context.Context, tx pgx.Tx, p ProviderRecord, actor string) (ProviderRecord, error) {
	return scanProvider(tx.QueryRow(ctx, `INSERT INTO ai.providers (kind, display_name, endpoint_url, model, local, allowed_data_classes,
		dpa_recorded_on, no_training_confirmed, region, secret_ref, enabled, price_in_per_mtok, price_out_per_mtok, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::date,$8,$9,$10,$11,$12,$13,$14,$14) RETURNING `+providerCols,
		p.Kind, p.DisplayName, p.EndpointURL, p.Model, p.Local, classStrings(p.AllowedDataClasses), p.DPARecordedOn,
		p.NoTrainingConfirm, p.Region, p.SecretRef, p.Enabled, p.PriceInPerMTok, p.PriceOutPerMTok, actor))
}

func (s *Store) updateProvider(ctx context.Context, tx pgx.Tx, id string, expected int, p ProviderRecord, actor string) (ProviderRecord, error) {
	out, err := scanProvider(tx.QueryRow(ctx, `UPDATE ai.providers SET display_name=$3, endpoint_url=$4, model=$5, local=$6,
		allowed_data_classes=$7, dpa_recorded_on=$8::date, no_training_confirmed=$9, region=$10, secret_ref=$11, enabled=$12,
		price_in_per_mtok=$13, price_out_per_mtok=$14, updated_by=$15, updated_at=now(), version=version+1
		WHERE id=$1 AND version=$2 RETURNING `+providerCols,
		id, expected, p.DisplayName, p.EndpointURL, p.Model, p.Local, classStrings(p.AllowedDataClasses), p.DPARecordedOn,
		p.NoTrainingConfirm, p.Region, p.SecretRef, p.Enabled, p.PriceInPerMTok, p.PriceOutPerMTok, actor))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderRecord{}, ErrVersionConflict
	}
	return out, err
}

// ---- sessions ----

type session struct {
	ID            string
	TenantID      string
	UserID        string
	AuthSessionID string
	ProviderID    string
	Transcript    []Message
	Scope         []ResourceRef
	TurnCount     int
	Version       int
	RetentionID   *string
	CreatedAt     time.Time
}

const sessionCols = `id::text, tenant_id, user_id::text, auth_session_id::text, provider_id::text, transcript, scope, turn_count, version, retention_id::text, created_at`

func scanSession(row pgx.Row) (session, error) {
	var se session
	var tr, sc []byte
	if err := row.Scan(&se.ID, &se.TenantID, &se.UserID, &se.AuthSessionID, &se.ProviderID, &tr, &sc, &se.TurnCount, &se.Version, &se.RetentionID, &se.CreatedAt); err != nil {
		return session{}, err
	}
	if err := json.Unmarshal(tr, &se.Transcript); err != nil {
		return session{}, fmt.Errorf("decode transcript: %w", err)
	}
	if err := json.Unmarshal(sc, &se.Scope); err != nil {
		return session{}, fmt.Errorf("decode scope: %w", err)
	}
	return se, nil
}

const (
	sessionIdle = 30 * time.Minute
	sessionMax  = 8 * time.Hour
	// turnLease bounds how long a crashed turn blocks its conversation.
	turnLease = 2 * time.Minute
)

func (s *Store) createSession(ctx context.Context, tokenHash []byte, c Caller, providerID string) (session, error) {
	return scanSession(s.pool.QueryRow(ctx, `INSERT INTO ai.sessions (token_hash, tenant_id, user_id, auth_session_id, provider_id, expires_at)
		VALUES ($1,$2,$3,$4,$5, now() + $6::interval) RETURNING `+sessionCols,
		tokenHash, c.TenantID, c.UserID, c.SessionID, providerID, fmt.Sprintf("%d seconds", int(sessionIdle.Seconds()))))
}

// findSession returns the live session bound to this exact tenant, User and authentication session (A12).
// Anything else, including another User's valid id, is ErrNotFound.
func (s *Store) findSession(ctx context.Context, tokenHash []byte, c Caller) (session, error) {
	se, err := scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM ai.sessions
		WHERE token_hash=$1 AND tenant_id=$2 AND user_id=$3 AND auth_session_id=$4 AND expires_at > now()`,
		tokenHash, c.TenantID, c.UserID, c.SessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return session{}, ErrNotFound
	}
	return se, err
}

// acquireTurn marks the conversation busy; false means another turn is running.
func (s *Store) acquireTurn(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE ai.sessions SET busy_until = now() + $2::interval
		WHERE id=$1 AND expires_at > now() AND (busy_until IS NULL OR busy_until <= now())`, id, fmt.Sprintf("%d seconds", int(turnLease.Seconds())))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) releaseTurn(ctx context.Context, id string) {
	// Best effort with its own context: the turn may have been cancelled.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.pool.Exec(c, `UPDATE ai.sessions SET busy_until = NULL WHERE id=$1`, id)
}

func (s *Store) saveTurn(ctx context.Context, se session) error {
	tr, err := json.Marshal(se.Transcript)
	if err != nil {
		return err
	}
	sc, err := json.Marshal(se.Scope)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ai.sessions SET transcript=$3, scope=$4, turn_count=turn_count+1, version=version+1,
		busy_until=NULL, last_active_at=now(), retention_id=COALESCE($7::uuid, retention_id),
		expires_at = LEAST(now() + $5::interval, created_at + $6::interval)
		WHERE id=$1 AND version=$2`, se.ID, se.Version, tr, sc,
		fmt.Sprintf("%d seconds", int(sessionIdle.Seconds())), fmt.Sprintf("%d seconds", int(sessionMax.Seconds())), se.RetentionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

func (s *Store) saveScope(ctx context.Context, se session) error {
	sc, err := json.Marshal(se.Scope)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ai.sessions SET scope=$2, version=version+1 WHERE id=$1 AND (busy_until IS NULL OR busy_until <= now())`, se.ID, sc)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrTurnInProgress
	}
	return nil
}

func (s *Store) deleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ai.sessions WHERE id=$1`, id)
	return err
}

// ExpireSessions deletes expired sessions and sessions whose authentication session ended (logout, revocation).
func (s *Store) ExpireSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ai.sessions a WHERE a.expires_at <= now()
		OR NOT EXISTS (SELECT 1 FROM platform.sessions p WHERE p.id = a.auth_session_id AND p.revoked_at IS NULL)`)
	if err != nil {
		return 0, fmt.Errorf("expire ai sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---- retained transcript (only while retain_conversations is on) ----

func (s *Store) retain(ctx context.Context, c Caller, providerID string, existing string, turnSeq int, user, assistant string, days int) (string, error) {
	id := existing
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		interval := fmt.Sprintf("%d days", days)
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO ai.conversations (tenant_id, user_id, provider_id, expires_at)
				VALUES ($1,$2,$3, now() + $4::interval) RETURNING id::text`, c.TenantID, c.UserID, providerID, interval).Scan(&id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE ai.conversations SET expires_at = now() + $2::interval WHERE id=$1`, id, interval); err != nil {
			return err
		}
		for i, m := range []struct{ role, text string }{{"user", user}, {"assistant", assistant}} {
			if m.text == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ai.messages (conversation_id, seq, role, content, expires_at)
				VALUES ($1,$2,$3,$4, now() + $5::interval)`, id, turnSeq*2+i, m.role, m.text, interval); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// PurgeRetention deletes expired retained conversations and usage rows older than keepUsageDays.
func (s *Store) PurgeRetention(ctx context.Context, keepUsageDays int) (conversations, messages, usageRows int64, err error) {
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `DELETE FROM ai.messages WHERE expires_at <= now()`)
		if e != nil {
			return e
		}
		messages = tag.RowsAffected()
		tag, e = tx.Exec(ctx, `DELETE FROM ai.conversations c WHERE c.expires_at <= now()
			OR NOT EXISTS (SELECT 1 FROM ai.messages m WHERE m.conversation_id = c.id)`)
		if e != nil {
			return e
		}
		conversations = tag.RowsAffected()
		cutoff := fmt.Sprintf("%d days", keepUsageDays)
		for _, q := range []string{
			`DELETE FROM ai.usage WHERE day < (now() - $1::interval)::date`,
			`DELETE FROM ai.installation_usage WHERE day < (now() - $1::interval)::date`,
			`DELETE FROM ai.usage_hours WHERE hour < now() - $1::interval`,
		} {
			tag, e = tx.Exec(ctx, q, cutoff)
			if e != nil {
				return e
			}
			usageRows += tag.RowsAffected()
		}
		return nil
	})
	return
}
