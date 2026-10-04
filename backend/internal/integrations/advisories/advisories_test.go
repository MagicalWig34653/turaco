package advisories

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNotConfigured(t *testing.T) {
	if _, err := (NotConfigured{}).Advisories(context.Background(), time.Time{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeFiltersAndCopies(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.AddDate(0, 6, 0)
	f := NewFake()
	f.Set(
		AdvisoryRecord{Source: "nvd", ExternalID: "CVE-1", ModifiedAt: &old, Criteria: []Criteria{{ProductName: "a", Rules: []VersionRule{{Kind: RuleFixed, Version: "1.0"}}}}},
		AdvisoryRecord{Source: "nvd", ExternalID: "CVE-2", PublishedAt: &recent},
		AdvisoryRecord{Source: "nvd", ExternalID: "CVE-3"},
	)
	got, err := f.Advisories(context.Background(), old.AddDate(0, 1, 0))
	if err != nil || len(got) != 2 || got[0].ExternalID != "CVE-2" || got[1].ExternalID != "CVE-3" {
		t.Fatalf("since filter = %+v %v", got, err)
	}
	all, _ := f.Advisories(context.Background(), time.Time{})
	all[0].Criteria[0].Rules[0].Version = "changed"
	again, _ := f.Advisories(context.Background(), time.Time{})
	if again[0].Criteria[0].Rules[0].Version != "1.0" {
		t.Fatal("the fake must return copies")
	}
	f.FailWith(errors.New("down"))
	if _, err := f.Advisories(context.Background(), time.Time{}); err == nil {
		t.Fatal("FailWith must fail")
	}
}
