// Package application implements Workforce Presence (F11, ADR-0028): Presence Entries, the derived Operational
// Availability and Team Coverage read models, per-Team minimums, settings with the kill switch, and retention.
//
// Privacy is structural. There is no absence reason or free text anywhere; availability is derived at read time,
// never stored per person; reads are limited to a window of at most MaxWindow days; and every read applies the
// viewer's scope (the viewer and the current members of the viewer's Teams) in the application and again in the
// repository query.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
)

// Permissions (docs/reference/permissions.md).
const (
	PermManageOwn        = "presence.manage_own"
	PermViewAvailability = "presence.view_availability"
	PermViewEntries      = "presence.view_entries"
	PermManageEntries    = "presence.manage_entries"
	PermManageTeams      = "presence.manage_teams"
	PermAdmin            = "presence.admin"
)

// Entry kinds and locations. There is deliberately no third kind and no reason: every absence is "unavailable".
const (
	KindWorkLocation = "work_location"
	KindUnavailable  = "unavailable"

	LocationLocation   = "location"
	LocationRemote     = "remote"
	LocationTravelling = "travelling"

	SourceManual = "manual"

	StatusActive    = "active"
	StatusCancelled = "cancelled"

	VisibilityAvailability = "availability"
	VisibilityDetail       = "detail"
)

// Operational Availability values.
const (
	Available   = "available"
	Limited     = "limited"
	Unavailable = "unavailable"
	Unknown     = "unknown"
)

// Explanation codes of a derived value.
const (
	ExplainUnavailable    = "unavailable_entry"
	ExplainLocation       = "work_location"
	ExplainRemoteNotOnsit = "remote_when_onsite_needed"
	ExplainTravelling     = "travelling"
	ExplainNoEntry        = "no_entry"
	ExplainSourceStale    = "source_stale"
	ExplainOutOfScope     = "out_of_scope"
	ExplainDisabled       = "disabled"
)

// Limits (ADR-0028, design W7/W8).
const (
	MaxWindow          = 31 * 24 * time.Hour
	MaxSpan            = 31 * 24 * time.Hour
	MaxRecurrenceDays  = 366
	MaxActiveEntries   = 200
	MaxAvailabilityIDs = 50
	// MinCoverageMembers is the Team size below which counts would reveal one person; such a Team reports unknown.
	MinCoverageMembers = 2
	// PastWindowSlack is how far into the past a read window or a new entry may reach.
	PastWindowSlack = 24 * time.Hour
	DefaultStale    = 24 * time.Hour

	PurgeJobType    = "presence.purge"
	PurgeJobTimeout = 2 * time.Minute
	PurgeInterval   = 24 * time.Hour

	EventEntryChanged = "PresenceEntryChanged"
)

var (
	ErrNotFound        = errors.New("presence: not found")
	ErrForbidden       = errors.New("presence: not permitted")
	ErrVersionConflict = errors.New("presence: version conflict")
	ErrDisabled        = errors.New("presence: disabled")
	ErrReadOnly        = errors.New("presence: externally sourced entries are read-only")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "presence: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// InvalidRecurrenceError is a rejected recurrence rule (presence.invalid_recurrence).
type InvalidRecurrenceError struct{ Message string }

func (e *InvalidRecurrenceError) Error() string { return "presence: invalid recurrence: " + e.Message }

// WindowError is a read window that is too large or reaches too far into the past (presence.window_too_large).
type WindowError struct{ Message string }

func (e *WindowError) Error() string { return "presence: invalid window: " + e.Message }

// InvalidTransitionError reports an operation the entry's status does not allow.
type InvalidTransitionError struct{ Operation, From string }

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("presence: operation %s is not allowed in status %s", e.Operation, e.From)
}

// Principal is the acting User and what the User may do. Scope is derived from Team membership, never passed in.
type Principal struct {
	UserID           string
	ManageOwn        bool
	ViewAvailability bool
	ViewEntries      bool
	ManageEntries    bool
	ManageTeams      bool
	Admin            bool
}

// Caller identifies who acts and under which request for audit and events.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

// Recurrence is the stored recurrence of an entry: a scheduling.Rule plus the mandatory end date.
type Recurrence struct {
	Frequency  string `json:"frequency"`
	Interval   int    `json:"interval"`
	Weekday    int    `json:"weekday,omitempty"`
	DayOfMonth int    `json:"dayOfMonth,omitempty"`
	TimeOfDay  string `json:"timeOfDay"`
	Timezone   string `json:"timezone"`
	StartsOn   string `json:"startsOn"`
	EndsOn     string `json:"endsOn"`
}

// Rule converts the recurrence to the platform scheduling rule.
func (r Recurrence) Rule() scheduling.Rule {
	return scheduling.Rule{Frequency: r.Frequency, Interval: r.Interval, Weekday: r.Weekday, DayOfMonth: r.DayOfMonth,
		TimeOfDay: r.TimeOfDay, Timezone: r.Timezone, StartsOn: r.StartsOn, EndsOn: r.EndsOn}
}

// Entry is a Presence Entry. For external entries Source is the source key and the entry is read-only here.
type Entry struct {
	ID           string
	UserID       string
	Kind         string
	LocationType *string
	LocationID   *string
	StartsAt     time.Time
	EndsAt       time.Time
	AllDay       bool
	Recurrence   *Recurrence
	Source       string
	SourceRef    *string
	Status       string
	ObservedFrom *time.Time
	ObservedTo   *time.Time
	ObservedAt   *time.Time
	Visibility   string
	EndedAt      time.Time
	CreatedBy    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CancelledAt  *time.Time
	Version      int
}

// Interval is a half-open instant range.
type Interval struct{ From, To time.Time }

// Settings is the single settings row.
type Settings struct {
	Enabled                bool
	DPIARecordedOn         *string // YYYY-MM-DD
	CouncilConfirmedOn     *string
	RetentionDays          int
	ExternalSourcesEnabled bool
	DisabledAt             *time.Time
	UpdatedBy              *string
	UpdatedAt              time.Time
	Version                int
}

// Minimum is the per-Team coverage minimum.
type Minimum struct {
	TeamID        string
	Minimum       int
	OnsiteMinimum *int
	LocationID    *string
	UpdatedBy     *string
	UpdatedAt     time.Time
	Version       int
}

// PurgeCounts are the counts of one purge run (never identifiers).
type PurgeCounts struct{ Entries int64 }

// Store is the PostgreSQL port.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	GetSettings(ctx context.Context) (Settings, error)
	LockSettingsTx(ctx context.Context, tx pgx.Tx) (Settings, error)
	UpdateSettingsTx(ctx context.Context, tx pgx.Tx, s Settings) (Settings, error)

	InsertEntryTx(ctx context.Context, tx pgx.Tx, e Entry) (Entry, error)
	LockEntryTx(ctx context.Context, tx pgx.Tx, id string) (Entry, error)
	// UpdateEntryTx writes the mutable columns (location, times, recurrence, status, ended_at) and bumps the version.
	UpdateEntryTx(ctx context.Context, tx pgx.Tx, e Entry) (Entry, error)
	CountActiveEntriesTx(ctx context.Context, tx pgx.Tx, userID string) (int, error)
	// EntriesInWindow returns the entries of userIDs that can have an occurrence overlapping [from, to) and are
	// not cancelled or closed. Rows of Users outside scope are never returned, whatever userIDs says.
	EntriesInWindow(ctx context.Context, scope, userIDs []string, from, to time.Time) ([]Entry, error)

	GetMinimum(ctx context.Context, teamID string) (Minimum, bool, error)
	// UpsertMinimumTx inserts (expectedVersion nil, none exists) or updates the Team's minimum.
	UpsertMinimumTx(ctx context.Context, tx pgx.Tx, m Minimum, expectedVersion *int) (Minimum, error)

	// PurgeTx deletes the entries that ended before cutoff (everything: all of them).
	PurgeTx(ctx context.Context, tx pgx.Tx, cutoff time.Time, everything bool) (PurgeCounts, error)
	// NewID returns a fresh UUID for audit targets that have no row.
	NewID(ctx context.Context, tx pgx.Tx) (string, error)
}

// Directory is the Organization work-directory contract (organization/public.WorkDirectory).
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveTeams(ctx context.Context, ids []string) (map[string]bool, error)
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	CurrentTeamIDs(ctx context.Context, userID string) ([]string, error)
	CurrentMemberIDs(ctx context.Context, teamID string) ([]string, error)
}
