package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// CriterionInput describes affected software: a Software Product by id, or a product name (with an
// optional publisher) that normalization matches to a Software Product by exact name or alias.
type CriterionInput struct {
	SoftwareProductID string
	ProductName       string
	Publisher         string
	OSPlatform        string
	Rules             []Rule
}

// AdvisoryInput describes an Advisory to create or import.
type AdvisoryInput struct {
	Source      string
	ExternalID  string
	Title       string
	Summary     string
	Severity    string
	SourceURL   string
	PublishedAt *time.Time
	ModifiedAt  *time.Time
	Criteria    []CriterionInput
}

// FromRecord converts a normalized feed record.
func FromRecord(r advisories.AdvisoryRecord) AdvisoryInput {
	in := AdvisoryInput{Source: r.Source, ExternalID: r.ExternalID, Title: r.Title, Summary: r.Summary, Severity: r.Severity,
		SourceURL: r.SourceURL, PublishedAt: r.PublishedAt, ModifiedAt: r.ModifiedAt}
	for _, c := range r.Criteria {
		ci := CriterionInput{ProductName: c.ProductName, Publisher: c.Publisher, OSPlatform: c.OSPlatform}
		for _, rule := range c.Rules {
			ci.Rules = append(ci.Rules, Rule{Kind: rule.Kind, Version: rule.Version})
		}
		in.Criteria = append(in.Criteria, ci)
	}
	return in
}

// cleanCriteria validates criteria without resolving products.
func cleanCriteria(in []CriterionInput) ([]Criterion, error) {
	if len(in) > MaxCriteria {
		return nil, invalid("an advisory has at most %d criteria", MaxCriteria)
	}
	out := make([]Criterion, 0, len(in))
	for i, c := range in {
		var cr Criterion
		cr.Position = i
		if id := strings.TrimSpace(c.SoftwareProductID); id != "" {
			if !uuidPattern.MatchString(id) {
				return nil, invalid("criteria[%d].softwareProductId must be a UUID", i)
			}
			id = strings.ToLower(id)
			cr.SoftwareProductID = &id
		}
		var err error
		if cr.ProductName, err = cleanLine(fmt.Sprintf("criteria[%d].productName", i), c.ProductName, maxProduct, false); err != nil {
			return nil, err
		}
		if cr.Publisher, err = cleanLine(fmt.Sprintf("criteria[%d].publisher", i), c.Publisher, maxProduct, false); err != nil {
			return nil, err
		}
		if cr.SoftwareProductID == nil && cr.ProductName == nil {
			return nil, invalid("criteria[%d] needs a softwareProductId or a productName", i)
		}
		if p := strings.ToLower(strings.TrimSpace(c.OSPlatform)); p != "" {
			if !slices.Contains(Platforms, p) {
				return nil, invalid("criteria[%d].osPlatform must be one of %s", i, strings.Join(Platforms, ", "))
			}
			cr.OSPlatform = &p
		}
		if len(c.Rules) > MaxRules {
			return nil, invalid("criteria[%d] has at most %d version rules", i, MaxRules)
		}
		for j, r := range c.Rules {
			kind := strings.ToLower(strings.TrimSpace(r.Kind))
			if !slices.Contains(advisories.RuleKinds, kind) {
				return nil, invalid("criteria[%d].rules[%d].kind must be one of %s", i, j, strings.Join(advisories.RuleKinds, ", "))
			}
			v, err := cleanLine(fmt.Sprintf("criteria[%d].rules[%d].version", i, j), r.Version, maxVersion, true)
			if err != nil {
				return nil, err
			}
			cr.Rules = append(cr.Rules, Rule{Kind: kind, Version: *v})
		}
		cr.Normalization = NormalizationUnmatched
		out = append(out, cr)
	}
	return out, nil
}

// normalize resolves the products of the criteria through the Endpoints inventory: an explicit product
// id must exist; a name is matched by exact product name or alias, else the criterion stays unmatched.
// With onlyUnmatched, matched criteria keep their product.
func (s *Service) normalize(ctx context.Context, crit []Criterion, onlyUnmatched bool) ([]Criterion, error) {
	var explicit []string
	for _, c := range crit {
		if c.SoftwareProductID != nil && (c.MatchMethod == nil || *c.MatchMethod == MethodExplicit) {
			explicit = append(explicit, *c.SoftwareProductID)
		}
	}
	known := map[string]SoftwareProduct{}
	if len(explicit) > 0 && !onlyUnmatched {
		var err error
		if known, err = s.inventory.SoftwareProducts(ctx, explicit); err != nil {
			return nil, fmt.Errorf("load software products: %w", err)
		}
	}
	out := slices.Clone(crit)
	for i, c := range out {
		if onlyUnmatched && c.Normalization == NormalizationMatched {
			continue
		}
		if c.SoftwareProductID != nil && (c.MatchMethod == nil || *c.MatchMethod == MethodExplicit) {
			if onlyUnmatched {
				continue
			}
			if _, ok := known[*c.SoftwareProductID]; !ok {
				return nil, ErrReferenceInvalid
			}
			m := MethodExplicit
			out[i].MatchMethod, out[i].Normalization = &m, NormalizationMatched
			continue
		}
		publisher := ""
		if c.Publisher != nil {
			publisher = *c.Publisher
		}
		p, method, found, err := s.inventory.FindSoftwareProduct(ctx, *c.ProductName, publisher)
		if err != nil {
			return nil, fmt.Errorf("find software product: %w", err)
		}
		if !found {
			out[i].SoftwareProductID, out[i].MatchMethod, out[i].Normalization = nil, nil, NormalizationUnmatched
			continue
		}
		id := p.ID
		out[i].SoftwareProductID, out[i].MatchMethod, out[i].Normalization = &id, &method, NormalizationMatched
	}
	return out, nil
}

// criteriaKey is the content of criteria as the source stated them (normalization results excluded).
func criteriaKey(crit []Criterion) string {
	var b strings.Builder
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	for _, c := range crit {
		explicit := ""
		if c.MatchMethod == nil || *c.MatchMethod == MethodExplicit {
			explicit = deref(c.SoftwareProductID)
		}
		fmt.Fprintf(&b, "%s\x1f%s\x1f%s\x1f%s", explicit, strings.ToLower(deref(c.ProductName)), strings.ToLower(deref(c.Publisher)), deref(c.OSPlatform))
		for _, r := range c.Rules {
			fmt.Fprintf(&b, "\x1f%s=%s", r.Kind, r.Version)
		}
		b.WriteByte('\x1e')
	}
	return b.String()
}

// cleanAdvisory validates the advisory fields of an input.
func cleanAdvisory(in AdvisoryInput) (Advisory, error) {
	var a Advisory
	a.Source = strings.TrimSpace(in.Source)
	if !sourcePattern.MatchString(a.Source) {
		return Advisory{}, invalid("source must be 2-40 lower-case letters, digits, '-' or '_' starting with a letter")
	}
	var err error
	if a.ExternalID, err = cleanLine("externalId", in.ExternalID, maxExternal, a.Source != SourceManual); err != nil {
		return Advisory{}, err
	}
	title, err := cleanLine("title", in.Title, maxTitle, true)
	if err != nil {
		return Advisory{}, err
	}
	a.Title = *title
	if a.Summary, err = cleanText("summary", in.Summary, maxSummary); err != nil {
		return Advisory{}, err
	}
	a.Severity = strings.ToLower(strings.TrimSpace(in.Severity))
	if !slices.Contains(Severities, a.Severity) {
		return Advisory{}, invalid("severity must be one of %s", strings.Join(Severities, ", "))
	}
	if a.SourceURL, err = CleanSourceURL(in.SourceURL); err != nil {
		return Advisory{}, err
	}
	if a.PublishedAt, err = checkTime("publishedAt", in.PublishedAt); err != nil {
		return Advisory{}, err
	}
	if a.ModifiedAt, err = checkTime("modifiedAt", in.ModifiedAt); err != nil {
		return Advisory{}, err
	}
	return a, nil
}

// commitAdvisory writes a changed Advisory, its transition (when the status moved), the
// SecurityAdvisoryPublished event (when it became applicable) and its audit entry in the caller's
// transaction. The Advisory must be locked.
func (s *Service) commitAdvisory(ctx context.Context, tx pgx.Tx, c Caller, cur, next Advisory, op, reason string, meta map[string]any) (Advisory, error) {
	out, err := s.store.UpdateAdvisoryTx(ctx, tx, next)
	if err != nil {
		return Advisory{}, err
	}
	if cur.Status != out.Status {
		if err := s.store.InsertAdvisoryTransitionTx(ctx, tx, transition(c, out.ID, &cur.Status, out.Status, op, reason)); err != nil {
			return Advisory{}, err
		}
		if out.Status == AdvisoryApplicable {
			if err := publish(ctx, tx, c, EventAdvisoryPublished, map[string]any{"advisoryId": out.ID, "severity": out.Severity}); err != nil {
				return Advisory{}, err
			}
		}
	}
	if reason != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reason"] = reason
	}
	if err := recordAudit(ctx, tx, c, "security.advisory."+op, "security_advisory", out.ID, advisoryState(&cur), advisoryState(&out), meta); err != nil {
		return Advisory{}, err
	}
	return out, nil
}

// insertAdvisory inserts a new Advisory with its criteria, its creation transition and audit entry and
// queues its matching job.
func (s *Service) insertAdvisory(ctx context.Context, tx pgx.Tx, c Caller, a Advisory, crit []Criterion, op string) (Advisory, error) {
	out, err := s.store.InsertAdvisoryTx(ctx, tx, a)
	if err != nil {
		return Advisory{}, err
	}
	if err := s.store.ReplaceCriteriaTx(ctx, tx, out.ID, crit); err != nil {
		return Advisory{}, err
	}
	if err := s.store.InsertAdvisoryTransitionTx(ctx, tx, transition(c, out.ID, nil, out.Status, "create", "")); err != nil {
		return Advisory{}, err
	}
	meta := map[string]any{"source": out.Source, "criteria": len(crit)}
	if err := recordAudit(ctx, tx, c, "security.advisory."+op, "security_advisory", out.ID, nil, advisoryState(&out), meta); err != nil {
		return Advisory{}, err
	}
	return out, enqueueMatch(ctx, tx, out.ID)
}

// Create creates an Advisory by hand (source "manual") in status new. Requires security.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in AdvisoryInput) (Advisory, []Criterion, error) {
	if err := c.validate(); err != nil {
		return Advisory{}, nil, err
	}
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Advisory{}, nil, ErrForbidden
	}
	in.Source = SourceManual
	a, err := cleanAdvisory(in)
	if err != nil {
		return Advisory{}, nil, err
	}
	crit, err := cleanCriteria(in.Criteria)
	if err != nil {
		return Advisory{}, nil, err
	}
	if crit, err = s.normalize(ctx, crit, false); err != nil {
		return Advisory{}, nil, err
	}
	a.Status, a.CreatedBy = AdvisoryNew, &p.UserID
	var out Advisory
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.insertAdvisory(ctx, tx, c, a, crit, "created")
		return err
	})
	if err != nil {
		return Advisory{}, nil, err
	}
	return out, crit, nil
}

// ImportError reports one rejected import record (by its position in the batch).
type ImportError struct {
	Index   int
	Message string
}

// ImportResult counts an import.
type ImportResult struct {
	Created   int
	Updated   int
	Unchanged int
	Rejected  int
	// Reanalyze counts materially changed applicable advisories returned to analyzing.
	Reanalyze int
	Errors    []ImportError
}

// Import upserts up to MaxImportRecords advisories by source and external id, idempotently: a new record
// creates an Advisory in status new; a changed record updates it (its criteria are replaced and
// re-matched when they changed; any changed applicable Advisory returns to analyzing, with reason
// criteria_changed or feed_changed); an unchanged record changes nothing. Invalid records are rejected one by one
// (reported by index) without stopping the batch. The source "manual" is reserved. Each record commits on
// its own. Requires security.manage.
func (s *Service) Import(ctx context.Context, c Caller, p Principal, records []AdvisoryInput) (ImportResult, error) {
	if err := c.validate(); err != nil {
		return ImportResult{}, err
	}
	if !p.Manage {
		return ImportResult{}, ErrForbidden
	}
	if len(records) == 0 || len(records) > MaxImportRecords {
		return ImportResult{}, invalid("an import contains 1-%d advisories", MaxImportRecords)
	}
	var res ImportResult
	reject := func(i int, err error) error {
		var inv *InvalidInputError
		switch {
		case errors.As(err, &inv):
			res.Rejected++
			if len(res.Errors) < MaxImportErrors {
				res.Errors = append(res.Errors, ImportError{Index: i, Message: inv.Message})
			}
			return nil
		case errors.Is(err, ErrReferenceInvalid):
			res.Rejected++
			if len(res.Errors) < MaxImportErrors {
				res.Errors = append(res.Errors, ImportError{Index: i, Message: "a referenced software product does not exist"})
			}
			return nil
		}
		return err
	}
	seen := map[string]bool{}
	for i, in := range records {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if strings.TrimSpace(in.Source) == SourceManual {
			if err := reject(i, invalid("the source %q is reserved for advisories entered by hand", SourceManual)); err != nil {
				return res, err
			}
			continue
		}
		a, err := cleanAdvisory(in)
		if err != nil {
			if err := reject(i, err); err != nil {
				return res, err
			}
			continue
		}
		key := a.Source + "\x1f" + *a.ExternalID
		if seen[key] {
			if err := reject(i, invalid("the batch contains this source and external id twice")); err != nil {
				return res, err
			}
			continue
		}
		seen[key] = true
		crit, err := cleanCriteria(in.Criteria)
		if err == nil {
			crit, err = s.normalize(ctx, crit, false)
		}
		if err != nil {
			if err := reject(i, err); err != nil {
				return res, err
			}
			continue
		}
		outcome, err := s.importOne(ctx, c, p, a, crit)
		if err != nil {
			return res, fmt.Errorf("import record %d: %w", i, err)
		}
		switch outcome {
		case "created":
			res.Created++
		case "updated":
			res.Updated++
		case "reanalyze":
			res.Updated++
			res.Reanalyze++
		default:
			res.Unchanged++
		}
	}
	return res, nil
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func sameStr(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (s *Service) importOne(ctx context.Context, c Caller, p Principal, in Advisory, crit []Criterion) (string, error) {
	outcome := "unchanged"
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := s.store.LockAdvisorySourceTx(ctx, tx, in.Source, *in.ExternalID); err != nil {
			return err
		}
		cur, err := s.store.LockAdvisoryBySourceTx(ctx, tx, in.Source, *in.ExternalID)
		if errors.Is(err, ErrNotFound) {
			in.Status = AdvisoryNew
			if p.UserID != "" {
				in.CreatedBy = &p.UserID
			}
			if _, err := s.insertAdvisory(ctx, tx, c, in, crit, "imported"); err != nil {
				return err
			}
			outcome = "created"
			return nil
		}
		if err != nil {
			return err
		}
		old, err := s.store.CriteriaTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		next := cur
		var changed []string
		if cur.Title != in.Title {
			next.Title = in.Title
			changed = append(changed, "title")
		}
		if !sameStr(cur.Summary, in.Summary) {
			next.Summary = in.Summary
			changed = append(changed, "summary")
		}
		if cur.Severity != in.Severity {
			next.Severity = in.Severity
			changed = append(changed, "severity")
		}
		if !sameStr(cur.SourceURL, in.SourceURL) {
			next.SourceURL = in.SourceURL
			changed = append(changed, "sourceUrl")
		}
		if !sameTime(cur.PublishedAt, in.PublishedAt) {
			next.PublishedAt = in.PublishedAt
			changed = append(changed, "publishedAt")
		}
		if !sameTime(cur.ModifiedAt, in.ModifiedAt) {
			next.ModifiedAt = in.ModifiedAt
			changed = append(changed, "modifiedAt")
		}
		criteriaChanged := criteriaKey(old) != criteriaKey(crit)
		if criteriaChanged {
			changed = append(changed, "criteria")
			next.CriteriaRevision = cur.CriteriaRevision + 1
		}
		if len(changed) == 0 {
			return nil
		}
		reason := ""
		outcome = "updated"
		if cur.Status == AdvisoryApplicable {
			reason = ReasonFeedChanged
			if criteriaChanged {
				reason = ReasonCriteriaChanged
			}
			next.Status, next.StatusReason, next.ApplicableAt = AdvisoryAnalyzing, &reason, nil
			outcome = "reanalyze"
		}
		if criteriaChanged {
			if err := s.store.ReplaceCriteriaTx(ctx, tx, cur.ID, crit); err != nil {
				return err
			}
		}
		op := "updated"
		if reason != "" {
			op = "reanalysis_started"
		}
		if _, err := s.commitAdvisory(ctx, tx, c, cur, next, op, reason, map[string]any{"changedFields": changed, "via": "import"}); err != nil {
			return err
		}
		if criteriaChanged {
			return enqueueMatch(ctx, tx, cur.ID)
		}
		return nil
	})
	return outcome, err
}

// Details changes an Advisory; nil fields stay unchanged; empty strings clear optional fields.
type Details struct {
	Title     *string
	Summary   *string
	Severity  *string
	SourceURL *string
}

// UpdateDetails changes title, summary, severity and source URL of an Advisory that is not archived.
// Requires security.manage and expectedVersion.
func (s *Service) UpdateDetails(ctx context.Context, c Caller, p Principal, id string, expected *int, in Details) (Advisory, error) {
	if err := c.validate(); err != nil {
		return Advisory{}, err
	}
	if !p.Manage {
		return Advisory{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Advisory{}, err
	}
	var title, summary, sourceURL *string
	if in.Title != nil {
		if title, err = cleanLine("title", *in.Title, maxTitle, true); err != nil {
			return Advisory{}, err
		}
	}
	if in.Summary != nil {
		if summary, err = cleanText("summary", *in.Summary, maxSummary); err != nil {
			return Advisory{}, err
		}
	}
	if in.SourceURL != nil {
		if sourceURL, err = CleanSourceURL(*in.SourceURL); err != nil {
			return Advisory{}, err
		}
	}
	severity := ""
	if in.Severity != nil {
		severity = strings.ToLower(strings.TrimSpace(*in.Severity))
		if !slices.Contains(Severities, severity) {
			return Advisory{}, invalid("severity must be one of %s", strings.Join(Severities, ", "))
		}
	}
	var out Advisory
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockAdvisory(ctx, tx, id, exp, "update", AdvisoryNew, AdvisoryAnalyzing, AdvisoryApplicable, AdvisoryNotApplicable,
			AdvisoryRemediationPlanned, AdvisoryRemediating, AdvisoryResolved)
		if err != nil {
			return err
		}
		next := cur
		var changed []string
		if title != nil && *title != cur.Title {
			next.Title = *title
			changed = append(changed, "title")
		}
		if in.Summary != nil && !sameStr(summary, cur.Summary) {
			next.Summary = summary
			changed = append(changed, "summary")
		}
		if severity != "" && severity != cur.Severity {
			next.Severity = severity
			changed = append(changed, "severity")
		}
		if in.SourceURL != nil && !sameStr(sourceURL, cur.SourceURL) {
			next.SourceURL = sourceURL
			changed = append(changed, "sourceUrl")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.commitAdvisory(ctx, tx, c, cur, next, "updated", "", map[string]any{"changedFields": changed})
		return err
	})
	return out, err
}

// EditCriteria replaces the affected criteria while the Advisory is new, analyzing or applicable,
// normalizes them and queues a re-match. Requires security.manage and expectedVersion.
func (s *Service) EditCriteria(ctx context.Context, c Caller, p Principal, id string, expected *int, in []CriterionInput) (Advisory, []Criterion, error) {
	if err := c.validate(); err != nil {
		return Advisory{}, nil, err
	}
	if !p.Manage {
		return Advisory{}, nil, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Advisory{}, nil, err
	}
	crit, err := cleanCriteria(in)
	if err != nil {
		return Advisory{}, nil, err
	}
	if crit, err = s.normalize(ctx, crit, false); err != nil {
		return Advisory{}, nil, err
	}
	var out Advisory
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockAdvisory(ctx, tx, id, exp, "edit_criteria", criteriaEditable...)
		if err != nil {
			return err
		}
		old, err := s.store.CriteriaTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		if criteriaKey(old) == criteriaKey(crit) {
			out, crit = cur, old
			return nil
		}
		if err := s.store.ReplaceCriteriaTx(ctx, tx, cur.ID, crit); err != nil {
			return err
		}
		next := cur
		next.CriteriaRevision++
		reason := ""
		if cur.Status == AdvisoryApplicable {
			next.Status, next.StatusReason, next.ApplicableAt = AdvisoryAnalyzing, strPtr(ReasonCriteriaChanged), nil
			reason = ReasonCriteriaChanged
		}
		if out, err = s.commitAdvisory(ctx, tx, c, cur, next, "criteria_changed", reason, map[string]any{"criteria": len(crit)}); err != nil {
			return err
		}
		return enqueueMatch(ctx, tx, cur.ID)
	})
	if err != nil {
		return Advisory{}, nil, err
	}
	return out, crit, nil
}

// NormalizeCriteria matches the unmatched criteria of an Advisory to Software Products again (by exact
// product name or alias, for example after a product or alias was registered) and queues a re-match when
// any criterion changed. Allowed while the criteria are editable. Requires security.manage and
// expectedVersion.
func (s *Service) NormalizeCriteria(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, []Criterion, error) {
	if err := c.validate(); err != nil {
		return Advisory{}, nil, err
	}
	if !p.Manage {
		return Advisory{}, nil, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Advisory{}, nil, err
	}
	if !uuidPattern.MatchString(id) {
		return Advisory{}, nil, ErrNotFound
	}
	id = strings.ToLower(id)
	before, err := s.store.Criteria(ctx, id)
	if err != nil {
		return Advisory{}, nil, err
	}
	crit, err := s.normalize(ctx, before, true)
	if err != nil {
		return Advisory{}, nil, err
	}
	matched := 0
	for i := range crit {
		if crit[i].Normalization == NormalizationMatched && before[i].Normalization != NormalizationMatched {
			matched++
		}
	}
	var out Advisory
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockAdvisory(ctx, tx, id, exp, "normalize_criteria", criteriaEditable...)
		if err != nil {
			return err
		}
		locked, err := s.store.CriteriaTx(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		// The criteria changed between the read and the lock: the caller retries.
		if criteriaKey(locked) != criteriaKey(before) {
			return ErrVersionConflict
		}
		if matched == 0 {
			out, crit = cur, locked
			return nil
		}
		if err := s.store.ReplaceCriteriaTx(ctx, tx, cur.ID, crit); err != nil {
			return err
		}
		next := cur
		next.CriteriaRevision++
		reason := ""
		if cur.Status == AdvisoryApplicable {
			next.Status, next.StatusReason, next.ApplicableAt = AdvisoryAnalyzing, strPtr(ReasonCriteriaChanged), nil
			reason = ReasonCriteriaChanged
		}
		if out, err = s.commitAdvisory(ctx, tx, c, cur, next, "criteria_normalized", reason, map[string]any{"matched": matched}); err != nil {
			return err
		}
		return enqueueMatch(ctx, tx, cur.ID)
	})
	if err != nil {
		return Advisory{}, nil, err
	}
	return out, crit, nil
}

// advisoryOp moves an Advisory to status to; reason is validated by the caller.
func (s *Service) advisoryOp(ctx context.Context, c Caller, p Principal, id string, expected *int, op, auditOp, to, reason string, from ...string) (Advisory, error) {
	if err := c.validate(); err != nil {
		return Advisory{}, err
	}
	if !p.Manage {
		return Advisory{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Advisory{}, err
	}
	var out Advisory
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockAdvisory(ctx, tx, id, exp, op, from...)
		if err != nil {
			return err
		}
		next := cur
		next.Status, next.StatusReason = to, strPtr(reason)
		now := s.now()
		switch to {
		case AdvisoryApplicable:
			next.ApplicableAt = &now
		case AdvisoryResolved:
			next.ResolvedAt = &now
		case AdvisoryArchived:
			next.ArchivedAt = &now
		case AdvisoryNotApplicable, AdvisoryAnalyzing, AdvisoryNew:
			next.ApplicableAt = nil
		}
		out, err = s.commitAdvisory(ctx, tx, c, cur, next, auditOp, reason, nil)
		return err
	})
	return out, err
}

// StartAnalysis moves a new Advisory to analyzing.
func (s *Service) StartAnalysis(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "start_analysis", "analysis_started", AdvisoryAnalyzing, "", AdvisoryNew)
}

// MarkApplicable records that the Advisory applies to this organization (from new or analyzing); it
// publishes SecurityAdvisoryPublished, which notifies the security.manage holders.
func (s *Service) MarkApplicable(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "mark_applicable", "applicable", AdvisoryApplicable, "", AdvisoryNew, AdvisoryAnalyzing)
}

// MarkNotApplicable records that the Advisory does not apply (from new, analyzing or applicable) with a
// reason code (NotApplicableReasons). Its findings stay as they are and are no longer matched.
func (s *Service) MarkNotApplicable(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Advisory, error) {
	if err := checkReason(reason, NotApplicableReasons); err != nil {
		return Advisory{}, err
	}
	return s.advisoryOp(ctx, c, p, id, expected, "mark_not_applicable", "not_applicable", AdvisoryNotApplicable, reason,
		AdvisoryNew, AdvisoryAnalyzing, AdvisoryApplicable)
}

// PlanRemediation moves an applicable Advisory to remediation_planned.
func (s *Service) PlanRemediation(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "plan_remediation", "remediation_planned", AdvisoryRemediationPlanned, "", AdvisoryApplicable)
}

// StartRemediation moves an applicable or planned Advisory to remediating.
func (s *Service) StartRemediation(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "start_remediation", "remediation_started", AdvisoryRemediating, "",
		AdvisoryApplicable, AdvisoryRemediationPlanned)
}

// Resolve closes the remediation of an Advisory (from applicable, remediation_planned or remediating).
// The findings keep their own states; they become remediated only through fresh observations.
func (s *Service) Resolve(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "resolve", "resolved", AdvisoryResolved, "",
		AdvisoryApplicable, AdvisoryRemediationPlanned, AdvisoryRemediating)
}

// Archive archives a new, not applicable or resolved Advisory (terminal).
func (s *Service) Archive(ctx context.Context, c Caller, p Principal, id string, expected *int) (Advisory, error) {
	return s.advisoryOp(ctx, c, p, id, expected, "archive", "archived", AdvisoryArchived, "",
		AdvisoryNew, AdvisoryNotApplicable, AdvisoryResolved)
}
