// Package application implements Time Tracking (F17 T-A, ADR-0041): manual Time Entries on Tickets, the settings
// with the recorded works-council co-determination basis, and retention.
//
// Time entries are personal data about employees. Privacy is structural: there is no per-person total, ranking or
// comparison in this module; the entries of other people are readable only by people who may work the Ticket and
// only while show_entry_authors allows it; note text is never audited or sent anywhere.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Permissions (docs/reference/permissions.md).
const (
	PermLog    = "timetracking.log"
	PermManage = "timetracking.manage"
	PermAdmin  = "timetracking.admin"
	PermReport = "timetracking.report"
)

// Subjects, sources, delete reasons and co-determination bases.
const (
	SubjectTicket = "ticket"

	SourceManual = "manual"
	SourceTimer  = "timer"

	DeleteWrongTicket   = "wrong_ticket"
	DeleteDuplicate     = "duplicate"
	DeleteWrongDuration = "wrong_duration"
	DeleteOther         = "other"

	BasisAgreement     = "agreement"
	BasisNotApplicable = "not_applicable"
)

// Limits (design section 7).
const (
	MaxDayMinutes     = 24 * 60
	MaxNoteLength     = 500
	MaxReasonLength   = 300
	MinReasonLength   = 3
	MaxMineDays       = 366
	MaxMineEntries    = 1000
	SoftDeleteKeep    = 30 * 24 * time.Hour
	StaleTimerKeep    = 30 * 24 * time.Hour
	PurgeJobType      = "timetracking.purge"
	PurgeJobTimeout   = 2 * time.Minute
	PurgeInterval     = 24 * time.Hour
	futureWorkDateDay = 1 // tolerance for time zones ahead of UTC
)

// Principal is the caller's Time Tracking authority (permissions are decided from the session).
type Principal struct {
	UserID string
	Log    bool
	Manage bool
	Admin  bool
	Report bool
}

// Caller identifies the actor of a mutation for the audit trail.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

// Settings is the single settings row.
type Settings struct {
	CodeterminationBasis      *string
	CodeterminationRecordedOn *string // YYYY-MM-DD
	DPIARecordedOn            *string
	EditWindowDays            int
	RetentionMonths           int
	ShowEntryAuthors          bool
	MinReportGroupSize        int
	Version                   int
	UpdatedBy                 *string
	UpdatedAt                 time.Time
}

// CodeterminationRecorded reports whether the precondition for switching the module on is met.
func (s Settings) CodeterminationRecorded() bool {
	return s.CodeterminationBasis != nil && s.CodeterminationRecordedOn != nil
}

// Entry is one Time Entry.
type Entry struct {
	ID              string
	SubjectType     string
	SubjectID       string
	QueueID         string
	UserID          string
	WorkDate        string // YYYY-MM-DD
	DurationMinutes int
	StartedAt       *time.Time
	Source          string
	Note            *string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Version         int
}

// PurgeCounts is what one retention run removed.
type PurgeCounts struct {
	Entries int
	Deleted int
	Timers  int
}

// TicketAccess is the caller's access to one Ticket, answered by Service Desk.
type TicketAccess struct {
	Found     bool
	Reference string
	QueueID   string
	CanView   bool
	CanWork   bool
}

// Tickets is the Service Desk port: access of a User to Tickets and the Queue of each.
type Tickets interface {
	Access(ctx context.Context, userID string, ticketIDs []string) (map[string]TicketAccess, error)
	// ViewQueues answers the Queues the User may view: all (global) or a list of Queue ids (report scope).
	ViewQueues(ctx context.Context, userID string) (global bool, queueIDs []string, err error)
}

// Directory is the Organization port.
type Directory interface {
	ActiveEmployees(ctx context.Context, ids []string) (map[string]bool, error)
	UserNames(ctx context.Context, ids []string) (map[string]string, error)
}

// Store is the persistence port.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	GetSettings(ctx context.Context) (Settings, error)
	LockSettingsTx(ctx context.Context, tx pgx.Tx) (Settings, error)
	UpdateSettingsTx(ctx context.Context, tx pgx.Tx, s Settings) (Settings, error)
	// LockPersonDateTx serializes the 24-hour check of one person and work date (transaction advisory lock).
	LockPersonDateTx(ctx context.Context, tx pgx.Tx, userID, workDate string) error
	// DayMinutesTx sums the non-deleted durations of the person on the date, excluding one entry (empty = none).
	DayMinutesTx(ctx context.Context, tx pgx.Tx, userID, workDate, excludeID string) (int, error)
	InsertEntryTx(ctx context.Context, tx pgx.Tx, e Entry) (Entry, error)
	// LockEntryTx returns a non-deleted entry FOR UPDATE (ErrNotFound otherwise).
	LockEntryTx(ctx context.Context, tx pgx.Tx, id string) (Entry, error)
	UpdateEntryTx(ctx context.Context, tx pgx.Tx, e Entry) (Entry, error)
	SoftDeleteTx(ctx context.Context, tx pgx.Tx, id, deletedBy, reason string) error
	SubjectTotal(ctx context.Context, subjectType, subjectID string) (int, error)
	ListBySubject(ctx context.Context, subjectType, subjectID string) ([]Entry, error)
	ListMine(ctx context.Context, userID, from, to string, limit int) ([]Entry, error)
	PurgeTx(ctx context.Context, tx pgx.Tx, workDateBefore string, deletedBefore, timersBefore time.Time) (PurgeCounts, error)
	NewID(ctx context.Context, tx pgx.Tx) (string, error)
	// Summary aggregates non-deleted entries by group; queueIDs nil means every Queue.
	Summary(ctx context.Context, from, to, groupBy string, queueIDs []string) ([]SummaryRow, error)
	// StartTimer inserts the person's timer; ErrTimerRunning when one exists.
	StartTimer(ctx context.Context, t Timer) (Timer, error)
	GetTimer(ctx context.Context, userID string) (Timer, error)
	// LockTimerTx returns the person's timer FOR UPDATE (ErrNotFound when none).
	LockTimerTx(ctx context.Context, tx pgx.Tx, userID string) (Timer, error)
	DeleteTimerTx(ctx context.Context, tx pgx.Tx, userID string) error
	DeleteTimer(ctx context.Context, userID string) error
}

// Timer is a person's single running stopwatch on a Ticket. It is personal state: not audited and visible to nobody else.
type Timer struct {
	UserID      string
	SubjectType string
	SubjectID   string
	StartedAt   time.Time
	Note        *string
}

// Errors.
var (
	ErrNotFound          = errors.New("timetracking: not found")
	ErrForbidden         = errors.New("timetracking: forbidden")
	ErrVersionConflict   = errors.New("timetracking: version conflict")
	ErrOutsideEditWindow = errors.New("timetracking: work date outside the edit window")
	ErrDayLimit          = errors.New("timetracking: more than 24 hours on one work date")
	ErrTimerRunning      = errors.New("timetracking: a timer is already running")
)

// InvalidInputError is a rejected input with a message safe to show.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Report group-bys (no per-person grouping exists).
const (
	GroupQueue  = "queue"
	GroupMonth  = "month"
	GroupTicket = "ticket"

	MaxReportDays = 366
	MaxReportRows = 200
)

// SummaryRow is one aggregated group from the store.
type SummaryRow struct {
	Key string
	// Month (YYYY-MM) is the calendar month of the cell; empty for per-Ticket rows.
	Month        string
	Minutes      int
	Entries      int
	Contributors int
}

// SummaryGroup is one group of the effort report. Suppressed groups carry no figures: fewer distinct people than the
// minimum group size contributed, so the number could describe one person.
type SummaryGroup struct {
	Key          string
	Label        string
	Minutes      int
	Entries      int
	Contributors int
	Suppressed   bool
	// Partial says that months with fewer contributors than the minimum were left out of the figures.
	Partial bool
}

// Summary is the effort report.
type Summary struct {
	From, To     string
	GroupBy      string
	MinGroupSize int
	Groups       []SummaryGroup
	Truncated    bool
}
