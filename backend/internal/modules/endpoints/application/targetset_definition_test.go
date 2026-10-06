package application

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	devA = "00000000-0000-7000-8000-00000000000a"
	devB = "00000000-0000-7000-8000-00000000000b"
	locA = "00000000-0000-7000-8000-0000000000a1"
)

func isInvalid(err error) bool {
	var inv *InvalidInputError
	return errors.As(err, &inv)
}

func TestParseTargetDefinitionIsStrict(t *testing.T) {
	bad := []string{
		`{"filters":{"platform":["windows"]},"unknown":1}`,
		`{"filters":{"colour":["red"]}}`,
		`{"filters":{"groups":[{"externalId":"g","nested":true}]}}`,
		`{"filters":{}} {}`,
		`[]`,
		`{"filters":{"platform":["beos"]}}`,
		`{"filters":{"ownership":["company"]}}`,
		`{"filters":{"compliance":["green"]}}`,
		`{"filters":{"assetLocationIds":["not-a-uuid"]}}`,
		`{"includeDeviceIds":["x"]}`,
		`{"includeDeviceIds":["` + devA + `"],"excludeDeviceIds":["` + strings.ToUpper(devA) + `"]}`,
		`{"filters":{"manufacturer":["` + strings.Repeat("m", 101) + `"]}}`,
		`{"filters":{"manufacturer":["bad​value"]}}`,
		`{"filters":{"osVersionPrefix":"` + strings.Repeat("1", 51) + `"}}`,
		`{"filters":{"groups":[{"externalId":"has space"}]}}`,
		`{"filters":{"groups":[{"externalId":""}]}}`,
	}
	for _, raw := range bad {
		if _, err := ParseTargetDefinition([]byte(raw)); !isInvalid(err) {
			t.Errorf("%s: accepted (%v)", raw, err)
		}
	}
	// Oversize input and too many values are refused before anything is evaluated.
	if _, err := ParseTargetDefinition([]byte(`{"filters":{"model":["` + strings.Repeat("x", MaxTargetDefinitionBytes) + `"]}}`)); !isInvalid(err) {
		t.Errorf("oversize accepted: %v", err)
	}
	var ids []string
	for i := 0; i <= MaxExplicitDevices; i++ {
		ids = append(ids, fmt.Sprintf(`"00000000-0000-7000-8000-%012d"`, i))
	}
	if _, err := ParseTargetDefinition([]byte(`{"includeDeviceIds":[` + strings.Join(ids, ",") + `]}`)); !isInvalid(err) {
		t.Errorf("501 includes accepted: %v", err)
	}
	var many []string
	for i := 0; i <= MaxFilterValues; i++ {
		many = append(many, fmt.Sprintf(`"m%d"`, i))
	}
	if _, err := ParseTargetDefinition([]byte(`{"filters":{"model":[` + strings.Join(many, ",") + `]}}`)); !isInvalid(err) {
		t.Errorf("21 models accepted: %v", err)
	}
}

func TestNormalizeTargetDefinitionIsCanonical(t *testing.T) {
	d, err := ParseTargetDefinition([]byte(`{"filters":{"platform":["windows","macos","windows"],"manufacturer":[" Dell ","dell","HP"],
		"groups":[{"externalId":"g2"},{"externalId":"g1"},{"externalId":"g1","includeNested":true}],"assetLocationIds":["` + strings.ToUpper(locA) + `"]},
		"includeDeviceIds":["` + strings.ToUpper(devB) + `","` + devA + `"]}`))
	if err != nil {
		t.Fatal(err)
	}
	f := d.Filters
	if strings.Join(f.Platform, ",") != "macos,windows" || len(f.Manufacturer) != 2 || f.Manufacturer[0] != "Dell" {
		t.Fatalf("lists not normalized: %+v", f)
	}
	if len(f.Groups) != 2 || f.Groups[0] != (TargetGroup{ExternalID: "g1", IncludeNested: true}) || f.Groups[1].ExternalID != "g2" {
		t.Fatalf("groups not normalized: %+v", f.Groups)
	}
	if f.AssetLocationIDs[0] != locA || d.IncludeDeviceIDs[0] != devA || d.IncludeDeviceIDs[1] != devB {
		t.Fatalf("ids not normalized: %+v", d)
	}
	a, _ := d.CanonicalJSON()
	again, err := ParseTargetDefinition(a)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := again.CanonicalJSON()
	if string(a) != string(b) {
		t.Fatalf("canonical form not stable:\n%s\n%s", a, b)
	}
	if d.AllDevices() {
		t.Fatal("a filtered definition is not all devices")
	}
	empty, err := ParseTargetDefinition([]byte(`{"filters":{},"excludeDeviceIds":["` + devA + `"]}`))
	if err != nil || !empty.AllDevices() {
		t.Fatalf("no filter and no include must select all devices: %v %+v", err, empty)
	}
}

func strp(s string) *string { return &s }

func TestMatchDeviceExplainsEveryClause(t *testing.T) {
	win := Device{ID: devA, OSPlatform: "windows", OSVersion: strp("10.0.22631"), Ownership: "corporate", ComplianceState: "compliant",
		Manufacturer: strp("Dell Inc."), Model: strp("Latitude 7440"), AssetID: strp("00000000-0000-7000-8000-0000000000f1")}
	facts := deviceFacts{Device: win, Groups: map[string]bool{"grp-child": true}, LocationID: strp(locA)}
	def := TargetDefinition{Filters: TargetFilters{Platform: []string{"windows"}, OSVersionPrefix: "10.0.22", Ownership: []string{"corporate"},
		Compliance: []string{"compliant"}, Manufacturer: []string{"dell inc."}, Model: []string{"LATITUDE 7440"},
		Groups: []TargetGroup{{ExternalID: "grp-parent", IncludeNested: true}}, AssetLocationIDs: []string{locA}}}
	nested := map[string]bool{"grp-parent": true, "grp-child": true}
	ok, clauses := matchDevice(resolveDefinition(def, nested), facts)
	if !ok || len(clauses) != 8 {
		t.Fatalf("all clauses should match: %v %+v", ok, clauses)
	}
	for _, c := range clauses {
		if c.Result != ClauseMatched {
			t.Fatalf("clause %+v", c)
		}
	}
	// Without the nested expansion the child group membership does not count.
	if ok, _ := matchDevice(resolveDefinition(def, map[string]bool{"grp-parent": true}), facts); ok {
		t.Fatal("membership of a nested group matched without nesting")
	}
	cases := []struct {
		name   string
		change func(*deviceFacts)
		clause string
		reason string
	}{
		{"platform", func(f *deviceFacts) { f.Device.OSPlatform = "macos" }, ClausePlatform, "value_differs"},
		{"os missing", func(f *deviceFacts) { f.Device.OSVersion = nil }, ClauseOSVersion, "value_missing"},
		{"os prefix", func(f *deviceFacts) { f.Device.OSVersion = strp("10.0.19045") }, ClauseOSVersion, "value_differs"},
		{"ownership", func(f *deviceFacts) { f.Device.Ownership = "personal" }, ClauseOwnership, "value_differs"},
		{"compliance", func(f *deviceFacts) { f.Device.ComplianceState = "noncompliant" }, ClauseCompliance, "value_differs"},
		{"manufacturer", func(f *deviceFacts) { f.Device.Manufacturer = nil }, ClauseManufacturer, "value_missing"},
		{"model", func(f *deviceFacts) { f.Device.Model = strp("XPS") }, ClauseModel, "value_differs"},
		{"group", func(f *deviceFacts) { f.Groups = map[string]bool{"other": true} }, ClauseGroup, "not_member"},
		{"no asset", func(f *deviceFacts) { f.Device.AssetID = nil }, ClauseAssetLocation, "no_asset_link"},
		{"no location", func(f *deviceFacts) { f.LocationID = nil }, ClauseAssetLocation, "no_location"},
		{"other location", func(f *deviceFacts) { f.LocationID = strp(devB) }, ClauseAssetLocation, "value_differs"},
	}
	for _, tc := range cases {
		f := facts
		f.Groups = map[string]bool{"grp-child": true}
		tc.change(&f)
		ok, clauses := matchDevice(resolveDefinition(def, nested), f)
		if ok {
			t.Errorf("%s: matched", tc.name)
			continue
		}
		failed := 0
		for _, c := range clauses {
			if c.Result == ClauseFailed {
				failed++
				if c.Clause != tc.clause || c.Reason != tc.reason {
					t.Errorf("%s: failed clause %+v", tc.name, c)
				}
			}
		}
		if failed != 1 {
			t.Errorf("%s: %d failed clauses", tc.name, failed)
		}
	}
}

func TestMatchDeviceIncludeExcludeAndAllDevices(t *testing.T) {
	d := deviceFacts{Device: Device{ID: devA, OSPlatform: "macos"}}
	all := TargetDefinition{}
	if ok, c := matchDevice(resolveDefinition(all, nil), d); !ok || c[0].Clause != ClauseAllDevices {
		t.Fatalf("all devices: %v %+v", ok, c)
	}
	excluded := TargetDefinition{ExcludeDeviceIDs: []string{devA}}
	if ok, c := matchDevice(resolveDefinition(excluded, nil), d); ok || c[0].Clause != ClauseExclude {
		t.Fatalf("exclude wins: %v %+v", ok, c)
	}
	// An explicit include overrides failing filters; without filters only included Devices match.
	inc := TargetDefinition{Filters: TargetFilters{Platform: []string{"windows"}}, IncludeDeviceIDs: []string{devA}}
	if ok, _ := matchDevice(resolveDefinition(inc, nil), d); !ok {
		t.Fatal("included device must match")
	}
	only := TargetDefinition{IncludeDeviceIDs: []string{devB}}
	if ok, c := matchDevice(resolveDefinition(only, nil), d); ok || c[0].Reason != "not_listed" {
		t.Fatalf("include-only set matched another device: %v %+v", ok, c)
	}
}
