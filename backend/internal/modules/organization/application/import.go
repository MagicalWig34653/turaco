package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// CSV import and bulk operations (F14 design section 1.6): a stored dry-run preview first, then an all-or-nothing
// apply of that preview. This file holds the pure part: parsing, per-row validation and the vocabulary shared by the
// repository and the transport. The persistence (preview store, apply) lives in the repository because every row runs
// the same transactional operations as the single-record API.

// Batch kinds.
const (
	ImportUsers       = "users"
	ImportLocations   = "locations"
	ImportDepartments = "departments"
	// BatchBulkUsers is a bulk operation on selected Users, stored like an import so that it is previewed and applied
	// by the same mechanism.
	BatchBulkUsers = "bulk_users"
)

// Import modes.
const (
	ModeCreateOnly = "create_only"
	ModeUpdateOnly = "update_only"
	ModeUpsert     = "upsert"
)

// Row actions. Bulk previews use "update" for "would change".
const (
	RowCreate    = "create"
	RowUpdate    = "update"
	RowUnchanged = "unchanged"
	RowReject    = "reject"
)

// Match keys.
const (
	KeyPrimaryEmail   = "primary_email"
	KeyEmployeeNumber = "employee_number"
	KeyCode           = "code"
)

// Bulk operations on Users.
const (
	BulkSetDepartment      = "set_department"
	BulkSetPrimaryLocation = "set_primary_location"
	BulkSetManager         = "set_manager"
	BulkDeactivate         = "deactivate"
)

// Limits (design 1.6). A bulk operation addresses at most MaxBulkUsers Users.
const (
	MaxImportBytes   = 2 << 20
	MaxImportRows    = 5000
	MaxImportColumns = 20
	MaxCellLength    = 500
	MaxBulkUsers     = 500
	// PreviewTTL is how long a stored preview (it contains personal data) stays valid.
	PreviewTTL = time.Hour
)

// Permissions checked inside the operations (the transport only authenticates and requires the coarse permission).
const (
	PermImport           = "organization.import"
	PermUsersManage      = "organization.users.manage"
	PermLocationsManage  = "organization.locations.manage"
	PermDepartmentManage = "organization.departments.manage"
)

// Errors of the import and bulk operations. The transport maps each to one API error code.
var (
	// ErrImportTooLarge: the file is larger than MaxImportBytes.
	ErrImportTooLarge = errors.New("organization: import file too large")
	// ErrImportTooManyRows: more than MaxImportRows data rows.
	ErrImportTooManyRows = errors.New("organization: too many import rows")
	// ErrImportStale: the data changed since the preview (a row version, a key that now exists, an expired preview);
	// the preview must be repeated.
	ErrImportStale = errors.New("organization: the preview is stale")
	// ErrImportHashMismatch: previewHash or expectedRejects does not match the stored preview.
	ErrImportHashMismatch = errors.New("organization: preview hash mismatch")
	// ErrAdminRequired: the operation is for platform administrators only.
	ErrAdminRequired = errors.New("organization: platform administrator required")
	// ErrDirectoryLinkRoles: link-directory-identity is refused while the User holds roles (review rule R5).
	ErrDirectoryLinkRoles = errors.New("organization: the user holds roles")
	// ErrDirectoryIdentityInUse: the directory identity is already attached to a User.
	ErrDirectoryIdentityInUse = errors.New("organization: the directory identity is already linked")
)

// ImportRowError is returned by apply when one row fails; the whole apply is rolled back.
type ImportRowError struct {
	Row   int
	Cause error
}

func (e *ImportRowError) Error() string { return "organization: import row failed: " + e.Cause.Error() }
func (e *ImportRowError) Unwrap() error { return e.Cause }

// RowIssue is a machine-readable finding on a row; the UI translates the code.
type RowIssue struct {
	Field string `json:"field,omitempty"`
	Code  string `json:"code"`
}

// Issue codes produced by the parser. The repository adds the codes of the operations (see IssueCodeOf).
const (
	IssueMissingKey          = "missing_key"
	IssueInvalidValue        = "invalid_value"
	IssueControlCharacters   = "control_characters"
	IssueFormulaPrefix       = "formula_prefix"
	IssueDuplicateKey        = "duplicate_key_in_file"
	IssueColumnCount         = "column_count"
	IssueCellTooLong         = "cell_too_long"
	IssueDisplayNameRequired = "display_name_required"
)

// ParsedRow is one data row after parsing. Data holds the non-empty known columns; an empty cell means "leave
// unchanged" (an import never clears a value).
type ParsedRow struct {
	No       int
	Key      string
	Data     map[string]string
	Errors   []RowIssue
	Warnings []RowIssue
}

// ParsedImport is a validated file.
type ParsedImport struct {
	Kind           string
	MatchKey       string
	Mode           string
	FileHash       string
	UnknownColumns []string
	Rows           []ParsedRow
}

var knownColumns = map[string][]string{
	ImportUsers:       {"display_name", "given_name", "family_name", "primary_email", "employee_number", "department_code", "location_code", "manager_email"},
	ImportLocations:   {"code", "name", "kind", "parent_code", "description"},
	ImportDepartments: {"code", "name", "parent_code"},
}

// ImportColumns returns the known columns of an import kind.
func ImportColumns(kind string) []string { return append([]string(nil), knownColumns[kind]...) }

// ValidImportOptions checks kind, match key and mode together and returns the effective match key.
func ValidImportOptions(kind, matchKey, mode string) (string, error) {
	if _, ok := knownColumns[kind]; !ok {
		return "", invalid("kind must be users, locations or departments")
	}
	switch mode {
	case ModeCreateOnly, ModeUpdateOnly, ModeUpsert:
	default:
		return "", invalid("mode must be create_only, update_only or upsert")
	}
	if kind == ImportUsers {
		if matchKey != KeyPrimaryEmail && matchKey != KeyEmployeeNumber {
			return "", invalid("matchKey must be primary_email or employee_number for users")
		}
		return matchKey, nil
	}
	if matchKey != "" && matchKey != KeyCode {
		return "", invalid("matchKey must be code for locations and departments")
	}
	return KeyCode, nil
}

// ParseImport parses and validates a CSV file without touching the database. Findings that concern one row are
// stored on the row (action reject at preview time); a file that cannot be read at all is an error.
func ParseImport(kind, matchKey, mode string, file []byte) (ParsedImport, error) {
	matchKey, err := ValidImportOptions(kind, matchKey, mode)
	if err != nil {
		return ParsedImport{}, err
	}
	if len(file) > MaxImportBytes {
		return ParsedImport{}, ErrImportTooLarge
	}
	sum := sha256.Sum256(file)
	out := ParsedImport{Kind: kind, MatchKey: matchKey, Mode: mode, FileHash: hex.EncodeToString(sum[:])}

	text := bytes.TrimPrefix(file, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(text) {
		return ParsedImport{}, invalid("the file must be UTF-8 encoded")
	}
	if len(bytes.TrimSpace(text)) == 0 {
		return ParsedImport{}, invalid("the file is empty")
	}
	reader := csv.NewReader(bytes.NewReader(text))
	reader.Comma = detectDelimiter(text)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return ParsedImport{}, invalid("the header row could not be read")
	}
	known := map[string]bool{}
	for _, c := range knownColumns[kind] {
		known[c] = true
	}
	if len(header) > MaxImportColumns {
		return ParsedImport{}, invalid("at most %d columns are supported", MaxImportColumns)
	}
	cols := make([]string, len(header))
	seen := map[string]bool{}
	for i, h := range header {
		name := normalizeHeader(h)
		if name == "" {
			return ParsedImport{}, invalid("column %d has no name", i+1)
		}
		if seen[name] {
			return ParsedImport{}, invalid("column %s appears twice", name)
		}
		seen[name] = true
		cols[i] = name
		if !known[name] {
			out.UnknownColumns = append(out.UnknownColumns, name)
		}
	}
	if !seen[matchKey] {
		return ParsedImport{}, invalid("the column %s is required", matchKey)
	}

	keys := map[string][]int{} // lower-case key -> row indexes
	for {
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ParsedImport{}, invalid("the file is not valid CSV")
		}
		if allEmpty(rec) {
			continue
		}
		if len(out.Rows) >= MaxImportRows {
			return ParsedImport{}, ErrImportTooManyRows
		}
		row := ParsedRow{No: len(out.Rows) + 1, Data: map[string]string{}}
		if len(rec) != len(cols) {
			row.Errors = append(row.Errors, RowIssue{Code: IssueColumnCount})
			out.Rows = append(out.Rows, row)
			continue
		}
		cells := map[string]string{}
		for i, c := range cols {
			if known[c] {
				cells[c] = strings.TrimSpace(rec[i])
			}
		}
		normalizeRow(kind, matchKey, cells, &row)
		if row.Key != "" {
			k := strings.ToLower(row.Key)
			keys[k] = append(keys[k], len(out.Rows))
		}
		out.Rows = append(out.Rows, row)
	}
	if len(out.Rows) == 0 {
		return ParsedImport{}, invalid("the file has no data rows")
	}
	for _, idx := range keys {
		if len(idx) > 1 {
			for _, i := range idx {
				out.Rows[i].Errors = append(out.Rows[i].Errors, RowIssue{Field: matchKey, Code: IssueDuplicateKey})
			}
		}
	}
	sort.Strings(out.UnknownColumns)
	return out, nil
}

func allEmpty(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func normalizeHeader(h string) string {
	h = strings.TrimPrefix(h, "\ufeff")
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(h)
}

// detectDelimiter picks comma or semicolon from the header line (outside quotes); comma wins a tie.
func detectDelimiter(text []byte) rune {
	line := text
	if i := bytes.IndexByte(text, '\n'); i >= 0 {
		line = text[:i]
	}
	commas, semis, quoted := 0, 0, false
	for _, b := range line {
		switch {
		case b == '"':
			quoted = !quoted
		case quoted:
		case b == ',':
			commas++
		case b == ';':
			semis++
		}
	}
	if semis > commas {
		return ';'
	}
	return ','
}

// normalizeRow validates the cells of one row with the same text rules as the single operations and stores the
// non-empty values.
func normalizeRow(kind, matchKey string, cells map[string]string, row *ParsedRow) {
	put := func(field string, v string, max int, check func(string) error) {
		if v == "" {
			return
		}
		if utf8.RuneCountInString(v) > MaxCellLength {
			row.Errors = append(row.Errors, RowIssue{Field: field, Code: IssueCellTooLong})
			return
		}
		if safetext.ContainsUnsafe(v, false) {
			row.Errors = append(row.Errors, RowIssue{Field: field, Code: IssueControlCharacters})
			return
		}
		if check != nil {
			if err := check(v); err != nil {
				row.Errors = append(row.Errors, RowIssue{Field: field, Code: IssueInvalidValue})
				return
			}
		}
		if utf8.RuneCountInString(v) > max {
			row.Errors = append(row.Errors, RowIssue{Field: field, Code: IssueInvalidValue})
			return
		}
		if strings.ContainsAny(v[:1], "=+-@\t\r") {
			row.Warnings = append(row.Warnings, RowIssue{Field: field, Code: IssueFormulaPrefix})
		}
		row.Data[field] = v
	}
	email := func(v string) error { _, err := cleanEmail(&v); return err }
	switch kind {
	case ImportUsers:
		put("display_name", cells["display_name"], maxPersonNameLength, nil)
		put("given_name", cells["given_name"], maxPersonNameLength, nil)
		put("family_name", cells["family_name"], maxPersonNameLength, nil)
		put("primary_email", cells["primary_email"], 254, email)
		put("employee_number", cells["employee_number"], maxCodeLength, nil)
		put("department_code", cells["department_code"], maxCodeLength, nil)
		put("location_code", cells["location_code"], maxCodeLength, nil)
		put("manager_email", cells["manager_email"], 254, email)
	case ImportLocations:
		put("code", cells["code"], maxCodeLength, nil)
		put("name", cells["name"], maxOrgNameLength, nil)
		put("kind", strings.ToLower(cells["kind"]), 10, func(v string) error {
			if v != LocationSite && v != LocationArea {
				return errors.New("kind")
			}
			return nil
		})
		put("parent_code", cells["parent_code"], maxCodeLength, nil)
		put("description", cells["description"], maxLocationDescr, nil)
	case ImportDepartments:
		put("code", cells["code"], maxCodeLength, nil)
		put("name", cells["name"], maxOrgNameLength, nil)
		put("parent_code", cells["parent_code"], maxCodeLength, nil)
	}
	row.Key = row.Data[matchKey]
	if row.Key == "" && !hasIssueField(row.Errors, matchKey) {
		row.Errors = append(row.Errors, RowIssue{Field: matchKey, Code: IssueMissingKey})
	}
}

func hasIssueField(issues []RowIssue, field string) bool {
	for _, i := range issues {
		if i.Field == field {
			return true
		}
	}
	return false
}

// ---- bulk operations ----

// BulkInput describes a bulk operation on Users.
type BulkInput struct {
	Operation string
	UserIDs   []string
	// Exactly the parameter of the operation is used: DepartmentID, LocationID, ManagerUserID (nil clears) or Reason.
	DepartmentID  *string
	LocationID    *string
	ManagerUserID *string
	Reason        string
}

// ValidBulkInput checks the shape of a bulk request and returns the de-duplicated, ordered ids.
func ValidBulkInput(in BulkInput) ([]string, error) {
	switch in.Operation {
	case BulkSetDepartment, BulkSetPrimaryLocation, BulkSetManager:
	case BulkDeactivate:
		found := false
		for _, r := range statusReasons[OpDeactivate] {
			found = found || r == in.Reason
		}
		if !found {
			return nil, invalid("reason must be one of: %s", strings.Join(statusReasons[OpDeactivate], ", "))
		}
	default:
		return nil, invalid("operation must be set_department, set_primary_location, set_manager or deactivate")
	}
	if len(in.UserIDs) == 0 || len(in.UserIDs) > MaxBulkUsers {
		return nil, invalid("userIds must contain between 1 and %d ids", MaxBulkUsers)
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range in.UserIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if !validUUID(id) {
			return nil, invalid("userIds must be UUIDs")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
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
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f'):
			return false
		}
	}
	return true
}

// RequiredPermission is the manage permission a batch kind needs, both at preview and at apply.
func RequiredPermission(kind string) string {
	switch kind {
	case ImportUsers, BatchBulkUsers:
		return PermUsersManage
	case ImportLocations:
		return PermLocationsManage
	case ImportDepartments:
		return PermDepartmentManage
	}
	return ""
}

// ---- stored batch ----

// BatchRow is a stored or previewed row.
type BatchRow struct {
	No            int                  `json:"row"`
	Action        string               `json:"action"`
	Key           string               `json:"key"`
	Diff          map[string]DiffValue `json:"diff"`
	Errors        []RowIssue           `json:"errors"`
	Warnings      []RowIssue           `json:"warnings"`
	TargetID      string               `json:"-"`
	TargetVersion int                  `json:"-"`
	Data          map[string]string    `json:"-"`
	Label         string               `json:"label,omitempty"`
}

// DiffValue is one field's change.
type DiffValue struct {
	From *string `json:"from"`
	To   *string `json:"to"`
}

// Batch is the stored preview of an import or bulk operation.
type Batch struct {
	ID             string
	Kind           string
	MatchKey       string
	Mode           string
	Operation      string
	FileHash       string
	PreviewHash    string
	RowCount       int
	Counts         map[string]int
	UnknownColumns []string
	CorrelationID  string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	Status         string
	AppliedAt      *time.Time
	AppliedCounts  map[string]int
}

// BatchPreview is the result of a preview: the batch and its first rows.
type BatchPreview struct {
	Batch Batch
	Rows  []BatchRow
	Next  string
}

// BatchApplied is the result of an apply. Replayed is true when the batch had been applied before (idempotent).
type BatchApplied struct {
	Batch    Batch
	Rows     []BatchRow
	Replayed bool
}

// RowFilter pages the rows of a batch.
type RowFilter struct {
	Action string
	After  int
	Limit  int
}

// BatchStore is the persistence port of imports and bulk operations. Every method authorizes against the caller's
// permissions inside the transaction; a batch is visible to its creator only (unknown and foreign batches are
// ErrNotFound).
type BatchStore interface {
	PreviewImport(ctx context.Context, c Caller, in ParsedImport) (BatchPreview, error)
	PreviewBulk(ctx context.Context, c Caller, in BulkInput, ids []string) (BatchPreview, error)
	GetBatch(ctx context.Context, c Caller, id string) (Batch, error)
	ListBatchRows(ctx context.Context, c Caller, id string, f RowFilter) ([]BatchRow, string, error)
	ApplyBatch(ctx context.Context, c Caller, id, previewHash string, expectedRejects int) (BatchApplied, error)
	PurgeExpiredBatches(ctx context.Context) (int, error)

	LinkDirectoryIdentity(ctx context.Context, c Caller, id string, version int, runID, externalID string) (User, error)
	ExtendAccess(ctx context.Context, c Caller, id string, version int, until time.Time, reason string) (User, error)
}
