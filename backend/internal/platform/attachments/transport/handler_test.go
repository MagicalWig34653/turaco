package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments/transport"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage/fsstore"
)

type owner struct{ id string }

func (o owner) Access(_ context.Context, p authorization.Principal, id string) (attachments.Access, error) {
	if id != o.id {
		return attachments.Access{}, attachments.ErrOwnerNotFound
	}
	return attachments.Access{Read: true, Attach: p.Has("attach"), Privileged: p.Has("attach"), Manage: p.Has("attach")}, nil
}

type scanner struct{}

func (scanner) Scan(_ context.Context, r io.Reader) (attachments.Verdict, error) {
	_, _ = io.Copy(io.Discard, r)
	return attachments.Verdict{Clean: true}, nil
}

type fixedAuth struct{ p authorization.Principal }

func (f fixedAuth) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return f.p, true, nil
}

func setup(t *testing.T, perms ...string) (http.Handler, *attachments.Service, string, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var uid, ownerID string
	_ = pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&uid)
	_ = pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&ownerID)
	fs, err := fsstore.New(filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	vault, _ := storage.NewVault(fs, bytes.Repeat([]byte{5}, 32))
	policy, _ := attachments.NewPolicy(4096, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := attachments.New(pool, vault, scanner{}, policy, logger)
	if err := svc.RegisterOwner("doc", owner{ownerID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.attachments WHERE owner_id = $1::uuid`, ownerID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE target_type = 'attachment' AND metadata->>'ownerId' = $1`, ownerID)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.jobs WHERE job_type = $1`, attachments.ScanJobType)
	})
	perm := map[string]struct{}{}
	for _, p := range perms {
		perm[p] = struct{}{}
	}
	mux := http.NewServeMux()
	transport.Register(mux, svc, fixedAuth{authorization.Principal{UserID: uid, Permissions: perm}}, logger)
	return httpx.Middleware(logger, mux), svc, ownerID, pool
}

func multipartBody(t *testing.T, name, ctype string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", ctype)
	part, _ := w.CreatePart(h)
	_, _ = part.Write(content)
	_ = w.Close()
	return &b, w.FormDataContentType()
}

func do(mux http.Handler, method, target string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUploadScanDownloadOverHTTP(t *testing.T) {
	mux, svc, ownerID, _ := setup(t, "attach")
	pdf := []byte("%PDF-1.7\nbody")
	body, ct := multipartBody(t, "../report ü.pdf", "application/pdf", pdf)
	rec := do(mux, "POST", "/api/v1/attachments?ownerType=doc&ownerId="+ownerID, body, ct)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)
	if created["scanStatus"] != "pending" || created["downloadable"] != false || created["fileName"] != "report ü.pdf" {
		t.Fatalf("created %v", created)
	}
	// Pending: 409 with the scan status, no content.
	rec = do(mux, "GET", "/api/v1/attachments/"+id+"/content", nil, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "attachments.scan_pending") {
		t.Fatalf("pending download: %d %s", rec.Code, rec.Body)
	}
	if _, err := svc.ScanPending(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	rec = do(mux, "GET", "/api/v1/attachments/"+id+"/content", nil, "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pdf) {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	h := rec.Header()
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("download headers %v", h)
	}
	if strings.Contains(h.Get("Content-Disposition"), "..") || strings.Contains(h.Get("Content-Disposition"), "/") {
		t.Fatalf("path leaked into the file name: %s", h.Get("Content-Disposition"))
	}
	rec = do(mux, "GET", "/api/v1/attachments?ownerType=doc&ownerId="+ownerID, nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec = do(mux, "DELETE", "/api/v1/attachments/"+id, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec = do(mux, "GET", "/api/v1/attachments/"+id, nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", rec.Code)
	}
}

func TestUploadRejections(t *testing.T) {
	mux, _, ownerID, _ := setup(t, "attach")
	url := "/api/v1/attachments?ownerType=doc&ownerId=" + ownerID
	big, ct := multipartBody(t, "a.pdf", "application/pdf", append([]byte("%PDF-"), make([]byte, 10000)...))
	if rec := do(mux, "POST", url, big, ct); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d %s", rec.Code, rec.Body)
	}
	exe, ct := multipartBody(t, "a.pdf", "application/pdf", []byte("MZ\x90\x00 not a pdf"))
	if rec := do(mux, "POST", url, exe, ct); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("type mismatch: %d", rec.Code)
	}
	if rec := do(mux, "POST", url, strings.NewReader(`{"a":1}`), "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("json body: %d", rec.Code)
	}
	nofile := &bytes.Buffer{}
	w := multipart.NewWriter(nofile)
	_ = w.WriteField("other", "x")
	_ = w.Close()
	if rec := do(mux, "POST", url, nofile, w.FormDataContentType()); rec.Code != http.StatusBadRequest {
		t.Fatalf("no file part: %d", rec.Code)
	}
	ok, ct := multipartBody(t, "a.pdf", "application/pdf", []byte("%PDF-1"))
	if rec := do(mux, "POST", "/api/v1/attachments?ownerType=doc&ownerId=not-a-uuid", ok, ct); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown owner: %d", rec.Code)
	}
}

func TestReadOnlyCallerCannotUploadAndConfig(t *testing.T) {
	mux, _, ownerID, _ := setup(t)
	body, ct := multipartBody(t, "a.pdf", "application/pdf", []byte("%PDF-1"))
	if rec := do(mux, "POST", "/api/v1/attachments?ownerType=doc&ownerId="+ownerID, body, ct); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only upload: %d", rec.Code)
	}
	rec := do(mux, "GET", "/api/v1/attachments/config", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"maxBytes":4096`) {
		t.Fatalf("config: %d %s", rec.Code, rec.Body)
	}
}

func TestNotConfigured(t *testing.T) {
	mux := http.NewServeMux()
	transport.Register(mux, nil, fixedAuth{authorization.Principal{UserID: "u"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := do(mux, "GET", "/api/v1/attachments/config", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("config: %d %s", rec.Code, rec.Body)
	}
	if rec = do(mux, "GET", "/api/v1/attachments?ownerType=a&ownerId=b", nil, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list: %d", rec.Code)
	}
}
