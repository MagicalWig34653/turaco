package intune_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
)

func TestFakeReportsConfiguredSnapshot(t *testing.T) {
	var p intune.Provider = intune.NewFake()
	f := p.(*intune.Fake)
	f.SetDevices(intune.DeviceRecord{ExternalID: "d1", Name: "PC-1"})
	f.SetSoftware("d1", intune.SoftwareRecord{Name: "7-Zip", Version: "23.1"})
	devs, err := p.Devices(context.Background())
	if err != nil || len(devs) != 1 || devs[0].ExternalID != "d1" {
		t.Fatalf("devices = %v, %v", devs, err)
	}
	sw, err := p.Software(context.Background(), "d1")
	if err != nil || len(sw) != 1 || sw[0].Name != "7-Zip" {
		t.Fatalf("software = %v, %v", sw, err)
	}
	f.FailWith(errors.New("boom"))
	if _, err := p.Devices(context.Background()); err == nil {
		t.Error("expected failure")
	}
}

func TestNotConfiguredFails(t *testing.T) {
	if _, err := (intune.NotConfigured{}).Devices(context.Background()); !errors.Is(err, intune.ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if _, err := (intune.NotConfigured{}).Software(context.Background(), "x"); !errors.Is(err, intune.ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
}

func TestFakeReportsManagementData(t *testing.T) {
	f := intune.NewFake()
	snap := intune.ManagementSnapshot{
		Artifacts: []intune.ArtifactRecord{{ExternalID: "a1", Kind: "application", Name: "App", AssignmentsKnown: true,
			Assignments: []intune.AssignmentRecord{{ProviderAssignmentID: "x", TargetKind: "all_users", Mode: "include"}}}},
		Observations: []intune.ObservationRecord{{ExternalDeviceID: "d1", ArtifactExternalID: "a1", State: "applied"}},
	}
	f.SetManagement(snap)
	got, err := f.Management(context.Background())
	if err != nil || len(got.Artifacts) != 1 || len(got.Observations) != 1 {
		t.Fatalf("management = %+v, %v", got, err)
	}
	// The caller's copy is independent of the fake's state.
	got.Artifacts[0].Assignments[0].Mode = "exclude"
	again, _ := f.Management(context.Background())
	if again.Artifacts[0].Assignments[0].Mode != "include" {
		t.Error("the fake leaked its internal slices")
	}
	f.FailManagementWith(errors.New("boom"))
	if _, err := f.Management(context.Background()); err == nil {
		t.Error("expected failure")
	}
	if _, err := f.Devices(context.Background()); err != nil {
		t.Errorf("a management failure must not break devices: %v", err)
	}
}

func TestNotConfiguredManagementFails(t *testing.T) {
	if _, err := (intune.NotConfigured{}).Management(context.Background()); !errors.Is(err, intune.ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
}
