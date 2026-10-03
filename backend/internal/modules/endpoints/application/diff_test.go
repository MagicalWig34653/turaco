package application

import "testing"

func side(assigned, assignedUnknown bool, expected string, observed string) DiffSide {
	s := DiffSide{Assigned: assigned, AssignedUnknown: assignedUnknown, Expected: ExpectedApplicability{Result: expected}}
	if observed != "" {
		s.Observed = &ObservedState{State: observed}
	}
	return s
}

func TestClassifyDevices(t *testing.T) {
	cases := []struct {
		name      string
		l, r      DiffSide
		class     string
		uncertain bool
		expected  string
	}{
		{"both absent", side(false, false, ExpectedNotApplicable, ""), side(false, false, ExpectedNotApplicable, ""), DiffSame, false, DimSame},
		{"identical", side(true, false, ExpectedApplicable, "applied"), side(true, false, ExpectedApplicable, "applied"), DiffSame, false, DimSame},
		{"only left", side(true, false, ExpectedApplicable, "applied"), side(false, false, ExpectedNotApplicable, ""), DiffOnlyLeft, false, DimDifferent},
		{"only right", side(false, false, ExpectedNotApplicable, ""), side(false, false, ExpectedExcluded, ""), DiffOnlyRight, false, DimDifferent},
		{"different observed", side(true, false, ExpectedApplicable, "applied"), side(true, false, ExpectedApplicable, "failed"), DiffDifferent, false, DimSame},
		{"different expected", side(true, false, ExpectedApplicable, "applied"), side(true, false, ExpectedExcluded, "applied"), DiffDifferent, false, DimDifferent},
		// An unknown never makes the sides equal or different: the dimension is unknown and the item uncertain.
		{"unknown expected", side(true, false, ExpectedApplicable, "applied"), side(true, false, ExpectedUnknown, "applied"), DiffSame, true, DimUnknown},
		{"both unknown", side(false, false, ExpectedUnknown, ""), side(false, false, ExpectedUnknown, ""), DiffSame, true, DimUnknown},
		{"unknown but observed differs", side(true, false, ExpectedApplicable, "applied"), side(false, true, ExpectedUnknown, ""), DiffDifferent, true, DimUnknown},
	}
	for _, c := range cases {
		class, dims, uncertain := classifyDevices(c.l, c.r)
		if class != c.class || uncertain != c.uncertain || dims.Expected != c.expected {
			t.Errorf("%s: class %s uncertain %v expected-dim %s; want %s %v %s", c.name, class, uncertain, dims.Expected, c.class, c.uncertain, c.expected)
		}
	}
}

func target(mode, intent, filterMode, filterID string) AssignedTarget {
	a := AssignedTarget{Mode: mode, Intent: intent, FilterMode: filterMode}
	if filterID != "" {
		a.Filter = &FilterSummary{ID: filterID}
	}
	return a
}

func TestClassifyGroups(t *testing.T) {
	inc := target("include", "required", "none", "")
	cases := []struct {
		name  string
		l, r  []AssignedTarget
		class string
		diffs []string
	}{
		{"only left", []AssignedTarget{inc}, nil, DiffOnlyLeft, nil},
		{"only right", nil, []AssignedTarget{inc}, DiffOnlyRight, nil},
		{"same", []AssignedTarget{inc}, []AssignedTarget{inc}, DiffSame, nil},
		{"intent", []AssignedTarget{inc}, []AssignedTarget{target("include", "available", "none", "")}, DiffDifferent, []string{"intent"}},
		{"mode and filter", []AssignedTarget{inc}, []AssignedTarget{target("exclude", "required", "include", "f1")}, DiffDifferent, []string{"mode", "filter"}},
		{"other filter", []AssignedTarget{target("include", "required", "include", "f1")}, []AssignedTarget{target("include", "required", "include", "f2")}, DiffDifferent, []string{"filter"}},
	}
	for _, c := range cases {
		class, diffs := classifyGroups(c.l, c.r)
		if class != c.class || len(diffs) != len(c.diffs) {
			t.Errorf("%s: %s %v, want %s %v", c.name, class, diffs, c.class, c.diffs)
			continue
		}
		for i := range diffs {
			if diffs[i] != c.diffs[i] {
				t.Errorf("%s: diffs %v, want %v", c.name, diffs, c.diffs)
			}
		}
	}
}
