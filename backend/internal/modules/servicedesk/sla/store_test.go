package sla

import "testing"

func TestPolicyValidation(t *testing.T) {
	valid := Input{Key: "it", Name: "IT", PauseWaitingReasons: []string{"customer"}, WarningPercent: 80}
	if !validInput(valid) {
		t.Fatal("valid policy rejected")
	}
	for _, in := range []Input{
		{Name: "", WarningPercent: 80},
		{Name: "IT", WarningPercent: 49},
		{Name: "IT", WarningPercent: 96},
		{Name: "IT", WarningPercent: 80, PauseWaitingReasons: []string{"customer", "customer"}},
		{Name: "IT", WarningPercent: 80, PauseWaitingReasons: []string{"stock"}},
	} {
		if validInput(in) {
			t.Errorf("accepted invalid policy: %+v", in)
		}
	}
}
func TestTargetMatrixValidation(t *testing.T) {
	id := "00000000-0000-0000-0000-000000000001"
	valid := []Target{{Priority: "urgent", Kind: "first_response", TargetMinutes: 30, TimeBasis: "business"}}
	if !validTargets(valid, &id) || validTargets(valid, nil) {
		t.Fatal("business target calendar requirement")
	}
	for _, bad := range []Target{
		{Priority: "critical", Kind: "resolution", TargetMinutes: 1, TimeBasis: "continuous"},
		{Priority: "low", Kind: "response", TargetMinutes: 1, TimeBasis: "continuous"},
		{Priority: "low", Kind: "resolution", TargetMinutes: 0, TimeBasis: "continuous"},
		{Priority: "low", Kind: "resolution", TargetMinutes: 129601, TimeBasis: "continuous"},
	} {
		if validTargets([]Target{bad}, nil) {
			t.Errorf("accepted invalid target: %+v", bad)
		}
	}
	if validTargets(append(valid, valid[0]), &id) {
		t.Fatal("duplicate target accepted")
	}
}
