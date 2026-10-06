package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Target Set definitions (F9 G2, docs/product/f9-software-lifecycle-design.md). A definition is a closed,
// validated structure: every clause is a fixed comparison Turaco evaluates in Go over bounded batches of live
// Devices. Nothing in it is free text that reaches SQL, and nothing is executed.

const (
	// MaxTargetDefinitionBytes bounds the stored (canonical) definition.
	MaxTargetDefinitionBytes = 64 << 10
	// MaxExplicitDevices bounds the explicit include and exclude lists (each).
	MaxExplicitDevices = 500
	// MaxFilterValues bounds the values of one list clause; MaxTargetGroups the group clause.
	MaxFilterValues = 20
	MaxTargetGroups = 20
	// maxFilterValueLen bounds a manufacturer or model value; maxOSPrefixLen the OS version prefix.
	maxFilterValueLen = 100
	maxOSPrefixLen    = 50
	maxGroupIDLen     = 200
)

// Clause names of a Target Set definition, as reported by Explain.
const (
	ClausePlatform      = "platform"
	ClauseOSVersion     = "os_version_prefix"
	ClauseOwnership     = "ownership"
	ClauseCompliance    = "compliance"
	ClauseManufacturer  = "manufacturer"
	ClauseModel         = "model"
	ClauseGroup         = "group"
	ClauseAssetLocation = "asset_location_id"
	ClauseInclude       = "include"
	ClauseExclude       = "exclude"
	ClauseAllDevices    = "all_devices"

	ClauseMatched = "matched"
	ClauseFailed  = "failed"
)

// TargetDefinition is the definition of a Target Set: filters that must all hold, plus Devices included or
// excluded by id. Without any filter and without an explicit include it selects every live Device.
type TargetDefinition struct {
	Filters          TargetFilters `json:"filters"`
	IncludeDeviceIDs []string      `json:"includeDeviceIds"`
	ExcludeDeviceIDs []string      `json:"excludeDeviceIds"`
}

// TargetFilters are the filter clauses; an empty clause is not set. List clauses match any of their values.
type TargetFilters struct {
	Platform         []string      `json:"platform,omitempty"`
	OSVersionPrefix  string        `json:"osVersionPrefix,omitempty"`
	Ownership        []string      `json:"ownership,omitempty"`
	Compliance       []string      `json:"compliance,omitempty"`
	Manufacturer     []string      `json:"manufacturer,omitempty"`
	Model            []string      `json:"model,omitempty"`
	Groups           []TargetGroup `json:"groups,omitempty"`
	AssetLocationIDs []string      `json:"assetLocationIds,omitempty"`
}

// TargetGroup is a provider Device Group by external id; IncludeNested also matches the Directory Groups nested
// below it (Organization directory graph).
type TargetGroup struct {
	ExternalID    string `json:"externalId"`
	IncludeNested bool   `json:"includeNested"`
}

func (f TargetFilters) empty() bool {
	return len(f.Platform) == 0 && f.OSVersionPrefix == "" && len(f.Ownership) == 0 && len(f.Compliance) == 0 &&
		len(f.Manufacturer) == 0 && len(f.Model) == 0 && len(f.Groups) == 0 && len(f.AssetLocationIDs) == 0
}

// effective returns the filters that can exclude a Device: a platform, ownership or compliance list that names
// every value of its enum matches every Device and counts as unset.
func (f TargetFilters) effective() TargetFilters {
	covers := func(values, enum []string) bool {
		for _, e := range enum {
			if !slices.Contains(values, e) {
				return false
			}
		}
		return true
	}
	if covers(f.Platform, OSPlatforms) {
		f.Platform = nil
	}
	if covers(f.Ownership, Ownerships) {
		f.Ownership = nil
	}
	if covers(f.Compliance, ComplianceStates) {
		f.Compliance = nil
	}
	return f
}

// AllDevices reports a definition that selects every live Device (minus the excluded ones): no filter and no
// explicit include, or filters that cannot exclude any Device (lists covering their whole enum). Targeting it is
// high impact.
func (d TargetDefinition) AllDevices() bool {
	if d.Filters.empty() {
		return len(d.IncludeDeviceIDs) == 0
	}
	return d.Filters.effective().empty()
}

// nestedGroups returns the external ids of the groups whose nested Directory Groups are included.
func (d TargetDefinition) nestedGroups() []string {
	var out []string
	for _, g := range d.Filters.Groups {
		if g.IncludeNested {
			out = append(out, g.ExternalID)
		}
	}
	return out
}

// revealsDevices reports a definition naming Devices, provider groups or Asset locations: authoring it needs
// endpoints.view.
func (d TargetDefinition) revealsDevices() bool {
	return len(d.IncludeDeviceIDs) > 0 || len(d.ExcludeDeviceIDs) > 0 || len(d.Filters.Groups) > 0 || len(d.Filters.AssetLocationIDs) > 0
}

// ParseTargetDefinition strictly decodes a stored or submitted definition: one JSON object, no unknown fields,
// at most MaxTargetDefinitionBytes; the result is validated and normalized.
func ParseTargetDefinition(raw []byte) (TargetDefinition, error) {
	if len(raw) > MaxTargetDefinitionBytes {
		return TargetDefinition{}, invalid("definition must be at most %d bytes", MaxTargetDefinitionBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d TargetDefinition
	if err := dec.Decode(&d); err != nil {
		return TargetDefinition{}, invalid("definition is not a valid target set definition")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return TargetDefinition{}, invalid("definition is not a valid target set definition")
	}
	return NormalizeTargetDefinition(d)
}

// CanonicalJSON is the stored form of a normalized definition (deterministic key and value order).
func (d TargetDefinition) CanonicalJSON() ([]byte, error) {
	if d.IncludeDeviceIDs == nil {
		d.IncludeDeviceIDs = []string{}
	}
	if d.ExcludeDeviceIDs == nil {
		d.ExcludeDeviceIDs = []string{}
	}
	return json.Marshal(d)
}

func enumList(name string, in, allowed []string) ([]string, error) {
	if len(in) > MaxFilterValues {
		return nil, invalid("%s takes at most %d values", name, MaxFilterValues)
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(allowed, v) {
			return nil, invalid("%s must be one of %s", name, strings.Join(allowed, ", "))
		}
		out = append(out, v)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func textList(name string, in []string) ([]string, error) {
	if len(in) > MaxFilterValues {
		return nil, invalid("%s takes at most %d values", name, MaxFilterValues)
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || utf8.RuneCountInString(v) > maxFilterValueLen || !utf8.ValidString(v) || safetext.ContainsUnsafe(v, false) {
			return nil, invalid("%s values must be 1-%d characters without control or invisible formatting characters", name, maxFilterValueLen)
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return slices.CompactFunc(out, strings.EqualFold), nil
}

func uuidList(name string, in []string, max int) ([]string, error) {
	if len(in) > max {
		return nil, invalid("%s takes at most %d ids", name, max)
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !validUUID(v) {
			return nil, invalid("%s must contain ids", name)
		}
		out = append(out, strings.ToLower(v))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// NormalizeTargetDefinition validates every clause and returns the canonical form: sorted, de-duplicated values
// and lower-case ids.
func NormalizeTargetDefinition(d TargetDefinition) (TargetDefinition, error) {
	var out TargetDefinition
	var err error
	f := d.Filters
	if out.Filters.Platform, err = enumList("platform", f.Platform, OSPlatforms); err != nil {
		return TargetDefinition{}, err
	}
	if out.Filters.Ownership, err = enumList("ownership", f.Ownership, Ownerships); err != nil {
		return TargetDefinition{}, err
	}
	if out.Filters.Compliance, err = enumList("compliance", f.Compliance, ComplianceStates); err != nil {
		return TargetDefinition{}, err
	}
	if out.Filters.Manufacturer, err = textList("manufacturer", f.Manufacturer); err != nil {
		return TargetDefinition{}, err
	}
	if out.Filters.Model, err = textList("model", f.Model); err != nil {
		return TargetDefinition{}, err
	}
	prefix := strings.TrimSpace(f.OSVersionPrefix)
	if utf8.RuneCountInString(prefix) > maxOSPrefixLen || !utf8.ValidString(prefix) || safetext.ContainsUnsafe(prefix, false) {
		return TargetDefinition{}, invalid("osVersionPrefix must be at most %d characters without control or invisible formatting characters", maxOSPrefixLen)
	}
	out.Filters.OSVersionPrefix = prefix
	if len(f.Groups) > MaxTargetGroups {
		return TargetDefinition{}, invalid("groups takes at most %d groups", MaxTargetGroups)
	}
	groups := make([]TargetGroup, 0, len(f.Groups))
	for _, g := range f.Groups {
		ext := strings.TrimSpace(g.ExternalID)
		if ext == "" || len(ext) > maxGroupIDLen || !utf8.ValidString(ext) || safetext.ContainsUnsafe(ext, false) || strings.ContainsAny(ext, " \t") {
			return TargetDefinition{}, invalid("a group needs an externalId of 1-%d characters without spaces or control characters", maxGroupIDLen)
		}
		groups = append(groups, TargetGroup{ExternalID: ext, IncludeNested: g.IncludeNested})
	}
	slices.SortFunc(groups, func(a, b TargetGroup) int {
		if c := strings.Compare(a.ExternalID, b.ExternalID); c != 0 {
			return c
		}
		if a.IncludeNested == b.IncludeNested {
			return 0
		}
		if !a.IncludeNested {
			return -1
		}
		return 1
	})
	// One entry per group: includeNested wins over the plain membership.
	for i := 0; i < len(groups); i++ {
		if i+1 < len(groups) && groups[i].ExternalID == groups[i+1].ExternalID {
			groups = slices.Delete(groups, i, i+1)
			i--
		}
	}
	if len(groups) > 0 {
		out.Filters.Groups = groups
	}
	if out.Filters.AssetLocationIDs, err = uuidList("assetLocationIds", f.AssetLocationIDs, MaxFilterValues); err != nil {
		return TargetDefinition{}, err
	}
	if out.IncludeDeviceIDs, err = uuidList("includeDeviceIds", d.IncludeDeviceIDs, MaxExplicitDevices); err != nil {
		return TargetDefinition{}, err
	}
	if out.ExcludeDeviceIDs, err = uuidList("excludeDeviceIds", d.ExcludeDeviceIDs, MaxExplicitDevices); err != nil {
		return TargetDefinition{}, err
	}
	for _, id := range out.IncludeDeviceIDs {
		if _, found := slices.BinarySearch(out.ExcludeDeviceIDs, id); found {
			return TargetDefinition{}, invalid("a device cannot be included and excluded at the same time")
		}
	}
	out.Filters = nilEmpty(out.Filters)
	raw, err := out.CanonicalJSON()
	if err != nil {
		return TargetDefinition{}, err
	}
	if len(raw) > MaxTargetDefinitionBytes {
		return TargetDefinition{}, invalid("definition must be at most %d bytes", MaxTargetDefinitionBytes)
	}
	return out, nil
}

// nilEmpty turns empty lists into nil so the canonical JSON leaves unset clauses out.
func nilEmpty(f TargetFilters) TargetFilters {
	z := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	f.Platform, f.Ownership, f.Compliance, f.Manufacturer, f.Model, f.AssetLocationIDs =
		z(f.Platform), z(f.Ownership), z(f.Compliance), z(f.Manufacturer), z(f.Model), z(f.AssetLocationIDs)
	return f
}

// ClauseResult is the outcome of one set clause for one Device. Reason is a code: not_member, no_asset_link,
// no_location, value_missing, explicit.
type ClauseResult struct {
	Clause string
	Result string
	Reason string
}

// deviceFacts is what the evaluation knows about one live Device: its attributes, its current provider group
// memberships (external ids) and the location of its linked Asset.
type deviceFacts struct {
	Device     Device
	Groups     map[string]bool
	LocationID *string
}

// resolvedDefinition is a definition with its group clause resolved to the set of provider group external ids
// (nested groups expanded).
type resolvedDefinition struct {
	TargetDefinition
	groupExts map[string]bool
	include   map[string]bool
	exclude   map[string]bool
}

func resolveDefinition(d TargetDefinition, groupExts map[string]bool) resolvedDefinition {
	r := resolvedDefinition{TargetDefinition: d, groupExts: groupExts, include: map[string]bool{}, exclude: map[string]bool{}}
	for _, id := range d.IncludeDeviceIDs {
		r.include[id] = true
	}
	for _, id := range d.ExcludeDeviceIDs {
		r.exclude[id] = true
	}
	return r
}

func anyFold(values []string, v *string) bool {
	if v == nil {
		return false
	}
	got := strings.TrimSpace(*v)
	return slices.ContainsFunc(values, func(x string) bool { return strings.EqualFold(x, got) })
}

// matchDevice evaluates a resolved definition for one Device and explains every clause that is set. A Device in
// the exclude list never matches; one in the include list always matches otherwise; without filters only the
// included Devices match, unless the definition selects every Device.
func matchDevice(r resolvedDefinition, f deviceFacts) (bool, []ClauseResult) {
	var out []ClauseResult
	add := func(clause string, ok bool, reason string) bool {
		res := ClauseFailed
		if ok {
			res, reason = ClauseMatched, ""
		}
		out = append(out, ClauseResult{Clause: clause, Result: res, Reason: reason})
		return ok
	}
	if r.exclude[f.Device.ID] {
		add(ClauseExclude, true, "")
		return false, out
	}
	if r.include[f.Device.ID] {
		add(ClauseInclude, true, "")
		return true, out
	}
	if len(r.IncludeDeviceIDs) > 0 {
		add(ClauseInclude, false, "not_listed")
	}
	fl := r.Filters
	if fl.empty() {
		if len(r.IncludeDeviceIDs) == 0 {
			add(ClauseAllDevices, true, "")
			return true, out
		}
		return false, out
	}
	all := true
	if len(fl.Platform) > 0 {
		all = add(ClausePlatform, slices.Contains(fl.Platform, f.Device.OSPlatform), "value_differs") && all
	}
	if fl.OSVersionPrefix != "" {
		ok, reason := false, "value_missing"
		if f.Device.OSVersion != nil {
			ok, reason = strings.HasPrefix(strings.ToLower(*f.Device.OSVersion), strings.ToLower(fl.OSVersionPrefix)), "value_differs"
		}
		all = add(ClauseOSVersion, ok, reason) && all
	}
	if len(fl.Ownership) > 0 {
		all = add(ClauseOwnership, slices.Contains(fl.Ownership, f.Device.Ownership), "value_differs") && all
	}
	if len(fl.Compliance) > 0 {
		all = add(ClauseCompliance, slices.Contains(fl.Compliance, f.Device.ComplianceState), "value_differs") && all
	}
	if len(fl.Manufacturer) > 0 {
		reason := "value_differs"
		if f.Device.Manufacturer == nil {
			reason = "value_missing"
		}
		all = add(ClauseManufacturer, anyFold(fl.Manufacturer, f.Device.Manufacturer), reason) && all
	}
	if len(fl.Model) > 0 {
		reason := "value_differs"
		if f.Device.Model == nil {
			reason = "value_missing"
		}
		all = add(ClauseModel, anyFold(fl.Model, f.Device.Model), reason) && all
	}
	if len(fl.Groups) > 0 {
		member := false
		for g := range f.Groups {
			if r.groupExts[g] {
				member = true
				break
			}
		}
		all = add(ClauseGroup, member, "not_member") && all
	}
	if len(fl.AssetLocationIDs) > 0 {
		ok, reason := false, "no_asset_link"
		if f.Device.AssetID != nil {
			reason = "no_location"
			if f.LocationID != nil {
				_, ok = slices.BinarySearch(fl.AssetLocationIDs, strings.ToLower(*f.LocationID))
				reason = "value_differs"
			}
		}
		all = add(ClauseAssetLocation, ok, reason) && all
	}
	return all, out
}
