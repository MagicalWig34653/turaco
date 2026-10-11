package application

import (
	"errors"
	"testing"
)

func TestAssetTechnicalFieldsRequireReadPermission(t *testing.T) {
	s := NewService(nil, nil, nil, nil)
	if _, err := s.QueryFields(Principal{UserID: "plain"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("fields without permission: %v", err)
	}
	if _, err := s.QueryFields(Principal{UserID: "reader", View: true}); err != nil {
		t.Fatalf("reader fields: %v", err)
	}
	for _, key := range []string{"firmware_version", "firmware_source", "firmware_observed_at", "last_calibrated_at", "next_calibration_due", "next_maintenance_due"} {
		if !AssetCatalog().Uses(key) {
			t.Errorf("missing field %s", key)
		}
	}
}
