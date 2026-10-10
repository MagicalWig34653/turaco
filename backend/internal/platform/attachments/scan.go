package attachments

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

// Scan and purge schedule constants.
const (
	ScanInterval    = time.Minute
	ScanJobTimeout  = 10 * time.Minute
	PurgeInterval   = time.Hour
	PurgeJobTimeout = 5 * time.Minute
	scanBatch       = 20
	purgeBatch      = 200
	// PendingStaleAfter is how long an attachment may wait for a scan before health reports a backlog.
	PendingStaleAfter = 10 * time.Minute
	// scanOne bounds the scan of a single attachment.
	scanOne = 5 * time.Minute
)

const (
	errScannerUnavailable = "scanner_unavailable"
	errObjectUnreadable   = "object_unreadable"
	errScanRefused        = "scan_refused"
)

// ScanHandler is the worker handler of ScanJobType: it scans pending attachments, oldest first. When the scanner is
// unreachable the job fails (and is retried with backoff) and the attachments stay pending.
func (s *Service) ScanHandler(ctx context.Context, _ jobs.Job) error {
	for {
		n, err := s.ScanPending(ctx, scanBatch)
		if err != nil {
			return err
		}
		if n < scanBatch {
			return nil
		}
	}
}

// PurgeHandler is the worker handler of PurgeJobType.
func (s *Service) PurgeHandler(ctx context.Context, _ jobs.Job) error {
	_, err := s.PurgeDeleted(ctx, purgeBatch)
	return err
}

// readErrReader remembers a read error so storage failures can be told apart from scanner failures.
type readErrReader struct {
	r   io.Reader
	err error
}

func (e *readErrReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		e.err = err
	}
	return n, err
}

// ScanPending scans up to limit pending attachments and returns how many it processed. A scanner outage returns
// an error after recording scanner_unavailable on the attachment; other attachments are not attempted.
func (s *Service) ScanPending(ctx context.Context, limit int) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, object_id FROM platform.attachments WHERE scan_status = 'pending' AND deleted_at IS NULL ORDER BY created_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("list pending attachments: %w", err)
	}
	type item struct{ id, object string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.object); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, it := range items {
		if err := s.scanOne(ctx, it.id, it.object); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

func (s *Service) scanOne(ctx context.Context, id, objectID string) error {
	ctx, cancel := context.WithTimeout(ctx, scanOne)
	defer cancel()
	rc, err := s.objects.Open(ctx, objectID)
	switch {
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, storage.ErrCorrupt), errors.Is(err, storage.ErrWrongKey):
		return s.finish(ctx, id, ScanFailed, "", errObjectUnreadable)
	case err != nil:
		return s.noteError(ctx, id, "storage_unavailable", fmt.Errorf("open attachment object: %w", err))
	}
	defer rc.Close()
	src := &readErrReader{r: rc}
	v, err := s.scanner.Scan(ctx, src)
	switch {
	case err == nil && v.Clean:
		return s.finish(ctx, id, ScanClean, "", "")
	case err == nil:
		return s.finish(ctx, id, ScanInfected, v.Signature, "")
	case src.err != nil && (errors.Is(src.err, storage.ErrCorrupt) || errors.Is(src.err, storage.ErrWrongKey)):
		return s.finish(ctx, id, ScanFailed, "", errObjectUnreadable)
	case src.err != nil:
		return s.noteError(ctx, id, "storage_unavailable", fmt.Errorf("read attachment object: %w", src.err))
	case errors.Is(err, ErrScannerUnavailable):
		return s.noteError(ctx, id, errScannerUnavailable, err)
	case ctx.Err() != nil:
		return s.noteError(ctx, id, errScannerUnavailable, ctx.Err())
	}
	// The scanner answered but refused this content (for example its stream size limit): permanent.
	return s.finish(ctx, id, ScanFailed, "", errScanRefused)
}

// noteError records a transient failure on a still-pending attachment and returns err for the job retry.
func (s *Service) noteError(ctx context.Context, id, code string, err error) error {
	_, uerr := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE platform.attachments SET scan_attempts = scan_attempts + 1, last_scan_error = $2 WHERE id = $1::uuid AND scan_status = 'pending'`, id, code)
	return errors.Join(err, uerr)
}

// finish moves a pending attachment to its final scan status; the change is audited by the system actor.
func (s *Service) finish(ctx context.Context, id, status, signature, errCode string) error {
	bg := context.WithoutCancel(ctx)
	return pgx.BeginFunc(bg, s.pool, func(tx pgx.Tx) error {
		var sig, ec any
		if signature != "" {
			sig = truncate(signature, 200)
		}
		if errCode != "" {
			ec = errCode
		}
		var ownerType, ownerID string
		err := tx.QueryRow(bg, `UPDATE platform.attachments SET scan_status = $2, scan_signature = $3, last_scan_error = $4, scanned_at = now(), scan_attempts = scan_attempts + 1
			WHERE id = $1::uuid AND scan_status = 'pending' AND deleted_at IS NULL RETURNING owner_type, owner_id::text`, id, status, sig, ec).Scan(&ownerType, &ownerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // scanned concurrently or deleted meanwhile
		}
		if err != nil {
			return err
		}
		return audit.Record(bg, tx, audit.Change{Action: ActionScanned, TargetType: "attachment", TargetID: id, Actor: audit.SystemActor("attachment-scan"), CorrelationID: uuid.NewString(),
			Metadata: map[string]any{"ownerType": ownerType, "ownerId": ownerID, "scanStatus": status, "signature": signature, "error": errCode}})
	})
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// PurgeDeleted removes the objects of deleted attachments (idempotent) and returns how many were removed.
func (s *Service) PurgeDeleted(ctx context.Context, limit int) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, object_id FROM platform.attachments WHERE deleted_at IS NOT NULL AND object_purged_at IS NULL ORDER BY deleted_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("list deleted attachments: %w", err)
	}
	type item struct{ id, object string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.object); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, it := range items {
		if err := s.objects.Delete(ctx, it.object); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE platform.attachments SET object_purged_at = now() WHERE id = $1::uuid AND object_purged_at IS NULL`, it.id); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// ScanStats are the stored facts platform health reads.
type ScanStats struct {
	Pending       int
	OldestPending *time.Time
	ScannerErrors int
	Failed        int
	Infected      int
	LastScannedAt *time.Time
	PendingPurge  int
}

// Stats reads the scan backlog from the database (no call to the scanner).
func (s *Service) Stats(ctx context.Context) (ScanStats, error) {
	var st ScanStats
	err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE scan_status = 'pending' AND deleted_at IS NULL),
		min(created_at) FILTER (WHERE scan_status = 'pending' AND deleted_at IS NULL),
		count(*) FILTER (WHERE scan_status = 'pending' AND deleted_at IS NULL AND last_scan_error IS NOT NULL),
		count(*) FILTER (WHERE scan_status = 'failed' AND deleted_at IS NULL),
		count(*) FILTER (WHERE scan_status = 'infected' AND deleted_at IS NULL),
		max(scanned_at),
		count(*) FILTER (WHERE deleted_at IS NOT NULL AND object_purged_at IS NULL)
		FROM platform.attachments`).Scan(&st.Pending, &st.OldestPending, &st.ScannerErrors, &st.Failed, &st.Infected, &st.LastScannedAt, &st.PendingPurge)
	return st, err
}

// CheckStorage probes the storage backend (a write, read and delete of a probe object).
func (s *Service) CheckStorage(ctx context.Context) error {
	c, ok := s.objects.(interface{ Check(context.Context) error })
	if !ok {
		return nil
	}
	return c.Check(ctx)
}

// StorageDriver names the storage adapter ("filesystem" or "s3").
func (s *Service) StorageDriver() string {
	if d, ok := s.objects.(interface{ Driver() string }); ok {
		return d.Driver()
	}
	return ""
}
