package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Storage drivers (ADR-0037).
const (
	StorageDriverFilesystem = "filesystem"
	StorageDriverS3         = "s3"
)

// DefaultAttachmentMaxBytes is the default size cap of one attachment (25 MiB).
const DefaultAttachmentMaxBytes int64 = 25 << 20

// MaxAttachmentMaxBytes is the upper bound of ATTACHMENT_MAX_BYTES (100 MiB).
const MaxAttachmentMaxBytes int64 = 100 << 20

// StorageConfig is the file storage and attachment configuration (ADR-0037). The S3 settings stay in Config.
type StorageConfig struct {
	// Driver is "", "filesystem" or "s3"; empty disables attachments.
	Driver        string
	Path          string
	MasterKeyFile string
	// ClamAVAddress is host:port of clamd (INSTREAM over TCP).
	ClamAVAddress string
	MaxBytes      int64
	// AllowedTypes overrides the default content-type allow-list when non-empty.
	AllowedTypes []string
}

// Enabled reports whether attachments are configured.
func (c StorageConfig) Enabled() bool { return c.Driver != "" }

var storageDescriptors = []Descriptor{
	{Name: "STORAGE_DRIVER", Type: "string", Description: "File storage adapter for attachments (ADR-0037): `filesystem` (single servers and on-premises; needs STORAGE_PATH) or `s3` (uses the S3_* settings). Empty disables attachments. Setting it also requires STORAGE_MASTER_KEY_FILE and CLAMAV_ADDRESS. API and worker must share the same storage (a shared volume or the bucket)."},
	{Name: "STORAGE_PATH", Type: "string", Description: "Absolute base directory of the filesystem driver, created with mode 0700. Holds only encrypted objects named by generated ids. Required when STORAGE_DRIVER=filesystem; back it up with PostgreSQL."},
	{Name: "STORAGE_MASTER_KEY_FILE", Type: "string", Secret: true, Description: "Path to a file with the 32-byte master key as 64 hexadecimal characters (for example `openssl rand -hex 32`; a Docker secret). Wraps the per-object data keys of attachments; mounted into turaco-api and turaco-worker. Losing it makes every stored attachment unreadable, so keep a copy separate from the data backups. Required when STORAGE_DRIVER is set."},
	{Name: "CLAMAV_ADDRESS", Type: "string", Description: "host:port of the ClamAV daemon (clamd, TCP, INSTREAM protocol) in its own container. New attachments stay `pending` and are not downloadable until the worker scanned them clean; an unreachable scanner leaves them pending and platform health reports it. Required when STORAGE_DRIVER is set. clamd StreamMaxLength must be at least ATTACHMENT_MAX_BYTES."},
	{Name: "ATTACHMENT_MAX_BYTES", Type: "int", Default: "26214400", Description: "Maximum size of one attachment in bytes (1024 to 104857600), enforced while streaming the upload."},
	{Name: "ATTACHMENT_ALLOWED_TYPES", Type: "string", Description: "Comma-separated content types accepted for attachments, replacing the default list (`application/pdf`, `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `text/plain`, `text/csv`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document`, `...spreadsheetml.sheet`, `...presentationml.presentation`, `application/zip`). Unsupported types stop startup. The first bytes of every upload must match the type."},
}

func init() { Registry = append(Registry, storageDescriptors...) }

// LoadStorage reads and validates the storage configuration. With STORAGE_DRIVER empty everything else is ignored.
func LoadStorage() (StorageConfig, error) {
	c := StorageConfig{
		Driver:        strings.TrimSpace(os.Getenv("STORAGE_DRIVER")),
		Path:          os.Getenv("STORAGE_PATH"),
		MasterKeyFile: os.Getenv("STORAGE_MASTER_KEY_FILE"),
		ClamAVAddress: strings.TrimSpace(os.Getenv("CLAMAV_ADDRESS")),
		MaxBytes:      DefaultAttachmentMaxBytes,
		AllowedTypes:  splitList(os.Getenv("ATTACHMENT_ALLOWED_TYPES")),
	}
	if c.Driver == "" {
		return StorageConfig{}, nil
	}
	if c.Driver != StorageDriverFilesystem && c.Driver != StorageDriverS3 {
		return StorageConfig{}, fmt.Errorf("STORAGE_DRIVER must be %q or %q", StorageDriverFilesystem, StorageDriverS3)
	}
	if c.Driver == StorageDriverFilesystem && (c.Path == "" || !filepath.IsAbs(c.Path)) {
		return StorageConfig{}, fmt.Errorf("STORAGE_PATH must be an absolute path when STORAGE_DRIVER=filesystem")
	}
	if c.MasterKeyFile == "" {
		return StorageConfig{}, fmt.Errorf("STORAGE_MASTER_KEY_FILE is required when STORAGE_DRIVER is set")
	}
	if c.ClamAVAddress == "" {
		return StorageConfig{}, fmt.Errorf("CLAMAV_ADDRESS is required when STORAGE_DRIVER is set: attachments are only released after a clean virus scan")
	}
	if i := strings.LastIndex(c.ClamAVAddress, ":"); i <= 0 || i == len(c.ClamAVAddress)-1 {
		return StorageConfig{}, fmt.Errorf("CLAMAV_ADDRESS must be host:port")
	}
	if v := os.Getenv("ATTACHMENT_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1024 || n > MaxAttachmentMaxBytes {
			return StorageConfig{}, fmt.Errorf("ATTACHMENT_MAX_BYTES must be an integer from 1024 to %d", MaxAttachmentMaxBytes)
		}
		c.MaxBytes = n
	}
	return c, nil
}
