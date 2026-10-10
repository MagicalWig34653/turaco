package repository

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

var _ application.BatchStore = (*Repository)(nil)

// Stored previews and their application (F14 design section 1.6). The dry run executes every row with the same
// transactional operations as the single-record API inside one transaction that is rolled back (each row in its own
// savepoint, so a rejected row leaves no trace and a following row sees the effect of the accepted ones). The
// outcome is stored; apply repeats the accepted rows in one transaction against the stored versions.

const rowPageSize = 50

// rowSpec is one input row of a dry run or an apply.
type rowSpec struct {
	no            int
	key           string
	data          map[string]string
	action        string // stored action (apply only)
	targetID      string
	targetVersion int
	preErrors     []application.RowIssue
	warnings      []application.RowIssue
}

type outcome struct {
	action        string
	diff          map[string]application.DiffValue
	errors        []application.RowIssue
	targetID      string
	targetVersion int
	label         string
}

func reject(issues ...application.RowIssue) outcome {
	return outcome{action: application.RowReject, errors: issues}
}

// issueOf maps a domain error of an operation to a row issue; ok is false for errors that are not row findings
// (database failures), which abort the preview.
func issueOf(err error) (application.RowIssue, bool) {
	var owned *application.FieldDirectoryOwnedError
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &owned):
		return application.RowIssue{Field: strings.Join(owned.Fields, ","), Code: "directory_owned"}, true
	case errors.As(err, &inv):
		return application.RowIssue{Code: application.IssueInvalidValue}, true
	case errors.Is(err, application.ErrNotFound):
		return application.RowIssue{Code: "not_found"}, true
	case errors.Is(err, application.ErrConflict):
		return application.RowIssue{Code: "conflict"}, true
	case errors.Is(err, application.ErrVersionConflict):
		return application.RowIssue{Code: "version_conflict"}, true
	case errors.Is(err, application.ErrDirectoryUser):
		return application.RowIssue{Code: "directory_user"}, true
	case errors.Is(err, application.ErrEmergencyAccount):
		return application.RowIssue{Code: "emergency_account"}, true
	case errors.Is(err, application.ErrLastAdministrator):
		return application.RowIssue{Code: "last_administrator"}, true
	case errors.Is(err, application.ErrSelfOperation):
		return application.RowIssue{Code: "self_operation"}, true
	case errors.Is(err, application.ErrDominanceRequired):
		return application.RowIssue{Code: "dominance_required"}, true
	case errors.Is(err, application.ErrWrongState):
		return application.RowIssue{Code: "invalid_state"}, true
	case errors.Is(err, application.ErrHierarchy):
		return application.RowIssue{Code: "invalid_hierarchy"}, true
	case errors.Is(err, application.ErrTargetInactive):
		return application.RowIssue{Code: "target_inactive"}, true
	case errors.Is(err, application.ErrDirectoryIdentityDisabled):
		return application.RowIssue{Code: "directory_identity_disabled"}, true
	}
	return application.RowIssue{}, false
}

func (r *Repository) newBatchID(ctx context.Context) (string, error) {
	var id string
	if err := r.pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return "", fmt.Errorf("new batch id: %w", err)
	}
	return id, nil
}

func batchCorrelation(id string) string { return "batch:" + id }

// dryRun runs exec for every row inside tx, each in a savepoint that is released for accepted rows and rolled back
// for rejected ones. The caller rolls tx back afterwards.
func dryRun(ctx context.Context, tx pgx.Tx, rows []rowSpec, exec func(ctx context.Context, tx pgx.Tx, s rowSpec) (outcome, error)) ([]outcome, error) {
	out := make([]outcome, len(rows))
	for i, s := range rows {
		if len(s.preErrors) > 0 {
			out[i] = reject(s.preErrors...)
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("savepoint: %w", err)
		}
		o, err := exec(ctx, sp, s)
		if err != nil {
			_ = sp.Rollback(ctx)
			return nil, err
		}
		if o.action == application.RowReject {
			if err := sp.Rollback(ctx); err != nil {
				return nil, fmt.Errorf("rollback savepoint: %w", err)
			}
		} else if err := sp.Commit(ctx); err != nil {
			return nil, fmt.Errorf("release savepoint: %w", err)
		}
		out[i] = o
	}
	return out, nil
}

// previewHash binds the preview to its inputs and results.
func previewHash(kind, key, mode, op string, params map[string]any, creator string, rows []rowSpec, res []outcome) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode([]any{kind, key, mode, op, params, creator})
	for i, s := range rows {
		_ = enc.Encode([]any{s.no, res[i].action, s.key, res[i].targetID, res[i].targetVersion, s.data})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func countOutcomes(res []outcome) map[string]int {
	counts := map[string]int{application.RowCreate: 0, application.RowUpdate: 0, application.RowUnchanged: 0, application.RowReject: 0}
	for _, o := range res {
		counts[o.action]++
	}
	return counts
}

func marshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("organization: marshal batch value: %v", err))
	}
	return b
}

// storeBatch writes the batch and its rows and audits the preview in one transaction.
type batchInsert struct {
	id, kind, matchKey, mode, operation string
	params                              map[string]any
	fileHash, hash                      string
	unknown                             []string
	rows                                []rowSpec
	res                                 []outcome
}

func (r *Repository) storeBatch(ctx context.Context, c application.Caller, b batchInsert) (application.BatchPreview, error) {
	counts := countOutcomes(b.res)
	expires := time.Now().UTC().Add(application.PreviewTTL).Truncate(time.Microsecond)
	if b.unknown == nil {
		b.unknown = []string{}
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// Expired previews carry personal data: they are deleted whenever a new one is stored (and by the purge job).
		if _, err := tx.Exec(ctx, `DELETE FROM organization.import_batches WHERE status = 'previewed' AND expires_at < now()`); err != nil {
			return fmt.Errorf("purge previews: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization.import_batches (id, kind, match_key, mode, operation, params, created_by, file_hash, preview_hash,
				row_count, counts, unknown_columns, correlation_id, expires_at)
			VALUES ($1::uuid, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, $7::uuid, NULLIF($8, ''), $9, $10, $11, $12, $13, $14)`,
			b.id, b.kind, b.matchKey, b.mode, b.operation, marshalJSON(b.params), c.Actor.UserID, b.fileHash, b.hash,
			len(b.rows), marshalJSON(counts), marshalJSON(b.unknown), batchCorrelation(b.id), expires); err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}
		batch := &pgx.Batch{}
		for i, s := range b.rows {
			o := b.res[i]
			data := s.data
			if b.kind == application.BatchBulkUsers {
				data = map[string]string{"label": o.label}
			}
			if data == nil {
				data = map[string]string{}
			}
			diff := o.diff
			if diff == nil {
				diff = map[string]application.DiffValue{}
			}
			errs := o.errors
			if errs == nil {
				errs = []application.RowIssue{}
			}
			warns := s.warnings
			if warns == nil {
				warns = []application.RowIssue{}
			}
			var target *string
			var version *int
			if o.targetID != "" {
				t, v := o.targetID, o.targetVersion
				target, version = &t, &v
			}
			batch.Queue(`
				INSERT INTO organization.import_rows (batch_id, row_no, action, row_key, data, target_id, target_version, diff, errors, warnings)
				VALUES ($1::uuid, $2, $3, NULLIF($4, ''), $5, $6::uuid, $7, $8, $9, $10)`,
				b.id, s.no, o.action, s.key, marshalJSON(data), target, version, marshalJSON(diff), marshalJSON(errs), marshalJSON(warns))
		}
		br := tx.SendBatch(ctx, batch)
		for range b.rows {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return fmt.Errorf("insert batch row: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("insert batch rows: %w", err)
		}
		action := "organization.import.previewed"
		meta := map[string]any{"kind": b.kind, "rowCount": len(b.rows), "counts": counts, "fileHash": b.fileHash, "batchCorrelationId": batchCorrelation(b.id)}
		if b.kind == application.BatchBulkUsers {
			action = "organization.bulk.previewed"
			meta = map[string]any{"operation": b.operation, "rowCount": len(b.rows), "counts": counts, "batchCorrelationId": batchCorrelation(b.id)}
		} else {
			meta["matchKey"], meta["mode"] = b.matchKey, b.mode
		}
		return r.record(ctx, tx, c, action, "import_batch", b.id, nil, nil, meta)
	})
	if err != nil {
		return application.BatchPreview{}, fmt.Errorf("store batch: %w", err)
	}
	batch, err := r.GetBatch(ctx, c, b.id)
	if err != nil {
		return application.BatchPreview{}, err
	}
	rows, next, err := r.ListBatchRows(ctx, c, b.id, application.RowFilter{Limit: rowPageSize})
	if err != nil {
		return application.BatchPreview{}, err
	}
	return application.BatchPreview{Batch: batch, Rows: rows, Next: next}, nil
}

// ---- reading ----

const batchColumns = `id::text, kind, coalesce(match_key, ''), coalesce(mode, ''), coalesce(operation, ''), coalesce(file_hash, ''), preview_hash,
	row_count, counts, unknown_columns, correlation_id, created_at, expires_at, status, applied_at, applied_counts, created_by::text`

func scanBatch(row pgx.Row) (application.Batch, string, error) {
	var b application.Batch
	var counts, unknown, applied []byte
	var creator string
	if err := row.Scan(&b.ID, &b.Kind, &b.MatchKey, &b.Mode, &b.Operation, &b.FileHash, &b.PreviewHash, &b.RowCount, &counts, &unknown,
		&b.CorrelationID, &b.CreatedAt, &b.ExpiresAt, &b.Status, &b.AppliedAt, &applied, &creator); err != nil {
		return b, "", err
	}
	_ = json.Unmarshal(counts, &b.Counts)
	_ = json.Unmarshal(unknown, &b.UnknownColumns)
	if applied != nil {
		_ = json.Unmarshal(applied, &b.AppliedCounts)
	}
	return b, creator, nil
}

// visibleBatch loads a batch for its creator only; expired previews are gone.
func visibleBatch(ctx context.Context, q pgx.Tx, c application.Caller, id string, lock bool) (application.Batch, error) {
	u, ok := parseID(id)
	if !ok {
		return application.Batch{}, application.ErrNotFound
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	b, creator, err := scanBatch(q.QueryRow(ctx, `SELECT `+batchColumns+` FROM organization.import_batches WHERE id = $1`+suffix, u))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, application.ErrNotFound
	}
	if err != nil {
		return b, fmt.Errorf("load batch: %w", err)
	}
	if !strings.EqualFold(creator, c.Actor.UserID) || (b.Status == "previewed" && !b.ExpiresAt.After(time.Now())) {
		return b, application.ErrNotFound
	}
	return b, nil
}

func (r *Repository) GetBatch(ctx context.Context, c application.Caller, id string) (application.Batch, error) {
	var out application.Batch
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		b, err := visibleBatch(ctx, tx, c, id, false)
		out = b
		return err
	})
	if err != nil {
		return application.Batch{}, finishBatch(err, "get batch")
	}
	if !c.CanView(out.Kind) {
		return application.Batch{}, application.ErrNotFound
	}
	return out, nil
}

func finishBatch(err error, what string) error {
	_, err = finishPeople(struct{}{}, err, what)
	return err
}

func (r *Repository) ListBatchRows(ctx context.Context, c application.Caller, id string, f application.RowFilter) ([]application.BatchRow, string, error) {
	b, err := r.GetBatch(ctx, c, id)
	if err != nil {
		return nil, "", err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = rowPageSize
	}
	rows, err := r.pool.Query(ctx, `
		SELECT row_no, action, coalesce(row_key, ''), data, coalesce(target_id::text, ''), coalesce(target_version, 0), diff, errors, warnings
		FROM organization.import_rows
		WHERE batch_id = $1::uuid AND row_no > $2 AND ($3 = '' OR action = $3)
		ORDER BY row_no LIMIT $4`, b.ID, f.After, f.Action, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list batch rows: %w", err)
	}
	defer rows.Close()
	out := []application.BatchRow{}
	for rows.Next() {
		var br application.BatchRow
		var data, diff, errs, warns []byte
		if err := rows.Scan(&br.No, &br.Action, &br.Key, &data, &br.TargetID, &br.TargetVersion, &diff, &errs, &warns); err != nil {
			return nil, "", fmt.Errorf("scan batch row: %w", err)
		}
		_ = json.Unmarshal(data, &br.Data)
		_ = json.Unmarshal(diff, &br.Diff)
		_ = json.Unmarshal(errs, &br.Errors)
		_ = json.Unmarshal(warns, &br.Warnings)
		br.Label = br.Data["label"]
		out = append(out, br)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate batch rows: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.Itoa(out[len(out)-1].No)
	}
	return out, next, nil
}

// PurgeExpiredBatches deletes previews past their expiry (personal data; the job runs every few minutes).
func (r *Repository) PurgeExpiredBatches(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM organization.import_batches WHERE status = 'previewed' AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("purge previews: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ---- apply ----

func (r *Repository) ApplyBatch(ctx context.Context, c application.Caller, id, hash string, expectedRejects int) (application.BatchApplied, error) {
	var out application.BatchApplied
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.requireGuards(); err != nil {
			return err
		}
		b, err := visibleBatch(ctx, tx, c, id, true)
		if err != nil {
			return err
		}
		// Every row is authorized again against the applier's current permissions.
		if !c.CanView(b.Kind) {
			return application.ErrNotFound
		}
		if !c.CanApply(b.Kind) {
			return application.ErrForbidden
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(b.PreviewHash)) != 1 || expectedRejects != b.Counts[application.RowReject] {
			return application.ErrImportHashMismatch
		}
		out.Batch = b
		if b.Status == "applied" {
			out.Replayed = true
			return nil
		}
		stored, err := loadStoredRows(ctx, tx, b.ID)
		if err != nil {
			return err
		}
		bc := c
		bc.CorrelationID = b.CorrelationID
		var params map[string]any
		var rawParams []byte
		if err := tx.QueryRow(ctx, `SELECT params FROM organization.import_batches WHERE id = $1::uuid`, b.ID).Scan(&rawParams); err != nil {
			return fmt.Errorf("load batch params: %w", err)
		}
		_ = json.Unmarshal(rawParams, &params)
		applied := map[string]int{application.RowCreate: 0, application.RowUpdate: 0, application.RowUnchanged: 0, application.RowReject: 0}
		for _, s := range stored {
			applied[s.action]++
			if s.action != application.RowCreate && s.action != application.RowUpdate {
				continue
			}
			var rerr error
			switch b.Kind {
			case application.ImportUsers:
				rerr = r.applyUserRow(ctx, tx, bc, b.MatchKey, s)
			case application.ImportLocations:
				rerr = r.applyLocationRow(ctx, tx, bc, s)
			case application.ImportDepartments:
				rerr = r.applyDepartmentRow(ctx, tx, bc, s)
			case application.BatchBulkUsers:
				rerr = r.applyBulkRow(ctx, tx, bc, b.Operation, params, s)
			default:
				rerr = fmt.Errorf("apply: unknown batch kind %q", b.Kind)
			}
			if rerr != nil {
				if _, isIssue := issueOf(rerr); isIssue {
					return &application.ImportRowError{Row: s.no, Cause: rerr}
				}
				return rerr
			}
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `
			UPDATE organization.import_batches SET status = 'applied', applied_at = $2, applied_counts = $3 WHERE id = $1::uuid`,
			b.ID, now, marshalJSON(applied)); err != nil {
			return fmt.Errorf("mark batch applied: %w", err)
		}
		if b.Kind == application.BatchBulkUsers {
			out.Rows, err = bulkResultRows(ctx, tx, b.ID)
			if err != nil {
				return err
			}
		}
		// Applied batches keep counts and the file hash only; the rows hold personal data.
		if _, err := tx.Exec(ctx, `DELETE FROM organization.import_rows WHERE batch_id = $1::uuid`, b.ID); err != nil {
			return fmt.Errorf("delete batch rows: %w", err)
		}
		b.Status, b.AppliedAt, b.AppliedCounts = "applied", &now, applied
		out.Batch = b
		action := "organization.import.applied"
		meta := map[string]any{"kind": b.Kind, "counts": applied, "fileHash": b.FileHash, "requestId": c.CorrelationID}
		if b.Kind == application.BatchBulkUsers {
			action = "organization.bulk.applied"
			meta = map[string]any{"operation": b.Operation, "counts": applied, "requestId": c.CorrelationID}
		}
		return r.record(ctx, tx, bc, action, "import_batch", b.ID, nil, nil, meta)
	})
	if err != nil {
		return application.BatchApplied{}, finishBatchApply(err)
	}
	return out, nil
}

func finishBatchApply(err error) error {
	var row *application.ImportRowError
	switch {
	case errors.As(err, &row), errors.Is(err, application.ErrImportHashMismatch), errors.Is(err, application.ErrForbidden):
		return err
	}
	_, err = finishPeople(struct{}{}, err, "apply batch")
	return err
}

func loadStoredRows(ctx context.Context, tx pgx.Tx, batchID string) ([]rowSpec, error) {
	rows, err := tx.Query(ctx, `
		SELECT row_no, action, coalesce(row_key, ''), data, coalesce(target_id::text, ''), coalesce(target_version, 0)
		FROM organization.import_rows WHERE batch_id = $1::uuid ORDER BY row_no`, batchID)
	if err != nil {
		return nil, fmt.Errorf("load batch rows: %w", err)
	}
	defer rows.Close()
	var out []rowSpec
	for rows.Next() {
		var s rowSpec
		var data []byte
		if err := rows.Scan(&s.no, &s.action, &s.key, &data, &s.targetID, &s.targetVersion); err != nil {
			return nil, fmt.Errorf("scan batch row: %w", err)
		}
		_ = json.Unmarshal(data, &s.data)
		out = append(out, s)
	}
	return out, rows.Err()
}

func bulkResultRows(ctx context.Context, tx pgx.Tx, batchID string) ([]application.BatchRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT row_no, action, coalesce(row_key, ''), data, diff, errors
		FROM organization.import_rows WHERE batch_id = $1::uuid ORDER BY row_no`, batchID)
	if err != nil {
		return nil, fmt.Errorf("load bulk results: %w", err)
	}
	defer rows.Close()
	out := []application.BatchRow{}
	for rows.Next() {
		var br application.BatchRow
		var data, diff, errs []byte
		if err := rows.Scan(&br.No, &br.Action, &br.Key, &data, &diff, &errs); err != nil {
			return nil, fmt.Errorf("scan bulk result: %w", err)
		}
		_ = json.Unmarshal(data, &br.Data)
		_ = json.Unmarshal(diff, &br.Diff)
		_ = json.Unmarshal(errs, &br.Errors)
		br.Label = br.Data["label"]
		out = append(out, br)
	}
	return out, rows.Err()
}
