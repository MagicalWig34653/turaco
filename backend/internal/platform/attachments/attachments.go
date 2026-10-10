// Package attachments is the platform Attachment concept (ADR-0037): metadata in platform.attachments, content in
// the encrypted storage port, a scan status driven by a virus scanner, and authorization delegated to the module
// that owns the thing a file is attached to. Modules register an Owner for their owner type; they never keep their
// own file tables.
package attachments

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
)

// Scan statuses.
const (
	ScanPending  = "pending"
	ScanClean    = "clean"
	ScanInfected = "infected"
	ScanFailed   = "failed"
)

// Audiences: "all" is visible to everyone who can read the owner, "privileged" only to callers the Owner reports
// as privileged (for example staff for the attachments of an internal comment).
const (
	AudienceAll        = "all"
	AudiencePrivileged = "privileged"
)

// MaxPerOwner is the number of live attachments one owner can hold.
const MaxPerOwner = 50

var (
	// ErrNotFound hides missing attachments, unknown owners and owners the caller may not see.
	ErrNotFound = errors.New("attachments: not found")
	// ErrForbidden means the caller may see the owner but may not perform the action.
	ErrForbidden = errors.New("attachments: not permitted")
	// ErrTooLarge means the upload exceeds the size cap.
	ErrTooLarge = errors.New("attachments: file too large")
	// ErrUnsupportedType means the content type is not allowed or does not match the content.
	ErrUnsupportedType = errors.New("attachments: unsupported content type")
	// ErrLimit means the owner holds the maximum number of attachments.
	ErrLimit = errors.New("attachments: attachment limit reached")
	// ErrRateLimited means the user started too many uploads within the last hour.
	ErrRateLimited = errors.New("attachments: upload rate limit reached")
	// ErrUserQuota means the user's attachment quota is exhausted.
	ErrUserQuota = errors.New("attachments: user storage quota exceeded")
	// ErrInstallationQuota means the installation's attachment quota is exhausted.
	ErrInstallationQuota = errors.New("attachments: installation storage quota exceeded")
	// ErrNotAvailable means the content is not downloadable (see NotAvailableError for the scan status).
	ErrNotAvailable = errors.New("attachments: content not available")
	// ErrUnavailable means the content could not be read from storage.
	ErrUnavailable = errors.New("attachments: storage unavailable")
	// ErrScannerUnavailable is returned by a Scanner that cannot reach its daemon; the attachment stays pending.
	ErrScannerUnavailable = errors.New("attachments: scanner unavailable")
)

// InvalidError is a malformed request.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return "attachments: " + e.Message }

// NotAvailableError carries the scan status that blocks a download.
type NotAvailableError struct{ Status string }

func (e *NotAvailableError) Error() string {
	return "attachments: content not available (" + e.Status + ")"
}
func (e *NotAvailableError) Unwrap() error { return ErrNotAvailable }

// ErrOwnerNotFound is returned by an Owner for an owner that does not exist or that the caller may not see.
var ErrOwnerNotFound = errors.New("attachments: owner not found")

// Access is what a caller may do with the attachments of one owner.
type Access struct {
	Read   bool
	Attach bool
	// Privileged callers see attachments of the privileged audience (they may add them when they can attach).
	Privileged bool
	// Manage lets the caller remove any attachment of the owner, not only their own.
	Manage bool
}

// Owner is implemented by the module that owns an owner type. It is the only authorization source for the
// attachments of that type: the platform never reads module tables.
type Owner interface {
	// Access reports the caller's rights on the owner, or ErrOwnerNotFound.
	Access(ctx context.Context, p authorization.Principal, ownerID string) (Access, error)
}

// Verdict is a scan result.
type Verdict struct {
	Clean     bool
	Signature string
}

// Scanner is the virus scan port (ClamAV adapter in integrations/clamav).
type Scanner interface {
	// Scan reads the stream completely. ErrScannerUnavailable (wrapped) means the daemon could not be reached; any
	// other error is a permanent result for this content (for example a size limit).
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
}

// Objects is the encrypted object store (storage.Vault).
type Objects interface {
	Put(ctx context.Context, id string, r io.Reader, maxBytes int64) (int64, error)
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	Delete(ctx context.Context, id string) error
}

// Attachment is the metadata of one stored file.
type Attachment struct {
	ID          string
	OwnerType   string
	OwnerID     string
	FileName    string
	ContentType string
	SizeBytes   int64
	SHA256      string
	ScanStatus  string
	Audience    string
	UploadedBy  string
	CreatedAt   time.Time
	ScannedAt   *time.Time
}

// Downloadable reports whether the content may be served.
func (a Attachment) Downloadable() bool { return a.ScanStatus == ScanClean }
