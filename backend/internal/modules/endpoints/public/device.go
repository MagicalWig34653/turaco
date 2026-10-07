package public

import (
	"context"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
)

// DeviceInfo is the identity and last observation of one Device, for Remote Access. It carries no provider data,
// compliance state or software.
type DeviceInfo struct {
	ID   string
	Name string
	// AssetID is the canonical Asset the Device is linked to (nil when unlinked).
	AssetID *string
	// Ownership is corporate, personal or unknown (as the management provider reports it).
	Ownership string
	// ObservedAt is when the management provider last reported the Device to Turaco.
	ObservedAt    time.Time
	LastCheckinAt *time.Time
	// RetiredAt is set when a later complete snapshot no longer contained the Device.
	RetiredAt *time.Time
}

// DeviceReader is what the contract reads (implemented by the endpoints repository).
type DeviceReader interface {
	DeviceIdentity(ctx context.Context, id string) (*application.DeviceIdentityRow, error)
}

// Devices is the Endpoints module's public Device identity read service.
type Devices struct{ r DeviceReader }

func NewDevices(r DeviceReader) *Devices { return &Devices{r: r} }

// Device returns the Device; found is false for unknown ids. The caller decides who may see it.
func (d *Devices) Device(ctx context.Context, id string) (info DeviceInfo, found bool, err error) {
	row, err := d.r.DeviceIdentity(ctx, id)
	if err != nil || row == nil {
		return DeviceInfo{}, false, err
	}
	return DeviceInfo{ID: row.ID, Name: row.Name, AssetID: row.AssetID, Ownership: row.Ownership, ObservedAt: row.ObservedAt,
		LastCheckinAt: row.LastCheckinAt, RetiredAt: row.RetiredAt}, true, nil
}
