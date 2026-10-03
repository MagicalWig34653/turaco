// Package intune is the adapter boundary to Microsoft Intune (docs/integrations/intune.md).
//
// Only the internal side exists so far: the normalized record types and the Provider contract the
// Endpoints module ingests from, an in-memory Fake for tests and local development, and a placeholder
// that reports "not configured". The Microsoft Graph client (authentication, paging, delta, throttling)
// needs a tenant and an app registration to be built and verified against, which this repository does
// not have. Graph DTOs never cross this boundary: providers return the normalized records below.
package intune

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ProviderKey is the name under which Intune devices are stored.
const ProviderKey = "intune"

// ErrNotConfigured is returned while no Graph client exists or is configured.
var ErrNotConfigured = errors.New("intune: the Graph client is not configured (not implemented yet)")

// DeviceRecord is one managed device as reported by the provider, already normalized: OSPlatform is
// one of windows, macos, ios, android, linux, other; Ownership one of corporate, personal, unknown;
// ComplianceState one of compliant, noncompliant, in_grace_period, unknown. Anything else is treated
// as other/unknown by the consumer.
type DeviceRecord struct {
	// ExternalID is the provider's stable device id.
	ExternalID      string
	Name            string
	SerialNumber    string
	OSPlatform      string
	OSVersion       string
	Manufacturer    string
	Model           string
	Ownership       string
	ComplianceState string
	LastCheckinAt   *time.Time
}

// SoftwareRecord is one application installation discovered on a device.
type SoftwareRecord struct {
	Name      string
	Version   string
	Publisher string
}

// Provider reads a complete snapshot of devices and their discovered software.
type Provider interface {
	// Devices returns every managed device.
	Devices(ctx context.Context) ([]DeviceRecord, error)
	// Software returns the applications discovered on one device.
	Software(ctx context.Context, externalDeviceID string) ([]SoftwareRecord, error)
}

// NotConfigured is the placeholder provider: every call fails with ErrNotConfigured.
type NotConfigured struct{}

func (NotConfigured) Devices(context.Context) ([]DeviceRecord, error) {
	return nil, ErrNotConfigured
}

func (NotConfigured) Software(context.Context, string) ([]SoftwareRecord, error) {
	return nil, ErrNotConfigured
}

// Fake is an in-memory provider for tests and local development.
type Fake struct {
	mu       sync.Mutex
	devices  []DeviceRecord
	software map[string][]SoftwareRecord
	err      error
}

func NewFake() *Fake { return &Fake{software: map[string][]SoftwareRecord{}} }

// SetDevices replaces the reported devices.
func (f *Fake) SetDevices(d ...DeviceRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = append([]DeviceRecord(nil), d...)
}

// SetSoftware replaces the software reported for a device.
func (f *Fake) SetSoftware(externalDeviceID string, s ...SoftwareRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.software[externalDeviceID] = append([]SoftwareRecord(nil), s...)
}

// FailWith makes every following call fail with err; nil restores normal behavior.
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *Fake) Devices(context.Context) ([]DeviceRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]DeviceRecord(nil), f.devices...), nil
}

func (f *Fake) Software(_ context.Context, id string) ([]SoftwareRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]SoftwareRecord(nil), f.software[id]...), nil
}
