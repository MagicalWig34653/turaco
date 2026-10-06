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

const advisoryCols = `id::text, reference, source, external_id, title, summary, severity, edited_by_user, published_at, modified_at, source_url,
	status, status_reason, criteria_revision, criteria_changed_at, matched_revision, matched_at, matched_ingestion_at, match_truncated, created_by::text,
	applicable_at, resolved_at, archived_at, version, created_at, updated_at,
	known_exploited, known_exploited_added_at, kev_due_date,
	(SELECT count(*) FROM security.advisory_criteria c WHERE c.advisory_id = security.advisories.id AND c.normalization = 'unmatched')`

func scanAdvisory(row pgx.Row) (application.Advisory, error) {
	var a application.Advisory
	err := row.Scan(&a.ID, &a.Reference, &a.Source, &a.ExternalID, &a.Title, &a.Summary, &a.Severity, &a.EditedByUser, &a.PublishedAt, &a.ModifiedAt,
		&a.SourceURL, &a.Status, &a.StatusReason, &a.CriteriaRevision, &a.CriteriaChangedAt, &a.MatchedRevision, &a.MatchedAt, &a.MatchedIngestionAt,
		&a.MatchTruncated, &a.CreatedBy, &a.ApplicableAt, &a.ResolvedAt, &a.ArchivedAt, &a.Version, &a.CreatedAt, &a.UpdatedAt,
		&a.KnownExploited, &a.KnownExploitedAddedAt, &a.KEVDueDate, &a.UnmatchedCriteria)
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
			status = $8, status_reason = $9, criteria_changed_at = CASE WHEN criteria_revision <> $10 THEN now() ELSE criteria_changed_at END,
			criteria_revision = $10, applicable_at = $11, resolved_at = $12, archived_at = $13,
			edited_by_user = $14,
			version = version + 1, updated_at = now()
		WHERE id = $1::uuid RETURNING `+advisoryCols,
		a.ID, a.Title, a.Summary, a.Severity, a.PublishedAt, a.ModifiedAt, a.SourceURL, a.Status, a.StatusReason, a.CriteriaRevision,
		a.ApplicableAt, a.ResolvedAt, a.ArchivedAt, a.EditedByUser))
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

// AdvisoriesByIDs loads the Advisory metadata needed to render a page of Findings in one query.
func (r *Repository) AdvisoriesByIDs(ctx context.Context, ids []string) (map[string]application.Advisory, error) {
	out := make(map[string]application.Advisory, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+advisoryCols+` FROM security.advisories WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("load advisories by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAdvisory(rows)
		if err != nil {
			return nil, fmt.Errorf("load advisories by ids: %w", err)
		}
		out[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load advisories by ids: %w", err)
	}
	return out, nil
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

func (r *Repository) AdvisoriesToMatch(ctx context.Context, statuses []string, productObservedAt map[string]time.Time, limit int) ([]string, error) {
	products := make([]string, 0, len(productObservedAt))
	times := make([]time.Time, 0, len(productObservedAt))
	for id, at := range productObservedAt {
		products = append(products, id)
		times = append(times, at)
	}
	rows, err := r.pool.Query(ctx, `SELECT a.id::text FROM security.advisories a
		WHERE a.status = ANY($1::text[]) AND (a.matched_revision IS NULL OR a.matched_revision < a.criteria_revision
			OR EXISTS (
				SELECT 1 FROM security.advisory_criteria c
				JOIN unnest($2::uuid[], $3::timestamptz[]) AS observation(product_id, observed_at)
					ON observation.product_id = c.software_product_id
				WHERE c.advisory_id = a.id AND observation.observed_at > a.matched_at))
		ORDER BY a.matched_at NULLS FIRST, a.id LIMIT $4`, statuses, products, times, limit)
	if err != nil {
		return nil, fmt.Errorf("advisories to match: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// MatchableProductIDs returns only normalized products referenced by matchable Advisories.
func (r *Repository) MatchableProductIDs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT c.software_product_id::text
		FROM security.advisory_criteria c JOIN security.advisories a ON a.id = c.advisory_id
		WHERE c.software_product_id IS NOT NULL
			AND a.status IN ('new', 'analyzing', 'applicable', 'remediation_planned', 'remediating', 'resolved')`)
	if err != nil {
		return nil, fmt.Errorf("matchable product ids: %w", err)
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

// InsertFindingsTx inserts observations in one statement. Existing finding identities are left untouched.
func (r *Repository) InsertFindingsTx(ctx context.Context, tx pgx.Tx, findings []application.Finding) ([]application.Finding, error) {
	if len(findings) == 0 {
		return nil, nil
	}
	advisories, devices, products := make([]string, 0, len(findings)), make([]string, 0, len(findings)), make([]string, 0, len(findings))
	versions, confidences := make([]string, 0, len(findings)), make([]string, 0, len(findings))
	first, last := make([]time.Time, 0, len(findings)), make([]time.Time, 0, len(findings))
	for _, f := range findings {
		advisories, devices, products = append(advisories, f.AdvisoryID), append(devices, f.DeviceID), append(products, f.SoftwareProductID)
		versions, confidences = append(versions, f.InstalledVersion), append(confidences, f.Confidence)
		first, last = append(first, f.FirstSeenAt), append(last, f.LastSeenAt)
	}
	return collectFindings(tx.Query(ctx, `INSERT INTO security.vulnerability_findings
		(advisory_id, device_id, software_product_id, installed_version, confidence, status, first_seen_at, last_seen_at)
		SELECT advisory_id, device_id, product_id, installed_version, confidence, 'open', first_seen_at, last_seen_at
		FROM unnest($1::uuid[], $2::uuid[], $3::uuid[], $4::text[], $5::text[], $6::timestamptz[], $7::timestamptz[])
			AS input(advisory_id, device_id, product_id, installed_version, confidence, first_seen_at, last_seen_at)
		WHERE true
		ON CONFLICT (advisory_id, device_id, software_product_id) DO NOTHING RETURNING `+findingCols,
		advisories, devices, products, versions, confidences, first, last))
}

// FindingsForDevicesTx reads only this match batch; the advisory row lock serializes match runs.
func (r *Repository) FindingsForDevicesTx(ctx context.Context, tx pgx.Tx, advisoryID string, deviceIDs []string) ([]application.Finding, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	return collectFindings(tx.Query(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings
		WHERE advisory_id = $1::uuid AND device_id = ANY($2::uuid[]) ORDER BY id`, advisoryID, deviceIDs))
}

// LockFindingsByIDsTx locks only findings selected for a status transition.
func (r *Repository) LockFindingsByIDsTx(ctx context.Context, tx pgx.Tx, ids []string) ([]application.Finding, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return collectFindings(tx.Query(ctx, `SELECT `+findingCols+` FROM security.vulnerability_findings
		WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, ids))
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

// DueRiskFindingIDs returns expired acceptances in stable order for the periodic review pass.
func (r *Repository) DueRiskFindingIDs(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text FROM security.vulnerability_findings
		WHERE status = 'risk_accepted' AND risk_review_by < CURRENT_DATE
		ORDER BY risk_review_by, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("due risk findings: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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

// ObserveFindingsTx refreshes observations in one statement. A plain freshness update is coalesced
// to hourly writes; a changed version or confidence is recorded immediately.
func (r *Repository) ObserveFindingsTx(ctx context.Context, tx pgx.Tx, findings []application.Finding) error {
	if len(findings) == 0 {
		return nil
	}
	ids, versions, confidences := make([]string, 0, len(findings)), make([]string, 0, len(findings)), make([]string, 0, len(findings))
	first, last := make([]time.Time, 0, len(findings)), make([]time.Time, 0, len(findings))
	for _, f := range findings {
		ids, versions, confidences = append(ids, f.ID), append(versions, f.InstalledVersion), append(confidences, f.Confidence)
		first, last = append(first, f.FirstSeenAt), append(last, f.LastSeenAt)
	}
	_, err := tx.Exec(ctx, `UPDATE security.vulnerability_findings AS f SET
		version = f.version + CASE WHEN f.confidence IS DISTINCT FROM input.confidence
			OR f.installed_version IS DISTINCT FROM input.installed_version THEN 1 ELSE 0 END,
		confidence = input.confidence, installed_version = input.installed_version,
		first_seen_at = LEAST(f.first_seen_at, input.first_seen_at),
		last_seen_at = GREATEST(f.last_seen_at, input.last_seen_at), updated_at = now()
		FROM unnest($1::uuid[], $2::text[], $3::text[], $4::timestamptz[], $5::timestamptz[])
			AS input(id, installed_version, confidence, first_seen_at, last_seen_at)
		WHERE f.id = input.id AND (f.confidence IS DISTINCT FROM input.confidence
			OR f.installed_version IS DISTINCT FROM input.installed_version
			OR input.first_seen_at < f.first_seen_at
			OR input.last_seen_at > f.last_seen_at + interval '1 hour')`, ids, versions, confidences, first, last)
	if err != nil {
		return fmt.Errorf("observe findings: %w", err)
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
	if f.AdvisoryID == "" {
		conds = append(conds, `EXISTS (SELECT 1 FROM security.advisories a WHERE a.id = advisory_id
			AND a.status IN ('new', 'analyzing', 'applicable', 'remediation_planned', 'remediating', 'resolved'))`)
		where = "WHERE " + strings.Join(conds, " AND ")
	}
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
	var open, investigating, accepted, planned, remediating, remediated, falsePositive, riskAccepted, probable, potential int
	if err := r.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'open'),
		count(*) FILTER (WHERE status = 'investigating'),
		count(*) FILTER (WHERE status = 'accepted'),
		count(*) FILTER (WHERE status = 'remediation_planned'),
		count(*) FILTER (WHERE status = 'remediating'),
		count(*) FILTER (WHERE status = 'remediated'),
		count(*) FILTER (WHERE status = 'false_positive'),
		count(*) FILTER (WHERE status = 'risk_accepted'),
		count(*) FILTER (WHERE confidence = 'probable'),
		count(*) FILTER (WHERE confidence = 'potential'),
		count(DISTINCT device_id) FILTER (WHERE status IN ('open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'risk_accepted')),
		min(first_seen_at) FILTER (WHERE status IN ('open', 'investigating', 'accepted', 'remediation_planned', 'remediating', 'risk_accepted')),
		(SELECT count(*) FROM security.advisory_criteria c WHERE c.advisory_id = $1::uuid AND c.normalization = 'unmatched')
		FROM security.vulnerability_findings WHERE advisory_id = $1::uuid`, advisoryID).
		Scan(&open, &investigating, &accepted, &planned, &remediating, &remediated, &falsePositive, &riskAccepted,
			&probable, &potential, &s.AffectedDevices, &s.OldestOpenSince, &s.UnmatchedCriteria); err != nil {
		return application.Summary{}, fmt.Errorf("summary: %w", err)
	}
	s.ByStatus["open"], s.ByStatus["investigating"], s.ByStatus["accepted"] = open, investigating, accepted
	s.ByStatus["remediation_planned"], s.ByStatus["remediating"], s.ByStatus["remediated"] = planned, remediating, remediated
	s.ByStatus["false_positive"], s.ByStatus["risk_accepted"] = falsePositive, riskAccepted
	s.ByConfidence["probable"], s.ByConfidence["potential"] = probable, potential
	return s, nil
}
