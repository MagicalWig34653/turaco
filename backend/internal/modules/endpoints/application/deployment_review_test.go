package application

import (
	"errors"
	"testing"
)

func TestCountIsHighImpact(t *testing.T) {
	cases := []struct {
		n, live, threshold, percent int
		want                        bool
	}{
		{199, 100000, 200, 25, false},
		{200, 100000, 200, 25, true},
		{25, 100, 200, 25, true},
		{24, 100, 200, 25, false},
		{1, 4, 200, 25, true},
		{0, 0, 200, 25, false},
		{50, 100, 200, 0, false},
	}
	for _, tc := range cases {
		if got := countIsHighImpact(tc.n, tc.live, tc.threshold, tc.percent); got != tc.want {
			t.Errorf("countIsHighImpact(%d, %d, %d, %d) = %v", tc.n, tc.live, tc.threshold, tc.percent, got)
		}
	}
}

func TestEvaluationIsOnePerUser(t *testing.T) {
	s := NewService(nil, nil, nil, false, nil)
	done, err := s.beginEvaluation("u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.beginEvaluation("u1"); !errors.Is(err, ErrEvaluationBusy) {
		t.Fatalf("second evaluation: %v", err)
	}
	other, err := s.beginEvaluation("u2")
	if err != nil {
		t.Fatalf("other user: %v", err)
	}
	other()
	done()
	again, err := s.beginEvaluation("u1")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestAddEditorRefusesBeyondTheLimit(t *testing.T) {
	d := Deployment{}
	for i := 0; i < maxEditors; i++ {
		var err error
		if d, err = addEditor(d, string(rune('a'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := addEditor(d, "new"); !errors.Is(err, ErrEditorsFull) {
		t.Fatalf("51st editor: %v", err)
	}
	if _, err := addEditor(d, d.Editors[0]); err != nil {
		t.Fatalf("existing editor: %v", err)
	}
}

func TestDefinitionSemantics(t *testing.T) {
	all := TargetDefinition{Filters: TargetFilters{Platform: OSPlatforms, Ownership: Ownerships}}
	if !all.AllDevices() {
		t.Fatal("full-enum filters must select all devices")
	}
	if (TargetDefinition{IncludeDeviceIDs: []string{"x"}}).AllDevices() || (TargetDefinition{Filters: TargetFilters{Platform: []string{"linux"}}}).AllDevices() {
		t.Fatal("narrow definitions are not all devices")
	}
	if (TargetDefinition{Filters: TargetFilters{Platform: []string{"linux"}}}).revealsDevices() ||
		!(TargetDefinition{Filters: TargetFilters{Groups: []TargetGroup{{ExternalID: "g"}}}}).revealsDevices() {
		t.Fatal("revealsDevices")
	}
}
