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

// Cancelled reports that the request was cancelled.
func (r Request) Cancelled() bool { return r.Status == application.NeedCancelled }

// Requests is the module's public read service for Procurement Requests.
type Requests struct{ svc *application.Service }

func NewRequests(svc *application.Service) *Requests { return &Requests{svc: svc} }

// Lookup returns id -> request for the existing Procurement Requests among ids
// (at most 500). It performs no permission check; the caller authorizes its own
// user (procurement.view|manage) first.
func (r *Requests) Lookup(ctx context.Context, ids []string) (map[string]Request, error) {
	found, err := r.svc.NeedsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Request, len(found))
	for id, n := range found {
		out[id] = Request{ID: n.ID, Reference: n.Reference, ProductID: n.ProductID, Quantity: n.Quantity, Status: n.Status}
	}
	return out, nil
}
