package transport

import (
	"errors"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Software approvals and packages (F9 G1) under /api/v1/software. Reads need software.view, software.approve or
// software.package; approval decisions and product status changes need software.approve; registering versions,
// requesting approval, packaging, publishing and the package synchronization need software.package.

const (
	permSoftwareView    = application.PermSoftwareView
	permSoftwareApprove = application.PermSoftwareApprove
	permSoftwarePackage = application.PermSoftwarePackage
	maxRegisterBody     = 32 << 10
)

func registerSoftware(route func(string, func(http.Handler) http.Handler, http.HandlerFunc), auth authorization.Authenticator, h *handler) {
	read := authorization.RequireAny(auth, permSoftwareView, permSoftwareApprove, permSoftwarePackage)
	approve := authorization.Require(auth, permSoftwareApprove)
	pkg := authorization.Require(auth, permSoftwarePackage)
	route("GET /api/v1/software/products", read, h.softwareProducts)
	route("POST /api/v1/software/products/{id}/{op}", approve, h.softwareProductOp)
	route("GET /api/v1/software/versions", read, h.softwareVersions)
	route("POST /api/v1/software/versions", pkg, h.registerSoftwareVersion)
	route("GET /api/v1/software/versions/{id}", read, h.softwareVersion)
	route("POST /api/v1/software/versions/{id}/request-approval", pkg, h.requestSoftwareApproval)
	route("POST /api/v1/software/versions/{id}/approve", approve, h.softwareVersionDecision("approve"))
	route("POST /api/v1/software/versions/{id}/reject", approve, h.softwareVersionDecision("reject"))
	route("POST /api/v1/software/versions/{id}/revoke", approve, h.softwareVersionDecision("revoke"))
	route("POST /api/v1/software/versions/{id}/package", pkg, h.packageSoftwareVersion)
	route("GET /api/v1/software/catalog/search", read, h.searchSoftwareCatalog)
	route("GET /api/v1/software/packages", read, h.softwarePackages)
	route("POST /api/v1/software/packages/sync", pkg, h.syncSoftwarePackages)
	route("POST /api/v1/software/packages/{id}/publish", pkg, h.publishSoftwarePackage)
}

// softwareFail maps the software errors and hands everything else to fail.
func (h *handler) softwareFail(w http.ResponseWriter, r *http.Request, err error) {
	var gate *application.GateError
	var tr *application.InvalidTransitionError
	switch {
	case errors.As(err, &gate):
		httpx.WriteError(w, http.StatusConflict, "endpoints."+gate.Code, "The operation is refused: "+gateText(gate.Code))
	case errors.As(err, &tr):
		httpx.WriteError(w, http.StatusConflict, "endpoints.invalid_transition", "The operation is not allowed in the current status.")
	case errors.Is(err, application.ErrSeparationOfDuties):
		httpx.WriteError(w, http.StatusForbidden, "endpoints.separation_of_duties", "The person who registered or requested a version cannot approve it.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "endpoints.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, softwaremgmt.ErrNotConfigured):
		httpx.WriteError(w, http.StatusConflict, "endpoints.software_provider_not_configured", "The software management provider is not configured.")
	case errors.Is(err, application.ErrSyncCooldown):
		httpx.WriteError(w, http.StatusTooManyRequests, "endpoints.software_sync_cooldown", "The packages were synchronized a moment ago; try again shortly.")
	case errors.Is(err, application.ErrRateLimited):
		httpx.WriteError(w, http.StatusTooManyRequests, "endpoints.software_rate_limited", "Too many catalog searches; try again in a minute.")
	case errors.Is(err, application.ErrSyncDisabled):
		httpx.WriteError(w, http.StatusConflict, "endpoints.software_sync_disabled", "Software package synchronization is not enabled.")
	default:
		h.fail(w, r, err)
	}
}

func gateText(code string) string {
	switch code {
	case "product_not_approved":
		return "the software product is not approved."
	case "product_blocked":
		return "the software product is blocked or retired."
	case "version_not_approved":
		return "the software version is not approved."
	case "package_not_ready":
		return "the package is not packaged yet."
	case "hash_mismatch":
		return "the installer hash reported by the provider does not equal the approved hash."
	case application.IssueTargetSetArchived:
		return "the target set is archived."
	case application.IssueChangeWindowInvalid:
		return "the change is not approved or scheduled, or its maintenance window has ended."
	case application.IssueApprovalRequired:
		return "the plan is high impact and needs an approved plan approval first."
	case "approval_not_required":
		return "the plan is not high impact and is scheduled without a plan approval."
	case "plan_changed":
		return "the plan changed after its approval; submit it again."
	}
	return "a precondition does not hold."
}

type productDTO struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Publisher      *string `json:"publisher"`
	ApprovalStatus string  `json:"approvalStatus"`
	ApprovalReason *string `json:"approvalReason"`
	Version        int     `json:"version"`
	UpdatedAt      string  `json:"updatedAt"`
}

func toProduct(p application.SoftwareProduct) productDTO {
	return productDTO{ID: p.ID, Name: p.Name, Publisher: p.Publisher, ApprovalStatus: p.ApprovalStatus, ApprovalReason: p.ApprovalReason,
		Version: p.Version, UpdatedAt: ts(p.UpdatedAt)}
}

type versionDTO struct {
	ID                   string  `json:"id"`
	ProductID            string  `json:"productId"`
	ProductName          string  `json:"productName"`
	ProductVersion       string  `json:"productVersion"`
	InstallerSHA256      string  `json:"installerSha256"`
	InstallerURL         *string `json:"installerUrl"`
	Publisher            *string `json:"publisher"`
	InstallCommand       *string `json:"installCommand"`
	InstallCommandSHA256 string  `json:"installCommandSha256"`
	DetectionRule        *string `json:"detectionRule"`
	DetectionRuleSHA256  string  `json:"detectionRuleSha256"`
	BindingSHA256        string  `json:"bindingSha256"`
	RegisteredBy         string  `json:"registeredBy"`
	ApprovalStatus       string  `json:"approvalStatus"`
	ApprovalReason       *string `json:"approvalReason"`
	RequestedBy          *string `json:"requestedBy"`
	RequestedAt          *string `json:"requestedAt"`
	DecidedBy            *string `json:"decidedBy"`
	DecidedAt            *string `json:"decidedAt"`
	Version              int     `json:"version"`
	CreatedAt            string  `json:"createdAt"`
}

// toVersion maps a version; installerUrl, installCommand and detectionRule are null when redacted for a reader
// who holds software.view only.
func toVersion(v application.SoftwareVersion) versionDTO {
	def := func(s string) *string {
		if v.DefinitionRedacted {
			return nil
		}
		return &s
	}
	return versionDTO{ID: v.ID, ProductID: v.ProductID, ProductName: v.ProductName, ProductVersion: v.ProductVersion,
		InstallerSHA256: v.InstallerSHA256, InstallerURL: def(v.InstallerURL), Publisher: v.Publisher, InstallCommand: def(v.InstallCommand),
		InstallCommandSHA256: v.InstallCommandSHA256, DetectionRule: def(v.DetectionRule), DetectionRuleSHA256: v.DetectionRuleSHA256,
		BindingSHA256: v.BindingSHA256, RegisteredBy: v.RegisteredBy, ApprovalStatus: v.ApprovalStatus, ApprovalReason: v.ApprovalReason,
		RequestedBy: v.RequestedBy, RequestedAt: tsPtr(v.RequestedAt), DecidedBy: v.DecidedBy, DecidedAt: tsPtr(v.DecidedAt),
		Version: v.Version, CreatedAt: ts(v.CreatedAt)}
}

type approvalDTO struct {
	ID              string  `json:"id"`
	FromStatus      string  `json:"fromStatus"`
	ToStatus        string  `json:"toStatus"`
	Operation       string  `json:"operation"`
	Reason          *string `json:"reason"`
	InstallerSHA256 string  `json:"installerSha256"`
	BindingSHA256   string  `json:"bindingSha256"`
	ActorUserID     *string `json:"actorUserId"`
	ActorSystem     *string `json:"actorSystem"`
	CreatedAt       string  `json:"createdAt"`
}

type packageDTO struct {
	ID                           string  `json:"id"`
	Provider                     string  `json:"provider"`
	ProviderPackageID            *string `json:"providerPackageId"`
	VersionID                    string  `json:"versionId"`
	Status                       string  `json:"status"`
	InstallerSHA256              *string `json:"installerSha256"`
	HashMismatch                 bool    `json:"hashMismatch"`
	ManagementProvider           *string `json:"managementProvider"`
	ManagementArtifactExternalID *string `json:"managementArtifactExternalId"`
	ManagementArtifactID         *string `json:"managementArtifactId"`
	Source                       string  `json:"source"`
	ObservedAt                   *string `json:"observedAt"`
	LastSyncedAt                 *string `json:"lastSyncedAt"`
	PublishRequestedAt           *string `json:"publishRequestedAt"`
	PackageAttempt               int     `json:"packageAttempt"`
	PublishAttempt               int     `json:"publishAttempt"`
	PublishedAt                  *string `json:"publishedAt"`
	PublishedAfterRevoke         bool    `json:"publishedAfterRevoke"`
	VersionRevoked               bool    `json:"versionRevoked"`
	ProductBlocked               bool    `json:"productBlocked"`
	Version                      int     `json:"version"`
}

func toPackage(p application.SoftwarePackage) packageDTO {
	return packageDTO{ID: p.ID, Provider: p.Provider, ProviderPackageID: p.ProviderPackageID, VersionID: p.VersionID, Status: p.Status,
		InstallerSHA256: p.InstallerSHA256, HashMismatch: p.HashMismatch, ManagementProvider: p.ManagementProvider,
		ManagementArtifactExternalID: p.ManagementArtifactExternalID, ManagementArtifactID: p.ManagementArtifactID, Source: p.Source,
		ObservedAt: tsPtr(p.ObservedAt), LastSyncedAt: tsPtr(p.LastSyncedAt), PublishRequestedAt: tsPtr(p.PublishRequestedAt),
		PackageAttempt: p.PackageAttempt, PublishAttempt: p.PublishAttempt, PublishedAt: tsPtr(p.PublishedAt), PublishedAfterRevoke: p.PublishedAfterRevoke,
		VersionRevoked: p.VersionRevoked, ProductBlocked: p.ProductBlocked, Version: p.Version}
}

func mapItems[T, D any](in []T, f func(T) D) []D {
	out := make([]D, 0, len(in))
	for _, it := range in {
		out = append(out, f(it))
	}
	return out
}

type decisionBody struct {
	Reason          string `json:"reason"`
	ExpectedVersion *int   `json:"expectedVersion"`
}

func (h *handler) softwareProducts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListSoftwareProducts(r.Context(), principal(r), application.SoftwareProductFilter{Status: r.URL.Query().Get("status"), Page: page})
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(res.Items, toProduct), "nextCursor": res.NextCursor})
}

func (h *handler) softwareProductOp(w http.ResponseWriter, r *http.Request) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	ctx, c, p, id := r.Context(), caller(w, r), principal(r), r.PathValue("id")
	var out application.SoftwareProduct
	var err error
	switch r.PathValue("op") {
	case "approve":
		if b.Reason != "" {
			httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "Approving a product takes no reason.")
			return
		}
		out, err = h.svc.ApproveProduct(ctx, c, p, id, b.ExpectedVersion)
	case "deprecate":
		out, err = h.svc.DeprecateProduct(ctx, c, p, id, b.Reason, b.ExpectedVersion)
	case "retire":
		out, err = h.svc.RetireProduct(ctx, c, p, id, b.Reason, b.ExpectedVersion)
	case "block":
		out, err = h.svc.BlockProduct(ctx, c, p, id, b.Reason, b.ExpectedVersion)
	case "unblock":
		out, err = h.svc.UnblockProduct(ctx, c, p, id, b.Reason, b.ExpectedVersion)
	default:
		httpx.WriteError(w, http.StatusNotFound, "endpoints.not_found", "The requested resource was not found.")
		return
	}
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProduct(out))
}

func (h *handler) softwareVersions(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := h.svc.ListSoftwareVersions(r.Context(), principal(r), application.SoftwareVersionFilter{ProductID: q.Get("productId"), Status: q.Get("status"), Page: page})
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(res.Items, toVersion), "nextCursor": res.NextCursor})
}

type registerBody struct {
	ProductID       string `json:"productId"`
	Version         string `json:"version"`
	InstallerSHA256 string `json:"installerSha256"`
	InstallerURL    string `json:"installerUrl"`
	Publisher       string `json:"publisher"`
	InstallCommand  string `json:"installCommand"`
	DetectionRule   string `json:"detectionRule"`
}

func (h *handler) registerSoftwareVersion(w http.ResponseWriter, r *http.Request) {
	var b registerBody
	if err := httpx.DecodeJSON(w, r, &b, maxRegisterBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The request body is not valid JSON for this operation.")
		return
	}
	v, created, err := h.svc.RegisterVersion(r.Context(), caller(w, r), principal(r), application.NewSoftwareVersion{ProductID: b.ProductID,
		ProductVersion: b.Version, InstallerSHA256: b.InstallerSHA256, InstallerURL: b.InstallerURL, Publisher: b.Publisher,
		InstallCommand: b.InstallCommand, DetectionRule: b.DetectionRule})
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, toVersion(v))
}

func (h *handler) softwareVersion(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetSoftwareVersion(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	approvals := mapItems(d.Approvals, func(t application.SoftwareTransition) approvalDTO {
		return approvalDTO{ID: t.ID, FromStatus: t.FromStatus, ToStatus: t.ToStatus, Operation: t.Operation, Reason: t.Reason,
			InstallerSHA256: t.InstallerSHA256, BindingSHA256: t.BindingSHA256, ActorUserID: t.ActorUserID, ActorSystem: t.ActorSystem, CreatedAt: ts(t.CreatedAt)}
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"version": toVersion(d.Version), "approvals": approvals, "packages": mapItems(d.Packages, toPackage)})
}

func (h *handler) requestSoftwareApproval(w http.ResponseWriter, r *http.Request) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	if b.Reason != "" {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "Requesting approval takes no reason.")
		return
	}
	v, err := h.svc.RequestVersionApproval(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toVersion(v))
}

func (h *handler) softwareVersionDecision(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b decisionBody
		if !decode(w, r, &b) {
			return
		}
		ctx, c, p, id := r.Context(), caller(w, r), principal(r), r.PathValue("id")
		var v application.SoftwareVersion
		var err error
		switch op {
		case "approve":
			if b.Reason != "" {
				httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "Approving a version takes no reason.")
				return
			}
			v, err = h.svc.ApproveVersion(ctx, c, p, id, b.ExpectedVersion)
		case "reject":
			v, err = h.svc.RejectVersion(ctx, c, p, id, b.Reason, b.ExpectedVersion)
		default:
			v, err = h.svc.RevokeVersion(ctx, c, p, id, b.Reason, b.ExpectedVersion)
		}
		if err != nil {
			h.softwareFail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toVersion(v))
	}
}

func (h *handler) packageSoftwareVersion(w http.ResponseWriter, r *http.Request) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.PackageVersion(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPackage(p))
}

func (h *handler) publishSoftwarePackage(w http.ResponseWriter, r *http.Request) {
	var b decisionBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.PublishPackage(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPackage(p))
}

type catalogDTO struct {
	ProviderID    string  `json:"providerId"`
	Name          string  `json:"name"`
	Publisher     *string `json:"publisher"`
	LatestVersion *string `json:"latestVersion"`
	SourceURL     *string `json:"sourceUrl"`
}

func (h *handler) searchSoftwareCatalog(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.SearchCatalog(r.Context(), principal(r), r.URL.Query().Get("q"))
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(items, func(e application.CatalogEntry) catalogDTO {
		return catalogDTO{ProviderID: e.ProviderID, Name: e.Name, Publisher: e.Publisher, LatestVersion: e.LatestVersion, SourceURL: e.SourceURL}
	})})
}

func (h *handler) softwarePackages(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := h.svc.ListSoftwarePackages(r.Context(), principal(r), application.SoftwarePackageFilter{Status: q.Get("status"), VersionID: q.Get("versionId"), Page: page})
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": mapItems(res.Items, toPackage), "nextCursor": res.NextCursor})
}

func (h *handler) syncSoftwarePackages(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.SyncPackages(r.Context(), caller(w, r), principal(r))
	if err != nil {
		h.softwareFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"checked": res.Checked, "changed": res.Changed, "linked": res.Linked,
		"findingsRaised": res.FindingsRaised, "findingsResolved": res.FindingsResolved, "stale": res.Stale, "errors": res.Errors})
}
