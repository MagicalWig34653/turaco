package attachments_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage/fsstore"
)

// fakeOwner grants rights per user id.
type fakeOwner struct {
	mu     sync.Mutex
	rights map[string]attachments.Access
	known  string
}

func (o *fakeOwner) Access(_ context.Context, p authorization.Principal, ownerID string) (attachments.Access, error) {
	if ownerID != o.known {
		return attachments.Access{}, attachments.ErrOwnerNotFound
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	a, ok := o.rights[p.UserID]
	if !ok || !a.Read {
		return attachments.Access{}, attachments.ErrOwnerNotFound
	}
	return a, nil
}

type fakeScanner struct {
	mu      sync.Mutex
	verdict attachments.Verdict
	err     error
	seen    [][]byte
}

func (f *fakeScanner) Scan(_ context.Context, r io.Reader) (attachments.Verdict, error) {
	b, err := io.ReadAll(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		return attachments.Verdict{}, err
	}
	f.seen = append(f.seen, b)
	return f.verdict, f.err
}

type env struct {
	svc     *attachments.Service
	pool    *pgxpool.Pool
	scanner *fakeScanner
	base    string
	ownerID string
	owner   *fakeOwner
	staff   authorization.Principal // read, attach, privileged
	author  authorization.Principal // read, attach
	reader  authorization.Principal // read only
	other   authorization.Principal // no rights
}

func newEnv(t *testing.T, maxBytes int64) *env { t.Helper(); return newEnvWith(t, maxBytes, nil) }

func newEnvWith(t *testing.T, maxBytes int64, tune func(*attachments.Policy)) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var ownerID, staffID, authorID, readerID, otherID string
	for _, p := range []*string{&ownerID, &staffID, &authorID, &readerID, &otherID} {
		if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(t.TempDir(), "store")
	fs, err := fsstore.New(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	vault, err := storage.NewVault(fs, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := attachments.NewPolicy(maxBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tune != nil {
		tune(&policy)
	}
	sc := &fakeScanner{verdict: attachments.Verdict{Clean: true}}
	svc := attachments.New(pool, vault, sc, policy, slog.New(slog.NewTextHandler(io.Discard, nil)))
	owner := &fakeOwner{known: ownerID, rights: map[string]attachments.Access{
		staffID:  {Read: true, Attach: true, Privileged: true, Manage: true},
		authorID: {Read: true, Attach: true},
		readerID: {Read: true},
	}}
	if err := svc.RegisterOwner("testdoc", owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1`, attachments.ScanJobType)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.attachments WHERE owner_id = $1::uuid`, ownerID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'attachment' AND actor_id IN ($1::uuid, $2::uuid, $3::uuid, $4::uuid)`, staffID, authorID, readerID, otherID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'attachment' AND metadata->>'ownerId' = $1`, ownerID)
	})
	pr := func(id string) authorization.Principal { return authorization.Principal{UserID: id} }
	return &env{owner: owner, svc: svc, pool: pool, scanner: sc, base: base, ownerID: ownerID, staff: pr(staffID), author: pr(authorID), reader: pr(readerID), other: pr(otherID)}
}

var pdf = []byte("%PDF-1.7\nhello attachment\n%%EOF")

func (e *env) upload(t *testing.T, p authorization.Principal, audience string, content []byte) (attachments.Attachment, error) {
	t.Helper()
	return e.svc.Upload(context.Background(), p, "corr-1", attachments.UploadInput{OwnerType: "testdoc", OwnerID: e.ownerID, Audience: audience,
		FileName: "../../evil name.pdf", DeclaredType: "application/pdf", Content: bytes.NewReader(content)})
}

func (e *env) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.audit_events WHERE action = $1 AND metadata->>'ownerId' = $2`, action, e.ownerID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUploadScanDownloadLifecycle(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, err := e.upload(t, e.author, "", pdf)
	if err != nil {
		t.Fatal(err)
	}
	if a.ScanStatus != attachments.ScanPending || a.Downloadable() || a.FileName != "evil name.pdf" || a.ContentType != "application/pdf" || a.SizeBytes != int64(len(pdf)) || len(a.SHA256) != 64 {
		t.Fatalf("unexpected upload result %+v", a)
	}
	// Pending content is never served.
	_, _, err = e.svc.Download(ctx, e.author, "c", a.ID)
	var na *attachments.NotAvailableError
	if !errors.As(err, &na) || na.Status != attachments.ScanPending {
		t.Fatalf("pending download: %v", err)
	}
	// The stored object holds no plaintext.
	var obj string
	_ = e.pool.QueryRow(ctx, `SELECT object_id FROM platform.attachments WHERE id = $1::uuid`, a.ID).Scan(&obj)
	raw, err := os.ReadFile(filepath.Join(e.base, obj[:2], obj[2:4], obj))
	if err != nil || bytes.Contains(raw, []byte("hello attachment")) {
		t.Fatalf("object on disk: %v", err)
	}
	// The upload enqueued a scan job.
	var jobs int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.jobs WHERE job_type = $1 AND status IN ('pending','processing')`, attachments.ScanJobType).Scan(&jobs)
	if jobs == 0 {
		t.Fatal("no scan job enqueued")
	}

	if n, err := e.svc.ScanPending(ctx, 50); err != nil || n < 1 {
		t.Fatalf("scan: n=%d err=%v", n, err)
	}
	if len(e.scanner.seen) == 0 || !bytes.Equal(e.scanner.seen[len(e.scanner.seen)-1], pdf) {
		t.Fatal("scanner did not receive the decrypted content")
	}
	a2, rc, err := e.svc.Download(ctx, e.reader, "c", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, pdf) || a2.ScanStatus != attachments.ScanClean || a2.ScannedAt == nil {
		t.Fatalf("download: %q %+v", got, a2)
	}
	for action, want := range map[string]int{attachments.ActionUploaded: 1, attachments.ActionScanned: 1, attachments.ActionDownloaded: 1} {
		if n := e.auditCount(t, action); n != want {
			t.Errorf("audit %s = %d, want %d", action, n, want)
		}
	}
}

func TestInfectedIsQuarantined(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	e.scanner.verdict = attachments.Verdict{Signature: "Eicar-Test-Signature"}
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.svc.Download(ctx, e.staff, "c", a.ID)
	var na *attachments.NotAvailableError
	if !errors.As(err, &na) || na.Status != attachments.ScanInfected {
		t.Fatalf("infected download: %v", err)
	}
	got, _ := e.svc.Get(ctx, e.author, a.ID)
	if got.ScanStatus != attachments.ScanInfected || got.Downloadable() {
		t.Fatalf("status %+v", got)
	}
	// A privileged caller removes it; the object is purged.
	if err := e.svc.Delete(ctx, e.staff, "c", a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestScannerOutageKeepsPending(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	e.scanner.err = attachments.ErrScannerUnavailable
	if _, err := e.svc.ScanPending(ctx, 50); !errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Fatalf("want scanner error, got %v", err)
	}
	got, _ := e.svc.Get(ctx, e.author, a.ID)
	if got.ScanStatus != attachments.ScanPending {
		t.Fatalf("status %s", got.ScanStatus)
	}
	st, err := e.svc.Stats(ctx)
	if err != nil || st.ScannerErrors < 1 || st.Pending < 1 || st.OldestPending == nil {
		t.Fatalf("stats %+v %v", st, err)
	}
	e.scanner.err = nil
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.svc.Get(ctx, e.author, a.ID); got.ScanStatus != attachments.ScanClean {
		t.Fatalf("status after recovery %s", got.ScanStatus)
	}
}

func TestScannerRefusalFails(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	e.scanner.err = errors.New("INSTREAM size limit exceeded")
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatalf("a refusal is a result, not a job error: %v", err)
	}
	if got, _ := e.svc.Get(ctx, e.author, a.ID); got.ScanStatus != attachments.ScanFailed {
		t.Fatalf("status %s", got.ScanStatus)
	}
}

func TestTamperedObjectFailsScan(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	var obj string
	_ = e.pool.QueryRow(ctx, `SELECT object_id FROM platform.attachments WHERE id = $1::uuid`, a.ID).Scan(&obj)
	path := filepath.Join(e.base, obj[:2], obj[2:4], obj)
	raw, _ := os.ReadFile(path)
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, e.author, a.ID); got.ScanStatus != attachments.ScanFailed {
		t.Fatalf("tampered object scanned as %s", got.ScanStatus)
	}
}

func TestUploadValidation(t *testing.T) {
	e := newEnv(t, 2048)
	big := append([]byte("%PDF-"), bytes.Repeat([]byte("x"), 4096)...)
	if _, err := e.upload(t, e.author, "", big); !errors.Is(err, attachments.ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.attachments WHERE owner_id = $1::uuid`, e.ownerID).Scan(&n)
	if n != 0 {
		t.Fatal("row stored for a rejected upload")
	}
	if entries, _ := filepath.Glob(filepath.Join(e.base, "*", "*", "*")); len(entries) != 0 {
		t.Fatalf("object left behind: %v", entries)
	}
	if _, err := e.upload(t, e.author, "", nil); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, err := e.upload(t, e.author, "", []byte("MZ not a pdf")); !errors.Is(err, attachments.ErrUnsupportedType) {
		t.Fatalf("type mismatch: %v", err)
	}
	if _, err := e.svc.Upload(context.Background(), e.author, "c", attachments.UploadInput{OwnerType: "testdoc", OwnerID: e.ownerID, DeclaredType: "text/html", FileName: "x.html", Content: strings.NewReader("<script>")}); !errors.Is(err, attachments.ErrUnsupportedType) {
		t.Fatalf("html: %v", err)
	}
	if _, err := e.upload(t, e.author, "weird", pdf); err == nil {
		t.Fatal("bad audience accepted")
	}
}

func TestAuthorizationIsDelegatedToTheOwner(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	if _, err := e.upload(t, e.other, "", pdf); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("no access to owner must look like not found: %v", err)
	}
	if _, err := e.upload(t, e.reader, "", pdf); !errors.Is(err, attachments.ErrForbidden) {
		t.Fatalf("read-only attach: %v", err)
	}
	if _, err := e.upload(t, e.author, attachments.AudiencePrivileged, pdf); !errors.Is(err, attachments.ErrForbidden) {
		t.Fatalf("privileged audience by non-privileged: %v", err)
	}
	if _, err := e.svc.List(ctx, e.other, "testdoc", e.ownerID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("list without access: %v", err)
	}
	if _, err := e.svc.List(ctx, e.staff, "unknown_type", e.ownerID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("unknown owner type: %v", err)
	}
	if _, err := e.svc.List(ctx, e.staff, "testdoc", "not-a-uuid"); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("bad owner id: %v", err)
	}

	pub, _ := e.upload(t, e.author, "", pdf)
	priv, err := e.upload(t, e.staff, attachments.AudiencePrivileged, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatal(err)
	}
	author, _ := e.svc.List(ctx, e.author, "testdoc", e.ownerID)
	staff, _ := e.svc.List(ctx, e.staff, "testdoc", e.ownerID)
	if len(author) != 1 || author[0].ID != pub.ID || len(staff) != 2 {
		t.Fatalf("lists: author=%d staff=%d", len(author), len(staff))
	}
	if _, err := e.svc.Get(ctx, e.author, priv.ID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("privileged attachment visible to author: %v", err)
	}
	if _, _, err := e.svc.Download(ctx, e.reader, "c", priv.ID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("privileged download by reader: %v", err)
	}
	if _, _, err := e.svc.Download(ctx, e.other, "c", pub.ID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("download without owner access: %v", err)
	}
}

func TestDeleteRulesAndPurge(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	s, _ := e.upload(t, e.staff, "", pdf)
	var obj string
	_ = e.pool.QueryRow(ctx, `SELECT object_id FROM platform.attachments WHERE id = $1::uuid`, a.ID).Scan(&obj)
	objPath := filepath.Join(e.base, obj[:2], obj[2:4], obj)

	if err := e.svc.Delete(ctx, e.reader, "c", a.ID); !errors.Is(err, attachments.ErrForbidden) {
		t.Fatalf("reader delete: %v", err)
	}
	if err := e.svc.Delete(ctx, e.author, "c", s.ID); !errors.Is(err, attachments.ErrForbidden) {
		t.Fatalf("delete of someone else's file: %v", err)
	}
	if err := e.svc.Delete(ctx, e.other, "c", a.ID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("delete without access: %v", err)
	}
	if err := e.svc.Delete(ctx, e.author, "c", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(objPath); !os.IsNotExist(err) {
		t.Fatalf("object not removed: %v", err)
	}
	if err := e.svc.Delete(ctx, e.author, "c", a.ID); !errors.Is(err, attachments.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := e.svc.Delete(ctx, e.staff, "c", s.ID); err != nil {
		t.Fatalf("privileged delete: %v", err)
	}
	if n := e.auditCount(t, attachments.ActionDeleted); n != 2 {
		t.Fatalf("delete audits = %d", n)
	}
	if n, err := e.svc.PurgeDeleted(ctx, 100); err != nil || n != 0 {
		t.Fatalf("purge after immediate removal: n=%d err=%v", n, err)
	}
	// Simulate a failed immediate removal: the purge job finishes the work.
	b, _ := e.upload(t, e.author, "", pdf)
	_, _ = e.pool.Exec(ctx, `UPDATE platform.attachments SET deleted_at = now(), deleted_by = uploaded_by WHERE id = $1::uuid`, b.ID)
	if n, err := e.svc.PurgeDeleted(ctx, 100); err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
}

func TestPerOwnerLimit(t *testing.T) {
	e := newEnv(t, 1<<20)
	for i := 0; i < attachments.MaxPerOwner; i++ {
		if _, err := e.upload(t, e.author, "", pdf); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	if _, err := e.upload(t, e.author, "", pdf); !errors.Is(err, attachments.ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
}

// onEOF runs a hook when the reader is exhausted, simulating a change that happens during a slow upload.
type onEOF struct {
	r    io.Reader
	hook func()
	done bool
}

func (o *onEOF) Read(b []byte) (int, error) {
	n, err := o.r.Read(b)
	if errors.Is(err, io.EOF) && !o.done {
		o.done = true
		o.hook()
	}
	return n, err
}

func (e *env) objectFiles() []string {
	entries, _ := filepath.Glob(filepath.Join(e.base, "*", "*", "*"))
	return entries
}

func TestUploadRateLimit(t *testing.T) {
	e := newEnvWith(t, 1<<20, func(p *attachments.Policy) { p.UploadsPerHour = 2 })
	for i := 0; i < 2; i++ {
		if _, err := e.upload(t, e.author, "", pdf); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.upload(t, e.author, "", pdf); !errors.Is(err, attachments.ErrRateLimited) {
		t.Fatalf("third upload: %v", err)
	}
	if len(e.objectFiles()) != 2 {
		t.Fatal("a rate-limited upload stored an object")
	}
	// The limit is per user.
	if _, err := e.upload(t, e.staff, "", pdf); err != nil {
		t.Fatalf("other user: %v", err)
	}
}

func TestUserQuota(t *testing.T) {
	size := int64(len(pdf))
	e := newEnvWith(t, 1<<20, func(p *attachments.Policy) { p.UserQuotaBytes = 2 * size })
	a, err := e.upload(t, e.author, "", pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.upload(t, e.author, "", pdf); err != nil {
		t.Fatal(err)
	}
	if _, err := e.upload(t, e.author, "", pdf); !errors.Is(err, attachments.ErrUserQuota) {
		t.Fatalf("over quota: %v", err)
	}
	if _, err := e.upload(t, e.staff, "", pdf); err != nil {
		t.Fatalf("other user has own quota: %v", err)
	}
	if err := e.svc.Delete(context.Background(), e.author, "c", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.upload(t, e.author, "", pdf); err != nil {
		t.Fatalf("quota freed by delete: %v", err)
	}
}

func TestInstallationQuotaAndCommitCheck(t *testing.T) {
	size := int64(len(pdf))
	var before int64
	_ = dbtest.Pool(t).QueryRow(context.Background(), `SELECT COALESCE(sum(size_bytes),0)::bigint FROM platform.attachments WHERE deleted_at IS NULL`).Scan(&before)
	e := newEnvWith(t, 1<<20, func(p *attachments.Policy) { p.InstallationQuotaBytes = before + size })
	if _, err := e.upload(t, e.author, "", pdf); err != nil {
		t.Fatal(err)
	}
	if _, err := e.upload(t, e.staff, "", pdf); !errors.Is(err, attachments.ErrInstallationQuota) {
		t.Fatalf("installation quota: %v", err)
	}
	if len(e.objectFiles()) != 1 {
		t.Fatalf("object left behind: %v", e.objectFiles())
	}
}

func TestQuotaIsCheckedAgainAtCommit(t *testing.T) {
	size := int64(len(pdf))
	e := newEnvWith(t, 1<<20, func(p *attachments.Policy) { p.UserQuotaBytes = size + size/2 })
	// The pre-check passes (nothing stored yet); a concurrent upload fills the quota while this one streams.
	in := &onEOF{r: bytes.NewReader(pdf), hook: func() {
		if _, err := e.upload(t, e.author, "", pdf); err != nil {
			t.Error(err)
		}
	}}
	_, err := e.svc.Upload(context.Background(), e.author, "c", attachments.UploadInput{OwnerType: "testdoc", OwnerID: e.ownerID, FileName: "a.pdf", DeclaredType: "application/pdf", Content: in})
	if !errors.Is(err, attachments.ErrUserQuota) {
		t.Fatalf("commit quota: %v", err)
	}
	if len(e.objectFiles()) != 1 {
		t.Fatalf("object of the refused upload remains: %v", e.objectFiles())
	}
}

func TestRevokedRightsDuringUploadAreHonoured(t *testing.T) {
	e := newEnv(t, 1<<20)
	in := &onEOF{r: bytes.NewReader(pdf), hook: func() {
		e.owner.mu.Lock()
		e.owner.rights[e.author.UserID] = attachments.Access{Read: true} // e.g. the ticket was closed
		e.owner.mu.Unlock()
	}}
	_, err := e.svc.Upload(context.Background(), e.author, "c", attachments.UploadInput{OwnerType: "testdoc", OwnerID: e.ownerID, FileName: "a.pdf", DeclaredType: "application/pdf", Content: in})
	if !errors.Is(err, attachments.ErrForbidden) {
		t.Fatalf("want forbidden, got %v", err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM platform.attachments WHERE owner_id = $1::uuid`, e.ownerID).Scan(&n)
	if n != 0 || len(e.objectFiles()) != 0 || e.auditCount(t, attachments.ActionUploaded) != 0 {
		t.Fatalf("refused upload left row=%d objects=%v", n, e.objectFiles())
	}
}

func TestDownloadAuditedOnlyAfterOpen(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, _ := e.upload(t, e.author, "", pdf)
	if _, err := e.svc.ScanPending(ctx, 50); err != nil {
		t.Fatal(err)
	}
	for _, f := range e.objectFiles() {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.svc.Download(ctx, e.author, "c", a.ID); !errors.Is(err, attachments.ErrUnavailable) {
		t.Fatalf("download of a missing object: %v", err)
	}
	if n := e.auditCount(t, attachments.ActionDownloaded); n != 0 {
		t.Fatalf("success audit recorded for a failed download: %d", n)
	}
	if n := e.auditCount(t, attachments.ActionDownloadFailed); n != 1 {
		t.Fatalf("failure audits = %d", n)
	}
}
