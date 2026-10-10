package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

var (
	// ErrForbidden means the caller may not change settings (platform.admin).
	ErrForbidden = errors.New("settings: not permitted")
	// ErrUnknownKey marks a key without a definition.
	ErrUnknownKey = errors.New("settings: unknown setting")
	// ErrInvalidValue marks a value that does not match the type, options or bounds.
	ErrInvalidValue = errors.New("settings: invalid value")
	// ErrVersionRequired marks a write without expectedVersion.
	ErrVersionRequired = errors.New("settings: expectedVersion is required")
	// ErrConflict is a version mismatch.
	ErrConflict = errors.New("settings: version conflict")
)

// DefaultCacheTTL bounds how long another API instance may serve an older value after a change.
const DefaultCacheTTL = 10 * time.Second

// Setting is one setting as shown: its definition, the effective value and the stored state.
type Setting struct {
	Definition
	// Value is the effective value: the stored one, else the default.
	Value     any        `json:"value"`
	Stored    bool       `json:"stored"`
	Version   int        `json:"version"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
}

type row struct {
	value     any
	version   int
	updatedBy string
	updatedAt time.Time
}

// Service reads and writes settings. Reads are cached for a short time; on a database error the last known values
// (or the code defaults) are served so a failing read never blocks sign-in or other callers.
type Service struct {
	pool *pgxpool.Pool
	defs map[string]Definition
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	cache    map[string]row
	loadedAt time.Time
	loaded   bool
}

// New returns the service. A zero ttl uses DefaultCacheTTL.
func New(pool *pgxpool.Pool, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	defs := map[string]Definition{}
	for _, d := range Definitions() {
		defs[d.Key] = d
	}
	return &Service{pool: pool, defs: defs, ttl: ttl, now: time.Now}
}

// normalize validates raw JSON against the definition and returns the canonical Go value (bool, int64 or string).
func normalize(d Definition, raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, ErrInvalidValue
	}
	switch d.Type {
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, ErrInvalidValue
		}
		return b, nil
	case TypeInt, TypeDuration:
		n, ok := v.(json.Number)
		if !ok {
			return nil, ErrInvalidValue
		}
		i, err := n.Int64()
		if err != nil {
			return nil, ErrInvalidValue
		}
		if (d.Min != nil && i < *d.Min) || (d.Max != nil && i > *d.Max) {
			return nil, ErrInvalidValue
		}
		return i, nil
	case TypeEnum:
		s, ok := v.(string)
		if !ok {
			return nil, ErrInvalidValue
		}
		for _, o := range d.Options {
			if o == s {
				return s, nil
			}
		}
		return nil, ErrInvalidValue
	}
	return nil, ErrInvalidValue
}

// load returns the stored rows, using the cache while it is fresh. Invalid stored values are skipped (the default
// applies); a read error serves the previous cache.
func (s *Service) load(ctx context.Context) map[string]row {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.now().Sub(s.loadedAt) < s.ttl {
		return s.cache
	}
	fresh, err := s.read(ctx)
	if err != nil {
		if !s.loaded {
			s.cache = map[string]row{}
		}
		// Retry after one ttl rather than on every call.
		s.loadedAt, s.loaded = s.now(), true
		return s.cache
	}
	s.cache, s.loadedAt, s.loaded = fresh, s.now(), true
	return s.cache
}

func (s *Service) read(ctx context.Context) (map[string]row, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value, version, updated_by::text, updated_at FROM platform.settings`)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()
	out := map[string]row{}
	for rows.Next() {
		var key string
		var raw []byte
		var r row
		if err := rows.Scan(&key, &raw, &r.version, &r.updatedBy, &r.updatedAt); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
		d, ok := s.defs[key]
		if !ok {
			continue
		}
		v, err := normalize(d, raw)
		if err != nil {
			continue
		}
		r.value = v
		out[key] = r
	}
	return out, rows.Err()
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()
}

func view(d Definition, r row, stored bool) Setting {
	out := Setting{Definition: d, Value: d.Default}
	if stored {
		t := r.updatedAt
		out.Value, out.Stored, out.Version, out.UpdatedAt, out.UpdatedBy = r.value, true, r.version, &t, r.updatedBy
	}
	return out
}

// List returns every setting in definition order with its effective value.
func (s *Service) List(ctx context.Context) []Setting {
	rows := s.load(ctx)
	defs := Definitions()
	out := make([]Setting, 0, len(defs))
	for _, d := range defs {
		r, ok := rows[d.Key]
		out = append(out, view(d, r, ok))
	}
	return out
}

// Stored returns the administrator-set value, if any. Callers with an older fallback (an environment value) use it:
// ok is false when the setting was never set.
func (s *Service) Stored(ctx context.Context, key string) (any, bool) {
	r, ok := s.load(ctx)[key]
	if !ok {
		return nil, false
	}
	return r.value, true
}

func (s *Service) value(ctx context.Context, key string) any {
	if v, ok := s.Stored(ctx, key); ok {
		return v
	}
	return s.defs[key].Default
}

// Bool returns the effective value of a bool setting (false for an unknown key or other type).
func (s *Service) Bool(ctx context.Context, key string) bool {
	b, _ := s.value(ctx, key).(bool)
	return b
}

// Int returns the effective value of an int setting.
func (s *Service) Int(ctx context.Context, key string) int64 {
	i, _ := s.value(ctx, key).(int64)
	return i
}

// Duration returns the effective value of a duration setting.
func (s *Service) Duration(ctx context.Context, key string) time.Duration {
	return time.Duration(s.Int(ctx, key)) * time.Second
}

// Enum returns the effective value of an enum setting.
func (s *Service) Enum(ctx context.Context, key string) string {
	str, _ := s.value(ctx, key).(string)
	return str
}

// StoredDuration returns the administrator-set duration, if any.
func (s *Service) StoredDuration(ctx context.Context, key string) (time.Duration, bool) {
	v, ok := s.Stored(ctx, key)
	if !ok {
		return 0, false
	}
	i, ok := v.(int64)
	if !ok {
		return 0, false
	}
	return time.Duration(i) * time.Second, true
}

// Set changes one setting. It needs platform.admin (canAdmin), requires expectedVersion (0 for a setting that was
// never set) and is audited in the same transaction.
func (s *Service) Set(ctx context.Context, userID string, canAdmin bool, key string, raw json.RawMessage, expectedVersion *int, correlationID string) (Setting, error) {
	if userID == "" || !canAdmin {
		return Setting{}, ErrForbidden
	}
	d, ok := s.defs[key]
	if !ok {
		return Setting{}, ErrUnknownKey
	}
	if expectedVersion == nil || *expectedVersion < 0 {
		return Setting{}, ErrVersionRequired
	}
	v, err := normalize(d, raw)
	if err != nil {
		return Setting{}, err
	}
	if correlationID == "" {
		correlationID = "settings-" + key
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return Setting{}, err
	}
	var out row
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		var prevRaw []byte
		err := tx.QueryRow(ctx, `SELECT version, value FROM platform.settings WHERE key = $1 FOR UPDATE`, key).Scan(&cur, &prevRaw)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if (exists && *expectedVersion != cur) || (!exists && *expectedVersion != 0) {
			return ErrConflict
		}
		before := map[string]any{"value": d.Default, "stored": false}
		if exists {
			var pv any
			_ = json.Unmarshal(prevRaw, &pv)
			before = map[string]any{"value": pv, "stored": true, "version": cur}
		}
		if exists {
			err = tx.QueryRow(ctx, `UPDATE platform.settings SET value = $2::jsonb, updated_by = $3::uuid, updated_at = now(), version = version + 1
				WHERE key = $1 RETURNING version, updated_by::text, updated_at`, key, string(encoded), userID).Scan(&out.version, &out.updatedBy, &out.updatedAt)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO platform.settings (key, value, updated_by) VALUES ($1, $2::jsonb, $3::uuid)
				RETURNING version, updated_by::text, updated_at`, key, string(encoded), userID).Scan(&out.version, &out.updatedBy, &out.updatedAt)
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "platform.settings.changed", TargetType: "setting", TargetID: key,
			Actor: audit.UserActor(userID), CorrelationID: correlationID,
			Before: before, After: map[string]any{"value": v, "stored": true, "version": out.version}})
	})
	if err != nil {
		return Setting{}, err
	}
	s.invalidate()
	out.value = v
	return view(d, out, true), nil
}
