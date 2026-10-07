package application

import "time"

// Inventory reads for other modules (endpoints/public): the Security module matches advisories against
// observed software installations. The rows carry provider-observed data with its freshness; callers
// decide what they may show.

// InstallationRow is one observed software installation with its Device.
type InstallationRow struct {
	ID                string
	DeviceID          string
	DeviceName        string
	DevicePlatform    string
	SoftwareProductID string
	RawVersion        string
	ObservedAt        time.Time
	// RetiredAt is set when a later snapshot no longer reported the installation.
	RetiredAt *time.Time
	// DeviceRetiredAt is set when the Device was tombstoned.
	DeviceRetiredAt *time.Time
}

// SoftwareProductRow is a normalized Software Product.
type SoftwareProductRow struct {
	ID        string
	Name      string
	Publisher *string
}

// DeviceIdentityRow is the identity and last observation of one Device.
type DeviceIdentityRow struct {
	ID            string
	Name          string
	AssetID       *string
	Ownership     string
	ObservedAt    time.Time
	LastCheckinAt *time.Time
	RetiredAt     *time.Time
}
