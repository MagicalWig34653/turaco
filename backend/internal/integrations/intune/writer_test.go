package intune

import (
	"context"
	"errors"
	"testing"
)

func TestFakeWriterAppliesAndIsIdempotent(t *testing.T) {
	p := NewFake()
	p.SetManagement(ManagementSnapshot{Artifacts: []ArtifactRecord{{ExternalID: "app1", Kind: "application", Name: "A", Platform: "windows", AssignmentsKnown: true}}})
	w := NewFakeWriter(p)
	op := RingAssignmentOp{OperationID: "d:r:1", ManagementArtifactExternalID: "app1", RingKey: "r", TargetGroupExternalID: RingGroupID("r"), Intent: "required", DeviceExternalIDs: []string{"dev1", "dev2"}}
	for i := 0; i < 2; i++ {
		if err := w.SetRingAssignment(context.Background(), op); err != nil {
			t.Fatal(err)
		}
	}
	if w.Applied() != 1 || len(w.Calls()) != 2 {
		t.Fatalf("applied %d calls %d", w.Applied(), len(w.Calls()))
	}
	m, _ := p.Management(context.Background())
	if len(m.Artifacts[0].Assignments) != 1 || len(m.Memberships) != 2 {
		t.Fatalf("snapshot %+v", m)
	}
	if err := w.ClearRingAssignment(context.Background(), "d:r:clear:1", "app1", "r"); err != nil {
		t.Fatal(err)
	}
	m, _ = p.Management(context.Background())
	if len(m.Artifacts[0].Assignments) != 0 || len(m.Memberships) != 0 {
		t.Fatalf("not cleared %+v", m)
	}
}

func TestFakeWriterErrors(t *testing.T) {
	p := NewFake()
	w := NewFakeWriter(p)
	op := RingAssignmentOp{OperationID: "o", ManagementArtifactExternalID: "x", RingKey: "r", TargetGroupExternalID: "g", Intent: "required"}
	if err := w.SetRingAssignment(context.Background(), op); !errors.Is(err, ErrPermanent) {
		t.Fatalf("unknown artifact: %v", err)
	}
	w.FailNext(ErrTransient)
	op.OperationID = "o2"
	if err := w.SetRingAssignment(context.Background(), op); !IsTransient(err) {
		t.Fatalf("transient: %v", err)
	}
	if !errors.Is((NotConfiguredWriter{}).SetRingAssignment(context.Background(), op), ErrPermanent) {
		t.Fatal("not configured must be permanent")
	}
	op.Intent = "bogus"
	if err := w.SetRingAssignment(context.Background(), op); !errors.Is(err, ErrPermanent) {
		t.Fatal("intent")
	}
}
