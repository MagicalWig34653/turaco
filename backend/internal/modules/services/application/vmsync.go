package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
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
// is read. The VM is read through the Infrastructure contract on its own pool
// connection while the caller's transaction holds another one, so the worker's
// consumer concurrency must stay below the pool size (the dispatcher handles
// events sequentially per worker).
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
	changed := map[string]any{}
	if want == nil {
		n, err := v.graph.UnlinkAll(ctx, tx, RelationshipOwner, node, relationships.Forward, reason, "")
		if err != nil {
			return fmt.Errorf("end vm links: %w", err)
		}
		if n > 0 {
			changed["endedLinks"] = n
		}
	} else {
		target := relationships.Node{Type: NodeAsset, ID: *want}
		rel, created, err := v.graph.Link(ctx, tx, relationships.LinkInput{
			Owner: RelationshipOwner, Source: node, Type: RelRunsOn, Target: target,
			Confidence: relationships.ConfidenceDerived, RecordedBy: "infrastructure",
		})
		if err != nil {
			return fmt.Errorf("link vm to hypervisor: %w", err)
		}
		if created {
			changed["linkedAssetId"] = *want
			changed["relationshipId"] = rel.ID
		}
		// Every other current RUNS_ON of the VM is stale, however many there are.
		n, err := v.graph.UnlinkAllExcept(ctx, tx, RelationshipOwner, node, relationships.Forward, target, reasonVMLinkChanged, "")
		if err != nil {
			return fmt.Errorf("end stale vm links: %w", err)
		}
		if n > 0 {
			changed["endedLinks"] = n
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

// Backfill job: Virtual Machines that got a hypervisor before F7b (or while the
// consumer was not running) have no RUNS_ON link yet. The job syncs every VM
// that has a hypervisor; Sync is idempotent and audits only real changes, so
// running it at every worker start is safe and converges after a crash.
const (
	BackfillJobType    = "services.vm_link_backfill"
	BackfillJobTimeout = 10 * time.Minute
	backfillBatch      = 200
)

// EnqueueBackfill enqueues the backfill job; at most one is pending or running at a time.
func EnqueueBackfill(ctx context.Context, q jobs.Querier) error {
	_, _, err := jobs.Enqueue(ctx, q, jobs.EnqueueRequest{Type: BackfillJobType, DedupeKey: BackfillJobType, MaxAttempts: 3})
	return err
}

// HandleBackfill is the job handler of BackfillJobType. Every VM is synced in
// its own transaction, in id order, so a failure resumes cheaply on retry.
func (v *VMLinks) HandleBackfill(ctx context.Context, job jobs.Job) error {
	after := ""
	for {
		ids, err := v.infra.VMIDsWithHypervisor(ctx, after, backfillBatch)
		if err != nil {
			return fmt.Errorf("list virtual machines with hypervisor: %w", err)
		}
		for _, id := range ids {
			id = strings.ToLower(id)
			err := v.store.InTx(ctx, func(tx pgx.Tx) error { return v.Sync(ctx, tx, id, "vm-link-backfill:"+job.ID) })
			if err != nil {
				return fmt.Errorf("backfill vm link %s: %w", id, err)
			}
		}
		if len(ids) < backfillBatch {
			return nil
		}
		after = ids[len(ids)-1]
	}
}
