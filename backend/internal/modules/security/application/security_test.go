package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

func TestEvaluateInstallation(t *testing.T) {
	id := "00000000-0000-7000-8000-000000000001"
	method := MethodProduct
	alias := MethodAlias
	platform := "windows"
	base := Criterion{SoftwareProductID: &id, OSPlatform: &platform, MatchMethod: &method,
		Rules: []Rule{{Kind: "introduced", Version: "1.0"}, {Kind: "fixed", Version: "2.0"}}}
	tests := []struct {
		name, version, product, os, want string
		alias                            bool
	}{
		{"inside range", "1.9", id, platform, ConfidenceProbable, false},
		{"fixed boundary", "2.0", id, platform, "", false},
		{"below introduced", "0.9", id, platform, "", false},
		{"unparseable", "2024 R2", id, platform, ConfidencePotential, false},
		{"alias", "1.9", id, platform, ConfidencePotential, true},
		{"other product", "1.9", "00000000-0000-7000-8000-000000000002", platform, "", false},
		{"other OS", "1.9", id, "linux", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			if tc.alias {
				c.MatchMethod = &alias
			}
			got, ok := evaluate([]Criterion{c}, Installation{SoftwareProductID: tc.product, DevicePlatform: tc.os, RawVersion: tc.version})
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("match = %q,%v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestSourceURLAndInputBounds(t *testing.T) {
	for _, raw := range []string{"http://example.test", "https://user:pass@example.test", "https://example.test/a b", "https://example.test/<script>", "https://example.test/`x`", strings.Repeat("x", maxURL+1)} {
		if _, err := CleanSourceURL(raw); err == nil {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
	if got, err := CleanSourceURL("https://example.test/advisory?id=1"); err != nil || got == nil {
		t.Fatalf("valid https URL: %v", err)
	}
	if _, err := cleanAdvisory(AdvisoryInput{Source: "vendor", ExternalID: "123", Title: strings.Repeat("x", maxTitle+1), Severity: "high"}); err == nil {
		t.Fatal("accepted oversized title")
	}
	if _, err := cleanCriteria(make([]CriterionInput, MaxCriteria+1)); err == nil {
		t.Fatal("accepted oversized criteria")
	}
}

func TestMutationAuthorizationAndRequiredVersion(t *testing.T) {
	s := NewService(nil, nil)
	c := Caller{Actor: audit.UserActor("00000000-0000-7000-8000-000000000001"), CorrelationID: "test"}
	if _, err := s.AcceptRisk(context.Background(), c, Principal{Manage: true}, "", nil, "business_need", time.Now().AddDate(0, 1, 0)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("security.manage must not accept risk: %v", err)
	}
	if _, err := s.AcceptRisk(context.Background(), c, Principal{AcceptRisk: true, UserID: c.Actor.UserID}, "", nil, "business_need", time.Now().AddDate(0, 1, 0)); err == nil || !strings.Contains(err.Error(), "expectedVersion") {
		t.Fatalf("accept risk without expectedVersion: %v", err)
	}
	if _, err := s.StartAnalysis(context.Background(), c, Principal{Manage: true}, "", nil); err == nil || !strings.Contains(err.Error(), "expectedVersion") {
		t.Fatalf("advisory transition without expectedVersion: %v", err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	s.WithClock(func() time.Time { return now })
	version := 1
	for _, date := range []time.Time{now, now.AddDate(1, 0, 1)} {
		if _, err := s.AcceptRisk(context.Background(), c, Principal{AcceptRisk: true, UserID: c.Actor.UserID}, "", &version, "business_need", date); err == nil || !strings.Contains(err.Error(), "reviewBy") {
			t.Errorf("invalid review date %s: %v", date, err)
		}
	}
	if _, err := s.AcceptRisk(context.Background(), c, Principal{AcceptRisk: true, UserID: c.Actor.UserID}, "", &version, "unknown_reason", now.AddDate(0, 1, 0)); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("unknown risk reason: %v", err)
	}
	if _, err := s.MarkFalsePositive(context.Background(), c, Principal{Manage: true}, "", &version, "configuration_not_affected"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("configuration exception without accept_risk = %v", err)
	}
	if _, err := s.MarkNotApplicable(context.Background(), c, Principal{Manage: true}, "", &version, "other"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("not applicable other without accept_risk = %v", err)
	}
}

func TestImportBoundsAndCVEKeyNormalization(t *testing.T) {
	a, err := cleanAdvisory(AdvisoryInput{Source: "vendor", ExternalID: " cve-2026-1234 ", Title: "Bulletin", Severity: "high"})
	if err != nil || a.ExternalID == nil || *a.ExternalID != "CVE-2026-1234" {
		t.Fatalf("normalized key = %+v, %v", a.ExternalID, err)
	}
	s := NewService(nil, nil)
	caller := Caller{Actor: audit.UserActor("00000000-0000-7000-8000-000000000001"), CorrelationID: "test"}
	records := make([]AdvisoryInput, MaxImportCriteria/MaxCriteria+1)
	for i := range records {
		records[i].Criteria = make([]CriterionInput, MaxCriteria)
	}
	if _, err := s.Import(context.Background(), caller, Principal{Manage: true}, records); err == nil || !strings.Contains(err.Error(), "criteria") {
		t.Fatalf("import criteria cap = %v", err)
	}
}

func TestHugePrereleaseIdentifierOrder(t *testing.T) {
	a, okA := ParseVersion("1.0-99999999999999999999999999999999999999")
	b, okB := ParseVersion("1.0-100000000000000000000000000000000000000")
	if !okA || !okB || a.Compare(b) >= 0 || b.Compare(a) <= 0 {
		t.Fatal("large numeric prerelease identifiers must compare exactly")
	}
}
