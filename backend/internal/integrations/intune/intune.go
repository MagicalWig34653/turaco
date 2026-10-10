// Package intune is the adapter boundary to Microsoft Intune (docs/integrations/intune.md).
//
// It holds the normalized record types and the Provider contract the Endpoints module ingests from, an in-memory
// Fake for tests and local development, the "not configured" placeholder, and the Microsoft Graph clients: the read
// provider (graph_provider.go) and the assignment writer (graph_writer.go). Both are written strictly from the
// vendor documentation and unverified against a live tenant. Graph DTOs never cross this boundary: providers
// return the normalized records below.
package intune

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ProviderKey is the name under which Intune devices are stored.
const ProviderKey = "intune"

// ErrNotConfigured is returned while the Graph read registration is not configured (MICROSOFT_GRAPH_*).
var ErrNotConfigured = errors.New("intune: the Graph client is not configured")

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

// FilterRecord is one assignment filter. Platform is one of windows, macos, ios, android, linux, other.
// Rule is the provider's rule expression as reported; Turaco displays it and evaluates only a small
// deterministic subset later, never executes it.
type FilterRecord struct {
	ExternalID string
	Name       string
	Platform   string
	Rule       string
	// Revision is the provider's etag or version where it reports one.
	Revision string
}

// AssignmentRecord is one assignment of an artifact, already normalized: TargetKind is one of group,
// all_devices, all_users; Mode include or exclude; Intent required, available, uninstall or none;
// FilterMode include, exclude or none (empty means none). TargetGroupExternalID is the provider's
// external id of the Directory Group (group targets only); FilterExternalID refers to a FilterRecord.
type AssignmentRecord struct {
	ProviderAssignmentID  string
	TargetKind            string
	TargetGroupExternalID string
	Mode                  string
	Intent                string
	FilterExternalID      string
	FilterMode            string
}

// ArtifactRecord is one assignable management object. Kind is one of application,
// configuration_profile, compliance_policy, endpoint_security_policy, script, remediation; Platform one
// of the device platforms. AssignmentsKnown is false when the assignments could not be read: the
// previous assignments are then left untouched.
type ArtifactRecord struct {
	ExternalID       string
	Kind             string
	Name             string
	Platform         string
	Revision         string
	AssignmentsKnown bool
	Assignments      []AssignmentRecord
}

// ObservationRecord is the provider's result for one artifact on one device. State is one of applied,
// pending, failed, conflict, not_applicable, unknown (anything else is treated as unknown); RawStatus
// is the provider's own status text, kept for troubleshooting.
type ObservationRecord struct {
	ExternalDeviceID   string
	ArtifactExternalID string
	RawStatus          string
	State              string
	ObservedAt         time.Time
}

// DeviceGroupMembershipRecord states that a device is a member of a provider group.
type DeviceGroupMembershipRecord struct {
	ExternalDeviceID string
	GroupExternalID  string
}

// ManagementSnapshot is the provider's management data: filters, artifacts with their assignments,
// observations and device group memberships.
type ManagementSnapshot struct {
	Filters      []FilterRecord
	Artifacts    []ArtifactRecord
	Observations []ObservationRecord
	Memberships  []DeviceGroupMembershipRecord
}

// Provider reads a complete snapshot of devices and their discovered software, and the management data.
type Provider interface {
	// Devices returns every managed device.
	Devices(ctx context.Context) ([]DeviceRecord, error)
	// Software returns the applications discovered on one device.
	Software(ctx context.Context, externalDeviceID string) ([]SoftwareRecord, error)
	// Management returns every filter, artifact (with assignments), observation and device group
	// membership the provider reports.
	Management(ctx context.Context) (ManagementSnapshot, error)
}

// NotConfigured is the placeholder provider: every call fails with ErrNotConfigured.
type NotConfigured struct{}

func (NotConfigured) Devices(context.Context) ([]DeviceRecord, error) {
	return nil, ErrNotConfigured
}

func (NotConfigured) Software(context.Context, string) ([]SoftwareRecord, error) {
	return nil, ErrNotConfigured
}

func (NotConfigured) Management(context.Context) (ManagementSnapshot, error) {
	return ManagementSnapshot{}, ErrNotConfigured
}

// Fake is an in-memory provider for tests and local development.
type Fake struct {
	mu       sync.Mutex
	devices  []DeviceRecord
	software map[string][]SoftwareRecord
	mgmt     ManagementSnapshot
	err      error
	mgmtErr  error
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

// SetManagement replaces the reported management data.
func (f *Fake) SetManagement(m ManagementSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mgmt = cloneManagement(m)
}

// FailManagementWith makes only Management fail with err; nil restores normal behavior.
func (f *Fake) FailManagementWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mgmtErr = err
}

func (f *Fake) Management(context.Context) (ManagementSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return ManagementSnapshot{}, f.err
	}
	if f.mgmtErr != nil {
		return ManagementSnapshot{}, f.mgmtErr
	}
	return cloneManagement(f.mgmt), nil
}

func cloneManagement(m ManagementSnapshot) ManagementSnapshot {
	out := ManagementSnapshot{
		Filters:      append([]FilterRecord(nil), m.Filters...),
		Observations: append([]ObservationRecord(nil), m.Observations...),
		Memberships:  append([]DeviceGroupMembershipRecord(nil), m.Memberships...),
		Artifacts:    make([]ArtifactRecord, len(m.Artifacts)),
	}
	for i, a := range m.Artifacts {
		a.Assignments = append([]AssignmentRecord(nil), a.Assignments...)
		out.Artifacts[i] = a
	}
	return out
}
