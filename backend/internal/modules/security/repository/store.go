// Package repository implements the Security store on PostgreSQL.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// Repository stores Advisories, criteria, Findings and their transitions.
type Repository struct{ pool *pgxpool.Pool }

var _ application.Store = (*Repository)(nil)

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, r.pool, fn)
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F'):
			return false
		}
	}
	return true
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ---- advisories ----

const advisoryCols = `id::text, reference, source, external_id, title, summary, severity, published_at, modified_at, source_url,
	status, status_reason, criteria_revision, matched_revision, matched_at, matched_ingestion_at, match_truncated, created_by::text,
	applicable_at, resolved_at, archived_at, version, created_at, updated_at`

func scanAdvisory(row pgx.Row) (application.Advisory, error) {
	var a application.Advisory
	err := row.Scan(&a.ID, &a.Reference, &a.Source, &a.ExternalID, &a.Title, &a.Summary, &a.Severity, &a.PublishedAt, &a.ModifiedAt,
		&a.SourceURL, &a.Status, &a.StatusReason, &a.CriteriaRevision, &a.MatchedRevision, &a.MatchedAt, &a.MatchedIngestionAt,
		&a.MatchTruncated, &a.CreatedBy, &a.ApplicableAt, &a.ResolvedAt, &a.ArchivedAt, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func advisoryResult(a application.Advisory, err error, op string) (application.Advisory, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return application.Advisory{}, application.ErrNotFound
	case isUnique(err):
		return application.Advisory{}, application.ErrDuplicate
	case err != nil:
		return application.Advisory{}, fmt.Errorf("%s advisory: %w", op, err)
	}
	return a, nil
}

func (r *Repository) InsertAdvisoryTx(ctx context.Context, tx pgx.Tx, a application.Advisory) (application.Advisory, error) {
	out, err := scanAdvisory(tx.QueryRow(ctx, `
		INSERT INTO security.advisories(source, external_id, title, summary, severity, published_at, modified_at, source_url, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid) RETURNING `+advisoryCols,
		a.Source, a.ExternalID, a.Title, a.Summary, a.Severity, a.PublishedAt, a.ModifiedAt, a.SourceURL, a.Status, a.CreatedBy))
	return advisoryResult(out, err, "insert")
}

func (r *Repository) LockAdvisorySourceTx(ctx context.Context, tx pgx.Tx, source, externalID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, source+"\x1f"+externalID)
	if err != nil {
		return fmt.Errorf("lock advisory source: %w", err)
	}
	return nil
}

func (r *Repository) LockAdvisoryTx(ctx context.Context, tx pgx.Tx, id string) (application.Advisory, error) {
	if !validUUID(id) {
		return application.Advisory{}, application.ErrNotFound
	}
	a, err := scanAdvisory(tx.QueryRow(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE id = $1::uuid FOR UPDATE`, id))
	return advisoryResult(a, err, "lock")
}

func (r *Repository) LockAdvisoryBySourceTx(ctx context.Context, tx pgx.Tx, source, externalID string) (application.Advisory, error) {
	a, err := scanAdvisory(tx.QueryRow(ctx, `SELECT `+advisoryCols+` FROM security.advisories
		WHERE source = $1 AND external_id = $2 FOR UPDATE`, source, externalID))
	return advisoryResult(a, err, "lock")
}

func (r *Repository) UpdateAdvisoryTx(ctx context.Context, tx pgx.Tx, a application.Advisory) (application.Advisory, error) {
	out, err := scanAdvisory(tx.QueryRow(ctx, `
		UPDATE security.advisories SET title = $2, summary = $3, severity = $4, published_at = $5, modified_at = $6, source_url = $7,
			status = $8, status_reason = $9, criteria_revision = $10, applicable_at = $11, resolved_at = $12, archived_at = $13,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+advisoryCols,
		a.ID, a.Title, a.Summary, a.Severity, a.PublishedAt, a.ModifiedAt, a.SourceURL, a.Status, a.StatusReason, a.CriteriaRevision,
		a.ApplicableAt, a.ResolvedAt, a.ArchivedAt))
	return advisoryResult(out, err, "update")
}

func (r *Repository) RecordMatchTx(ctx context.Context, tx pgx.Tx, id string, m application.MatchState) error {
	_, err := tx.Exec(ctx, `UPDATE security.advisories SET matched_revision = LEAST($2, criteria_revision), matched_at = now(),
		matched_ingestion_at = $3, match_truncated = $4 WHERE id = $1::uuid`, id, m.Revision, m.IngestionAt, m.Truncated)
	if err != nil {
		return fmt.Errorf("record match: %w", err)
	}
	return nil
}

func (r *Repository) GetAdvisory(ctx context.Context, id string) (application.Advisory, error) {
	if !validUUID(id) {
		return application.Advisory{}, application.ErrNotFound
	}
	a, err := scanAdvisory(r.pool.QueryRow(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE id = $1::uuid`, id))
	return advisoryResult(a, err, "get")
}

func (r *Repository) ListAdvisories(ctx context.Context, f application.AdvisoryFilter) (application.Result[application.Advisory], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Severity != "" {
		add("severity = $%d", f.Severity)
	}
	if f.Query != "" {
		q := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(f.Query)) + "%"
		args = append(args, q)
		n := len(args)
		conds = append(conds, fmt.Sprintf("(lower(title) LIKE $%d OR lower(reference) LIKE $%d OR lower(coalesce(external_id, '')) LIKE $%d)", n, n, n))
	}
	if f.Page.Cursor != "" {
		if !validUUID(f.Page.Cursor) {
			return application.Result[application.Advisory]{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", f.Page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Page.Limit+1)
	rows, err := r.pool.Query(ctx, `SELECT `+advisoryCols+` FROM security.advisories `+where+
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return application.Result[application.Advisory]{}, fmt.Errorf("list advisories: %w", err)
	}
	defer rows.Close()
	var res application.Result[application.Advisory]
	for rows.Next() {
		a, err := scanAdvisory(rows)
		if err != nil {
			return application.Result[application.Advisory]{}, fmt.Errorf("list advisories: %w", err)
		}
		res.Items = append(res.Items, a)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Advisory]{}, fmt.Errorf("list advisories: %w", err)
	}
	if len(res.Items) > f.Page.Limit {
		res.Items = res.Items[:f.Page.Limit]
		res.NextCursor = res.Items[f.Page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) AdvisoriesToMatch(ctx context.Context, statuses []string, ingestionAt *time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM security.advisories
		WHERE status = ANY($1::text[]) AND (matched_revision IS NULL OR matched_revision < criteria_revision
			OR ($2::timestamptz IS NOT NULL AND (matched_ingestion_at IS NULL OR matched_ingestion_at < $2::timestamptz)))
		ORDER BY matched_at NULLS FIRST, id LIMIT $3`, statuses, ingestionAt, limit)
	if err != nil {
		return nil, fmt.Errorf("advisories to match: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ---- criteria ----

func (r *Repository) ReplaceCriteriaTx(ctx context.Context, tx pgx.Tx, advisoryID string, crit []application.Criterion) error {
	if _, err := tx.Exec(ctx, `DELETE FROM security.advisory_criteria WHERE advisory_id = $1::uuid`, advisoryID); err != nil {
		return fmt.Errorf("replace criteria: %w", err)
	}
	for i, c := range crit {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO security.advisory_criteria(advisory_id, position, software_product_id, product_name, publisher,
			os_platform, normalization, match_method) VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8) RETURNING id::text`,
			advisoryID, i, c.SoftwareProductID, c.ProductName, c.Publisher, c.OSPlatform, c.Normalization, c.MatchMethod).Scan(&id); err != nil {
			return fmt.Errorf("insert criterion: %w", err)
		}
		for j, rule := range c.Rules {
			if _, err := tx.Exec(ctx, `INSERT INTO security.advisory_criteria_rules(criteria_id, position, kind, version)
				VALUES ($1::uuid, $2, $3, $4)`, id, j, rule.Kind, rule.Version); err != nil {
				return fmt.Errorf("insert criterion rule: %w", err)
			}
		}
	}
	return nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadCriteria(ctx context.Context, q querier, advisoryID string) ([]application.Criterion, error) {
	rows, err := q.Query(ctx, `SELECT c.id::text, c.position, c.software_product_id::text, c.product_name, c.publisher, c.os_platform,
			c.normalization, c.match_method,
			COALESCE((SELECT array_agg(r.kind ORDER BY r.position) FROM security.advisory_criteria_rules r WHERE r.criteria_id = c.id), '{}'),
			COALESCE((SELECT array_agg(r.version ORDER BY r.position) FROM security.advisory_criteria_rules r WHERE r.criteria_id = c.id), '{}')
		FROM security.advisory_criteria c WHERE c.advisory_id = $1::uuid ORDER BY c.position`, advisoryID)
	if err != nil {
		return nil, fmt.Errorf("load criteria: %w", err)
	}
	defer rows.Close()
	var out []application.Criterion
	for rows.Next() {
		var c application.Criterion
		var kinds, versions []string
		if err := rows.Scan(&c.ID, &c.Position, &c.SoftwareProductID, &c.ProductName, &c.Publisher, &c.OSPlatform, &c.Normalization,
			&c.MatchMethod, &kinds, &versions); err != nil {
			return nil, fmt.Errorf("load criteria: %w", err)
		}
		for i := range kinds {
			c.Rules = append(c.Rules, application.Rule{Kind: kinds[i], Version: versions[i]})
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) Criteria(ctx context.Context, advisoryID string) ([]application.Criterion, error) {
	return loadCriteria(ctx, r.pool, advisoryID)
}

func (r *Repository) CriteriaTx(ctx context.Context, tx pgx.Tx, advisoryID string) ([]application.Criterion, error) {
	return loadCriteria(ctx, tx, advisoryID)
}

// ---- transitions ----

func insertTransition(ctx context.Context, tx pgx.Tx, table, subjectCol string, t application.Transition) error {
	_, err := tx.Exec(ctx, `INSERT INTO security.`+table+`(`+subjectCol+`, from_status, to_status, operation, reason, actor_user_id, actor_system, correlation_id)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		t.SubjectID, t.FromStatus, t.ToStatus, t.Operation, t.Reason, t.ActorUserID, t.ActorSystem, t.CorrelationID)
	if err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

func (r *Repository) InsertAdvisoryTransitionTx(ctx context.Context, tx pgx.Tx, t application.Transition) error {
	return insertTransition(ctx, tx, "advisory_transitions", "advisory_id", t)
}

func (r *Repository) InsertFindingTransitionTx(ctx context.Context, tx pgx.Tx, t application.Transition) error {
	return insertTransition(ctx, tx, "finding_transitions", "finding_id", t)
}

func (r *Repository) transitions(ctx context.Context, table, subjectCol, id string, page application.Page) (application.Result[application.Transition], error) {
	args := []any{id}
	cond := ""
	if page.Cursor != "" {
		if !validUUID(page.Cursor) {
			return application.Result[application.Transition]{}, application.ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cond = " AND id > $2::uuid"
	}
	args = append(args, page.Limit+1)
	rows, err := r.pool.Query(ctx, `SELECT id::text, `+subjectCol+`::text, from_status, to_status, operation, reason, actor_user_id::text,
		actor_system, correlation_id, created_at FROM security.`+table+` WHERE `+subjectCol+` = $1::uuid`+cond+
		fmt.Sprintf(" ORDER BY id LIMIT $%d", len(args)), args...)
	if err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()
	var res application.Result[application.Transition]
	for rows.Next() {
		var t application.Transition
		if err := rows.Scan(&t.ID, &t.SubjectID, &t.FromStatus, &t.ToStatus, &t.Operation, &t.Reason, &t.ActorUserID, &t.ActorSystem,
			&t.CorrelationID, &t.CreatedAt); err != nil {
			return application.Result[application.Transition]{}, fmt.Errorf("list %s: %w", table, err)
		}
		res.Items = append(res.Items, t)
	}
	if err := rows.Err(); err != nil {
		return application.Result[application.Transition]{}, fmt.Errorf("list %s: %w", table, err)
	}
	if len(res.Items) > page.Limit {
		res.Items = res.Items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) AdvisoryTransitions(ctx context.Context, advisoryID string, page application.Page) (application.Result[application.Transition], error) {
	return r.transitions(ctx, "advisory_transitions", "advisory_id", advisoryID, page)
}

func (r *Repository) FindingTransitions(ctx context.Context, findingID string, page application.Page) (application.Result[application.Transition], error) {
	return r.transitions(ctx, "finding_transitions", "finding_id", findingID, page)
}

// ---- findings ----

const findingCols = `id::text, reference, advisory_id::text, device_id::text, software_product_id::text, installed_version, confidence,
	status, status_reason, risk_accepted_by::text, risk_accepted_at, risk_review_by, first_seen_at, last_seen_at, remediated_at,
	version, created_at, updated_at`

func scanFinding(row pgx.Row) (application.Finding, error) {
	var f application.Finding
	err := row.Scan(&f.ID, &f.Reference, &f.AdvisoryID, &f.DeviceID, &f.SoftwareProductID, &f.InstalledVersion, &f.Confidence,
		&f.Status, &f.StatusReason, &f.RiskAcceptedBy, &f.RiskAcceptedAt, &f.RiskReviewBy, &f.FirstSeenAt, &f.LastSeenAt,
		&f.RemediatedAt, &f.Version, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

func findingResult(f application.Finding, err error, op string) (application.Finding, error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return application.Finding{}, application.ErrNotFound
	case err != nil:
		return application.Finding{}, fmt.Errorf("%s finding: %w", op, err)
	}
	return f, nil
}

func collectFindings(rows pgx.Rows, err error) ([]application.Finding, error) {
	if err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	defer rows.Close()
	var out []application.Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("list findings: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	return out, nil
}

func (r *Repository) InsertFindingTx(ctx context.Context, tx pgx.Tx, f application.Finding) (application.Finding, bool, error) {
	out, err := scanFinding(tx.QueryRow(ctx, `
		INSERT INTO security.vulnerability_findings(advisory_id, device_id, software_product_id, installed_version, confidence, status,
			first_seen_at, last_seen_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)
		ON CONFLICT (advisory_id, device_id, software_product_id) DO NOTHING RETURNING `+findingCols,
		f.AdvisoryID, f.DeviceID, f.SoftwareProductID, f.InstalledVersion, f.Confidence, f.Status, f.FirstSeenAt, f.LastSeenAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Finding{}, false, nil
	}
	if err != nil {
		return application.Finding{}, false, fmt.Errorf("insert finding: %w", err)
	}
	return out, true, nil
}

func (r *Repository) LockFindingTx(ctx context.Context, tx pgx.Tx, id string) (application.Finding, error) {
	if !validUUID(id) {
		return application.Finding{}, application.ErrNotFound
	}
	f, err := scanFinding(tx.QueryRow(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings WHERE id = $1::uuid FOR UPDATE`, id))
	return findingResult(f, err, "lock")
}

func (r *Repository) LockFindingsOfAdvisoryTx(ctx context.Context, tx pgx.Tx, advisoryID string) ([]application.Finding, error) {
	return collectFindings(tx.Query(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings
		WHERE advisory_id = $1::uuid ORDER BY id FOR UPDATE`, advisoryID))
}

func (r *Repository) FindingsOfAdvisory(ctx context.Context, advisoryID string) ([]application.Finding, error) {
	return collectFindings(r.pool.Query(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings
		WHERE advisory_id = $1::uuid ORDER BY id`, advisoryID))
}

func (r *Repository) UpdateFindingTx(ctx context.Context, tx pgx.Tx, f application.Finding) (application.Finding, error) {
	out, err := scanFinding(tx.QueryRow(ctx, `
		UPDATE security.vulnerability_findings SET installed_version = $2, confidence = $3, status = $4, status_reason = $5,
			risk_accepted_by = $6::uuid, risk_accepted_at = $7, risk_review_by = $8, first_seen_at = $9, last_seen_at = $10,
			remediated_at = $11, version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+findingCols,
		f.ID, f.InstalledVersion, f.Confidence, f.Status, f.StatusReason, f.RiskAcceptedBy, f.RiskAcceptedAt, f.RiskReviewBy,
		f.FirstSeenAt, f.LastSeenAt, f.RemediatedAt))
	return findingResult(out, err, "update")
}

func (r *Repository) ObserveFindingTx(ctx context.Context, tx pgx.Tx, f application.Finding) error {
	_, err := tx.Exec(ctx, `
		UPDATE security.vulnerability_findings SET
			version = version + CASE WHEN confidence <> $2 OR installed_version <> $3 THEN 1 ELSE 0 END,
			confidence = $2, installed_version = $3, first_seen_at = $4, last_seen_at = $5, updated_at = now()
		WHERE id = $1::uuid`, f.ID, f.Confidence, f.InstalledVersion, f.FirstSeenAt, f.LastSeenAt)
	if err != nil {
		return fmt.Errorf("observe finding: %w", err)
	}
	return nil
}

func (r *Repository) GetFinding(ctx context.Context, id string) (application.Finding, error) {
	if !validUUID(id) {
		return application.Finding{}, application.ErrNotFound
	}
	f, err := scanFinding(r.pool.QueryRow(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings WHERE id = $1::uuid`, id))
	return findingResult(f, err, "get")
}

func (r *Repository) ListFindings(ctx context.Context, f application.FindingFilter) (application.Result[application.Finding], error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.AdvisoryID != "" {
		add("advisory_id = $%d::uuid", f.AdvisoryID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Confidence != "" {
		add("confidence = $%d", f.Confidence)
	}
	if f.Page.Cursor != "" {
		if !validUUID(f.Page.Cursor) {
			return application.Result[application.Finding]{}, application.ErrInvalidCursor
		}
		add("id < $%d::uuid", f.Page.Cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Page.Limit+1)
	items, err := collectFindings(r.pool.Query(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings `+where+
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)), args...))
	if err != nil {
		return application.Result[application.Finding]{}, err
	}
	res := application.Result[application.Finding]{Items: items}
	if len(items) > f.Page.Limit {
		res.Items = items[:f.Page.Limit]
		res.NextCursor = res.Items[f.Page.Limit-1].ID
	}
	return res, nil
}

func (r *Repository) Summary(ctx context.Context, advisoryID string) (application.Summary, error) {
	s := application.Summary{ByStatus: map[string]int{}, ByConfidence: map[string]int{}}
	rows, err := r.pool.Query(ctx, `SELECT status, confidence, count(*) FROM security.vulnerability_findings
		WHERE advisory_id = $1::uuid GROUP BY status, confidence`, advisoryID)
	if err != nil {
		return application.Summary{}, fmt.Errorf("summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status, confidence string
		var n int
		if err := rows.Scan(&status, &confidence, &n); err != nil {
			return application.Summary{}, fmt.Errorf("summary: %w", err)
		}
		s.ByStatus[status] += n
		s.ByConfidence[confidence] += n
	}
	if err := rows.Err(); err != nil {
		return application.Summary{}, fmt.Errorf("summary: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(DISTINCT device_id), min(first_seen_at) FROM security.vulnerability_findings
		WHERE advisory_id = $1::uuid AND status IN ('open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'risk_accepted')`, advisoryID).
		Scan(&s.AffectedDevices, &s.OldestOpenSince); err != nil {
		return application.Summary{}, fmt.Errorf("summary: %w", err)
	}
	return s, nil
}
