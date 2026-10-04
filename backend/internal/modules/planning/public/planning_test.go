package public

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
)

func TestZeroMaintenanceScopeRedactsDetails(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Hour)
	owner := "owner"
	e := application.ChangeCalendarEntry{Change: application.ChangeInfo{ID: "id", Reference: "CHG-1", Title: "secret",
		Kind: "normal", Risk: "high", Status: "scheduled", RequesterID: "requester", OwnerID: &owner,
		WindowStart: &start, WindowEnd: &end}, Affected: []application.Node{{Type: "service", ID: "service-id"}}}
	got := scopedMaintenance(e, []string{"initiative-id"}, MaintenanceScope{})
	if got.Reference != "CHG-1" || got.Status != "scheduled" || got.Title != "" || got.Kind != "" ||
		got.Risk != "" || got.RequesterID != "" || got.OwnerID != nil || len(got.Affected) != 0 || len(got.InitiativeIDs) != 0 {
		t.Fatalf("zero scope leaked maintenance details: %+v", got)
	}
	full := scopedMaintenance(e, []string{"initiative-id"}, MaintenanceScope{IncludeChangeDetails: true, IncludeAffectedIDs: true, IncludeInitiativeIDs: true})
	if full.Title != "secret" || len(full.Affected) != 1 || len(full.InitiativeIDs) != 1 {
		t.Fatalf("explicit scope lost maintenance details: %+v", full)
	}
}

func TestDueMilestonesRequiresExplicitOwnerScope(t *testing.T) {
	x := &Planning{}
	for _, scope := range []DueMilestoneScope{{}, {OwnerID: "owner", AllOwners: true}} {
		if _, err := x.DueMilestones(context.Background(), time.Time{}, time.Time{}, scope); !errors.Is(err, ErrInvalidScope) {
			t.Fatalf("scope %+v: want ErrInvalidScope, got %v", scope, err)
		}
	}
}
