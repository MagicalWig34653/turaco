package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

// VMLinks keeps the derived relationship "VM RUNS_ON hypervisor Asset" in step
// with Infrastructure: it listens to VirtualMachineChanged and derives the link
// from the VM's current state (read through the Infrastructure contract), not
// from the event payload, so replays, duplicates and out-of-order events all
// converge on the same result.
type VMLinks struct {
	store Store
	graph *relationships.Graph
	infra Infrastructure
}

func NewVMLinks(store Store, graph *relationships.Graph, infra Infrastructure) *VMLinks {
	return &VMLinks{store: store, graph: graph, infra: infra}
}

// OnVirtualMachineChanged is the outbox consumer for VirtualMachineChanged.
func (v *VMLinks) OnVirtualMachineChanged(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var payload struct {
		VirtualMachineID string `json:"virtualMachineId"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil || !uuidPattern.MatchString(payload.VirtualMachineID) {
		return events.Permanent(fmt.Errorf("VirtualMachineChanged: malformed payload"))
	}
	return v.Sync(ctx, tx, strings.ToLower(payload.VirtualMachineID), ev.CorrelationID)
}

// Sync makes the RUNS_ON relationship of one VM match its current state: a
// live VM with a hypervisor has exactly one derived link to that Asset; a VM
// without hypervisor, decommissioned or unknown has none. Concurrent syncs of
// the same VM are serialized by a transaction-scoped lock taken before the VM
// is read.
func (v *VMLinks) Sync(ctx context.Context, tx pgx.Tx, vmID, correlationID string) error {
	if err := v.store.LockVMLinkTx(ctx, tx, vmID); err != nil {
		return err
	}
	found, err := v.infra.VMs(ctx, []string{vmID})
	if err != nil {
		return fmt.Errorf("read virtual machine: %w", err)
	}
	var want *string
	reason := reasonVMLinkCleared
	vm, exists := found[vmID]
	switch {
	case !exists || vm.Decommissioned():
		reason = reasonVMDecommission
	case vm.HypervisorAssetID != nil:
		l := strings.ToLower(*vm.HypervisorAssetID)
		want = &l
	}
	node := relationships.Node{Type: NodeVM, ID: vmID}
	current, _, err := v.graph.Outgoing(ctx, tx, node, []string{RelRunsOn}, 10)
	if err != nil {
		return fmt.Errorf("read vm links: %w", err)
	}
	var keep bool
	changed := map[string]any{}
	for _, r := range current {
		if want != nil && r.Target.Type == NodeAsset && r.Target.ID == *want {
			keep = true
			continue
		}
		why := reason
		if want != nil {
			why = reasonVMLinkChanged
		}
		if _, ended, err := v.graph.Unlink(ctx, tx, r.ID, why, ""); err != nil {
			return fmt.Errorf("end vm link: %w", err)
		} else if ended {
			changed["endedAssetId"] = r.Target.ID
		}
	}
	if want != nil && !keep {
		rel, created, err := v.graph.Link(ctx, tx, relationships.LinkInput{
			Source: node, Type: RelRunsOn, Target: relationships.Node{Type: NodeAsset, ID: *want},
			Confidence: relationships.ConfidenceDerived, RecordedBy: "infrastructure",
		})
		if err != nil {
			return fmt.Errorf("link vm to hypervisor: %w", err)
		}
		if created {
			changed["linkedAssetId"] = *want
			changed["relationshipId"] = rel.ID
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: "services.vm_link.synced", TargetType: "virtual_machine", TargetID: vmID,
		Actor: audit.SystemActor("services"), CorrelationID: correlationID, Metadata: changed,
	})
}
