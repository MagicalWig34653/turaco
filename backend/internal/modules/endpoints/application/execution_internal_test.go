package application

import (
	"testing"
	"time"
)

func TestEvidenceCodeGatesPromotion(t *testing.T) {
	now := time.Now()
	settled := now.Add(-2 * time.Hour)
	run := DeploymentRingRun{SettledAt: &settled}
	ring := DeploymentRing{SuccessThresholdPercent: 80, SoakMinutes: 60}
	min90 := 90
	counts := func(succ, fresh, failed, open, freshObs int) RingCounts {
		return RingCounts{ByState: map[string]int{TargetSuccessful: succ, TargetFailed: failed, TargetAwaitingObservation: open, TargetAlreadySatisfied: 3, TargetCancelled: 1},
			FreshSuccessful: fresh, FreshObserved: freshObs}
	}
	cases := []struct {
		name string
		run  DeploymentRingRun
		ring DeploymentRing
		c    RingCounts
		want string
	}{
		{"open targets", run, ring, counts(8, 8, 0, 2, 10), CodeRingNotAwaiting},
		{"never settled", DeploymentRingRun{}, ring, counts(10, 10, 0, 0, 10), CodeRingNotAwaiting},
		{"soak running", DeploymentRingRun{SettledAt: &now}, ring, counts(10, 10, 0, 0, 10), CodeSoakNotElapsed},
		{"below threshold", run, ring, counts(7, 7, 3, 0, 10), CodeThresholdNotMet},
		{"stale evidence", run, ring, counts(9, 5, 1, 0, 10), CodeEvidenceNotFresh},
		{"coverage below minimum", run, DeploymentRing{SuccessThresholdPercent: 80, SoakMinutes: 60, MinFreshEvidencePercent: &min90}, counts(9, 9, 1, 0, 8), CodeEvidenceNotFresh},
		{"fresh and enough", run, ring, counts(9, 9, 1, 0, 10), ""},
		{"exactly at threshold", run, ring, counts(8, 8, 2, 0, 10), ""},
	}
	for _, tc := range cases {
		if got := evidenceCode(tc.run, tc.ring, tc.c, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// already_satisfied, not_applicable and cancelled stay out of the denominator.
	c := counts(8, 8, 2, 0, 10)
	if c.Denominator() != 10 || c.Total() != 14 {
		t.Fatalf("denominator %d total %d", c.Denominator(), c.Total())
	}
	if r := ringRate(RingCounts{ByState: map[string]int{TargetAlreadySatisfied: 2}}); r != nil {
		t.Fatalf("rate without denominator: %v", *r)
	}
}

func TestDeploymentStatusSetsAreConsistent(t *testing.T) {
	for _, s := range []string{DeploymentPendingApproval, DeploymentApproved, DeploymentScheduled, DeploymentResolvingTargets, DeploymentReady, DeploymentRunning, DeploymentPaused} {
		found := false
		for _, b := range DeploymentBindingStatuses {
			found = found || b == s
		}
		if !found {
			t.Errorf("non-terminal status %s is not a binding status", s)
		}
	}
	for _, s := range []string{DeploymentCompleted, DeploymentCompletedWithError, DeploymentFailed, DeploymentCancelled, DeploymentDraft} {
		for _, b := range DeploymentBindingStatuses {
			if b == s {
				t.Errorf("terminal or draft status %s binds target sets", s)
			}
		}
	}
	if got := nextGate(DeploymentRingRun{Status: RingHalted}, DeploymentRing{}, RingCounts{}, true, time.Now()); got != "resume" {
		t.Errorf("halted gate %q", got)
	}
}

func TestNoEvidenceIsNeverSuccess(t *testing.T) {
	now := time.Now()
	settled := now.Add(-2 * time.Hour)
	run := DeploymentRingRun{SettledAt: &settled}
	ring := DeploymentRing{SuccessThresholdPercent: 80, SoakMinutes: 60}
	// Every target was satisfied or not applicable: denominator 0 (the threshold arithmetic alone would pass 0 >= 0).
	none := RingCounts{ByState: map[string]int{TargetAlreadySatisfied: 4, TargetNotApplicable: 1}}
	if got := evidenceCode(run, ring, none, now); got != CodeNoEvidence {
		t.Fatalf("den=0: %q", got)
	}
	cases := []struct {
		den, na, want int
	}{{0, 0, 1}, {1, 0, 1}, {2, 0, 2}, {3, 0, 3}, {40, 0, MinAutoHaltSample}, {1, 2, 3}}
	for _, c := range cases {
		rc := RingCounts{ByState: map[string]int{TargetSuccessful: c.den}, ProviderNA: c.na}
		if got := rc.MinEvidence(); got != c.want {
			t.Errorf("MinEvidence(den %d, na %d) = %d, want %d", c.den, c.na, got, c.want)
		}
	}
	// Two targets of two succeeded: a small ring needs all of its targets, not MinAutoHaltSample of them.
	small := RingCounts{ByState: map[string]int{TargetSuccessful: 2}, FreshSuccessful: 2, FreshObserved: 2}
	if got := evidenceCode(run, ring, small, now); got != "" {
		t.Fatalf("small ring: %q", got)
	}
}
