package public

import (
	"context"
	"errors"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/search"
)

// DeviceSearcher is the platform Searcher of managed Devices. It runs the device list search as the caller
// (endpoints.view or endpoints.manage): the name or serial number starts with the text, like the device list.
type DeviceSearcher struct{ svc *application.Service }

// NewDeviceSearcher builds the Searcher.
func NewDeviceSearcher(svc *application.Service) DeviceSearcher { return DeviceSearcher{svc: svc} }

func (DeviceSearcher) Type() string   { return "device" }
func (DeviceSearcher) Module() string { return "endpoints" }

func (s DeviceSearcher) Search(ctx context.Context, p authorization.Principal, q string, limit int) ([]search.Hit, error) {
	res, err := s.svc.ListDevices(ctx, application.Principal{UserID: p.UserID, View: p.Has("endpoints.view"), Manage: p.Has("endpoints.manage")},
		application.DeviceFilter{Query: q, Page: application.Page{Limit: limit}})
	switch {
	case errors.Is(err, application.ErrForbidden):
		return nil, search.ErrNotAllowed
	case err != nil:
		return nil, err
	}
	hits := make([]search.Hit, 0, len(res.Items))
	for _, d := range res.Items {
		h := search.Hit{ID: d.ID, Title: d.Name}
		if d.SerialNumber != nil {
			h.Subtitle = *d.SerialNumber
		}
		hits = append(hits, h)
	}
	return hits, nil
}
