// Package application holds the Remote Access use cases: attended Remote Access Sessions through external
// Remote Access Providers (ADR-0026, docs/product/f10-remote-access-design.md). The module owns the session
// record, its policy gates, one-time launch handles and the Device-to-peer mappings. The providers own the
// transport; launch links are built by the provider connectors and are never persisted, logged or audited.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Session statuses (docs/domain/state-machines.md#remote-access-session).
const (
	StatusRequested       = "requested"
	StatusPendingApproval = "pending_approval"
	StatusAuthorized      = "authorized"
	StatusLaunched        = "launched"
	StatusClosed          = "closed"
	StatusRejected        = "rejected"
	StatusCancelled       = "cancelled"
	StatusExpired         = "expired"
	StatusFailed          = "failed"
)

// Statuses lists every status.
var Statuses = []string{StatusRequested, StatusPendingApproval, StatusAuthorized, StatusLaunched, StatusClosed,
	StatusRejected, StatusCancelled, StatusExpired, StatusFailed}

// Consent values; consent is separate from authorization and from provider observations.
const (
	ConsentGranted     = "granted"
	ConsentDeclined    = "declined"
	ConsentNotRequired = "not_required"
	ConsentUnknown     = "unknown"
)

// Permissions of the module (platform/permissions registry).
const (
	PermView         = "remote_access.view"
	PermStart        = "remote_access.start_attended"
	PermViewSessions = "remote_access.view_sessions"
	PermAdmin        = "remote_access.admin"
)

// Reason code sets.
var (
	// MismatchReasons explain why the Ticket's affected User is not the Device's holder.
	MismatchReasons = []string{"holder_changed", "shared_device", "on_behalf"}
	CloseReasons    = []string{"completed", "technician_aborted", "connection_failed", "other"}
	CancelReasons   = []string{"no_longer_needed", "wrong_device", "ticket_resolved", "other"}
	MapReasons      = []string{"initial_mapping", "correction", "provider_change", "other"}
	UnmapReasons    = []string{"no_longer_valid", "wrong_device", "decommissioned", "other"}
)

// Reason codes written by the module itself.
const (
	ReasonConsentDeclined       = "consent_declined"
	ReasonApprovalRejected      = "approval_rejected"
	ReasonApproverNotAuthorized = "approver_not_authorized"
	ReasonNotLaunched           = "not_launched"
	ReasonApprovalTimeout       = "approval_timeout"
	ReasonExpiredOpen           = "expired_open"
	ReasonLaunchFailed          = "launch_failed"
	// ReasonMappingChanged ends open sessions of a Device whose peer mapping was replaced or removed.
	ReasonMappingChanged = "mapping_changed"
	reasonReplaced       = "replaced"
)

// Approval, event, notification and job names.
const (
	// SubjectType is the Approval subject type of a session.
	SubjectType = "remote_access_session"

	EventRequested  = "RemoteAccessSessionRequested"
	EventAuthorized = "RemoteAccessSessionAuthorized"
	EventLaunched   = "RemoteAccessSessionLaunched"
	EventClosed     = "RemoteAccessSessionClosed"

	// NotificationCategory tells the Device's holder that a technician started a session (ADR-0026: the end user
	// always sees that a session is active). The reference is the only session content included.
	NotificationCategory = "remoteaccess.session_started"

	ExpireJobType     = "remoteaccess.expire_sessions"
	ExpireJobTimeout  = 2 * time.Minute
	ExpireInterval    = time.Minute
	ObserveJobType    = "remoteaccess.observe"
	ObserveJobTimeout = 2 * time.Minute
	ObserveInterval   = 5 * time.Minute
)

// Timings and limits.
const (
	// LaunchHandleTTL is how long a launch handle can be exchanged.
	LaunchHandleTTL = 60 * time.Second
	// AuthorizedTTL: an authorized session not launched in time expires.
	AuthorizedTTL = 15 * time.Minute
	// PendingTTL: a session whose approval is not decided in time expires.
	PendingTTL = 24 * time.Hour
	// LaunchedTTL: a launched session nobody closed is closed with expired_open.
	LaunchedTTL = 8 * time.Hour
	// DeviceFreshness is how recent the last observation of a Device must be.
	DeviceFreshness = 7 * 24 * time.Hour
	// RateLimit request attempts (refused ones included) per user and RateWindow.
	RateLimit  = 20
	RateWindow = time.Hour
	// AttemptRetention is how long request attempts are kept for the rate limit.
	AttemptRetention = 24 * time.Hour
	// ObservationSlack widens the time window that matches a provider record to a launched session.
	ObservationSlack = 2 * time.Minute
	// ObservationLookback is how far back provider records are read.
	ObservationLookback = 48 * time.Hour
	// ObservationSummaryWindow is how far back the observation summary counts records.
	ObservationSummaryWindow = 30 * 24 * time.Hour
	// maxRecordAge and maxRecordDuration bound the times of a provider record; clockSkew is the tolerated future.
	maxRecordAge      = 30 * 24 * time.Hour
	maxRecordDuration = 7 * 24 * time.Hour
	clockSkew         = 5 * time.Minute
	maxOperator       = 100

	DefaultLimit = 50
	MaxLimit     = 200
	maxNote      = 500
	maxBatch     = 200
)

// Session is a Remote Access Session. Turaco-authorized facts, consent and provider-observed facts are separate.
type Session struct {
	ID                string
	Reference         string
	DeviceID          string
	TicketID          string
	Provider          string
	PeerID            string
	Mode              string
	Status            string
	StatusReason      *string
	InitiatedBy       string
	ApprovalID        *string
	ExcludedUserIDs   []string
	Consent           string
	ConsentRecordedBy *string
	ConsentRecordedAt *time.Time
	MismatchReason    *string
	LaunchedAt        *time.Time
	ClosedAt          *time.Time
	ExpiresAt         time.Time
	Note              *string
	// Provider-observed facts; nil means unknown, never inferred.
	ObservedConnectedAt *time.Time
	ObservedEndedAt     *time.Time
	ObservedSource      *string
	ObservedAt          *time.Time
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Open reports a session that has not ended.
func (s Session) Open() bool {
	switch s.Status {
	case StatusRequested, StatusPendingApproval, StatusAuthorized, StatusLaunched:
		return true
	}
	return false
}

// Transition is one entry of a session's append-only history.
type Transition struct {
	ID            string
	SessionID     string
	FromStatus    *string
	ToStatus      string
	Operation     string
	Reason        *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
	CreatedAt     time.Time
}

// PeerMapping maps a Device to its identity at one provider.
type PeerMapping struct {
	ID          string
	DeviceID    string
	Provider    string
	PeerID      string
	Source      string
	Reason      *string
	MappedBy    *string
	MappedAt    time.Time
	ClosedAt    *time.Time
	ClosedBy    *string
	CloseReason *string
}

// Provider record flags: why a record is not (or not cleanly) attributed to one session.
const (
	FlagUnattributed = "unattributed"
	FlagDuplicate    = "duplicate"
	FlagAfterClose   = "after_close"
)

// ObservationSources is the allow-list of provider record sources.
var ObservationSources = []string{"rustdesk.audit", "anydesk.history", "hoptodesk.history"}

// ProviderRecord is a provider's record of a connection as stored: provider facts with their source, and the
// session it was attributed to (nil when unattributed). Attribution never changes once a session is set.
type ProviderRecord struct {
	ID                string
	Provider          string
	ProviderSessionID string
	PeerID            string
	StartedAt         time.Time
	EndedAt           *time.Time
	Operator          *string
	Source            string
	ObservedAt        time.Time
	SessionID         *string
	Flag              *string
	FirstSeenAt       time.Time
	// New is set by UpsertRecordTx when the record was stored for the first time (not persisted).
	New bool
}

// Handle is a stored launch handle (the hash only).
type Handle struct {
	ID        string
	SessionID string
	UserID    string
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// Principal is the caller's authority.
type Principal struct {
	UserID       string
	View         bool
	Start        bool
	ViewSessions bool
	Admin        bool
}

// Caller identifies who performs a mutation and the request it belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
}

func (c Caller) validate() error {
	if err := c.Actor.Validate(); err != nil {
		return err
	}
	if c.CorrelationID == "" {
		return errors.New("remoteaccess: correlation id is required")
	}
	return nil
}

// Errors.
var (
	ErrNotFound           = errors.New("remoteaccess: not found")
	ErrForbidden          = errors.New("remoteaccess: forbidden")
	ErrInvalidCursor      = errors.New("remoteaccess: invalid cursor")
	ErrVersionConflict    = errors.New("remoteaccess: version conflict")
	ErrNoEligibleApprover = errors.New("remoteaccess: no eligible approver")
	ErrRateLimited        = errors.New("remoteaccess: rate limit reached")
	ErrHandleUsed         = errors.New("remoteaccess: launch handle already used")
	ErrHandleExpired      = errors.New("remoteaccess: launch handle or session expired")
	ErrHandleActive       = errors.New("remoteaccess: a launch handle is still valid")
	ErrConsentRecorded    = errors.New("remoteaccess: consent already recorded")
	ErrPeerTaken          = errors.New("remoteaccess: the peer id is mapped to another device")
	ErrLaunchFailed       = errors.New("remoteaccess: the provider could not build the launch link")
	ErrRecordConflict     = errors.New("remoteaccess: provider record conflicts with the stored one")
)

// RefusedError is a policy gate that refuses an operation; Code is stable and user-facing (remoteaccess.<code>).
type RefusedError struct{ Code string }

func (e *RefusedError) Error() string { return "remoteaccess: refused: " + e.Code }

func refused(code string) error { return &RefusedError{Code: code} }

// Refusal codes.
const (
	RefProviderDisabled = "provider_disabled"
	RefNoPeerMapping    = "no_peer_mapping"
	RefStaleDevice      = "stale_device"
	RefDeviceRetired    = "device_retired"
	RefDeviceUnknown    = "device_unknown"
	// RefTicketUnavailable is the single answer for an unknown, closed or unauthorized Ticket (no oracle).
	RefTicketUnavailable = "ticket_unavailable"
	RefMappingChanged    = "mapping_changed"
	RefNoRecipient       = "no_recipient"
	RefApprovalRequired  = "approval_required"
	RefHolderMismatch    = "holder_mismatch"
	RefSessionOpen       = "session_open"
	RefApproverRequired  = "approver_required"
)

// InvalidTransitionError reports an operation the status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("remoteaccess: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "remoteaccess: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Page is keyset pagination over the UUIDv7 primary key.
type Page struct {
	Limit  int
	Cursor string
}

func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	return p
}

// Result is one page; NextCursor is empty on the last page.
type Result[T any] struct {
	Items      []T
	NextCursor string
}

// Filter selects sessions. OnlyInitiator restricts the list to one initiator (set by the service for callers
// without remote_access.view_sessions).
type Filter struct {
	Status        string
	DeviceID      string
	TicketID      string
	InitiatedBy   string
	OnlyInitiator string
	Page          Page
}

// Store persists sessions, transitions, handles and peer mappings. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	InsertSessionTx(ctx context.Context, tx pgx.Tx, s Session) (Session, error)
	LockSessionTx(ctx context.Context, tx pgx.Tx, id string) (Session, error)
	// UpdateSessionTx writes every mutable column and bumps the version.
	UpdateSessionTx(ctx context.Context, tx pgx.Tx, s Session) (Session, error)
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context, f Filter) (Result[Session], error)
	// LockUserTx serializes the rate-limit check of one User for the transaction.
	LockUserTx(ctx context.Context, tx pgx.Tx, userID string) error
	// CountAttemptsTx counts request attempts (refused ones included) since the time; InsertAttemptTx records one;
	// PruneAttempts deletes attempts older than the time.
	CountAttemptsTx(ctx context.Context, tx pgx.Tx, userID string, since time.Time) (int, error)
	InsertAttemptTx(ctx context.Context, tx pgx.Tx, userID string, at time.Time) error
	PruneAttempts(ctx context.Context, before time.Time) error
	// OpenSessionsOfDeviceTx locks the open sessions of a Device and provider (by id).
	OpenSessionsOfDeviceTx(ctx context.Context, tx pgx.Tx, deviceID, provider string) ([]Session, error)
	// DueSessions lists open sessions whose expiry passed.
	DueSessions(ctx context.Context, now time.Time, limit int) ([]Session, error)
	// CandidateSessions lists the launched or closed sessions of a provider and peer whose launched window
	// [launched_at, closed_at + ObservationSlack] contains the start (open sessions have no upper bound).
	CandidateSessions(ctx context.Context, provider, peerID string, started time.Time) ([]Session, error)

	// UpsertRecordTx stores a provider record keyed by (provider, provider session id); a repeat updates the end,
	// the observation time and the operator only. ErrRecordConflict when the identity (peer, start, source) differs.
	UpsertRecordTx(ctx context.Context, tx pgx.Tx, r ProviderRecord) (ProviderRecord, error)
	// SetRecordAttributionTx sets the session and flag of an unattributed record.
	SetRecordAttributionTx(ctx context.Context, tx pgx.Tx, id string, sessionID, flag *string) error
	// HasAttributedRecordTx reports whether another record is the attributed one of the session.
	HasAttributedRecordTx(ctx context.Context, tx pgx.Tx, sessionID, exceptRecordID string) (bool, error)
	// RecordSummary counts the flagged records first seen since the time, by flag.
	RecordSummary(ctx context.Context, since time.Time) (map[string]int, error)

	InsertTransitionTx(ctx context.Context, tx pgx.Tx, t Transition) error
	Transitions(ctx context.Context, sessionID string, page Page) (Result[Transition], error)

	InsertHandleTx(ctx context.Context, tx pgx.Tx, h Handle, tokenHash []byte) error
	// LockHandleTx locks the handle with this token hash (ErrNotFound when none).
	LockHandleTx(ctx context.Context, tx pgx.Tx, tokenHash []byte) (Handle, error)
	MarkHandleUsedTx(ctx context.Context, tx pgx.Tx, id string, at time.Time) error
	// LiveHandlesTx counts the unused, unexpired handles of a session.
	LiveHandlesTx(ctx context.Context, tx pgx.Tx, sessionID string, now time.Time) (int, error)

	ActiveMapping(ctx context.Context, deviceID, provider string) (PeerMapping, bool, error)
	Mappings(ctx context.Context, deviceID string, includeClosed bool) ([]PeerMapping, error)
	InsertMappingTx(ctx context.Context, tx pgx.Tx, m PeerMapping) (PeerMapping, error)
	LockActiveMappingTx(ctx context.Context, tx pgx.Tx, deviceID, provider string) (PeerMapping, bool, error)
	CloseMappingTx(ctx context.Context, tx pgx.Tx, id string, closedBy *string, reason string, at time.Time) error
}

// DeviceInfo is what the module needs of a Device (endpoints/public).
type DeviceInfo struct {
	ID         string
	Name       string
	AssetID    *string
	Ownership  string
	ObservedAt time.Time
	// LastCheckinAt is the Device's last check-in at its management provider (nil: unknown, treated as stale).
	LastCheckinAt *time.Time
	RetiredAt     *time.Time
}

// Devices is the Endpoints contract.
type Devices interface {
	Device(ctx context.Context, id string) (DeviceInfo, bool, error)
}

// TicketInfo is what the module needs of a Ticket (servicedesk/public).
type TicketInfo struct {
	ID             string
	Reference      string
	Open           bool
	AffectedUserID string
	ReporterUserID string
	// AssigneeUserID is empty when the Ticket is unassigned.
	AssigneeUserID string
}

// Tickets is the Service Desk contract.
type Tickets interface {
	Ticket(ctx context.Context, id string) (TicketInfo, bool, error)
}

// Holders is the Assets contract: assetID -> User id for the assets currently held by a User.
type Holders interface {
	UserHolders(ctx context.Context, assetIDs []string) (map[string]string, error)
}

// Directory is what the module asks the Organization module.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
}

// ObservationSummary tells operators how many provider records could not be attributed cleanly.
type ObservationSummary struct {
	// UnattributedRecords counts flagged records (unattributed, duplicate or after close) first seen since Since.
	UnattributedRecords int
	ByReason            map[string]int
	Since               time.Time
}

// Approver names who approves a session: exactly one of User and Team.
type Approver struct {
	UserID *string
	TeamID *string
}

// ApprovalInfo is the state of one approval step.
type ApprovalInfo struct {
	ID              string
	Status          string
	ApproverUserID  *string
	ApproverTeamID  *string
	DecidedByUserID *string
}

// Approvals is the Approvals contract (subject remote_access_session).
type Approvals interface {
	// RequestInTx creates a pending approval; ErrNoEligibleApprover when nobody could decide it.
	RequestInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID, label string, approver Approver, excluded []string) (string, error)
	CancelBySubjectInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, correlationID, subjectID string) error
	ForSubject(ctx context.Context, subjectID string) ([]ApprovalInfo, error)
}

// Approvers answers who may approve: effective permissions and Team memberships.
type Approvers interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
	TeamMemberIDs(ctx context.Context, teamID string) ([]string, error)
	TeamIDsOfUser(ctx context.Context, userID string) ([]string, error)
}

// Providers resolves the enabled provider connectors.
type Providers interface {
	Get(key string) (remoteaccess.Provider, bool)
	Keys() []string
}

// Notifier creates notifications inside the caller's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}
