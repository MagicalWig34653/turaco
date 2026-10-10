package transport

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/csvsafe"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permImport        = "organization.import"
	maxMultipartExtra = 64 << 10
	maxBulkBody       = 64 << 10
	maxFieldLength    = 64
)

type importHandler struct {
	imports *application.Imports
	logger  *slog.Logger
}

// RegisterImports mounts the CSV import, bulk operation, directory linking and access extension routes (F14 section
// 1.6, ADR-0034 R5). Stored previews are visible to their creator only and every operation authorizes against the
// caller's current permissions again inside the transaction.
func RegisterImports(mux *http.ServeMux, imports *application.Imports, auth authorization.Authenticator, logger *slog.Logger) {
	h := &importHandler{imports: imports, logger: logger}
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	upload := authorization.Require(auth, permImport)
	bulk := authorization.Require(auth, permUsersManage)
	authed := authorization.RequireAuthenticated(auth)
	route("POST /api/v1/import-batches", upload, h.preview)
	route("GET /api/v1/import-batches/{id}", authed, h.get)
	route("GET /api/v1/import-batches/{id}/rows", authed, h.rows)
	route("GET /api/v1/import-batches/{id}/rejected.csv", authed, h.rejectedCSV)
	route("POST /api/v1/import-batches/{id}/apply", authed, h.apply)
	route("POST /api/v1/users/bulk-operations", bulk, h.bulk)
	route("POST /api/v1/users/{id}/link-directory-identity", authorization.Require(auth, permPlatformAdmin), h.linkDirectory)
	route("POST /api/v1/users/{id}/extend-access", authorization.Require(auth, permExternalParties), h.extendAccess)
}

func (h *importHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failPeople(h.logger, w, r, err)
}

// ---- DTOs ----

type batchDTO struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	MatchKey       string         `json:"matchKey,omitempty"`
	Mode           string         `json:"mode,omitempty"`
	Operation      string         `json:"operation,omitempty"`
	Status         string         `json:"status"`
	FileHash       string         `json:"fileHash,omitempty"`
	PreviewHash    string         `json:"previewHash"`
	RowCount       int            `json:"rowCount"`
	Counts         map[string]int `json:"counts"`
	UnknownColumns []string       `json:"unknownColumns"`
	ExpiresAt      string         `json:"expiresAt"`
	AppliedAt      *string        `json:"appliedAt,omitempty"`
	AppliedCounts  map[string]int `json:"appliedCounts,omitempty"`
}

type rowDTO struct {
	Row      int                              `json:"row"`
	Action   string                           `json:"action"`
	Key      string                           `json:"key"`
	Label    string                           `json:"label,omitempty"`
	Diff     map[string]application.DiffValue `json:"diff"`
	Errors   []application.RowIssue           `json:"errors"`
	Warnings []application.RowIssue           `json:"warnings"`
	// UserID is the target of a bulk row.
	UserID string `json:"userId,omitempty"`
}

type previewDTO struct {
	batchDTO
	Rows       []rowDTO `json:"rows"`
	NextCursor string   `json:"nextCursor,omitempty"`
	Replayed   bool     `json:"replayed,omitempty"`
}

func toBatch(b application.Batch) batchDTO {
	d := batchDTO{ID: b.ID, Kind: b.Kind, MatchKey: b.MatchKey, Mode: b.Mode, Operation: b.Operation, Status: b.Status, FileHash: b.FileHash,
		PreviewHash: b.PreviewHash, RowCount: b.RowCount, Counts: b.Counts, UnknownColumns: b.UnknownColumns, ExpiresAt: ts(b.ExpiresAt),
		AppliedAt: tsPtr(b.AppliedAt), AppliedCounts: b.AppliedCounts}
	if d.UnknownColumns == nil {
		d.UnknownColumns = []string{}
	}
	return d
}

func toRows(kind string, rows []application.BatchRow) []rowDTO {
	out := make([]rowDTO, 0, len(rows))
	for _, r := range rows {
		d := rowDTO{Row: r.No, Action: r.Action, Key: r.Key, Label: r.Label, Diff: r.Diff, Errors: r.Errors, Warnings: r.Warnings}
		if d.Diff == nil {
			d.Diff = map[string]application.DiffValue{}
		}
		if d.Errors == nil {
			d.Errors = []application.RowIssue{}
		}
		if d.Warnings == nil {
			d.Warnings = []application.RowIssue{}
		}
		if kind == application.BatchBulkUsers {
			d.UserID = r.Key
		}
		out = append(out, d)
	}
	return out
}

// rowIssueCode names the reason of a failed apply row; it never contains data of the row.
func rowIssueCode(err error) (string, bool) {
	switch {
	case errors.Is(err, application.ErrVersionConflict):
		return "version_conflict", true
	case errors.Is(err, application.ErrNotFound):
		return "not_found", true
	case errors.Is(err, application.ErrConflict):
		return "conflict", true
	case errors.Is(err, application.ErrDominanceRequired):
		return "dominance_required", true
	case errors.Is(err, application.ErrLastAdministrator):
		return "last_administrator", true
	case errors.Is(err, application.ErrWrongState):
		return "invalid_state", true
	case errors.Is(err, application.ErrSelfOperation):
		return "self_operation", true
	case errors.Is(err, application.ErrHierarchy):
		return "invalid_hierarchy", true
	case errors.Is(err, application.ErrTargetInactive):
		return "target_inactive", true
	}
	var owned *application.FieldDirectoryOwnedError
	if errors.As(err, &owned) {
		return "directory_owned", true
	}
	return "", false
}

// ---- handlers ----

// preview reads multipart/form-data with the fields kind, matchKey, mode and the file part "file". The file is
// streamed into memory with a hard limit; nothing is written to disk.
func (h *importHandler) preview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, application.MaxImportBytes+maxMultipartExtra)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "The request must be multipart/form-data.")
		return
	}
	fields := map[string]string{}
	var file []byte
	haveFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.fail(w, r, tooLargeOr(err))
			return
		}
		switch part.FormName() {
		case "file":
			file, err = io.ReadAll(io.LimitReader(part, application.MaxImportBytes+1))
			haveFile = true
		case "kind", "matchKey", "mode":
			var b []byte
			b, err = io.ReadAll(io.LimitReader(part, maxFieldLength+1))
			if len(b) > maxFieldLength {
				httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "A form field is too long.")
				return
			}
			fields[part.FormName()] = string(b)
		}
		if err != nil {
			h.fail(w, r, tooLargeOr(err))
			return
		}
	}
	if !haveFile {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "The file part is required.")
		return
	}
	p, err := h.imports.PreviewImport(r.Context(), caller(w, r), fields["kind"], fields["matchKey"], fields["mode"], file)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toPreview(p))
}

func tooLargeOr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return application.ErrImportTooLarge
	}
	return &application.InvalidInputError{Message: "the multipart body could not be read"}
}

func toPreview(p application.BatchPreview) previewDTO {
	return previewDTO{batchDTO: toBatch(p.Batch), Rows: toRows(p.Batch.Kind, p.Rows), NextCursor: p.Next}
}

func (h *importHandler) get(w http.ResponseWriter, r *http.Request) {
	b, err := h.imports.Get(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toBatch(b))
}

func (h *importHandler) rowFilter(w http.ResponseWriter, r *http.Request) (application.RowFilter, bool) {
	f := application.RowFilter{Action: r.URL.Query().Get("action")}
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_cursor", "The cursor is invalid.")
			return f, false
		}
		f.After = n
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_limit", "The limit must be a positive integer.")
			return f, false
		}
		f.Limit = n
	}
	return f, true
}

func (h *importHandler) rows(w http.ResponseWriter, r *http.Request) {
	f, valid := h.rowFilter(w, r)
	if !valid {
		return
	}
	c := caller(w, r)
	b, err := h.imports.Get(r.Context(), c, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rows, next, err := h.imports.Rows(r.Context(), c, b.ID, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": toRows(b.Kind, rows), "nextCursor": next})
}

// rejectedCSV writes the rejected rows with csvsafe (the one CSV writer: formula prefixes and control characters are
// neutralized). Cells hold row number, key, field and reason codes only.
func (h *importHandler) rejectedCSV(w http.ResponseWriter, r *http.Request) {
	c := caller(w, r)
	b, err := h.imports.Get(r.Context(), c, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var all []application.BatchRow
	after := 0
	for {
		rows, next, err := h.imports.Rows(r.Context(), c, b.ID, application.RowFilter{Action: application.RowReject, After: after, Limit: 200})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		all = append(all, rows...)
		if next == "" {
			break
		}
		after, _ = strconv.Atoi(next)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="rejected-rows.csv"`)
	cw := csvsafe.NewWriter(w)
	_ = cw.Write([]string{"row", "key", "field", "reason"})
	for _, row := range all {
		for _, e := range row.Errors {
			_ = cw.Write([]string{strconv.Itoa(row.No), row.Key, e.Field, e.Code})
		}
	}
	_ = cw.Flush()
}

func (h *importHandler) apply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreviewHash     string `json:"previewHash"`
		ExpectedRejects int    `json:"expectedRejects"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	h.applyBatch(w, r, r.PathValue("id"), body.PreviewHash, body.ExpectedRejects)
}

func (h *importHandler) applyBatch(w http.ResponseWriter, r *http.Request, id, hash string, rejects int) {
	res, err := h.imports.Apply(r.Context(), caller(w, r), id, hash, rejects)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, previewDTO{batchDTO: toBatch(res.Batch), Rows: toRows(res.Batch.Kind, res.Rows), Replayed: res.Replayed})
}

// bulk previews (dryRun true) or applies (dryRun false with the batch id and hash of a preview) a bulk operation.
func (h *importHandler) bulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation       string   `json:"operation"`
		UserIDs         []string `json:"userIds"`
		DepartmentID    *string  `json:"departmentId"`
		LocationID      *string  `json:"locationId"`
		ManagerUserID   *string  `json:"managerUserId"`
		Reason          string   `json:"reason"`
		DryRun          *bool    `json:"dryRun"`
		BatchID         string   `json:"batchId"`
		PreviewHash     string   `json:"previewHash"`
		ExpectedRejects int      `json:"expectedRejects"`
	}
	if err := httpx.DecodeJSON(w, r, &body, maxBulkBody); err != nil || body.DryRun == nil {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "The request body is not valid JSON for this operation; dryRun is required.")
		return
	}
	if !*body.DryRun {
		h.applyBatch(w, r, body.BatchID, body.PreviewHash, body.ExpectedRejects)
		return
	}
	p, err := h.imports.PreviewBulk(r.Context(), caller(w, r), application.BulkInput{Operation: body.Operation, UserIDs: body.UserIDs,
		DepartmentID: body.DepartmentID, LocationID: body.LocationID, ManagerUserID: body.ManagerUserID, Reason: body.Reason})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toPreview(p))
}

func (h *importHandler) linkDirectory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int    `json:"expectedVersion"`
		RunID           string `json:"runId"`
		ExternalID      string `json:"externalId"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	u, err := h.imports.LinkDirectoryIdentity(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, body.RunID, body.ExternalID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toUser(u))
}

func (h *importHandler) extendAccess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int    `json:"expectedVersion"`
		AccessExpiresAt string `json:"accessExpiresAt"`
		Reason          string `json:"reason"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	until, err := time.Parse(time.RFC3339, body.AccessExpiresAt)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "organization.invalid_request", "accessExpiresAt must be an RFC 3339 timestamp.")
		return
	}
	u, err := h.imports.ExtendAccess(r.Context(), caller(w, r), r.PathValue("id"), body.ExpectedVersion, until, body.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toUser(u))
}
