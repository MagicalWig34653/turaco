package intune

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
)

// GraphProvider is the Microsoft Graph read client for Intune (Mode real, status unverified until a call
// succeeded). It is written strictly from the vendor documentation and has not been exercised against a live
// tenant; every endpoint below names the documentation page it follows and the date it was read.
//
// Required application permissions (read registration, docs/integrations/intune.md): DeviceManagementManagedDevices.Read.All,
// DeviceManagementApps.Read.All, DeviceManagementConfiguration.Read.All, GroupMember.Read.All and Device.Read.All.
//
// Graph DTOs stay in this file; the provider returns the normalized records of intune.go. Unknown JSON fields are
// ignored. Every list is paged through @odata.nextLink restricted to the Graph host and bounded by the caps of
// microsoft.Graph. Nothing here writes.
type GraphProvider struct {
	g    *microsoft.Graph
	beta bool
}

var _ Provider = (*GraphProvider)(nil)

// NewGraphProvider wraps a Graph client of the read registration. With beta true the provider also calls the
// Graph beta endpoints for the objects Microsoft documents only there (assignment filters, settings catalog
// policies and app install statuses); without it filtered assignments are reported as rejected by the ingestion
// and no observations are produced.
func NewGraphProvider(g *microsoft.Graph, beta bool) *GraphProvider {
	return &GraphProvider{g: g, beta: beta}
}

// Status implements providerstatus.Reporter.
func (p *GraphProvider) Status() providerstatus.Snapshot { return p.g.Status() }

// Limits that keep a hostile or misbehaving answer bounded.
const (
	maxGraphGroups       = 2000
	maxGraphObservations = 500000
	maxGraphObsArtifacts = 3000
	maxGraphMemberships  = 500000
	ringGroupNamePrefix  = ringGroupPrefix // "turaco-ring-"
)

// ErrTooManyGroups is returned when assignments target more distinct groups than the membership read supports.
var ErrTooManyGroups = errors.New("intune: too many assignment target groups to read memberships")

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
	ringGroupName = regexp.MustCompile(`^turaco-ring-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// query builds "path?$a=v&$b=w" with values percent-encoded and the OData "$" names kept literal.
func query(path string, kv ...string) string {
	var b strings.Builder
	b.WriteString(path)
	for i := 0; i+1 < len(kv); i += 2 {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(url.QueryEscape(kv[i+1]), "+", "%20"))
	}
	return b.String()
}

// ---- devices ---------------------------------------------------------------------------------------------

// graphDevice follows https://learn.microsoft.com/en-us/graph/api/intune-devices-manageddevice-list?view=graph-rest-1.0
// and the property table of https://learn.microsoft.com/en-us/graph/api/resources/intune-devices-manageddevice?view=graph-rest-1.0
// (both read 2026-10-10): GET /deviceManagement/managedDevices, permission DeviceManagementManagedDevices.Read.All;
// managedDeviceOwnerType unknown|company|personal; complianceState unknown|compliant|noncompliant|conflict|error|
// inGracePeriod|configManager; lastSyncDateTime is a DateTimeOffset.
type graphDevice struct {
	ID                     string `json:"id"`
	DeviceName             string `json:"deviceName"`
	OperatingSystem        string `json:"operatingSystem"`
	OSVersion              string `json:"osVersion"`
	SerialNumber           string `json:"serialNumber"`
	Manufacturer           string `json:"manufacturer"`
	Model                  string `json:"model"`
	ManagedDeviceOwnerType string `json:"managedDeviceOwnerType"`
	ComplianceState        string `json:"complianceState"`
	LastSyncDateTime       string `json:"lastSyncDateTime"`
	AzureADDeviceID        string `json:"azureADDeviceId"`
}

const deviceSelect = "id,deviceName,operatingSystem,osVersion,serialNumber,manufacturer,model,managedDeviceOwnerType,complianceState,lastSyncDateTime,azureADDeviceId"

// Devices implements Provider.
func (p *GraphProvider) Devices(ctx context.Context) ([]DeviceRecord, error) {
	recs, _, err := p.readDevices(ctx)
	return recs, err
}

// readDevices also returns the map from the Entra device id (azureADDeviceId, lower case) to the managed device id,
// which the group membership read needs.
func (p *GraphProvider) readDevices(ctx context.Context) ([]DeviceRecord, map[string]string, error) {
	var out []DeviceRecord
	aad := map[string]string{}
	seen := map[string]bool{}
	err := p.g.List(ctx, query("/v1.0/deviceManagement/managedDevices", "$select", deviceSelect), nil, func(raw json.RawMessage) error {
		var d graphDevice
		if json.Unmarshal(raw, &d) != nil || d.ID == "" || seen[d.ID] {
			return nil
		}
		seen[d.ID] = true
		out = append(out, DeviceRecord{
			ExternalID: d.ID, Name: d.DeviceName, SerialNumber: d.SerialNumber, OSPlatform: mapOS(d.OperatingSystem), OSVersion: d.OSVersion,
			Manufacturer: d.Manufacturer, Model: d.Model, Ownership: mapOwnership(d.ManagedDeviceOwnerType),
			ComplianceState: mapCompliance(d.ComplianceState), LastCheckinAt: parseTime(d.LastSyncDateTime),
		})
		if g := strings.ToLower(strings.TrimSpace(d.AzureADDeviceID)); g != "" && g != "00000000-0000-0000-0000-000000000000" {
			aad[g] = d.ID
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, aad, nil
}

func mapOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "windows":
		return "windows"
	case "macos":
		return "macos"
	case "ios", "ipados":
		return "ios"
	case "android":
		return "android"
	case "linux":
		return "linux"
	}
	return "other"
}

func mapOwnership(s string) string {
	switch strings.ToLower(s) {
	case "company":
		return "corporate"
	case "personal":
		return "personal"
	}
	return "unknown"
}

func mapCompliance(s string) string {
	switch strings.ToLower(s) {
	case "compliant":
		return "compliant"
	case "noncompliant":
		return "noncompliant"
	case "ingraceperiod":
		return "in_grace_period"
	}
	return "unknown"
}

// parseTime reads a Graph DateTimeOffset. Zero and unparsable values are "not reported".
func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return nil
	}
	t = t.UTC()
	return &t
}

// ---- software --------------------------------------------------------------------------------------------

// graphDetectedApp follows https://learn.microsoft.com/en-us/graph/api/resources/intune-devices-detectedapp?view=graph-rest-1.0
// (read 2026-10-10): id, displayName, version, publisher, platform. The per-device collection is the
// managedDevice relationship detectedApps ("All applications currently installed on the device", documented on
// https://learn.microsoft.com/en-us/graph/api/resources/intune-devices-manageddevice?view=graph-rest-beta, read
// 2026-10-10); the v1.0 reference only documents GET /deviceManagement/detectedApps and the reverse relationship,
// so this path is the least verified call of the client.
type graphDetectedApp struct {
	DisplayName string `json:"displayName"`
	Version     string `json:"version"`
	Publisher   string `json:"publisher"`
}

// Software implements Provider.
func (p *GraphProvider) Software(ctx context.Context, externalDeviceID string) ([]SoftwareRecord, error) {
	if !idPattern.MatchString(externalDeviceID) {
		return nil, nil
	}
	var out []SoftwareRecord
	err := p.g.List(ctx, query("/v1.0/deviceManagement/managedDevices/"+url.PathEscape(externalDeviceID)+"/detectedApps",
		"$select", "displayName,version,publisher"), nil, func(raw json.RawMessage) error {
		var a graphDetectedApp
		if json.Unmarshal(raw, &a) == nil && strings.TrimSpace(a.DisplayName) != "" {
			out = append(out, SoftwareRecord{Name: a.DisplayName, Version: a.Version, Publisher: a.Publisher})
		}
		return nil
	})
	if microsoft.IsGraphStatus(err, http.StatusNotFound) {
		// The device is gone since the device list was read.
		return nil, nil
	}
	return out, err
}

// ---- management data -------------------------------------------------------------------------------------

type graphTarget struct {
	ODataType  string `json:"@odata.type"`
	GroupID    string `json:"groupId"`
	FilterID   string `json:"deviceAndAppManagementAssignmentFilterId"`
	FilterType string `json:"deviceAndAppManagementAssignmentFilterType"`
}

type graphAssignment struct {
	ID     string      `json:"id"`
	Intent string      `json:"intent"`
	Target graphTarget `json:"target"`
}

type graphArtifact struct {
	ODataType            string             `json:"@odata.type"`
	ID                   string             `json:"id"`
	DisplayName          string             `json:"displayName"`
	Name                 string             `json:"name"`
	Platforms            string             `json:"platforms"`
	LastModifiedDateTime string             `json:"lastModifiedDateTime"`
	Version              *int               `json:"version"`
	Assignments          *[]graphAssignment `json:"assignments"`
}

// artifactSource describes one assignable object collection.
//
// Applications: https://learn.microsoft.com/en-us/graph/api/intune-apps-mobileapp-list?view=graph-rest-1.0 (read
// 2026-10-10) GET /deviceAppManagement/mobileApps, permission DeviceManagementApps.Read.All (or
// DeviceManagementConfiguration.Read.All); the relationship assignments (mobileAppAssignment: id, intent
// available|required|uninstall|availableWithoutEnrollment, target) is documented on
// https://learn.microsoft.com/en-us/graph/api/resources/intune-apps-mobileappassignment?view=graph-rest-1.0.
// Configuration profiles: https://learn.microsoft.com/en-us/graph/api/intune-deviceconfig-deviceconfiguration-list?view=graph-rest-1.0
// GET /deviceManagement/deviceConfigurations, DeviceManagementConfiguration.Read.All, relationship assignments.
// Compliance policies: https://learn.microsoft.com/en-us/graph/api/intune-deviceconfig-devicecompliancepolicy-list?view=graph-rest-1.0
// GET /deviceManagement/deviceCompliancePolicies, DeviceManagementConfiguration.Read.All, relationship assignments.
// Settings catalog policies (beta only): resource deviceManagementConfigurationPolicy,
// https://learn.microsoft.com/en-us/graph/api/resources/intune-deviceconfigv2-devicemanagementconfigurationpolicy?view=graph-rest-beta
// (read 2026-10-10), GET /deviceManagement/configurationPolicies.
// Scripts, remediations and endpoint security intents are not read yet.
type artifactSource struct {
	kind         string
	path         string
	selectFields string
	beta         bool
}

var artifactSources = []artifactSource{
	{kind: "application", path: "/v1.0/deviceAppManagement/mobileApps", selectFields: "id,displayName,lastModifiedDateTime"},
	{kind: "configuration_profile", path: "/v1.0/deviceManagement/deviceConfigurations", selectFields: "id,displayName,lastModifiedDateTime,version"},
	{kind: "compliance_policy", path: "/v1.0/deviceManagement/deviceCompliancePolicies", selectFields: "id,displayName,lastModifiedDateTime,version"},
	{kind: "configuration_profile", path: "/beta/deviceManagement/configurationPolicies", selectFields: "id,name,platforms,lastModifiedDateTime", beta: true},
}

// Management implements Provider. It is all-or-nothing: any failing call fails the run, so the ingestion never
// retires data because of a partial read.
func (p *GraphProvider) Management(ctx context.Context) (ManagementSnapshot, error) {
	var snap ManagementSnapshot
	groupIDs := map[string]bool{}
	for _, src := range artifactSources {
		if src.beta && !p.beta {
			continue
		}
		recs, err := p.readArtifacts(ctx, src, groupIDs)
		if err != nil {
			return ManagementSnapshot{}, err
		}
		snap.Artifacts = append(snap.Artifacts, recs...)
	}
	if p.beta {
		filters, err := p.readFilters(ctx)
		if err != nil {
			return ManagementSnapshot{}, err
		}
		snap.Filters = filters
	}
	if len(groupIDs) > maxGraphGroups {
		return ManagementSnapshot{}, ErrTooManyGroups
	}
	ringKeys, err := p.ringGroupExternalIDs(ctx, len(groupIDs) > 0)
	if err != nil {
		return ManagementSnapshot{}, err
	}
	remapGroups(&snap, ringKeys)
	if len(groupIDs) > 0 {
		snap.Memberships, err = p.readMemberships(ctx, groupIDs, ringKeys)
		if err != nil {
			return ManagementSnapshot{}, err
		}
	}
	if p.beta {
		snap.Observations, err = p.readObservations(ctx, snap.Artifacts)
		if err != nil {
			return ManagementSnapshot{}, err
		}
	}
	return snap, nil
}

func (p *GraphProvider) readArtifacts(ctx context.Context, src artifactSource, groupIDs map[string]bool) ([]ArtifactRecord, error) {
	var out []ArtifactRecord
	target := query(src.path, "$select", src.selectFields, "$expand", "assignments")
	err := p.g.List(ctx, target, nil, func(raw json.RawMessage) error {
		var a graphArtifact
		if json.Unmarshal(raw, &a) != nil || a.ID == "" {
			return nil
		}
		rec := ArtifactRecord{ExternalID: a.ID, Kind: src.kind, Name: firstNonEmpty(a.DisplayName, a.Name), Platform: artifactPlatform(a), Revision: artifactRevision(a)}
		if a.Assignments != nil {
			rec.AssignmentsKnown = true
			for _, ga := range *a.Assignments {
				ar, ok := mapAssignment(ga, src.kind == "application")
				if !ok {
					// A target type Turaco cannot represent faithfully: keep the previous assignments of this artifact.
					rec.AssignmentsKnown, rec.Assignments = false, nil
					break
				}
				if ar.TargetKind == "group" {
					groupIDs[ar.TargetGroupExternalID] = true
				}
				rec.Assignments = append(rec.Assignments, ar)
			}
		}
		out = append(out, rec)
		return nil
	})
	return out, err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func artifactRevision(a graphArtifact) string {
	rev := a.LastModifiedDateTime
	if a.Version != nil {
		rev = "v" + itoa(*a.Version) + " " + rev
	}
	return strings.TrimSpace(rev)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// artifactPlatform derives the platform from the Graph @odata.type (for example #microsoft.graph.win32LobApp,
// #microsoft.graph.macOSLobApp, #microsoft.graph.iosVppApp) or, for settings catalog policies, from platforms.
func artifactPlatform(a graphArtifact) string {
	if a.Platforms != "" {
		switch strings.ToLower(a.Platforms) {
		case "windows10":
			return "windows"
		case "macos":
			return "macos"
		case "ios":
			return "ios"
		case "android":
			return "android"
		case "linux":
			return "linux"
		}
		return "other"
	}
	t := strings.ToLower(a.ODataType)
	switch {
	case strings.Contains(t, "macos"):
		return "macos"
	case strings.Contains(t, "ios") || strings.Contains(t, "ipados"):
		return "ios"
	case strings.Contains(t, "android"):
		return "android"
	case strings.Contains(t, "windows") || strings.Contains(t, "win32") || strings.Contains(t, "officesuite") || strings.Contains(t, "msi"):
		return "windows"
	case strings.Contains(t, "linux"):
		return "linux"
	}
	return "other"
}

// mapAssignment converts one Graph assignment. Target types follow
// https://learn.microsoft.com/en-us/graph/api/resources/intune-shared-deviceandappmanagementassignmenttarget?view=graph-rest-beta
// (read 2026-10-10: filter id and filter type none|include|exclude) and its derived types groupAssignmentTarget
// (groupId), exclusionGroupAssignmentTarget, allDevicesAssignmentTarget and allLicensedUsersAssignmentTarget.
func mapAssignment(a graphAssignment, hasIntent bool) (AssignmentRecord, bool) {
	if a.ID == "" {
		return AssignmentRecord{}, false
	}
	rec := AssignmentRecord{ProviderAssignmentID: a.ID, Intent: "none", FilterMode: "none"}
	if hasIntent {
		switch strings.ToLower(a.Intent) {
		case "required":
			rec.Intent = "required"
		case "available", "availablewithoutenrollment":
			rec.Intent = "available"
		case "uninstall":
			rec.Intent = "uninstall"
		}
	}
	switch strings.TrimPrefix(strings.ToLower(a.Target.ODataType), "#") {
	case "microsoft.graph.groupassignmenttarget":
		rec.TargetKind, rec.Mode, rec.TargetGroupExternalID = "group", "include", a.Target.GroupID
	case "microsoft.graph.exclusiongroupassignmenttarget":
		rec.TargetKind, rec.Mode, rec.TargetGroupExternalID = "group", "exclude", a.Target.GroupID
	case "microsoft.graph.alldevicesassignmenttarget":
		rec.TargetKind, rec.Mode = "all_devices", "include"
	case "microsoft.graph.alllicensedusersassignmenttarget":
		rec.TargetKind, rec.Mode = "all_users", "include"
	default:
		return AssignmentRecord{}, false
	}
	if rec.TargetKind == "group" && a.Target.GroupID == "" {
		return AssignmentRecord{}, false
	}
	switch strings.ToLower(a.Target.FilterType) {
	case "include", "exclude":
		rec.FilterMode, rec.FilterExternalID = strings.ToLower(a.Target.FilterType), a.Target.FilterID
	}
	return rec, true
}

// ---- filters (beta) --------------------------------------------------------------------------------------

// Assignment filters are documented only in beta:
// https://learn.microsoft.com/en-us/graph/api/intune-policyset-deviceandappmanagementassignmentfilter-list?view=graph-rest-beta
// (read 2026-10-10) GET /deviceManagement/assignmentFilters with id, displayName, platform, rule, lastModifiedDateTime.
type graphFilter struct {
	ID                   string `json:"id"`
	DisplayName          string `json:"displayName"`
	Platform             string `json:"platform"`
	Rule                 string `json:"rule"`
	LastModifiedDateTime string `json:"lastModifiedDateTime"`
}

func (p *GraphProvider) readFilters(ctx context.Context) ([]FilterRecord, error) {
	var out []FilterRecord
	err := p.g.List(ctx, query("/beta/deviceManagement/assignmentFilters", "$select", "id,displayName,platform,rule,lastModifiedDateTime"), nil,
		func(raw json.RawMessage) error {
			var f graphFilter
			if json.Unmarshal(raw, &f) != nil || f.ID == "" {
				return nil
			}
			out = append(out, FilterRecord{ExternalID: f.ID, Name: f.DisplayName, Platform: filterPlatform(f.Platform), Rule: f.Rule, Revision: f.LastModifiedDateTime})
			return nil
		})
	return out, err
}

func filterPlatform(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.HasPrefix(l, "windows"):
		return "windows"
	case l == "macos":
		return "macos"
	case strings.HasPrefix(l, "ios"):
		return "ios"
	case strings.HasPrefix(l, "android"):
		return "android"
	}
	return "other"
}

// ---- groups and memberships ------------------------------------------------------------------------------

// ringGroupExternalIDs maps the Graph ids of Turaco-owned ring groups (display name turaco-ring-<ringId>) to the
// deterministic external id the Deployment engine uses (RingGroupID). Graph chooses the group id itself, so the
// adapter translates in both directions: the writer finds the group by name and this read reports it by name.
// GET /groups with $filter startswith(displayName,...): https://learn.microsoft.com/en-us/graph/api/group-list?view=graph-rest-1.0
// (read 2026-10-10); permission GroupMember.Read.All is listed among the higher privileged permissions.
func (p *GraphProvider) ringGroupExternalIDs(ctx context.Context, needed bool) (map[string]string, error) {
	out := map[string]string{}
	if !needed {
		return out, nil
	}
	target := query("/v1.0/groups", "$filter", "startswith(displayName,'"+ringGroupNamePrefix+"')", "$select", "id,displayName", "$count", "true")
	err := p.g.List(ctx, target, http.Header{"ConsistencyLevel": {"eventual"}}, func(raw json.RawMessage) error {
		var g struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal(raw, &g) == nil && g.ID != "" && ringGroupName.MatchString(g.DisplayName) {
			out[strings.ToLower(g.ID)] = strings.ToLower(g.DisplayName)
		}
		return nil
	})
	return out, err
}

func remapGroups(snap *ManagementSnapshot, ring map[string]string) {
	if len(ring) == 0 {
		return
	}
	for i := range snap.Artifacts {
		as := snap.Artifacts[i].Assignments
		for j := range as {
			if ext, ok := ring[strings.ToLower(as[j].TargetGroupExternalID)]; ok {
				as[j].TargetGroupExternalID = ext
			}
		}
	}
}

// readMemberships reads the device members of every group that an assignment targets.
// GET /groups/{id}/transitiveMembers/microsoft.graph.device: members including nested groups, cast to device
// (OData cast shown on https://learn.microsoft.com/en-us/graph/api/group-list-members?view=graph-rest-1.0, read
// 2026-10-10; transitive variant of the same API family). Device objects are matched to managed devices by
// Device.deviceId == managedDevice.azureADDeviceId; Entra devices without a managed device are ignored.
// Permissions GroupMember.Read.All and Device.Read.All (without Device.Read.All members come back with limited
// information, no deviceId, and no membership can be matched).
func (p *GraphProvider) readMemberships(ctx context.Context, groupIDs map[string]bool, ring map[string]string) ([]DeviceGroupMembershipRecord, error) {
	_, aad, err := p.readDevices(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(groupIDs))
	for id := range groupIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []DeviceGroupMembershipRecord
	for _, gid := range ids {
		if !idPattern.MatchString(gid) {
			continue
		}
		ext := gid
		if r, ok := ring[strings.ToLower(gid)]; ok {
			ext = r
		}
		seen := map[string]bool{}
		err := p.g.List(ctx, query("/v1.0/groups/"+url.PathEscape(gid)+"/transitiveMembers/microsoft.graph.device", "$select", "id,deviceId", "$top", "999"),
			http.Header{"ConsistencyLevel": {"eventual"}}, func(raw json.RawMessage) error {
				var m struct {
					DeviceID string `json:"deviceId"`
				}
				if json.Unmarshal(raw, &m) != nil {
					return nil
				}
				managed, ok := aad[strings.ToLower(m.DeviceID)]
				if !ok || seen[managed] {
					return nil
				}
				seen[managed] = true
				if len(out) >= maxGraphMemberships {
					return microsoft.ErrTooManyResults
				}
				out = append(out, DeviceGroupMembershipRecord{ExternalDeviceID: managed, GroupExternalID: ext})
				return nil
			})
		if microsoft.IsGraphStatus(err, http.StatusNotFound) {
			continue // the group was deleted after the assignments were read
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- observations (beta) ---------------------------------------------------------------------------------

// App install statuses are documented only in beta:
// https://learn.microsoft.com/en-us/graph/api/intune-apps-mobileappinstallstatus-list?view=graph-rest-beta (read
// 2026-10-10) GET /deviceAppManagement/mobileApps/{id}/deviceStatuses, permission DeviceManagementApps.Read.All;
// installState installed|failed|notInstalled|uninstallFailed|pendingInstall|unknown|notApplicable.
// Whether deviceId is the managed device id is not stated by the documentation (unverified). Configuration and
// compliance device statuses (v1.0) carry only the device display name, no device id, and are not read.
type graphAppStatus struct {
	DeviceID         string `json:"deviceId"`
	InstallState     string `json:"installState"`
	LastSyncDateTime string `json:"lastSyncDateTime"`
}

func (p *GraphProvider) readObservations(ctx context.Context, artifacts []ArtifactRecord) ([]ObservationRecord, error) {
	var out []ObservationRecord
	apps := 0
	for _, a := range artifacts {
		if a.Kind != "application" || !idPattern.MatchString(a.ExternalID) {
			continue
		}
		if apps++; apps > maxGraphObsArtifacts {
			return nil, microsoft.ErrTooManyResults
		}
		err := p.g.List(ctx, "/beta/deviceAppManagement/mobileApps/"+url.PathEscape(a.ExternalID)+"/deviceStatuses", nil, func(raw json.RawMessage) error {
			var s graphAppStatus
			if json.Unmarshal(raw, &s) != nil || s.DeviceID == "" {
				return nil
			}
			if len(out) >= maxGraphObservations {
				return microsoft.ErrTooManyResults
			}
			at := time.Now().UTC()
			if t := parseTime(s.LastSyncDateTime); t != nil {
				at = *t
			}
			out = append(out, ObservationRecord{ExternalDeviceID: s.DeviceID, ArtifactExternalID: a.ExternalID, RawStatus: s.InstallState,
				State: mapInstallState(s.InstallState), ObservedAt: at})
			return nil
		})
		if microsoft.IsGraphStatus(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func mapInstallState(s string) string {
	switch strings.ToLower(s) {
	case "installed":
		return "applied"
	case "failed", "uninstallfailed":
		return "failed"
	case "pendinginstall":
		return "pending"
	case "notapplicable":
		return "not_applicable"
	}
	return "unknown"
}
