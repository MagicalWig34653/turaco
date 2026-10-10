package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

// Audit actions.
const (
	ActionUploaded   = "platform.attachment.uploaded"
	ActionDownloaded = "platform.attachment.downloaded"
	ActionDeleted    = "platform.attachment.deleted"
	ActionScanned    = "platform.attachment.scanned"
)

// Job types handled by the worker (see scan.go).
const (
	ScanJobType  = "platform.attachments.scan"
	PurgeJobType = "platform.attachments.purge"
	scanNowKey   = "platform.attachments.scan.now"
)

// Service is the attachment service. Owners are registered once at composition time.
type Service struct {
	pool    *pgxpool.Pool
	objects Objects
	scanner Scanner
	policy  Policy
	owners  map[string]Owner
	logger  *slog.Logger
}

// New builds the service. The scanner is required: attachments are only released after a scan.
func New(pool *pgxpool.Pool, objects Objects, scanner Scanner, policy Policy, logger *slog.Logger) *Service {
	return &Service{pool: pool, objects: objects, scanner: scanner, policy: policy, owners: map[string]Owner{}, logger: logger}
}

// RegisterOwner registers the authorization source of an owner type.
func (s *Service) RegisterOwner(ownerType string, o Owner) error {
	if ownerType == "" || o == nil {
		return errors.New("attachments: owner type and owner are required")
	}
	if _, dup := s.owners[ownerType]; dup {
		return fmt.Errorf("attachments: owner type %q registered twice", ownerType)
	}
	s.owners[ownerType] = o
	return nil
}

// Policy returns the upload policy (limits for the UI).
func (s *Service) Policy() Policy { return s.policy }

// access resolves the caller's rights on the owner; unknown types and owners are ErrNotFound.
func (s *Service) access(ctx context.Context, p authorization.Principal, ownerType, ownerID string) (Access, error) {
	o, ok := s.owners[ownerType]
	if !ok || p.UserID == "" {
		return Access{}, ErrNotFound
	}
	if _, err := uuid.Parse(ownerID); err != nil {
		return Access{}, ErrNotFound
	}
	a, err := o.Access(ctx, p, ownerID)
	if errors.Is(err, ErrOwnerNotFound) || (err == nil && !a.Read) {
		return Access{}, ErrNotFound
	}
	return a, err
}

// UploadInput is one upload. Content is read once, streaming.
type UploadInput struct {
	OwnerType    string
	OwnerID      string
	Audience     string
	FileName     string
	DeclaredType string
	Content      io.Reader
}

const columns = `id::text, owner_type, owner_id::text, file_name, content_type, size_bytes, sha256, scan_status, audience, uploaded_by::text, created_at, scanned_at`

func scan(row pgx.Row) (Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.OwnerType, &a.OwnerID, &a.FileName, &a.ContentType, &a.SizeBytes, &a.SHA256, &a.ScanStatus, &a.Audience, &a.UploadedBy, &a.CreatedAt, &a.ScannedAt)
	return a, err
}

// Upload stores a file for an owner the caller may attach to. The object is encrypted and written first, the row
// (status pending), the audit event and the scan job follow in one transaction; a failed transaction removes the
// object again.
func (s *Service) Upload(ctx context.Context, p authorization.Principal, correlationID string, in UploadInput) (Attachment, error) {
	acc, err := s.access(ctx, p, in.OwnerType, in.OwnerID)
	if err != nil {
		return Attachment{}, err
	}
	if !acc.Attach {
		return Attachment{}, ErrForbidden
	}
	if in.Audience == "" {
		in.Audience = AudienceAll
	}
	switch in.Audience {
	case AudienceAll:
	case AudiencePrivileged:
		if !acc.Privileged || !acc.Attach {
			return Attachment{}, ErrForbidden
		}
	default:
		return Attachment{}, &InvalidError{Message: "audience must be all or privileged"}
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM platform.attachments WHERE owner_type = $1 AND owner_id = $2::uuid AND deleted_at IS NULL`, in.OwnerType, in.OwnerID).Scan(&n); err != nil {
		return Attachment{}, fmt.Errorf("count attachments: %w", err)
	}
	if n >= MaxPerOwner {
		return Attachment{}, ErrLimit
	}

	head := make([]byte, HeadSize)
	hn, err := io.ReadFull(in.Content, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Attachment{}, err
	}
	head = head[:hn]
	if hn == 0 {
		return Attachment{}, &InvalidError{Message: "the file is empty"}
	}
	ctype, err := s.policy.Check(in.DeclaredType, head)
	if err != nil {
		return Attachment{}, err
	}
	objectID, err := storage.NewID()
	if err != nil {
		return Attachment{}, err
	}
	h := sha256.New()
	size, err := s.objects.Put(ctx, objectID, io.TeeReader(io.MultiReader(bytes.NewReader(head), in.Content), h), s.policy.MaxBytes)
	if errors.Is(err, storage.ErrTooLarge) {
		return Attachment{}, ErrTooLarge
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var out Attachment
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('platform.attachments:' || $1::text, 0))`, in.OwnerID); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.attachments WHERE owner_type = $1 AND owner_id = $2::uuid AND deleted_at IS NULL`, in.OwnerType, in.OwnerID).Scan(&live); err != nil {
			return err
		}
		if live >= MaxPerOwner {
			return ErrLimit
		}
		out, err = scan(tx.QueryRow(ctx, `INSERT INTO platform.attachments (owner_type, owner_id, object_id, file_name, content_type, size_bytes, sha256, audience, uploaded_by)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9::uuid) RETURNING `+columns,
			in.OwnerType, in.OwnerID, objectID, CleanFileName(in.FileName), ctype, size, hex.EncodeToString(h.Sum(nil)), in.Audience, p.UserID))
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: ActionUploaded, TargetType: "attachment", TargetID: out.ID, Actor: audit.UserActor(p.UserID), CorrelationID: correlationID,
			Metadata: map[string]any{"ownerType": out.OwnerType, "ownerId": out.OwnerID, "sizeBytes": out.SizeBytes, "contentType": out.ContentType, "audience": out.Audience}}); err != nil {
			return err
		}
		_, _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{Type: ScanJobType, DedupeKey: scanNowKey, MaxAttempts: 5})
		return err
	})
	if err != nil {
		_ = s.objects.Delete(context.WithoutCancel(ctx), objectID)
		return Attachment{}, err
	}
	return out, nil
}

// visibleTo reports whether the caller may see the attachment given the rights on its owner.
func visibleTo(a Attachment, acc Access) bool {
	return acc.Read && (a.Audience == AudienceAll || acc.Privileged)
}

// List returns the live attachments of an owner the caller may read, oldest first. Privileged attachments are left
// out for callers who are not privileged.
func (s *Service) List(ctx context.Context, p authorization.Principal, ownerType, ownerID string) ([]Attachment, error) {
	acc, err := s.access(ctx, p, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM platform.attachments WHERE owner_type = $1 AND owner_id = $2::uuid AND deleted_at IS NULL
		AND ($3 OR audience = 'all') ORDER BY created_at, id`, ownerType, ownerID, acc.Privileged)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) load(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, p authorization.Principal, id string, forUpdate bool) (Attachment, Access, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Attachment{}, Access{}, ErrNotFound
	}
	sql := `SELECT ` + columns + ` FROM platform.attachments WHERE id = $1::uuid AND deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	a, err := scan(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, Access{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, Access{}, err
	}
	acc, err := s.access(ctx, p, a.OwnerType, a.OwnerID)
	if err != nil {
		return Attachment{}, Access{}, err
	}
	if !visibleTo(a, acc) {
		return Attachment{}, Access{}, ErrNotFound
	}
	return a, acc, nil
}

// Get returns the metadata of an attachment the caller may see.
func (s *Service) Get(ctx context.Context, p authorization.Principal, id string) (Attachment, error) {
	a, _, err := s.load(ctx, s.pool, p, id, false)
	return a, err
}

// Download returns the decrypted content of a clean attachment. The download is audited before any byte is
// returned. Pending, infected and failed attachments are NotAvailableError.
func (s *Service) Download(ctx context.Context, p authorization.Principal, correlationID, id string) (Attachment, io.ReadCloser, error) {
	a, _, err := s.load(ctx, s.pool, p, id, false)
	if err != nil {
		return Attachment{}, nil, err
	}
	if !a.Downloadable() {
		return Attachment{}, nil, &NotAvailableError{Status: a.ScanStatus}
	}
	var objectID string
	if err := s.pool.QueryRow(ctx, `SELECT object_id FROM platform.attachments WHERE id = $1::uuid`, id).Scan(&objectID); err != nil {
		return Attachment{}, nil, err
	}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: ActionDownloaded, TargetType: "attachment", TargetID: a.ID, Actor: audit.UserActor(p.UserID), CorrelationID: correlationID,
			Metadata: map[string]any{"ownerType": a.OwnerType, "ownerId": a.OwnerID, "sizeBytes": a.SizeBytes}})
	}); err != nil {
		return Attachment{}, nil, err
	}
	rc, err := s.objects.Open(ctx, objectID)
	if err != nil {
		return Attachment{}, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return a, rc, nil
}

// Delete removes an attachment: the uploader while they may still attach, callers who manage the owner always. The row is
// marked deleted and audited; the worker removes the object (a best-effort attempt follows immediately).
func (s *Service) Delete(ctx context.Context, p authorization.Principal, correlationID, id string) error {
	var objectID string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		a, acc, err := s.load(ctx, tx, p, id, true)
		if err != nil {
			return err
		}
		if !acc.Manage && !(a.UploadedBy == p.UserID && acc.Attach) {
			return ErrForbidden
		}
		if err := tx.QueryRow(ctx, `UPDATE platform.attachments SET deleted_at = now(), deleted_by = $2::uuid WHERE id = $1::uuid RETURNING object_id`, id, p.UserID).Scan(&objectID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: ActionDeleted, TargetType: "attachment", TargetID: a.ID, Actor: audit.UserActor(p.UserID), CorrelationID: correlationID,
			Metadata: map[string]any{"ownerType": a.OwnerType, "ownerId": a.OwnerID, "sizeBytes": a.SizeBytes, "scanStatus": a.ScanStatus}})
	})
	if err != nil {
		return err
	}
	s.purgeOne(context.WithoutCancel(ctx), id, objectID)
	return nil
}

func (s *Service) purgeOne(ctx context.Context, id, objectID string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.objects.Delete(ctx, objectID); err != nil {
		s.logger.Warn("attachment object delete deferred to the purge job", "attachmentId", id, "error", err)
		return
	}
	if _, err := s.pool.Exec(ctx, `UPDATE platform.attachments SET object_purged_at = now() WHERE id = $1::uuid AND object_purged_at IS NULL`, id); err != nil {
		s.logger.Warn("record attachment purge", "attachmentId", id, "error", err)
	}
}
