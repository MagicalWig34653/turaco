package intune

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Management Assignment Writer (F9 G3, ADR-0027 decision P5). Turaco is the only writer of the assignments it
// orchestrates: each Deployment Ring maps to one Turaco-owned provider group that holds the ring's target Devices
// and one assignment of the published Management Artifact to that group. The writer only sets and clears that
// ring assignment; it never reads, edits other assignments or runs commands on Devices. A write counts as
// Assigned in Turaco only after the normal management synchronization reads it back.
//
// The real Microsoft Graph writer (graph_writer.go) needs its own app registration with write permissions and a
// separate secret (MICROSOFT_GRAPH_WRITE_*); NotConfiguredWriter is the wiring without it.
// Every call is idempotent per OperationID: a retry returns the result of the first call and writes nothing twice.

// MaxRingDevices bounds the Devices of one ring assignment.
const MaxRingDevices = 5000

// Errors a writer returns. ErrTransient may be retried with a new attempt; ErrPermanent must not.
var (
	ErrWriterNotConfigured = errors.New("intune: the assignment writer is not configured")
	ErrTransient           = errors.New("intune: transient write error")
	ErrPermanent           = errors.New("intune: permanent write error")
	ErrArtifactUnknown     = fmt.Errorf("%w: the management artifact is not known to the provider", ErrPermanent)
)

// RingAssignmentOp is one idempotent request to assign a Management Artifact to a ring's Turaco-owned group.
// DeviceExternalIDs are the provider device ids that must be members of the group (the ring's snapshot).
type RingAssignmentOp struct {
	OperationID                  string
	ManagementArtifactExternalID string
	RingKey                      string
	TargetGroupExternalID        string
	// Intent is required (install/update) or uninstall.
	Intent            string
	DeviceExternalIDs []string
}

// Validate checks the request shape.
func (op RingAssignmentOp) Validate() error {
	switch {
	case op.OperationID == "" || len(op.OperationID) > 200:
		return fmt.Errorf("%w: operation id", ErrPermanent)
	case op.ManagementArtifactExternalID == "" || op.RingKey == "" || op.TargetGroupExternalID == "":
		return fmt.Errorf("%w: artifact, ring key and group are required", ErrPermanent)
	case op.Intent != "required" && op.Intent != "uninstall":
		return fmt.Errorf("%w: intent must be required or uninstall", ErrPermanent)
	case len(op.DeviceExternalIDs) > MaxRingDevices:
		return fmt.Errorf("%w: too many devices", ErrPermanent)
	}
	return nil
}

// AssignmentWriter sets and clears the assignment of one ring. It is the only write port of the endpoint
// provider.
type AssignmentWriter interface {
	SetRingAssignment(ctx context.Context, op RingAssignmentOp) error
	ClearRingAssignment(ctx context.Context, operationID, managementArtifactExternalID, ringKey string) error
}

// IsTransient reports an error a later attempt may overcome.
func IsTransient(err error) bool {
	return errors.Is(err, ErrTransient) || errors.Is(err, context.DeadlineExceeded)
}

// NotConfiguredWriter is the production placeholder: every call fails permanently.
type NotConfiguredWriter struct{}

func (NotConfiguredWriter) SetRingAssignment(context.Context, RingAssignmentOp) error {
	return fmt.Errorf("%w: %w", ErrPermanent, ErrWriterNotConfigured)
}

func (NotConfiguredWriter) ClearRingAssignment(context.Context, string, string, string) error {
	return fmt.Errorf("%w: %w", ErrPermanent, ErrWriterNotConfigured)
}

// WriteCall is one recorded call of the FakeWriter.
type WriteCall struct {
	OperationID string
	Clear       bool
	Op          RingAssignmentOp
	RingKey     string
	Artifact    string
	// Applied is false for a replay of an operation id (nothing was written again).
	Applied bool
}

// FakeWriter applies ring assignments to a Fake provider's management snapshot, so the normal management
// ingestion reads them back. It records every call, can inject failures and latency and is idempotent per
// operation id. ApplyDelay is a number of Set calls to swallow before applying (an assignment that is not yet
// visible); with the default 0 the snapshot changes immediately.
type FakeWriter struct {
	mu       sync.Mutex
	provider *Fake
	done     map[string]error
	calls    []WriteCall
	failures []error
	latency  time.Duration
	hold     bool
	held     []RingAssignmentOp
}

// NewFakeWriter returns a writer that changes the snapshot of provider.
func NewFakeWriter(provider *Fake) *FakeWriter {
	return &FakeWriter{provider: provider, done: map[string]error{}}
}

// FailNext makes the next calls fail with the given errors, one per call (nil entries succeed).
func (w *FakeWriter) FailNext(errs ...error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures = append(w.failures, errs...)
}

// SetLatency delays every call.
func (w *FakeWriter) SetLatency(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.latency = d
}

// HoldApplication keeps accepted Set calls out of the snapshot until Release (the provider accepted the request
// but the assignment is not visible yet).
func (w *FakeWriter) HoldApplication(hold bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hold = hold
}

// Release applies the held assignments to the snapshot.
func (w *FakeWriter) Release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, op := range w.held {
		w.apply(op)
	}
	w.held = nil
}

// Calls returns the recorded calls.
func (w *FakeWriter) Calls() []WriteCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

// Applied counts the calls that wrote (replays excluded).
func (w *FakeWriter) Applied() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if c.Applied {
			n++
		}
	}
	return n
}

func (w *FakeWriter) pause(ctx context.Context) error {
	w.mu.Lock()
	d := w.latency
	w.mu.Unlock()
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *FakeWriter) nextFailure() error {
	if len(w.failures) == 0 {
		return nil
	}
	err := w.failures[0]
	w.failures = w.failures[1:]
	return err
}

func (w *FakeWriter) SetRingAssignment(ctx context.Context, op RingAssignmentOp) error {
	if err := op.Validate(); err != nil {
		return err
	}
	if err := w.pause(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err, ok := w.done[op.OperationID]; ok {
		w.calls = append(w.calls, WriteCall{OperationID: op.OperationID, Op: op})
		return err
	}
	if err := w.nextFailure(); err != nil {
		w.calls = append(w.calls, WriteCall{OperationID: op.OperationID, Op: op})
		if !IsTransient(err) {
			w.done[op.OperationID] = err
		}
		return err
	}
	w.calls = append(w.calls, WriteCall{OperationID: op.OperationID, Op: op, Applied: true})
	if !w.provider.hasArtifact(op.ManagementArtifactExternalID) {
		w.done[op.OperationID] = ErrArtifactUnknown
		return ErrArtifactUnknown
	}
	w.done[op.OperationID] = nil
	if w.hold {
		w.held = append(w.held, op)
		return nil
	}
	w.apply(op)
	return nil
}

func (w *FakeWriter) apply(op RingAssignmentOp) {
	w.provider.applyRing(op)
}

func (w *FakeWriter) ClearRingAssignment(ctx context.Context, operationID, artifact, ringKey string) error {
	if operationID == "" || artifact == "" || ringKey == "" {
		return fmt.Errorf("%w: operation id, artifact and ring key are required", ErrPermanent)
	}
	if err := w.pause(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err, ok := w.done[operationID]; ok {
		w.calls = append(w.calls, WriteCall{OperationID: operationID, Clear: true, RingKey: ringKey, Artifact: artifact})
		return err
	}
	if err := w.nextFailure(); err != nil {
		w.calls = append(w.calls, WriteCall{OperationID: operationID, Clear: true, RingKey: ringKey, Artifact: artifact})
		if !IsTransient(err) {
			w.done[operationID] = err
		}
		return err
	}
	w.calls = append(w.calls, WriteCall{OperationID: operationID, Clear: true, RingKey: ringKey, Artifact: artifact, Applied: true})
	w.done[operationID] = nil
	w.provider.clearRing(artifact, ringKey)
	return nil
}

// ringGroupPrefix marks the Turaco-owned ring groups of the Fake.
const ringGroupPrefix = "turaco-ring-"

func (f *Fake) hasArtifact(ext string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.mgmt.Artifacts {
		if a.ExternalID == ext {
			return true
		}
	}
	return false
}

// applyRing makes the artifact assigned to the ring's group and the devices members of it. Memberships of the
// group are replaced; assignments of other groups stay untouched. The device external ids are the provider's.
func (f *Fake) applyRing(op RingAssignmentOp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.mgmt.Artifacts {
		if a.ExternalID != op.ManagementArtifactExternalID {
			continue
		}
		id := "turaco-" + op.RingKey
		kept := a.Assignments[:0:0]
		for _, as := range a.Assignments {
			if as.ProviderAssignmentID != id {
				kept = append(kept, as)
			}
		}
		kept = append(kept, AssignmentRecord{ProviderAssignmentID: id, TargetKind: "group", TargetGroupExternalID: op.TargetGroupExternalID,
			Mode: "include", Intent: op.Intent})
		f.mgmt.Artifacts[i].Assignments = kept
		f.mgmt.Artifacts[i].AssignmentsKnown = true
	}
	members := f.mgmt.Memberships[:0:0]
	for _, m := range f.mgmt.Memberships {
		if m.GroupExternalID != op.TargetGroupExternalID {
			members = append(members, m)
		}
	}
	for _, d := range op.DeviceExternalIDs {
		members = append(members, DeviceGroupMembershipRecord{ExternalDeviceID: d, GroupExternalID: op.TargetGroupExternalID})
	}
	f.mgmt.Memberships = members
}

func (f *Fake) clearRing(artifact, ringKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "turaco-" + ringKey
	var group string
	for i, a := range f.mgmt.Artifacts {
		if a.ExternalID != artifact {
			continue
		}
		kept := a.Assignments[:0:0]
		for _, as := range a.Assignments {
			if as.ProviderAssignmentID == id {
				group = as.TargetGroupExternalID
				continue
			}
			kept = append(kept, as)
		}
		f.mgmt.Artifacts[i].Assignments = kept
	}
	if group == "" {
		return
	}
	members := f.mgmt.Memberships[:0:0]
	for _, m := range f.mgmt.Memberships {
		if m.GroupExternalID != group {
			members = append(members, m)
		}
	}
	f.mgmt.Memberships = members
}

// RingGroupID is the external id of the Turaco-owned group of a ring (ring ids are UUIDs).
func RingGroupID(ringID string) string { return ringGroupPrefix + ringID }
