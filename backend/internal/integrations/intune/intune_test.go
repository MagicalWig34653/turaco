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
