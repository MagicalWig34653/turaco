package public

import (
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

func TestZeroReadScopeRedactsChangeDetails(t *testing.T) {
	owner := "owner"
	c := application.Change{ID: "id", Reference: "CHG-1", Title: "secret", Kind: "normal", Risk: "high",
		Status: "scheduled", RequesterID: "requester", OwnerID: &owner}
	got := scopedInfo(c, ReadScope{})
	if got.ID != c.ID || got.Reference != c.Reference || got.Status != c.Status ||
		got.Title != "" || got.Kind != "" || got.Risk != "" || got.RequesterID != "" || got.OwnerID != nil {
		t.Fatalf("zero scope leaked change details: %+v", got)
	}
	full := scopedInfo(c, ReadScope{IncludeDetails: true})
	if full.Title != c.Title || full.Kind != c.Kind || full.Risk != c.Risk || full.OwnerID == nil {
		t.Fatalf("explicit scope lost change details: %+v", full)
	}
}

func TestZeroReadScopeRedactsCalendarAffectedIDs(t *testing.T) {
	e := application.CalendarEntry{Change: application.Change{ID: "id", Reference: "CHG-1", Title: "secret"},
		Affected: []relationships.Node{{Type: "service", ID: "service-id"}}}
	got := scopedCalendarEntry(e, ReadScope{})
	if got.Change.Title != "" || len(got.Affected) != 0 {
		t.Fatalf("zero scope leaked calendar details: %+v", got)
	}
	full := scopedCalendarEntry(e, ReadScope{IncludeDetails: true, IncludeAffectedIDs: true})
	if full.Change.Title != "secret" || len(full.Affected) != 1 || full.Affected[0].ID != "service-id" {
		t.Fatalf("explicit scope lost calendar details: %+v", full)
	}
}
