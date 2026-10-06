// Package application holds the Security use cases (docs/product/f8-security-briefing-design.md, slice
// F8a): Security Advisories with their affected criteria and lifecycle, the bounded advisory import,
// the deterministic matching of advisories against observed software installations (endpoints/public)
// and the Vulnerability Findings it derives, with their triage, risk acceptance and remediation states.
// Findings are Turaco-derived and say so: confidence is probable or potential, never confirmed.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Advisory statuses (docs/domain/state-machines.md#security-advisory--finding).
const (
	AdvisoryNew                = "new"
	AdvisoryAnalyzing          = "analyzing"
	AdvisoryApplicable         = "applicable"
	AdvisoryNotApplicable      = "not_applicable"
	AdvisoryRemediationPlanned = "remediation_planned"
	AdvisoryRemediating        = "remediating"
	AdvisoryResolved           = "resolved"
	AdvisoryArchived           = "archived"
)

// AdvisoryStatuses lists every Advisory status.
var AdvisoryStatuses = []string{AdvisoryNew, AdvisoryAnalyzing, AdvisoryApplicable, AdvisoryNotApplicable,
	AdvisoryRemediationPlanned, AdvisoryRemediating, AdvisoryResolved, AdvisoryArchived}

// criteriaEditable are the Advisory statuses in which the affected criteria may change.
var criteriaEditable = []string{AdvisoryNew, AdvisoryAnalyzing, AdvisoryApplicable}

// matchable are the Advisory statuses the matching job derives findings for; not applicable and archived
// advisories keep their findings unchanged.
var matchable = []string{AdvisoryNew, AdvisoryAnalyzing, AdvisoryApplicable, AdvisoryRemediationPlanned, AdvisoryRemediating, AdvisoryResolved}

// Finding statuses. Accepted acknowledges a finding; risk_accepted is a separate, reviewed exception.
const (
	FindingOpen               = "open"
	FindingInvestigating      = "investigating"
	FindingAccepted           = "accepted"
	FindingRemediationPlanned = "remediation_planned"
	FindingRemediating        = "remediating"
	FindingRemediated         = "remediated"
	FindingFalsePositive      = "false_positive"
	FindingRiskAccepted       = "risk_accepted"
)

// FindingStatuses lists every Finding status.
var FindingStatuses = []string{FindingOpen, FindingInvestigating, FindingAccepted, FindingRemediationPlanned, FindingRemediating,
	FindingRemediated, FindingFalsePositive, FindingRiskAccepted}

// openFinding are the statuses in which a finding still describes an exposure that is being handled.
var openFinding = []string{FindingOpen, FindingInvestigating, FindingAccepted, FindingRemediationPlanned, FindingRemediating}

// remediable are the statuses a fresh observation can move to remediated (a risk acceptance ends when the
// exposure is gone; a false positive stays what it is).
var remediable = []string{FindingOpen, FindingInvestigating, FindingAccepted, FindingRemediationPlanned, FindingRemediating, FindingRiskAccepted}

// Confidence of a finding (Turaco-derived).
const (
	ConfidenceProbable  = "probable"
	ConfidencePotential = "potential"
)

// Confidences lists the stored confidences.
var Confidences = []string{ConfidenceProbable, ConfidencePotential}

// Severities of an Advisory as provided by the source.
var Severities = []string{"none", "low", "medium", "high", "critical"}

// Platforms are the device OS platforms a Criterion may name.
var Platforms = []string{"windows", "macos", "ios", "android", "linux", "other"}

// Normalization states and match methods of a Criterion.
const (
	NormalizationMatched   = "matched"
	NormalizationUnmatched = "unmatched"
	MethodExplicit         = "explicit"
	MethodProduct          = "product"
	MethodAlias            = "alias"
)

// Reason codes.
var (
	// NotApplicableReasons are the reason codes MarkNotApplicable accepts.
	NotApplicableReasons = []string{"product_not_used", "version_not_used", "configuration_not_affected", "duplicate", "other"}
	// AcceptRiskReasons are the reason codes AcceptRisk accepts.
	AcceptRiskReasons = []string{"compensating_control", "low_exposure", "no_fix_available", "business_need", "other"}
	// FalsePositiveReasons are the reason codes MarkFalsePositive accepts.
	FalsePositiveReasons = []string{"version_misreported", "product_mismatch", "not_installed", "configuration_not_affected", "other"}
	// ReopenReasons are the reason codes Reopen accepts.
	ReopenReasons = []string{"review_due", "new_information", "error_correction", "other"}
)

// Reason codes written by the module itself.
const (
	ReasonCriteriaChanged  = "criteria_changed"
	ReasonFeedChanged      = "feed_changed"
	ReasonNoLongerObserved = "no_longer_observed"
	ReasonVersionChanged   = "version_changed"
	ReasonDeviceRetired    = "device_retired"
	ReasonObservedAgain    = "observed_again"
	systemActor            = "security-matching"
)

// Permissions of the Security module (platform/permissions registry).
const (
	PermView       = "security.view"
	PermManage     = "security.manage"
	PermAcceptRisk = "security.accept_risk"
)

const (
	// EventAdvisoryPublished is published when an Advisory becomes applicable.
	EventAdvisoryPublished = "SecurityAdvisoryPublished"
	// EventAdvisoryPublishedFanOut continues the security.advisory notification fan-out (internal).
	EventAdvisoryPublishedFanOut = "SecurityAdvisoryPublishedFanOut"
	// EventFindingChanged is published when a finding is created or changes status.
	EventFindingChanged = "VulnerabilityFindingChanged"
	// NotificationCategory tells security.manage holders that an Advisory became applicable.
	NotificationCategory = "security.advisory"
	RiskReviewCategory   = "security.risk_review_due"
	RiskReminderJobType  = "security.risk_review_reminders"
	RiskReminderInterval = 24 * time.Hour

	// MatchJobType matches one Advisory; MatchAllJobType re-queues every Advisory that needs it.
	MatchJobType       = "security.match"
	MatchAllJobType    = "security.match_all"
	MatchJobTimeout    = 10 * time.Minute
	MatchAllJobTimeout = 2 * time.Minute
	MatchAllInterval   = 6 * time.Hour
	// MaxMatchDevices bounds the Devices one match run derives findings for (reported as truncated).
	MaxMatchDevices = 20000
	// MaxMatchAllAdvisories bounds the Advisories one match_all run queues.
	MaxMatchAllAdvisories = 1000

	// MaxImportRecords bounds one import; MaxImportErrors the per-record errors reported.
	MaxImportRecords  = 500
	MaxImportErrors   = 50
	MaxImportCriteria = 2000
	ImportTimeout     = 60 * time.Second
	// MaxCriteria bounds the criteria of an Advisory; MaxRules the version rules of one criterion.
	MaxCriteria = 50
	MaxRules    = 20
	// MaxRiskAcceptanceMonths is the longest risk acceptance before a review.
	MaxRiskAcceptanceMonths = 12

	DefaultLimit = 50
	MaxLimit     = 200
	maxTitle     = 300
	maxSummary   = 4000
	maxURL       = 2000
	maxExternal  = 200
	maxProduct   = 200
	maxVersion   = 100
	maxQuery     = 100
)

// Advisory is a Security Advisory.
type Advisory struct {
	ID                 string
	Reference          string
	Source             string
	ExternalID         *string
	Title              string
	Summary            *string
	Severity           string
	PublishedAt        *time.Time
	ModifiedAt         *time.Time
	SourceURL          *string
	Status             string
	StatusReason       *string
	CriteriaRevision   int
	CriteriaChangedAt  time.Time
	MatchedRevision    *int
	MatchedAt          *time.Time
	MatchedIngestionAt *time.Time
	MatchTruncated     bool
	EditedByUser       bool
	UnmatchedCriteria  int
	// KnownExploited and its dates are set only by the CISA KEV enrichment of the feed sync.
	KnownExploited        bool
	KnownExploitedAddedAt *time.Time
	KEVDueDate            *time.Time
	CreatedBy             *string
	ApplicableAt          *time.Time
	ResolvedAt            *time.Time
	ArchivedAt            *time.Time
	Version               int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// Criterion names affected software of an Advisory.
type Criterion struct {
	ID                string
	Position          int
	SoftwareProductID *string
	ProductName       *string
	Publisher         *string
	OSPlatform        *string
	Normalization     string
	MatchMethod       *string
	Rules             []Rule
}

// Finding is a Vulnerability Finding: an Advisory matched to a Device's Software Product.
type Finding struct {
	ID                string
	Reference         string
	AdvisoryID        string
	DeviceID          string
	SoftwareProductID string
	InstalledVersion  string
	Confidence        string
	Status            string
	StatusReason      *string
	RiskAcceptedBy    *string
	RiskAcceptedAt    *time.Time
	RiskReviewBy      *time.Time
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	RemediatedAt      *time.Time
	Version           int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Transition is one entry of an Advisory's or a Finding's append-only state history.
type Transition struct {
	ID            string
	SubjectID     string
	FromStatus    *string
	ToStatus      string
	Operation     string
	Reason        *string
	ActorUserID   *string
	ActorSystem   *string
	CorrelationID string
	CreatedAt     time.Time
}

// Principal is the caller's authority: View, Manage and AcceptRisk are security.view, security.manage
// and security.accept_risk; EndpointsView (endpoints.view|manage) decides whether device ids and names
// of findings are shown.
type Principal struct {
	UserID        string
	View          bool
	Manage        bool
	AcceptRisk    bool
	EndpointsView bool
	TasksView     bool
	TasksManage   bool
	ChangesView   bool
}

// reads reports that the caller may read advisories and findings.
func (p Principal) reads() bool { return p.View || p.Manage || p.AcceptRisk }

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
		return errors.New("security: correlation id is required")
	}
	return nil
}

var (
	ErrNotFound         = errors.New("security: not found")
	ErrForbidden        = errors.New("security: forbidden")
	ErrInvalidCursor    = errors.New("security: invalid cursor")
	ErrVersionConflict  = errors.New("security: version conflict")
	ErrDuplicate        = errors.New("security: an advisory with this source and external id exists")
	ErrReferenceInvalid = errors.New("security: referenced software product does not exist")
)

// InvalidTransitionError reports an operation the status does not allow.
type InvalidTransitionError struct {
	Operation string
	From      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("security: operation %s is not allowed in status %s", e.Operation, e.From)
}

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "security: invalid input: " + e.Message }

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

// AdvisoryFilter selects Advisories.
type AdvisoryFilter struct {
	Status   string
	Severity string
	Query    string
	Page     Page
}

// FindingFilter selects Findings.
type FindingFilter struct {
	AdvisoryID string
	Status     string
	Confidence string
	Page       Page
}

// Summary counts the findings of an Advisory.
type Summary struct {
	ByStatus          map[string]int
	ByConfidence      map[string]int
	UnmatchedCriteria int
	// AffectedDevices counts distinct Devices with a live exposure, including acknowledged and
	// risk-accepted findings.
	AffectedDevices int
	// OldestOpenSince is the first observation of the oldest such finding.
	OldestOpenSince *time.Time
}

// MatchState is what a match run records on its Advisory.
type MatchState struct {
	Revision    int
	IngestionAt *time.Time
	Truncated   bool
}

// Store persists Advisories, criteria, Findings and their transitions. Tx methods run inside InTx.
type Store interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	InsertAdvisoryTx(ctx context.Context, tx pgx.Tx, a Advisory) (Advisory, error)
	// LockAdvisorySourceTx serializes imports of one source/external key, including the missing row.
	LockAdvisorySourceTx(ctx context.Context, tx pgx.Tx, source, externalID string) error
	LockAdvisoryTx(ctx context.Context, tx pgx.Tx, id string) (Advisory, error)
	// LockAdvisoryBySourceTx locks the Advisory of a source and external id; ErrNotFound when none.
	LockAdvisoryBySourceTx(ctx context.Context, tx pgx.Tx, source, externalID string) (Advisory, error)
	// UpdateAdvisoryTx writes every mutable column and bumps the version.
	UpdateAdvisoryTx(ctx context.Context, tx pgx.Tx, a Advisory) (Advisory, error)
	// RecordMatchTx stores the match bookkeeping without changing the version.
	RecordMatchTx(ctx context.Context, tx pgx.Tx, id string, m MatchState) error
	GetAdvisory(ctx context.Context, id string) (Advisory, error)
	AdvisoriesByIDs(ctx context.Context, ids []string) (map[string]Advisory, error)
	MatchableProductIDs(ctx context.Context) ([]string, error)
	DueRiskFindingIDs(ctx context.Context, limit int) ([]string, error)
	ListAdvisories(ctx context.Context, f AdvisoryFilter) (Result[Advisory], error)
	// AdvisoriesToMatch lists Advisories in a matchable status whose criteria changed since their last
	// match, that were never matched or whose last match predates the ingestion time; at most limit.
	AdvisoriesToMatch(ctx context.Context, statuses []string, productObservedAt map[string]time.Time, limit int) ([]string, error)

	// ReplaceCriteriaTx replaces every criterion of the Advisory (positions are reassigned in order).
	ReplaceCriteriaTx(ctx context.Context, tx pgx.Tx, advisoryID string, c []Criterion) error
	Criteria(ctx context.Context, advisoryID string) ([]Criterion, error)
	CriteriaTx(ctx context.Context, tx pgx.Tx, advisoryID string) ([]Criterion, error)

	InsertAdvisoryTransitionTx(ctx context.Context, tx pgx.Tx, t Transition) error
	AdvisoryTransitions(ctx context.Context, advisoryID string, page Page) (Result[Transition], error)

	// InsertFindingTx inserts a finding; ok is false when one exists for the advisory, device and product.
	InsertFindingTx(ctx context.Context, tx pgx.Tx, f Finding) (out Finding, ok bool, err error)
	InsertFindingsTx(ctx context.Context, tx pgx.Tx, findings []Finding) ([]Finding, error)
	LockFindingTx(ctx context.Context, tx pgx.Tx, id string) (Finding, error)
	FindingsForDevicesTx(ctx context.Context, tx pgx.Tx, advisoryID string, deviceIDs []string) ([]Finding, error)
	LockFindingsByIDsTx(ctx context.Context, tx pgx.Tx, ids []string) ([]Finding, error)
	// LockFindingsOfAdvisoryTx locks and returns every finding of the Advisory.
	LockFindingsOfAdvisoryTx(ctx context.Context, tx pgx.Tx, advisoryID string) ([]Finding, error)
	FindingsOfAdvisory(ctx context.Context, advisoryID string) ([]Finding, error)
	// UpdateFindingTx writes every mutable column and bumps the version.
	UpdateFindingTx(ctx context.Context, tx pgx.Tx, f Finding) (Finding, error)
	// ObserveFindingTx writes confidence, installed version and observation times; the version is bumped
	// only when the confidence or the installed version changed.
	ObserveFindingTx(ctx context.Context, tx pgx.Tx, f Finding) error
	ObserveFindingsTx(ctx context.Context, tx pgx.Tx, findings []Finding) error
	GetFinding(ctx context.Context, id string) (Finding, error)
	ListFindings(ctx context.Context, f FindingFilter) (Result[Finding], error)
	Summary(ctx context.Context, advisoryID string) (Summary, error)

	InsertFindingTransitionTx(ctx context.Context, tx pgx.Tx, t Transition) error
	FindingTransitions(ctx context.Context, findingID string, page Page) (Result[Transition], error)
}

// Installation is an observed software installation (endpoints/public).
type Installation struct {
	ID                string
	DeviceID          string
	DevicePlatform    string
	SoftwareProductID string
	RawVersion        string
	ObservedAt        time.Time
	Retired           bool
	RetiredAt         *time.Time
	DeviceRetired     bool
}

// SoftwareProduct is a normalized Software Product (endpoints/public).
type SoftwareProduct struct {
	ID        string
	Name      string
	Publisher string
}

// Inventory is the Endpoints contract (endpoints/public adapter). It never writes endpoint data.
type Inventory interface {
	// InstallationsByProducts pages the current installations (live Devices only) of the products.
	InstallationsByProducts(ctx context.Context, productIDs []string, cursor string, limit int) ([]Installation, string, error)
	// InstallationsOnDevices lists every installation (retired ones included) of the products on the devices.
	InstallationsOnDevices(ctx context.Context, deviceIDs, productIDs []string) ([]Installation, bool, error)
	DeviceRetired(ctx context.Context, deviceIDs []string) (map[string]*time.Time, error)
	DeviceNames(ctx context.Context, deviceIDs []string) (map[string]string, error)
	LatestIngestionAt(ctx context.Context) (*time.Time, error)
	LatestObservedByProducts(ctx context.Context, productIDs []string) (map[string]time.Time, error)
	SoftwareProducts(ctx context.Context, ids []string) (map[string]SoftwareProduct, error)
	// FindSoftwareProduct resolves a product name to a product by exact name or alias (method product|alias).
	FindSoftwareProduct(ctx context.Context, name, publisher string) (p SoftwareProduct, method string, found bool, err error)
}

// Directory is what Security asks the Organization module.
type Directory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
}

// PermissionResolver returns the effective permissions of a User (platform/authorization/roles).
type PermissionResolver interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// PermissionHolders lists candidate holders of a permission (platform/authorization/roles): Users with a
// direct role assignment that grants it, by id after the cursor.
type PermissionHolders interface {
	ActiveUsers(ctx context.Context, after string, limit int) ([]string, error)
}

// Notifier creates notifications inside the caller's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}
