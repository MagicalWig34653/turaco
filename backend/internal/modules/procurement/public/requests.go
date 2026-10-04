package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
)

// Request is a Procurement Request (need) as other modules see it.
type Request struct {
	ID        string
	Reference string
	ProductID string
	Quantity  int
	Status    string
}

// ReadScope selects request fields the caller is authorized to receive. The
// zero value returns only id, reference and status.
type ReadScope struct{ IncludeDetails bool }

// Cancelled reports that the request was cancelled.
func (r Request) Cancelled() bool { return r.Status == application.NeedCancelled }

// Requests is the module's public read service for Procurement Requests.
type Requests struct{ svc *application.Service }

func NewRequests(svc *application.Service) *Requests { return &Requests{svc: svc} }

func scopedRequest(n application.Need, scope ReadScope) Request {
	item := Request{ID: n.ID, Reference: n.Reference, Status: n.Status}
	if scope.IncludeDetails {
		item.ProductID, item.Quantity = n.ProductID, n.Quantity
	}
	return item
}

// Lookup returns id -> request for the existing Procurement Requests among ids
// (at most 500). The caller authorizes details for its own user
// (procurement.view|manage) before setting IncludeDetails.
func (r *Requests) Lookup(ctx context.Context, ids []string, scope ReadScope) (map[string]Request, error) {
	found, err := r.svc.NeedsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Request, len(found))
	for id, n := range found {
		out[id] = scopedRequest(n, scope)
	}
	return out, nil
}
