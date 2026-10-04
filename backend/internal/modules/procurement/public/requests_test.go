package public

import (
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
)

func TestZeroReadScopeRedactsRequestDetails(t *testing.T) {
	n := application.Need{ID: "id", Reference: "PRQ-1", ProductID: "product", Quantity: 8, Status: "approved"}
	got := scopedRequest(n, ReadScope{})
	if got.ID != n.ID || got.Reference != n.Reference || got.Status != n.Status || got.ProductID != "" || got.Quantity != 0 {
		t.Fatalf("zero scope leaked request details: %+v", got)
	}
	full := scopedRequest(n, ReadScope{IncludeDetails: true})
	if full.ProductID != n.ProductID || full.Quantity != n.Quantity {
		t.Fatalf("explicit scope lost request details: %+v", full)
	}
}
