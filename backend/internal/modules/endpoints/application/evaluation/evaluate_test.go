package evaluation

import (
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func mem(ids ...string) []Membership {
	out := make([]Membership, 0, len(ids))
	for _, id := range ids {
		out = append(out, Membership{GroupExternalID: id, ObservedAt: now.Add(-time.Hour)})
	}
	return out
}

func grp(id, g, mode string) Assignment {
	return Assignment{ID: id, TargetKind: TargetGroup, GroupExternalID: g, Mode: mode, Intent: "required", FilterMode: FilterModeNone, LastSyncedAt: now.Add(-time.Hour)}
}

func filtered(a Assignment, rule, mode string) Assignment {
	a.Filter, a.FilterMode = &Filter{ID: "f", Name: "F", Rule: rule}, mode
	return a
}

func base() Input {
	return Input{Now: now, Device: Device{Platform: "windows", Ownership: "corporate", Manufacturer: "Dell Inc.", Model: "Latitude", OSVersion: "10.0.22631", SyncedAt: now.Add(-time.Hour)}, UserKnown: true}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name       string
		mod        func(*Input)
		result     string
		confidence string
		reasons    []string
		path       []string // step kinds
	}{
		{"include by device group", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded}, []string{StepMembership, StepAssignment, StepFinal}},
		{"include by user group is medium", func(i *Input) {
			i.UserMemberships = mem("g1")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceMedium, []string{ReasonIncluded, ReasonMemberViaUser}, []string{StepMembership, StepAssignment, StepFinal}},
		{"nested child inherits parent target", func(i *Input) {
			i.DeviceMemberships = mem("child")
			i.Nesting = map[string][]string{"child": {"mid"}, "mid": {"top"}}
			i.Assignments = []Assignment{grp("a1", "top", ModeInclude)}
		}, Applicable, ConfidenceMedium, []string{ReasonIncluded, ReasonMemberNested}, []string{StepMembership, StepNesting, StepNesting, StepAssignment, StepFinal}},
		{"nesting cycle terminates", func(i *Input) {
			i.DeviceMemberships = mem("a")
			i.Nesting = map[string][]string{"a": {"b"}, "b": {"a"}}
			i.Assignments = []Assignment{grp("a1", "zzz", ModeInclude)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonNoMatch}, []string{StepFinal}},
		{"exclude beats include", func(i *Input) {
			i.DeviceMemberships = mem("g1", "g2")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude), grp("a2", "g2", ModeExclude)}
		}, Excluded, ConfidenceHigh, []string{ReasonExcluded, ReasonExclusionWins}, []string{StepMembership, StepAssignment, StepMembership, StepAssignment, StepFinal}},
		{"exclude via nested group of user", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.UserMemberships = mem("kid")
			i.Nesting = map[string][]string{"kid": {"blocked"}}
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude), grp("a2", "blocked", ModeExclude)}
		}, Excluded, ConfidenceMedium, []string{ReasonExcluded, ReasonExclusionWins, ReasonMemberNested, ReasonMemberViaUser}, nil},
		{"exclude without include is not applicable", func(i *Input) {
			i.DeviceMemberships = mem("g2")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude), grp("a2", "g2", ModeExclude)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonExcluded, ReasonNoMatch}, nil},
		{"not a member", func(i *Input) {
			i.DeviceMemberships = mem("other")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonNoMatch}, []string{StepFinal}},
		{"no assignments", func(i *Input) {}, NotApplicable, ConfidenceHigh, []string{ReasonNoAssignments}, []string{StepFinal}},
		{"all devices", func(i *Input) {
			i.Assignments = []Assignment{{ID: "a1", TargetKind: TargetAllDevices, Mode: ModeInclude, FilterMode: FilterModeNone}}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded, ReasonTargetAllDevices}, []string{StepTarget, StepAssignment, StepFinal}},
		{"all users with known user is medium", func(i *Input) {
			i.Assignments = []Assignment{{ID: "a1", TargetKind: TargetAllUsers, Mode: ModeInclude, FilterMode: FilterModeNone}}
		}, Applicable, ConfidenceMedium, []string{ReasonIncluded, ReasonTargetAllUsers, ReasonMemberViaUser}, []string{StepTarget, StepAssignment, StepFinal}},
		{"all users with unknown user", func(i *Input) {
			i.UserKnown = false
			i.Assignments = []Assignment{{ID: "a1", TargetKind: TargetAllUsers, Mode: ModeInclude, FilterMode: FilterModeNone}}
		}, UnknownResult, ConfidenceLow, []string{ReasonTargetAllUsers, ReasonUserUnknown}, []string{StepUserUnk, StepAssignment, StepFinal}},
		{"unknown user but device member still applies", func(i *Input) {
			i.UserKnown = false
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded}, nil},
		{"unknown user and not a device member is unknown, never not_applicable", func(i *Input) {
			i.UserKnown = false
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, UnknownResult, ConfidenceLow, []string{ReasonUserUnknown}, nil},
		{"unknown user may be excluded", func(i *Input) {
			i.UserKnown = false
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude), grp("a2", "blocked", ModeExclude)}
		}, UnknownResult, ConfidenceLow, []string{ReasonUserUnknown}, nil},
		{"filter match", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `(device.platform -eq "Windows") and (device.manufacturer -startsWith "dell")`, FilterModeInc)}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded, ReasonFilterMatched}, []string{StepMembership, StepAssignment, StepFilter, StepFinal}},
		{"filter no match removes the include", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `device.ownership -eq "personal"`, FilterModeInc)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonNoMatch, ReasonFilterNotMatched}, []string{StepMembership, StepAssignment, StepFilter, StepFinal}},
		{"exclude-mode filter", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `device.model -contains "latitude"`, FilterModeExc)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonNoMatch, ReasonFilterNotMatched}, nil},
		{"unsupported filter is unknown", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `device.osVersion -ge "10"`, FilterModeInc)}
		}, UnknownResult, ConfidenceLow, []string{ReasonFilterUnsupported}, nil},
		{"unsupported filter on a non-member does not matter", func(i *Input) {
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `device.osVersion -ge "10"`, FilterModeInc)}
		}, NotApplicable, ConfidenceHigh, []string{ReasonNoMatch}, nil},
		{"missing filter object is unknown", func(i *Input) {
			i.DeviceMemberships = mem("g1")
			a := grp("a1", "g1", ModeInclude)
			a.FilterMode = FilterModeInc
			i.Assignments = []Assignment{a}
		}, UnknownResult, ConfidenceLow, []string{ReasonFilterMissing}, nil},
		{"missing device attribute makes the filter unknown", func(i *Input) {
			i.Device.Model = ""
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{filtered(grp("a1", "g1", ModeInclude), `device.model -eq "x"`, FilterModeInc)}
		}, UnknownResult, ConfidenceLow, []string{ReasonFilterUnknown}, nil},
		{"stale membership lowers confidence", func(i *Input) {
			i.DeviceMemberships = []Membership{{GroupExternalID: "g1", ObservedAt: now.Add(-49 * time.Hour)}}
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceLow, []string{ReasonIncluded, ReasonInputsStale}, nil},
		{"stale device lowers confidence of not_applicable", func(i *Input) {
			i.Device.SyncedAt = now.Add(-72 * time.Hour)
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, NotApplicable, ConfidenceLow, []string{ReasonNoMatch, ReasonInputsStale}, nil},
		{"exactly at the limit is fresh", func(i *Input) {
			i.DeviceMemberships = []Membership{{GroupExternalID: "g1", ObservedAt: now.Add(-FreshnessLimit)}}
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded}, nil},
		{"stale user memberships are ignored when the user is unknown", func(i *Input) {
			i.UserKnown = false
			i.UserMemberships = []Membership{{GroupExternalID: "x", ObservedAt: now.Add(-100 * time.Hour)}}
			i.DeviceMemberships = mem("g1")
			i.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
		}, Applicable, ConfidenceHigh, []string{ReasonIncluded}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base()
			c.mod(&in)
			got := Evaluate(in)
			if got.Result != c.result || got.Confidence != c.confidence {
				t.Fatalf("result = %s/%s, want %s/%s (reasons %v)", got.Result, got.Confidence, c.result, c.confidence, got.Reasons)
			}
			if !slices.Equal(got.Reasons, c.reasons) {
				t.Errorf("reasons = %v, want %v", got.Reasons, c.reasons)
			}
			if c.path != nil {
				var kinds []string
				for _, s := range got.Path {
					kinds = append(kinds, s.Kind)
				}
				if !slices.Equal(kinds, c.path) {
					t.Errorf("path = %v, want %v", kinds, c.path)
				}
			}
			if last := got.Path[len(got.Path)-1]; last.Kind != StepFinal || last.Result != got.Result {
				t.Errorf("path must end in the result, got %+v", last)
			}
		})
	}
}

func TestPathMarksOrigin(t *testing.T) {
	in := base()
	in.UserMemberships = mem("kid")
	in.DeviceMemberships = mem("g1")
	in.Nesting = map[string][]string{"kid": {"g1"}}
	in.Assignments = []Assignment{grp("a1", "g1", ModeInclude)}
	got := Evaluate(in)
	origins := map[string]bool{}
	for _, s := range got.Path {
		if s.Kind == StepMembership || s.Kind == StepNesting {
			origins[s.Origin] = true
		}
	}
	if !origins[OriginDevice] || !origins[OriginUser] {
		t.Errorf("path must show both origins: %+v", got.Path)
	}
	if got.Assignments[0].Traces[0].Origin != OriginDevice || got.Assignments[0].Traces[0].Nested() {
		t.Errorf("device trace must be the direct one: %+v", got.Assignments[0].Traces)
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	in := base()
	in.DeviceMemberships = mem("a", "b", "c")
	in.Nesting = map[string][]string{"a": {"z", "y"}, "b": {"y", "z"}}
	in.Assignments = []Assignment{grp("a1", "z", ModeInclude)}
	first := Evaluate(in)
	for i := 0; i < 20; i++ {
		got := Evaluate(in)
		if !slices.Equal(got.Reasons, first.Reasons) || len(got.Path) != len(first.Path) || got.Path[0] != first.Path[0] {
			t.Fatal("evaluation is not deterministic")
		}
	}
}

func TestFilterSubset(t *testing.T) {
	d := Device{Platform: "windows", Ownership: "corporate", Manufacturer: "Dell Inc.", Model: "Latitude 7440", OSVersion: "10.0.22631"}
	cases := []struct {
		rule      string
		want      Tri
		supported bool
	}{
		{`device.platform -eq "windows"`, Yes, true},
		{`device.platform -ne "windows"`, No, true},
		{`device.ownership -eq "Corporate"`, Yes, true},
		{`device.deviceOwnership -eq "personal"`, No, true},
		{`device.manufacturer -startsWith "dell"`, Yes, true},
		{`device.model -contains "7440"`, Yes, true},
		{`device.osVersion -in ["10.0.1", "10.0.22631"]`, Yes, true},
		{`device.platform -in ["macos","ios"]`, No, true},
		{`(device.platform -eq "windows" and device.model -contains "x") or device.manufacturer -eq "dell inc."`, Yes, true},
		{`device.platform -eq "windows" and (device.model -contains "x" or device.model -contains "Lat")`, Yes, true},
		{`device.platform -eq "macos" and device.model -contains "x"`, No, true},
		{`device.osVersion -ge "10"`, Unknown, false},
		{`device.deviceTrustType -eq "AzureAD"`, Unknown, false},
		{`(device.platform -eq "windows"`, Unknown, false},
		{`device.platform -eq windows`, Unknown, false},
		{`device.platform -eq "windows" and`, Unknown, false},
		{`-not (device.platform -eq "windows")`, Unknown, false},
		{`device.platform -in "a"`, Unknown, false},
		{``, Unknown, false},
		{`device.platform -eq "windows" xor device.model -eq "a"`, Unknown, false},
	}
	for _, c := range cases {
		got, ok := EvalFilter(c.rule, d)
		if got != c.want || ok != c.supported {
			t.Errorf("%q = %s,%v want %s,%v", c.rule, got, ok, c.want, c.supported)
		}
	}
	// Three-valued: a missing attribute is unknown, but false-and-unknown is false and true-or-unknown is true.
	empty := Device{Platform: "windows"}
	for rule, want := range map[string]Tri{
		`device.model -eq "x"`:                                   Unknown,
		`device.platform -eq "macos" and device.model -eq "x"`:   No,
		`device.platform -eq "windows" or device.model -eq "x"`:  Yes,
		`device.platform -eq "windows" and device.model -eq "x"`: Unknown,
	} {
		if got, _ := EvalFilter(rule, empty); got != want {
			t.Errorf("%q = %s, want %s", rule, got, want)
		}
	}
	if _, ok := EvalFilter(string(make([]byte, 2001)), d); ok {
		t.Error("an over-long rule must be unsupported")
	}
}
