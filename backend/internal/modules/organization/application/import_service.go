package application

import (
	"context"
	"strings"
	"time"
)

// Imports validates and runs CSV imports, bulk operations, directory linking and access extension (F14 section 1.6,
// ADR-0034 R5 and section 6).
type Imports struct {
	store BatchStore
	now   func() time.Time
}

func NewImports(store BatchStore) *Imports { return &Imports{store: store, now: time.Now} }

// WithClock replaces the clock (tests).
func (i *Imports) WithClock(now func() time.Time) *Imports { i.now = now; return i }

// Purge job of expired previews.
const (
	ImportPurgeJobType    = "organization.import.purge"
	ImportPurgeJobTimeout = time.Minute
	ImportPurgeInterval   = 5 * time.Minute
)

// Access extension (ADR-0034 point 4): an explicit, audited operation with a closed list of reason codes.
const (
	// MaxAccessDays is the longest an external account's access may run from the moment of an extension.
	MaxAccessDays = 365
)

var extendReasons = []string{"contract_renewed", "project_extended", "sponsor_request", "correction"}

// ExtendReasons returns the allowed reason codes of ExtendAccess.
func ExtendReasons() []string { return append([]string(nil), extendReasons...) }

// PreviewImport parses the CSV and stores a dry-run preview. The caller needs organization.import (CSV upload) and
// the manage permission of the kind.
func (i *Imports) PreviewImport(ctx context.Context, c Caller, kind, matchKey, mode string, file []byte) (BatchPreview, error) {
	if err := c.validate(); err != nil {
		return BatchPreview{}, err
	}
	parsed, err := ParseImport(kind, matchKey, mode, file)
	if err != nil {
		return BatchPreview{}, err
	}
	if !c.can(PermImport) || !c.can(RequiredPermission(kind)) {
		return BatchPreview{}, ErrForbidden
	}
	return i.store.PreviewImport(ctx, c, parsed)
}

// PreviewBulk stores the dry run of a bulk operation on Users.
func (i *Imports) PreviewBulk(ctx context.Context, c Caller, in BulkInput) (BatchPreview, error) {
	if err := c.validate(); err != nil {
		return BatchPreview{}, err
	}
	ids, err := ValidBulkInput(in)
	if err != nil {
		return BatchPreview{}, err
	}
	if !c.can(PermUsersManage) {
		return BatchPreview{}, ErrForbidden
	}
	for _, id := range []*string{in.DepartmentID, in.LocationID, in.ManagerUserID} {
		if id != nil && !validUUID(strings.ToLower(*id)) {
			return BatchPreview{}, invalid("department, location and manager ids must be UUIDs")
		}
	}
	return i.store.PreviewBulk(ctx, c, in, ids)
}

func (i *Imports) Get(ctx context.Context, c Caller, id string) (Batch, error) {
	if err := c.validate(); err != nil {
		return Batch{}, err
	}
	return i.store.GetBatch(ctx, c, id)
}

// Rows pages the stored rows of a batch (Limit defaults to 50 and is capped at 200; After is a row number).
func (i *Imports) Rows(ctx context.Context, c Caller, id string, f RowFilter) ([]BatchRow, string, error) {
	if err := c.validate(); err != nil {
		return nil, "", err
	}
	switch f.Action {
	case "", RowCreate, RowUpdate, RowUnchanged, RowReject:
	default:
		return nil, "", invalid("action must be create, update, unchanged or reject")
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	return i.store.ListBatchRows(ctx, c, id, f)
}

// Apply writes a stored preview. previewHash is the hash the preview returned and expectedRejects the number of
// rejected rows the user saw; both must match the stored preview.
func (i *Imports) Apply(ctx context.Context, c Caller, id, previewHash string, expectedRejects int) (BatchApplied, error) {
	if err := c.validate(); err != nil {
		return BatchApplied{}, err
	}
	if len(previewHash) != 64 || expectedRejects < 0 {
		return BatchApplied{}, invalid("previewHash and expectedRejects are required")
	}
	return i.store.ApplyBatch(ctx, c, id, previewHash, expectedRejects)
}

// LinkDirectoryIdentity attaches a directory identity, found in the conflict list of a synchronization run, to a
// local account (review rule R5: platform administrator only; atomic; refused while the User holds roles).
func (i *Imports) LinkDirectoryIdentity(ctx context.Context, c Caller, id string, version int, runID, externalID string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if !c.PlatformAdmin {
		return User{}, ErrAdminRequired
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	if !validUUID(strings.ToLower(runID)) || strings.TrimSpace(externalID) == "" || len(externalID) > 512 {
		return User{}, invalid("runId and externalId are required")
	}
	return i.store.LinkDirectoryIdentity(ctx, c, id, version, strings.ToLower(runID), externalID)
}

// ExtendAccess moves the access end of an external account to a later date, at most MaxAccessDays from now.
func (i *Imports) ExtendAccess(ctx context.Context, c Caller, id string, version int, until time.Time, reason string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if !c.ExternalPartiesManage {
		return User{}, ErrForbidden
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	found := false
	for _, r := range extendReasons {
		found = found || r == reason
	}
	if !found {
		return User{}, invalid("reason must be one of: %s", strings.Join(extendReasons, ", "))
	}
	now := i.now().UTC()
	until = until.UTC().Truncate(time.Microsecond)
	if until.IsZero() || !until.After(now) {
		return User{}, invalid("accessExpiresAt must be in the future")
	}
	if until.After(now.AddDate(0, 0, MaxAccessDays)) {
		return User{}, invalid("accessExpiresAt must be at most %d days from now", MaxAccessDays)
	}
	return i.store.ExtendAccess(ctx, c, id, version, until, reason)
}

// CanView reports whether the caller may read stored previews of a batch kind (the manage permission of the kind).
func (c Caller) CanView(kind string) bool {
	p := RequiredPermission(kind)
	return p != "" && c.can(p)
}

// CanApply reports whether the caller may preview and apply a batch kind: the manage permission of the kind and, for
// CSV imports, organization.import.
func (c Caller) CanApply(kind string) bool {
	if !c.CanView(kind) {
		return false
	}
	return kind == BatchBulkUsers || c.can(PermImport)
}
