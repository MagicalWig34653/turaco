package application

import (
	"strings"
	"testing"
)

func TestDetectClusters(t *testing.T) {
	g := func(dim, val string, failed, total int) FailureGroup {
		return FailureGroup{Dimension: dim, Key: val, Value: val, Failed: failed, Total: total}
	}
	tests := []struct {
		name          string
		failures, all int
		groups        []FailureGroup
		want          []string
	}{
		{"nothing", 0, 10, nil, nil},
		{"two failures are never a cluster", 2, 10, []FailureGroup{g(DimensionErrorCode, "x", 2, 0)}, nil},
		{"three of ten failures: 30% of all failures", 10, 100, []FailureGroup{g(DimensionErrorCode, "0x1", 3, 0)}, []string{"error_code:0x1"}},
		{"three of twenty failures is 15%, below the share", 20, 100, []FailureGroup{g(DimensionErrorCode, "0x1", 3, 0)}, nil},
		{"exactly 20% of the failures", 15, 100, []FailureGroup{g(DimensionOSVersion, "10.0.1", 3, 40)}, []string{"os_version:10.0.1"}},
		{"half of its own group fails although the share is small", 30, 200, []FailureGroup{g(DimensionModel, "X1", 4, 8)}, []string{"model:X1"}},
		{"just under half of the group and a small share", 30, 200, []FailureGroup{g(DimensionModel, "X1", 3, 7)}, nil},
		{"a group that spans every target says nothing", 5, 10, []FailureGroup{g(DimensionManufacturer, "Dell", 5, 10)}, nil},
		{"the error code has no group and is kept even for all failures", 5, 10, []FailureGroup{g(DimensionErrorCode, "0x2", 5, 0)}, []string{"error_code:0x2"}},
		{"largest first, ties by dimension and key", 20, 100, []FailureGroup{
			g(DimensionRing, "r2", 4, 10), g(DimensionModel, "M", 6, 10), g(DimensionErrorCode, "e", 4, 0)},
			[]string{"model:M", "error_code:e", "ring:r2"}},
		{"empty values are skipped", 10, 100, []FailureGroup{g(DimensionModel, "", 5, 6)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range DetectClusters(tt.failures, tt.all, tt.groups) {
				got = append(got, c.FindingKey())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectClustersIsBounded(t *testing.T) {
	var groups []FailureGroup
	for i := 0; i < 30; i++ {
		v := string(rune('a' + i%26))
		groups = append(groups, FailureGroup{Dimension: DimensionErrorCode, Key: v + string(rune('0'+i/26)), Value: v, Failed: 5, Total: 0})
	}
	if got := DetectClusters(20, 100, groups); len(got) != MaxClustersPerDeployment {
		t.Fatalf("clusters = %d, want %d", len(got), MaxClustersPerDeployment)
	}
}

func TestCleanClusterValue(t *testing.T) {
	if got := cleanClusterValue("  0x87D1\x00\n041C \t x "); got != "0x87D1 041C x" {
		t.Fatalf("got %q", got)
	}
	if got := cleanClusterValue(strings.Repeat("ä", 300)); len([]rune(got)) != maxClusterValueRunes {
		t.Fatalf("length %d", len([]rune(got)))
	}
	groups := cleanGroups([]FailureGroup{{Dimension: DimensionModel, Key: " X ", Value: " X ", Failed: 3}, {Dimension: DimensionModel, Value: "  "},
		{Dimension: DimensionRing, Key: "ring-id", Value: "Pilot", Failed: 3}})
	if len(groups) != 2 || groups[0].Key != "X" || groups[1].Key != "ring-id" {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestSuccessRate(t *testing.T) {
	f := func(p *float64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	tests := []struct {
		s, f, e int
		want    any
	}{{0, 0, 0, nil}, {1, 0, 0, 100.0}, {2, 4, 0, 33.3}, {2, 1, 1, 50.0}, {0, 3, 0, 0.0}, {2, 0, 1, 66.7}, {1, 6, 0, 14.3}}
	for _, tt := range tests {
		if got := f(SuccessRate(tt.s, tt.f, tt.e)); got != tt.want {
			t.Errorf("SuccessRate(%d,%d,%d) = %v, want %v", tt.s, tt.f, tt.e, got, tt.want)
		}
	}
	if SuccessRate(-1, 2, 0) != nil {
		t.Error("negative counts must give no rate")
	}
}

func TestFollowUpTitleAndAttention(t *testing.T) {
	if got := followUpTitle("DEP-000007", ReasonFailureThreshold); got != "Deployment DEP-000007: ring halted, too many failures" {
		t.Fatalf("title %q", got)
	}
	if got := followUpTitle("DEP-000007", "something_new"); got != "Deployment DEP-000007: needs attention" {
		t.Fatalf("generic title %q", got)
	}
	for _, r := range []string{"", ReasonManualPause, ReasonManualHalt, ReasonRingHalted, ReasonDeploymentCancel} {
		if attentionReason(r) {
			t.Errorf("%q must not need follow-up", r)
		}
	}
	for _, r := range []string{ReasonFailureThreshold, ReasonVersionRevoked, ReasonWindowClosed, ReasonNoTargets, FollowUpReasonCluster} {
		if !attentionReason(r) {
			t.Errorf("%q must need follow-up", r)
		}
	}
	// Every title is free of anything but the reference and fixed words.
	for reason, phrase := range followUpTitles {
		if strings.ContainsAny(phrase, "{}%") || phrase == "" {
			t.Errorf("title of %s: %q", reason, phrase)
		}
	}
}

func TestGateOf(t *testing.T) {
	reason := ReasonFailureThreshold
	if g, ok := gateOf(ReportRingTransition{To: RingHalted, Reason: &reason}); !ok || g.Passed || g.Gate != ReasonFailureThreshold {
		t.Fatalf("halt gate %+v", g)
	}
	if g, ok := gateOf(ReportRingTransition{To: RingActive, Operation: "settled"}); !ok || !g.Passed || g.Gate != "success_threshold" {
		t.Fatalf("settled gate %+v", g)
	}
	if _, ok := gateOf(ReportRingTransition{To: RingActive, Operation: "activated"}); ok {
		t.Fatal("activation is not a gate")
	}
}
