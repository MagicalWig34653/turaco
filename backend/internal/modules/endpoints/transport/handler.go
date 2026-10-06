// Package transport exposes the Endpoints HTTP API under /api/v1. Reads need endpoints.view or
// endpoints.manage; linking, unlinking and synchronization need endpoints.manage. Callers without
// either permission see no endpoint data at all.
package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const (
	permView       = "endpoints.view"
	permManage     = "endpoints.manage"
	permAssetsView = "assets.view"
	permMgmtView   = "endpoint.management.view"
	permDirView    = "organization.directory.view"
	maxBody        = 4 << 10
)

type handler struct {
	svc    *application.Service
	logger *slog.Logger
}

// Register mounts the Endpoints routes.
func Register(mux *http.ServeMux, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	read := authorization.RequireAny(auth, permView, permManage)
	write := authorization.Require(auth, permManage)
	route := func(pattern string, mw func(http.Handler) http.Handler, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(mw(fn)))
	}
	route("GET /api/v1/devices", read, h.list)
	route("GET /api/v1/devices/{id}", read, h.get)
	route("POST /api/v1/devices/{id}/link", write, h.link)
	route("POST /api/v1/devices/{id}/unlink", write, h.unlink)
	route("GET /api/v1/endpoint-findings", read, h.findings)
	route("POST /api/v1/endpoint-sync", write, h.sync)
	mgmt := authorization.RequireAny(auth, permMgmtView, permManage)
	route("GET /api/v1/management-artifacts", mgmt, h.listArtifacts)
	route("GET /api/v1/management-artifacts/{id}", mgmt, h.getArtifact)
	route("GET /api/v1/management-filters", mgmt, h.listFilters)
	// Device observations reveal the device: the service also requires endpoints.view or endpoints.manage.
	route("GET /api/v1/devices/{id}/management-observations", mgmt, h.deviceObservations)
	// Views: the service additionally checks device access (endpoints.view), organization.directory.view and assets.view where needed.
	route("GET /api/v1/devices/{id}/management", mgmt, h.deviceManagement)
	route("GET /api/v1/devices/{id}/management/{artifactId}/path", mgmt, h.assignmentPath)
	route("GET /api/v1/directory-groups/{id}/management", mgmt, h.groupManagement)
	route("GET /api/v1/users/{id}/management", mgmt, h.userManagement)
	route("GET /api/v1/management-artifacts/{id}/targets", mgmt, h.artifactTargets)
	// History and diff (slice 4): same permission model as the views above.
	route("GET /api/v1/management-artifacts/{id}/history", mgmt, h.artifactHistory)
	route("GET /api/v1/devices/{id}/management-history", mgmt, h.deviceHistory)
	route("GET /api/v1/devices/{id}/management/diff", mgmt, h.deviceDiff)
	route("GET /api/v1/directory-groups/{id}/management/diff", mgmt, h.groupDiff)
	registerSoftware(route, auth, h)
	registerDeployments(route, auth, h)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *application.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", inv.Message)
	case errors.Is(err, application.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "endpoints.not_found", "The requested resource was not found.")
	case errors.Is(err, application.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, application.ErrSyncCooldown):
		httpx.WriteError(w, http.StatusTooManyRequests, "endpoints.sync_cooldown", "The provider was synchronized a moment ago; try again shortly.")
	case errors.Is(err, application.ErrSyncRunning):
		httpx.WriteError(w, http.StatusConflict, "endpoints.sync_running", "Another synchronization of this provider is running; try again later.")
	case errors.Is(err, application.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "endpoints.conflict", "The operation does not fit the device's current link state, or the asset is linked to another device.")
	case errors.Is(err, application.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "endpoints.version_conflict", "The device was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrAssetInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.asset_invalid", "The asset does not exist or is disposed, lost or retired.")
	case errors.Is(err, application.ErrSyncDisabled):
		httpx.WriteError(w, http.StatusConflict, "endpoints.sync_disabled", "Provider synchronization is not enabled.")
	case errors.Is(err, intune.ErrNotConfigured):
		httpx.WriteError(w, http.StatusConflict, "endpoints.provider_not_configured", "The endpoint provider is not configured.")
	case errors.Is(err, application.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_cursor", "The cursor is invalid.")
	default:
		h.logger.ErrorContext(r.Context(), "endpoints request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func principal(r *http.Request) application.Principal {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Principal{UserID: p.UserID, View: p.Has(permView), Manage: p.Has(permManage), AssetsView: p.Has(permAssetsView), ManagementView: p.Has(permMgmtView), DirectoryView: p.Has(permDirView),
		SoftwareView: p.Has(permSoftwareView), SoftwareApprove: p.Has(permSoftwareApprove), SoftwarePackage: p.Has(permSoftwarePackage),
		DeploymentsView: p.Has(permDeploymentsView), DeploymentsManage: p.Has(permDeploymentsManage), DeploymentsExecute: p.Has(permDeploymentsExecute),
		DeploymentsHighImpact: p.Has(permDeploymentsHighImpact)}
}

func caller(w http.ResponseWriter, r *http.Request) application.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: httpx.RequestID(w)}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

type deviceDTO struct {
	ID                string  `json:"id"`
	Provider          string  `json:"provider"`
	ExternalID        string  `json:"externalId"`
	Name              string  `json:"name"`
	SerialNumber      *string `json:"serialNumber"`
	AssetID           *string `json:"assetId"`
	AssetLinkSource   *string `json:"assetLinkSource"`
	AutoLinkBlocked   bool    `json:"autoLinkBlocked"`
	OSPlatform        string  `json:"osPlatform"`
	OSVersion         *string `json:"osVersion"`
	Manufacturer      *string `json:"manufacturer"`
	Model             *string `json:"model"`
	Ownership         string  `json:"ownership"`
	ComplianceState   string  `json:"complianceState"`
	LastCheckinAt     *string `json:"lastCheckinAt"`
	Source            string  `json:"source"`
	ObservedAt        string  `json:"observedAt"`
	LastSyncedAt      string  `json:"lastSyncedAt"`
	DeletedObservedAt *string `json:"deletedObservedAt"`
	Version           int     `json:"version"`
}

func toDevice(d application.Device) deviceDTO {
	return deviceDTO{
		ID: d.ID, Provider: d.Provider, ExternalID: d.ExternalID, Name: d.Name, SerialNumber: d.SerialNumber, AssetID: d.AssetID,
		AssetLinkSource: d.AssetLinkSource, AutoLinkBlocked: d.AutoLinkBlocked, OSPlatform: d.OSPlatform, OSVersion: d.OSVersion,
		Manufacturer: d.Manufacturer, Model: d.Model, Ownership: d.Ownership, ComplianceState: d.ComplianceState,
		LastCheckinAt: tsPtr(d.LastCheckinAt), Source: d.Source, ObservedAt: ts(d.ObservedAt), LastSyncedAt: ts(d.LastSyncedAt),
		DeletedObservedAt: tsPtr(d.DeletedObservedAt), Version: d.Version,
	}
}

type installationDTO struct {
	ID                string  `json:"id"`
	SoftwareProductID *string `json:"softwareProductId"`
	ProductName       *string `json:"productName"`
	RawName           string  `json:"rawName"`
	RawVersion        string  `json:"rawVersion"`
	RawPublisher      *string `json:"rawPublisher"`
	ObservedAt        string  `json:"observedAt"`
}

type findingDTO struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	DeviceID   string          `json:"deviceId"`
	DeviceName string          `json:"deviceName"`
	Status     string          `json:"status"`
	Detail     json.RawMessage `json:"detail"`
	RaisedAt   string          `json:"raisedAt"`
	ResolvedAt *string         `json:"resolvedAt"`
}

func toFinding(f application.Finding) findingDTO {
	detail := f.Detail
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	return findingDTO{ID: f.ID, Kind: f.Kind, DeviceID: f.DeviceID, DeviceName: f.DeviceName, Status: f.Status, Detail: detail,
		RaisedAt: ts(f.RaisedAt), ResolvedAt: tsPtr(f.ResolvedAt)}
}

func parsePage(w http.ResponseWriter, r *http.Request) (application.Page, bool) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_limit", "The limit must be a positive integer.")
		return application.Page{}, false
	}
	return application.Page{Limit: limit, Cursor: r.URL.Query().Get("cursor")}.Normalize(), true
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	q := v.Get("q")
	if utf8.RuneCountInString(q) > 100 || !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The search query is invalid.")
		return
	}
	f := application.DeviceFilter{Platform: v.Get("platform"), Compliance: v.Get("compliance"), Query: q, IncludeDeleted: v.Get("includeDeleted") == "true",
		ManagementState: v.Get("managementState"), HasFinding: v.Get("hasFinding"), OSVersionPrefix: v.Get("osVersion"), Page: page}
	if days := v.Get("lastCheckinOlderThanDays"); days != "" {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 || n > application.MaxCheckinDays {
			httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "lastCheckinOlderThanDays must be a positive number of days.")
			return
		}
		f.LastCheckinOlderThanDays = n
	}
	switch v.Get("linked") {
	case "":
	case "true", "false":
		b := v.Get("linked") == "true"
		f.Linked = &b
	default:
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "linked must be true or false.")
		return
	}
	res, err := h.svc.ListDevices(r.Context(), principal(r), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []deviceDTO `json:"items"`
		NextCursor string      `json:"nextCursor,omitempty"`
	}{Items: make([]deviceDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, d := range res.Items {
		out.Items = append(out.Items, toDevice(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetDevice(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		deviceDTO
		Software []installationDTO `json:"software"`
		Findings []findingDTO      `json:"findings"`
	}{deviceDTO: toDevice(d.Device), Software: make([]installationDTO, 0, len(d.Software)), Findings: make([]findingDTO, 0, len(d.Findings))}
	for _, i := range d.Software {
		out.Software = append(out.Software, installationDTO{ID: i.ID, SoftwareProductID: i.SoftwareProduct, ProductName: i.ProductName,
			RawName: i.RawName, RawVersion: i.RawVersion, RawPublisher: i.RawPublisher, ObservedAt: ts(i.ObservedAt)})
	}
	for _, f := range d.Findings {
		out.Findings = append(out.Findings, toFinding(f))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) findings(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListFindings(r.Context(), principal(r), application.FindingFilter{Kind: v.Get("kind"), Status: v.Get("status"), DeviceID: v.Get("deviceId"), Page: page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []findingDTO `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: make([]findingDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, f := range res.Items {
		out.Items = append(out.Items, toFinding(f))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type linkBody struct {
	AssetID         string `json:"assetId"`
	Reason          string `json:"reason"`
	ExpectedVersion *int   `json:"expectedVersion"`
}

func (h *handler) link(w http.ResponseWriter, r *http.Request) {
	var b linkBody
	if !decode(w, r, &b) {
		return
	}
	d, err := h.svc.ManualLink(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.AssetID, b.Reason, b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDevice(d))
}

func (h *handler) unlink(w http.ResponseWriter, r *http.Request) {
	var b linkBody
	if !decode(w, r, &b) {
		return
	}
	if b.AssetID != "" {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "An unlink takes no asset.")
		return
	}
	d, err := h.svc.ManualUnlink(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Reason, b.ExpectedVersion)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toDevice(d))
}

func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Sync(r.Context(), caller(w, r), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m := res.Management
	httpx.JSON(w, http.StatusOK, map[string]int{
		"filtersCreated": m.FiltersCreated, "filtersUpdated": m.FiltersUpdated, "filtersUnchanged": m.FiltersUnchanged, "filtersTombstoned": m.FiltersTombstoned, "filtersRejected": m.FiltersRejected,
		"artifactsCreated": m.ArtifactsCreated, "artifactsUpdated": m.ArtifactsUpdated, "artifactsUnchanged": m.ArtifactsUnchanged, "artifactsTombstoned": m.ArtifactsTombstoned,
		"artifactsRejected": m.ArtifactsRejected, "artifactsLinked": m.ArtifactsLinked,
		"assignmentsOpened": m.AssignmentsOpened, "assignmentsClosed": m.AssignmentsClosed, "assignmentsUnchanged": m.AssignmentsUnchanged, "assignmentsRejected": m.AssignmentsRejected,
		"observationsCreated": m.ObservationsCreated, "observationsChanged": m.ObservationsChanged, "observationsUnchanged": m.ObservationsUnchanged, "observationsSkipped": m.ObservationsSkipped,
		"membershipsOpened": m.MembershipsOpened, "membershipsClosed": m.MembershipsClosed, "membershipsUnchanged": m.MembershipsUnchanged, "membershipsSkipped": m.MembershipsSkipped,
		"managementTombstonesSkipped": m.ManagementTombstonesSkipped, "providerFindingsRaised": m.ProviderFindingsRaised, "providerFindingsResolved": m.ProviderFindingsResolved,
		"ineffectiveFindingsRaised": m.IneffectiveFindingsRaised, "ineffectiveFindingsResolved": m.IneffectiveFindingsResolved, "ineffectiveDevicesSkipped": m.IneffectiveDevicesSkipped,
		"managementErrors": m.ManagementErrors,
		"devicesCreated":   res.DevicesCreated, "devicesUpdated": res.DevicesUpdated, "devicesUnchanged": res.DevicesUnchanged,
		"devicesTombstoned": res.DevicesTombstoned, "tombstonesSkipped": res.TombstonesSkipped, "devicesRejected": res.DevicesRejected, "devicesLinked": res.DevicesLinked,
		"softwareObserved": res.SoftwareObserved, "softwareSkipped": res.SoftwareSkipped, "softwareErrors": res.SoftwareErrors,
		"findingsRaised": res.FindingsRaised, "findingsResolved": res.FindingsResolved,
	})
}
