package modules

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// PermManage is the permission to see the module overview and switch optional modules.
const PermManage = "modules.manage"

// States of a module in the overview.
const (
	StateEnabled  = "enabled"
	StateDisabled = "disabled"
	// StateBlocked means the switch is on but a precondition of the module is not met; it behaves as disabled.
	StateBlocked = "blocked"
)

// Reason codes an administrator gives for a switch (and the code the upgrade migration used).
var reasonCodes = []string{"initial_setup", "business_need", "not_needed", "maintenance", "compliance_review", "evaluation"}

// Blocked reason codes that are not module-specific. Module preconditions return their own codes
// (for example startup_gate_off, dpia_not_recorded, no_enabled_provider, no_providers_configured).
const BlockedDependencyDisabled = "dependency_disabled"

// Errors of the lifecycle operations.
var (
	ErrForbidden       = errors.New("modules: not permitted")
	ErrNotFound        = errors.New("modules: unknown module")
	ErrNotSwitchable   = errors.New("modules: core modules cannot be switched")
	ErrVersionConflict = errors.New("modules: version conflict")
	ErrNoChange        = errors.New("modules: the module is already in the requested state")
	// ErrModuleDisabled is what a public contract of a disabled module returns to other modules, and what
	// Service.Require returns.
	ErrModuleDisabled = errors.New("modules: module is not enabled")
)

// InvalidError is a rejected input.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// BlockedError refuses enabling a module whose own precondition is not met. The switch never bypasses it.
type BlockedError struct{ Key, Reason string }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("modules: %s is blocked: %s", e.Key, e.Reason)
}

// DependencyError refuses a switch because of module dependencies. For an enable, Blockers are the required
// modules that are off; for a disable, the enabled modules that require the module.
type DependencyError struct {
	Key      string
	Enabling bool
	Blockers []string
}

func (e *DependencyError) Error() string {
	if e.Enabling {
		return fmt.Sprintf("modules: %s requires disabled modules: %s", e.Key, strings.Join(e.Blockers, ", "))
	}
	return fmt.Sprintf("modules: %s is required by enabled modules: %s", e.Key, strings.Join(e.Blockers, ", "))
}

// Precondition reports why a module cannot be on: an empty code means satisfied. It must be cheap and read-only.
type Precondition func(ctx context.Context) (blockedCode string, err error)

// Caller is the authorized actor of a lifecycle operation or read.
type Caller struct {
	UserID        string
	CorrelationID string
	// CanManage is the effective modules.manage permission.
	CanManage bool
}

// Info is the state of one module.
type Info struct {
	Module        Module
	Enabled       bool // effective: switch on and nothing blocking
	SwitchOn      bool // position of the switch itself
	State         string
	BlockedReason string
	Version       int
	ChangedAt     *time.Time
	ChangedBy     *string
	ReasonCode    string
}

// Row is a stored switch.
type Row struct {
	Key        string
	Enabled    bool
	Version    int
	ReasonCode string
	UpdatedBy  *string
	UpdatedAt  time.Time
}

// Options configures a Service.
type Options struct {
	// Index defaults to DefaultIndex().
	Index *Index
	// Preconditions maps a module key to its own precondition check.
	Preconditions map[string]Precondition
	// CacheTTL bounds how stale another process's change may be; default 2s. A change made through this Service is
	// visible to it immediately.
	CacheTTL time.Duration
	Now      func() time.Time
}

// Service is the module registry.
type Service struct {
	pool *pgxpool.Pool
	ix   *Index
	pre  map[string]Precondition
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	cache    map[string]Info
	cachedAt time.Time
}

// NewService creates the registry over the platform database.
func NewService(pool *pgxpool.Pool, opts Options) *Service {
	if opts.Index == nil {
		opts.Index = DefaultIndex()
	}
	if opts.CacheTTL == 0 {
		opts.CacheTTL = 2 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{pool: pool, ix: opts.Index, pre: opts.Preconditions, ttl: opts.CacheTTL, now: opts.Now}
}

// Index returns the catalog index.
func (s *Service) Index() *Index { return s.ix }

func (s *Service) loadRows(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[string]Row, error) {
	rows, err := q.Query(ctx, `SELECT module_key, enabled, version, reason_code, updated_by::text, updated_at FROM platform.module_switches`)
	if err != nil {
		return nil, fmt.Errorf("list module switches: %w", err)
	}
	defer rows.Close()
	out := map[string]Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Key, &r.Enabled, &r.Version, &r.ReasonCode, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan module switch: %w", err)
		}
		out[r.Key] = r
	}
	return out, rows.Err()
}

// switchOn is the switch of a module: core modules are always on, others follow the stored row or the default.
func (s *Service) switchOn(m Module, rows map[string]Row) bool {
	if m.Core {
		return true
	}
	if r, ok := rows[m.Key]; ok {
		return r.Enabled
	}
	return m.DefaultEnabled
}

func (s *Service) compute(ctx context.Context) (map[string]Info, error) {
	rows, err := s.loadRows(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Info, len(s.ix.list))
	for _, m := range s.ix.list {
		in := Info{Module: m, State: StateEnabled}
		on := s.switchOn(m, rows)
		in.SwitchOn = on
		if r, ok := rows[m.Key]; ok {
			t, by := r.UpdatedAt, r.UpdatedBy
			in.Version, in.ChangedAt, in.ChangedBy, in.ReasonCode = r.Version, &t, by, r.ReasonCode
		}
		if !m.Core {
			if pre := s.pre[m.Key]; pre != nil {
				code, err := pre(ctx)
				if err != nil {
					return nil, fmt.Errorf("precondition of module %s: %w", m.Key, err)
				}
				in.BlockedReason = code
			}
			if in.BlockedReason == "" {
				for _, req := range m.Requires {
					if !s.switchOn(s.ix.byKey[req], rows) {
						in.BlockedReason = BlockedDependencyDisabled
						break
					}
				}
			}
		}
		switch {
		case !on:
			in.State = StateDisabled
		case in.BlockedReason != "":
			in.State = StateBlocked
		}
		in.Enabled = in.State == StateEnabled
		out[m.Key] = in
	}
	return out, nil
}

func (s *Service) snapshot(ctx context.Context) (map[string]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && s.now().Sub(s.cachedAt) < s.ttl {
		return s.cache, nil
	}
	m, err := s.compute(ctx)
	if err != nil {
		return nil, err
	}
	s.cache, s.cachedAt = m, s.now()
	return m, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// Enabled reports whether a module is effectively on. Unknown keys are off; core modules are on. A database
// error is returned so callers fail closed.
func (s *Service) Enabled(ctx context.Context, key string) (bool, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return false, err
	}
	in, ok := snap[key]
	return ok && in.Enabled, nil
}

// Require returns ErrModuleDisabled unless the module is effectively on. Modules that call another module's
// public contract use it (through wiring) to degrade gracefully instead of reading hidden data.
func (s *Service) Require(ctx context.Context, key string) error {
	on, err := s.Enabled(ctx, key)
	if err != nil {
		return err
	}
	if !on {
		return ErrModuleDisabled
	}
	return nil
}

// Status is what every signed-in User may see: only which modules are effectively on.
func (s *Service) Status(ctx context.Context) ([]Info, error) {
	return s.list(ctx)
}

func (s *Service) list(ctx context.Context) ([]Info, error) {
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(s.ix.list))
	for _, m := range s.ix.list {
		out = append(out, snap[m.Key])
	}
	return out, nil
}

// List returns the overview of all modules. Needs modules.manage.
func (s *Service) List(ctx context.Context, c Caller) ([]Info, error) {
	if c.UserID == "" || !c.CanManage {
		return nil, ErrForbidden
	}
	// Fresh read: an administrator looks at the truth, not at a cache.
	s.invalidate()
	return s.list(ctx)
}

// Enable switches an optional module on. Needs modules.manage. It refuses while a required module is off
// (DependencyError) or while the module's own precondition is not met (BlockedError).
func (s *Service) Enable(ctx context.Context, c Caller, key, reasonCode string, expectedVersion *int) (Info, error) {
	return s.change(ctx, c, key, reasonCode, expectedVersion, true)
}

// Disable switches an optional module off. Needs modules.manage. It refuses while an enabled module requires it.
// No data is deleted.
func (s *Service) Disable(ctx context.Context, c Caller, key, reasonCode string, expectedVersion *int) (Info, error) {
	return s.change(ctx, c, key, reasonCode, expectedVersion, false)
}

func (s *Service) change(ctx context.Context, c Caller, key, reasonCode string, expectedVersion *int, enable bool) (Info, error) {
	if c.UserID == "" || !c.CanManage {
		return Info{}, ErrForbidden
	}
	m, ok := s.ix.Get(key)
	if !ok {
		return Info{}, ErrNotFound
	}
	if m.Core {
		return Info{}, ErrNotSwitchable
	}
	if !slices.Contains(reasonCodes, reasonCode) {
		return Info{}, &InvalidError{Message: "reasonCode must be one of " + strings.Join(reasonCodes, ", ")}
	}
	if expectedVersion == nil || *expectedVersion < 0 {
		return Info{}, &InvalidError{Message: "expectedVersion is required"}
	}
	if enable {
		if pre := s.pre[key]; pre != nil {
			code, err := pre(ctx)
			if err != nil {
				return Info{}, fmt.Errorf("precondition of module %s: %w", key, err)
			}
			if code != "" {
				return Info{}, &BlockedError{Key: key, Reason: code}
			}
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One lock serializes all switch changes, so the dependency check cannot race with another change.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('platform.module_switches'))`); err != nil {
			return fmt.Errorf("lock module switches: %w", err)
		}
		rows, err := s.loadRows(ctx, tx)
		if err != nil {
			return err
		}
		cur := rows[key]
		if cur.Version != *expectedVersion {
			return ErrVersionConflict
		}
		if s.switchOn(m, rows) == enable {
			return ErrNoChange
		}
		var blockers []string
		if enable {
			for _, req := range m.Requires {
				if !s.switchOn(s.ix.byKey[req], rows) {
					blockers = append(blockers, req)
				}
			}
		} else {
			for _, dep := range s.ix.RequiredBy(key) {
				if s.switchOn(s.ix.byKey[dep], rows) {
					blockers = append(blockers, dep)
				}
			}
		}
		if len(blockers) > 0 {
			return &DependencyError{Key: key, Enabling: enable, Blockers: blockers}
		}
		before := map[string]any{"enabled": s.switchOn(m, rows), "version": cur.Version}
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.module_switches (module_key, enabled, version, reason_code, updated_by, updated_at)
			VALUES ($1, $2, 1, $3, $4::uuid, now())
			ON CONFLICT (module_key) DO UPDATE
			SET enabled = EXCLUDED.enabled, version = platform.module_switches.version + 1,
			    reason_code = EXCLUDED.reason_code, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			key, enable, reasonCode, c.UserID); err != nil {
			return fmt.Errorf("write module switch: %w", err)
		}
		action := "platform.module.disabled"
		if enable {
			action = "platform.module.enabled"
		}
		return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "module", TargetID: key, Actor: audit.UserActor(c.UserID),
			CorrelationID: c.CorrelationID, Before: before, After: map[string]any{"enabled": enable, "version": cur.Version + 1},
			Metadata: map[string]any{"reasonCode": reasonCode}})
	})
	s.invalidate()
	if err != nil {
		return Info{}, err
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Info{}, err
	}
	return snap[key], nil
}
