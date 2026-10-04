package transport

import (
	"net/http"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 64 << 10
const maxImportBody = 1 << 20

type versionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
	ReviewBy        string `json:"reviewBy"`
}
type ruleBody struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}
type criterionBody struct {
	SoftwareProductID string     `json:"softwareProductId"`
	ProductName       string     `json:"productName"`
	Publisher         string     `json:"publisher"`
	OSPlatform        string     `json:"osPlatform"`
	Rules             []ruleBody `json:"rules"`
}

func criterionInputs(in []criterionBody) []application.CriterionInput {
	out := make([]application.CriterionInput, 0, len(in))
	for _, c := range in {
		v := application.CriterionInput{SoftwareProductID: c.SoftwareProductID, ProductName: c.ProductName, Publisher: c.Publisher, OSPlatform: c.OSPlatform}
		for _, rule := range c.Rules {
			v.Rules = append(v.Rules, application.Rule{Kind: rule.Kind, Version: rule.Version})
		}
		out = append(out, v)
	}
	return out
}

type advisoryBody struct {
	Source      string          `json:"source"`
	ExternalID  string          `json:"externalId"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	Severity    string          `json:"severity"`
	SourceURL   string          `json:"sourceUrl"`
	PublishedAt *time.Time      `json:"publishedAt"`
	ModifiedAt  *time.Time      `json:"modifiedAt"`
	Criteria    []criterionBody `json:"criteria"`
}

func (b advisoryBody) input() application.AdvisoryInput {
	return application.AdvisoryInput{Source: b.Source, ExternalID: b.ExternalID, Title: b.Title, Summary: b.Summary, Severity: b.Severity, SourceURL: b.SourceURL, PublishedAt: b.PublishedAt, ModifiedAt: b.ModifiedAt, Criteria: criterionInputs(b.Criteria)}
}
func decode(w http.ResponseWriter, r *http.Request, dst any, max int64) bool {
	if err := httpx.DecodeJSON(w, r, dst, max); err != nil {
		httpx.WriteError(w, 400, "security.invalid_request", "The request body is not valid JSON for this operation or exceeds the size limit.")
		return false
	}
	return true
}
func page(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	n, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, 400, "security.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: n, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}
func (h *handler) listAdvisories(w http.ResponseWriter, r *http.Request) {
	p, ok := page(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v, e := h.svc.ListAdvisories(r.Context(), principal(r), application.AdvisoryFilter{Status: q.Get("status"), Severity: q.Get("severity"), Query: q.Get("q"), Page: p})
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, a := range v.Items {
		items = append(items, advisoryDTO(a))
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "nextCursor": v.NextCursor})
}
func (h *handler) createAdvisory(w http.ResponseWriter, r *http.Request) {
	var b advisoryBody
	if !decode(w, r, &b, maxBody) {
		return
	}
	a, c, e := h.svc.Create(r.Context(), caller(w, r), principal(r), b.input())
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 201, map[string]any{"advisory": advisoryDTO(a), "criteria": criteriaDTO(c)})
}
func (h *handler) importAdvisories(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Records []advisoryBody `json:"records"`
	}
	if !decode(w, r, &b, maxImportBody) {
		return
	}
	if len(b.Records) == 0 || len(b.Records) > application.MaxImportRecords {
		httpx.WriteError(w, 400, "security.invalid_request", "An import must contain 1 to 500 records.")
		return
	}
	records := make([]application.AdvisoryInput, 0, len(b.Records))
	for _, v := range b.Records {
		records = append(records, v.input())
	}
	out, e := h.svc.Import(r.Context(), caller(w, r), principal(r), records)
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, out)
}
func (h *handler) getAdvisory(w http.ResponseWriter, r *http.Request) {
	a, e := h.svc.GetAdvisory(r.Context(), principal(r), r.PathValue("id"))
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, map[string]any{"advisory": advisoryDTO(a.Advisory), "criteria": criteriaDTO(a.Criteria), "productNames": a.ProductNames})
}
func (h *handler) updateAdvisory(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		Title           *string `json:"title"`
		Summary         *string `json:"summary"`
		Severity        *string `json:"severity"`
		SourceURL       *string `json:"sourceUrl"`
	}
	if !decode(w, r, &b, maxBody) {
		return
	}
	a, e := h.svc.UpdateDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.Details{Title: b.Title, Summary: b.Summary, Severity: b.Severity, SourceURL: b.SourceURL})
	h.advisoryResponse(w, r, a, e)
}
func (h *handler) editCriteria(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int            `json:"expectedVersion"`
		Criteria        []criterionBody `json:"criteria"`
	}
	if !decode(w, r, &b, maxBody) {
		return
	}
	a, c, e := h.svc.EditCriteria(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, criterionInputs(b.Criteria))
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, map[string]any{"advisory": advisoryDTO(a), "criteria": criteriaDTO(c)})
}
func (h *handler) normalizeCriteria(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b, maxBody) {
		return
	}
	a, c, e := h.svc.NormalizeCriteria(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, map[string]any{"advisory": advisoryDTO(a), "criteria": criteriaDTO(c)})
}
func (h *handler) advisoryOperation(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b, maxBody) {
		return
	}
	ctx, c, p, id := r.Context(), caller(w, r), principal(r), r.PathValue("id")
	var a application.Advisory
	var e error
	switch strings.TrimPrefix(r.URL.Path, r.URL.Path[:strings.LastIndex(r.URL.Path, "/")+1]) {
	case "start-analysis":
		a, e = h.svc.StartAnalysis(ctx, c, p, id, b.ExpectedVersion)
	case "applicable":
		a, e = h.svc.MarkApplicable(ctx, c, p, id, b.ExpectedVersion)
	case "not-applicable":
		a, e = h.svc.MarkNotApplicable(ctx, c, p, id, b.ExpectedVersion, b.Reason)
	case "plan-remediation":
		a, e = h.svc.PlanRemediation(ctx, c, p, id, b.ExpectedVersion)
	case "start-remediation":
		a, e = h.svc.StartRemediation(ctx, c, p, id, b.ExpectedVersion)
	case "resolve":
		a, e = h.svc.Resolve(ctx, c, p, id, b.ExpectedVersion)
	case "archive":
		a, e = h.svc.Archive(ctx, c, p, id, b.ExpectedVersion)
	}
	h.advisoryResponse(w, r, a, e)
}
func (h *handler) advisoryResponse(w http.ResponseWriter, r *http.Request, a application.Advisory, e error) {
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	dto := advisoryDTO(a)
	if strings.HasSuffix(r.URL.Path, "/not-applicable") || strings.HasSuffix(r.URL.Path, "/resolve") || strings.HasSuffix(r.URL.Path, "/archive") {
		warnings := []string{}
		if a.UnmatchedCriteria > 0 {
			warnings = append(warnings, "unmatched_criteria")
		}
		dto["warnings"] = warnings
	}
	httpx.JSON(w, 200, dto)
}
func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	v, e := h.svc.Summary(r.Context(), principal(r), r.PathValue("id"))
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, map[string]any{"byStatus": v.ByStatus, "byConfidence": v.ByConfidence, "affectedDevices": v.AffectedDevices, "oldestOpenSince": v.OldestOpenSince, "unmatchedCriteria": v.UnmatchedCriteria})
}
func (h *handler) advisoryTransitions(w http.ResponseWriter, r *http.Request) {
	p, ok := page(w, r)
	if !ok {
		return
	}
	v, e := h.svc.AdvisoryTransitions(r.Context(), principal(r), r.PathValue("id"), p)
	h.transitionsResponse(w, r, v, e)
}
func (h *handler) transitionsResponse(w http.ResponseWriter, r *http.Request, v application.Result[application.Transition], e error) {
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, t := range v.Items {
		items = append(items, map[string]any{"id": t.ID, "fromStatus": t.FromStatus, "toStatus": t.ToStatus, "operation": t.Operation, "reason": t.Reason, "actorUserId": t.ActorUserID, "actorSystem": t.ActorSystem, "correlationId": t.CorrelationID, "createdAt": t.CreatedAt})
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "nextCursor": v.NextCursor})
}
func (h *handler) listFindings(w http.ResponseWriter, r *http.Request) {
	p, ok := page(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	h.findings(w, r, application.FindingFilter{Status: q.Get("status"), Confidence: q.Get("confidence"), AdvisoryID: q.Get("advisoryId"), Page: p})
}
func (h *handler) advisoryFindings(w http.ResponseWriter, r *http.Request) {
	p, ok := page(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	h.findings(w, r, application.FindingFilter{Status: q.Get("status"), Confidence: q.Get("confidence"), AdvisoryID: r.PathValue("id"), Page: p})
}
func (h *handler) findings(w http.ResponseWriter, r *http.Request, f application.FindingFilter) {
	v, e := h.svc.ListFindings(r.Context(), principal(r), f)
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, x := range v.Items {
		items = append(items, findingDTO(x))
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "nextCursor": v.NextCursor})
}
func (h *handler) getFinding(w http.ResponseWriter, r *http.Request) {
	v, e := h.svc.GetFinding(r.Context(), principal(r), r.PathValue("id"))
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, findingDTO(v))
}
func (h *handler) findingOperation(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b, maxBody) {
		return
	}
	ctx, c, p, id := r.Context(), caller(w, r), principal(r), r.PathValue("id")
	var f application.Finding
	var e error
	op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch op {
	case "investigate":
		f, e = h.svc.Investigate(ctx, c, p, id, b.ExpectedVersion)
	case "accept":
		f, e = h.svc.AcceptFinding(ctx, c, p, id, b.ExpectedVersion)
	case "accept-risk":
		var review time.Time
		if b.ReviewBy != "" {
			review, e = time.Parse("2006-01-02", b.ReviewBy)
			if e != nil {
				httpx.WriteError(w, 400, "security.invalid_request", "reviewBy must be a YYYY-MM-DD date.")
				return
			}
		}
		f, e = h.svc.AcceptRisk(ctx, c, p, id, b.ExpectedVersion, b.Reason, review)
	case "false-positive":
		f, e = h.svc.MarkFalsePositive(ctx, c, p, id, b.ExpectedVersion, b.Reason)
	case "plan-remediation":
		f, e = h.svc.PlanFindingRemediation(ctx, c, p, id, b.ExpectedVersion)
	case "start-remediation":
		f, e = h.svc.StartFindingRemediation(ctx, c, p, id, b.ExpectedVersion)
	case "reopen":
		f, e = h.svc.Reopen(ctx, c, p, id, b.ExpectedVersion, b.Reason)
	}
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	v, e := h.svc.GetFinding(ctx, p, f.ID)
	if e != nil {
		h.writeErr(w, r, e)
		return
	}
	httpx.JSON(w, 200, findingDTO(v))
}
func (h *handler) findingTransitions(w http.ResponseWriter, r *http.Request) {
	p, ok := page(w, r)
	if !ok {
		return
	}
	v, e := h.svc.FindingTransitions(r.Context(), principal(r), r.PathValue("id"), p)
	h.transitionsResponse(w, r, v, e)
}

func advisoryDTO(a application.Advisory) map[string]any {
	return map[string]any{"id": a.ID, "reference": a.Reference, "source": a.Source, "externalId": a.ExternalID, "title": a.Title, "summary": a.Summary, "severity": a.Severity, "publishedAt": a.PublishedAt, "modifiedAt": a.ModifiedAt, "sourceUrl": a.SourceURL, "status": a.Status, "statusReason": a.StatusReason, "criteriaRevision": a.CriteriaRevision, "matchedRevision": a.MatchedRevision, "matchedAt": a.MatchedAt, "matchedIngestionAt": a.MatchedIngestionAt, "matchTruncated": a.MatchTruncated, "unmatchedCriteria": a.UnmatchedCriteria, "createdBy": a.CreatedBy, "applicableAt": a.ApplicableAt, "resolvedAt": a.ResolvedAt, "archivedAt": a.ArchivedAt, "version": a.Version, "createdAt": a.CreatedAt, "updatedAt": a.UpdatedAt}
}
func criteriaDTO(criteria []application.Criterion) []any {
	out := make([]any, 0, len(criteria))
	for _, c := range criteria {
		rules := make([]any, 0, len(c.Rules))
		for _, r := range c.Rules {
			rules = append(rules, map[string]any{"kind": r.Kind, "version": r.Version})
		}
		out = append(out, map[string]any{"id": c.ID, "position": c.Position, "softwareProductId": c.SoftwareProductID, "productName": c.ProductName, "publisher": c.Publisher, "osPlatform": c.OSPlatform, "normalization": c.Normalization, "matchMethod": c.MatchMethod, "rules": rules})
	}
	return out
}
func findingDTO(v application.FindingView) map[string]any {
	f := v.Finding
	return map[string]any{"id": f.ID, "reference": f.Reference, "advisoryId": f.AdvisoryID, "advisoryReference": v.AdvisoryReference, "advisoryTitle": v.AdvisoryTitle, "deviceId": f.DeviceID, "deviceHidden": v.DeviceHidden, "deviceName": v.DeviceName, "softwareProductId": f.SoftwareProductID, "productName": v.ProductName, "installedVersion": f.InstalledVersion, "confidence": f.Confidence, "status": f.Status, "statusReason": f.StatusReason, "riskAcceptedBy": f.RiskAcceptedBy, "riskAcceptedAt": f.RiskAcceptedAt, "riskReviewBy": f.RiskReviewBy, "firstSeenAt": f.FirstSeenAt, "lastSeenAt": f.LastSeenAt, "remediatedAt": f.RemediatedAt, "version": f.Version, "createdAt": f.CreatedAt, "updatedAt": f.UpdatedAt}
}
