// Package transport exposes the attachment HTTP API under /api/v1/attachments (ADR-0037). Authorization is
// delegated to the module owning the attachment's owner type; any signed-in User reaches the routes and sees only
// what the owner module allows.
package transport

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// multipartOverhead is the allowance for multipart framing on top of the file size cap.
const multipartOverhead = 64 << 10

// transferTimeout bounds one upload or download.
const transferTimeout = 10 * time.Minute

type handler struct {
	svc    *attachments.Service
	logger *slog.Logger
}

// Register mounts the attachment routes. With a nil service (attachments not configured) every route answers 503
// attachments.not_configured and /attachments/config reports enabled=false.
func Register(mux *http.ServeMux, svc *attachments.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	authed := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(authed(fn))) }
	route("GET /api/v1/attachments/config", h.config)
	route("GET /api/v1/attachments", h.list)
	route("POST /api/v1/attachments", h.upload)
	route("GET /api/v1/attachments/{id}", h.get)
	route("GET /api/v1/attachments/{id}/content", h.download)
	route("DELETE /api/v1/attachments/{id}", h.remove)
}

type dto struct {
	ID           string  `json:"id"`
	OwnerType    string  `json:"ownerType"`
	OwnerID      string  `json:"ownerId"`
	FileName     string  `json:"fileName"`
	ContentType  string  `json:"contentType"`
	SizeBytes    int64   `json:"sizeBytes"`
	SHA256       string  `json:"sha256"`
	ScanStatus   string  `json:"scanStatus"`
	Audience     string  `json:"audience"`
	UploadedBy   string  `json:"uploadedBy"`
	CreatedAt    string  `json:"createdAt"`
	ScannedAt    *string `json:"scannedAt"`
	Downloadable bool    `json:"downloadable"`
}

func toDTO(a attachments.Attachment) dto {
	d := dto{ID: a.ID, OwnerType: a.OwnerType, OwnerID: a.OwnerID, FileName: a.FileName, ContentType: a.ContentType, SizeBytes: a.SizeBytes, SHA256: a.SHA256,
		ScanStatus: a.ScanStatus, Audience: a.Audience, UploadedBy: a.UploadedBy, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339), Downloadable: a.Downloadable()}
	if a.ScannedAt != nil {
		s := a.ScannedAt.UTC().Format(time.RFC3339)
		d.ScannedAt = &s
	}
	return d
}

func (h *handler) ready(w http.ResponseWriter) bool {
	if h.svc == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "attachments.not_configured", "File attachments are not configured on this server.")
		return false
	}
	return true
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *attachments.InvalidError
	var na *attachments.NotAvailableError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "attachments.invalid_request", inv.Message)
	case errors.As(err, &mbe), errors.Is(err, attachments.ErrTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "attachments.too_large", "The file is larger than the allowed size.")
	case errors.Is(err, attachments.ErrUnsupportedType):
		httpx.WriteError(w, http.StatusUnsupportedMediaType, "attachments.unsupported_type", "This file type is not allowed or the content does not match the type.")
	case errors.Is(err, attachments.ErrLimit):
		httpx.WriteError(w, http.StatusConflict, "attachments.limit", "The maximum number of attachments is reached.")
	case errors.As(err, &na):
		code, msg := "attachments.scan_pending", "The file is still being scanned for malware."
		switch na.Status {
		case attachments.ScanInfected:
			code, msg = "attachments.quarantined", "The file was blocked because it contains malware."
		case attachments.ScanFailed:
			code, msg = "attachments.scan_failed", "The file could not be scanned and is not available."
		}
		httpx.WriteErrorDetails(w, http.StatusConflict, code, msg, map[string]any{"scanStatus": na.Status})
	case errors.Is(err, attachments.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "attachments.not_found", "The requested resource was not found.")
	case errors.Is(err, attachments.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	default:
		h.logger.Error("attachments request failed", "path", r.URL.Path, "error", err, "requestId", httpx.RequestID(w))
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal", "The request could not be completed.")
	}
}

func (h *handler) config(w http.ResponseWriter, _ *http.Request) {
	if h.svc == nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"enabled": false, "maxBytes": 0, "allowedTypes": []string{}})
		return
	}
	p := h.svc.Policy()
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": true, "maxBytes": p.MaxBytes, "allowedTypes": p.AllowedTypes(), "maxPerOwner": attachments.MaxPerOwner})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	p, _ := authorization.PrincipalFrom(r.Context())
	q := r.URL.Query()
	list, err := h.svc.List(r.Context(), p, q.Get("ownerType"), q.Get("ownerId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]dto, 0, len(list))
	for _, a := range list {
		items = append(items, toDTO(a))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	p, _ := authorization.PrincipalFrom(r.Context())
	a, err := h.svc.Get(r.Context(), p, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDTO(a))
}

// upload accepts a multipart/form-data body with one part named "file". The owner is named in the query
// (ownerType, ownerId, optional audience), so authorization happens before the body is read.
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	max := h.svc.Policy().MaxBytes
	if r.ContentLength > max+multipartOverhead {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "attachments.too_large", "The file is larger than the allowed size.")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
		httpx.WriteError(w, http.StatusBadRequest, "attachments.invalid_request", "The upload must be multipart/form-data with one file part.")
		return
	}
	// The server-wide 30 second timeouts suit JSON; a large upload on a slow link gets its own deadline.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(transferTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(transferTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, max+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "attachments.invalid_request", "The upload must be multipart/form-data with one file part.")
		return
	}
	var part *multipart.Part
	for {
		pt, err := mr.NextPart()
		if err != nil {
			h.fail(w, r, wrapBody(err))
			return
		}
		if pt.FormName() == "file" && pt.FileName() != "" {
			part = pt
			break
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(pt, 1<<10))
	}
	p, _ := authorization.PrincipalFrom(r.Context())
	q := r.URL.Query()
	a, err := h.svc.Upload(r.Context(), p, httpx.RequestID(w), attachments.UploadInput{
		OwnerType: q.Get("ownerType"), OwnerID: q.Get("ownerId"), Audience: q.Get("audience"),
		FileName: part.FileName(), DeclaredType: part.Header.Get("Content-Type"), Content: part,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toDTO(a))
}

// wrapBody maps body read failures: the size cap is 413, a malformed body 400.
func wrapBody(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return err
	}
	return &attachments.InvalidError{Message: "the upload must contain a file part"}
}

func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	p, _ := authorization.PrincipalFrom(r.Context())
	a, rc, err := h.svc.Download(r.Context(), p, httpx.RequestID(w), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer rc.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout))
	hd := w.Header()
	hd.Set("Content-Type", a.ContentType) // validated against the allow-list at upload
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": a.FileName})
	if disposition == "" {
		disposition = "attachment"
	}
	hd.Set("Content-Disposition", disposition)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.FormatInt(a.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	// A mid-stream authentication failure cannot change the status; the short body fails the client's length check.
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.Error("attachment stream failed", "attachmentId", a.ID, "error", err, "requestId", httpx.RequestID(w))
	}
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	p, _ := authorization.PrincipalFrom(r.Context())
	if err := h.svc.Delete(r.Context(), p, httpx.RequestID(w), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
