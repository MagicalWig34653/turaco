package application

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// cleanAddress validates a management address: an IP address or a host name.
// Empty means none.
func cleanAddress(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if len(s) > maxHost {
		return nil, invalid("management address must be an IP address or host name of at most %d characters", maxHost)
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		v := a.String()
		return &v, nil
	}
	host := strings.TrimSuffix(s, ".")
	for _, label := range strings.Split(host, ".") {
		if !hostLabel.MatchString(label) {
			return nil, invalid("management address must be an IP address or host name")
		}
	}
	return &s, nil
}

func vmState(v *VirtualMachine) any {
	if v == nil {
		return nil
	}
	return map[string]any{"state": v.State, "hypervisorAssetId": v.HypervisorAssetID, "vcpu": v.VCPU, "memoryMb": v.MemoryMB, "version": v.Version}
}

func vmEvent(v VirtualMachine, operation string) map[string]any {
	return map[string]any{"virtualMachineId": v.ID, "operation": operation, "state": v.State, "hypervisorAssetId": v.HypervisorAssetID}
}

func checkSize(vcpu, memoryMB int) error {
	if vcpu < 1 || vcpu > MaxVCPU {
		return invalid("vCPU must be between 1 and %d", MaxVCPU)
	}
	if memoryMB < 1 || memoryMB > MaxMemoryMB {
		return invalid("memory must be between 1 and %d MB", MaxMemoryMB)
	}
	return nil
}

// hypervisor checks that a hypervisor Asset exists and is usable and returns
// its normalized (lower-case) id. Only an unusable Asset is a client error;
// a failing Assets lookup propagates as an internal error.
func (s *Service) hypervisor(ctx context.Context, id *string) (*string, error) {
	if id == nil {
		return nil, nil
	}
	if err := checkIDs(*id); err != nil {
		return nil, err
	}
	l := strings.ToLower(*id)
	if err := s.usableAsset(ctx, l); err != nil {
		if errors.Is(err, ErrAssetUnusable) {
			return nil, ErrReferenceInvalid
		}
		return nil, err
	}
	return &l, nil
}

// CreateVM registers a Virtual Machine by hand. Requires infrastructure.manage.
func (s *Service) CreateVM(ctx context.Context, c Caller, p Principal, in VMInput) (VirtualMachine, error) {
	if err := c.validate(); err != nil {
		return VirtualMachine{}, err
	}
	if err := p.require(true); err != nil {
		return VirtualMachine{}, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return VirtualMachine{}, err
	}
	if in.State == "" {
		in.State = VMUnknown
	}
	if !oneOf(in.State, VMSettableStates) {
		return VirtualMachine{}, invalid("state must be one of %s", strings.Join(VMSettableStates, ", "))
	}
	if err := checkSize(in.VCPU, in.MemoryMB); err != nil {
		return VirtualMachine{}, err
	}
	addr, err := cleanAddress(in.ManagementAddress)
	if err != nil {
		return VirtualMachine{}, err
	}
	netNote, err := cleanText("network note", in.NetworkNote, maxNetNote, false)
	if err != nil {
		return VirtualMachine{}, err
	}
	notes, err := cleanText("notes", in.Notes, maxNotes, true)
	if err != nil {
		return VirtualMachine{}, err
	}
	hv, err := s.hypervisor(ctx, in.HypervisorAssetID)
	if err != nil {
		return VirtualMachine{}, err
	}
	vm := VirtualMachine{Name: name, State: in.State, HypervisorAssetID: hv, VCPU: in.VCPU, MemoryMB: in.MemoryMB,
		ManagementAddress: addr, NetworkNote: netNote, Notes: notes, CreatedBy: strPtr(c.Actor.UserID)}
	var out VirtualMachine
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertVMTx(ctx, tx, vm)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.vm.created", "virtual_machine", out.ID, nil, vmState(&out), nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "VirtualMachineChanged", vmEvent(out, "created"))
	})
	return out, err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// UpdateVMDetails changes name, size, management address and texts. Empty
// optional texts clear them. Requires infrastructure.manage.
func (s *Service) UpdateVMDetails(ctx context.Context, c Caller, p Principal, id string, expected int, in VMDetails) (VirtualMachine, error) {
	if err := c.validate(); err != nil {
		return VirtualMachine{}, err
	}
	if err := p.require(true); err != nil {
		return VirtualMachine{}, err
	}
	var name string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return VirtualMachine{}, err
		}
		name = n
	}
	var addr, netNote, notes *string
	var err error
	if in.ManagementAddress != nil {
		if addr, err = cleanAddress(*in.ManagementAddress); err != nil {
			return VirtualMachine{}, err
		}
	}
	if in.NetworkNote != nil {
		if netNote, err = cleanText("network note", *in.NetworkNote, maxNetNote, false); err != nil {
			return VirtualMachine{}, err
		}
	}
	if in.Notes != nil {
		if notes, err = cleanText("notes", *in.Notes, maxNotes, true); err != nil {
			return VirtualMachine{}, err
		}
	}
	var out VirtualMachine
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockVMTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.State == VMDecommissioned {
			return ErrDecommissioned
		}
		next := cur
		var changed []string
		if in.Name != nil && name != cur.Name {
			next.Name = name
			changed = append(changed, "name")
		}
		if in.VCPU != nil || in.MemoryMB != nil {
			vcpu, mem := cur.VCPU, cur.MemoryMB
			if in.VCPU != nil {
				vcpu = *in.VCPU
			}
			if in.MemoryMB != nil {
				mem = *in.MemoryMB
			}
			if err := checkSize(vcpu, mem); err != nil {
				return err
			}
			if vcpu != cur.VCPU {
				next.VCPU = vcpu
				changed = append(changed, "vcpu")
			}
			if mem != cur.MemoryMB {
				next.MemoryMB = mem
				changed = append(changed, "memoryMb")
			}
		}
		if in.ManagementAddress != nil && !samePtr(addr, cur.ManagementAddress) {
			next.ManagementAddress = addr
			changed = append(changed, "managementAddress")
		}
		if in.NetworkNote != nil && !samePtr(netNote, cur.NetworkNote) {
			next.NetworkNote = netNote
			changed = append(changed, "networkNote")
		}
		if in.Notes != nil && !samePtr(notes, cur.Notes) {
			next.Notes = notes
			changed = append(changed, "notes")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.store.UpdateVMTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.vm.updated", "virtual_machine", id, vmState(&cur), vmState(&out), map[string]any{"changedFields": changed}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "VirtualMachineChanged", vmEvent(out, "updated"))
	})
	return out, err
}

// ChangeVMState records that a VM is running, stopped or unknown. Requires infrastructure.manage.
func (s *Service) ChangeVMState(ctx context.Context, c Caller, p Principal, id string, expected *int, state string) (VirtualMachine, error) {
	if err := c.validate(); err != nil {
		return VirtualMachine{}, err
	}
	if err := p.require(true); err != nil {
		return VirtualMachine{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return VirtualMachine{}, err
	}
	if !oneOf(state, VMSettableStates) {
		return VirtualMachine{}, invalid("state must be one of %s", strings.Join(VMSettableStates, ", "))
	}
	var out VirtualMachine
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockVMTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.State == VMDecommissioned {
			return ErrDecommissioned
		}
		if cur.State == state {
			out = cur
			return nil
		}
		next := cur
		next.State = state
		out, err = s.store.UpdateVMTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.vm.state_changed", "virtual_machine", id, vmState(&cur), vmState(&out), nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "VirtualMachineChanged", vmEvent(out, "state_changed"))
	})
	return out, err
}

// AssignVMHypervisor sets the hypervisor Asset of a VM; nil clears it.
// Requires infrastructure.manage.
func (s *Service) AssignVMHypervisor(ctx context.Context, c Caller, p Principal, id string, expected *int, assetID *string) (VirtualMachine, error) {
	if err := c.validate(); err != nil {
		return VirtualMachine{}, err
	}
	if err := p.require(true); err != nil {
		return VirtualMachine{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return VirtualMachine{}, err
	}
	assetID, err := s.hypervisor(ctx, assetID)
	if err != nil {
		return VirtualMachine{}, err
	}
	var out VirtualMachine
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockVMTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.State == VMDecommissioned {
			return ErrDecommissioned
		}
		if samePtr(assetID, cur.HypervisorAssetID) {
			out = cur
			return nil
		}
		next := cur
		next.HypervisorAssetID = assetID
		out, err = s.store.UpdateVMTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.vm.hypervisor_changed", "virtual_machine", id, vmState(&cur), vmState(&out), nil); err != nil {
			return err
		}
		return publish(ctx, tx, c, "VirtualMachineChanged", vmEvent(out, "hypervisor_changed"))
	})
	return out, err
}

// DecommissionVM retires a VM with a reason code. The record stays as a
// tombstone and cannot be changed again; repeating a successful decommission
// with the same reason returns the current record. Requires infrastructure.manage.
func (s *Service) DecommissionVM(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (VirtualMachine, error) {
	if err := c.validate(); err != nil {
		return VirtualMachine{}, err
	}
	if err := p.require(true); err != nil {
		return VirtualMachine{}, err
	}
	if _, err := requireVersion(expected); err != nil {
		return VirtualMachine{}, err
	}
	if !oneOf(reason, VMDecommissionReasons) {
		return VirtualMachine{}, invalid("reason must be one of %s", strings.Join(VMDecommissionReasons, ", "))
	}
	var out VirtualMachine
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockVMTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.State == VMDecommissioned {
			// A retry of a decommission that already succeeded (same reason,
			// version from before it) returns the current record.
			if cur.DecommissionReason != nil && *cur.DecommissionReason == reason && (*expected == cur.Version-1 || *expected == cur.Version) {
				out = cur
				return nil
			}
			return ErrDecommissioned
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		next := cur
		next.State = VMDecommissioned
		next.DecommissionReason = &reason
		out, err = s.store.UpdateVMTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "infrastructure.vm.decommissioned", "virtual_machine", id, vmState(&cur), vmState(&out), map[string]any{"reason": reason}); err != nil {
			return err
		}
		ev := vmEvent(out, "decommissioned")
		ev["reason"] = reason
		return publish(ctx, tx, c, "VirtualMachineChanged", ev)
	})
	return out, err
}

// ListVMs lists Virtual Machines. Requires infrastructure.view.
func (s *Service) ListVMs(ctx context.Context, p Principal, f VMFilter) (Result[VirtualMachine], error) {
	if err := p.require(false); err != nil {
		return Result[VirtualMachine]{}, err
	}
	if f.State != "" && !oneOf(f.State, VMStates) {
		return Result[VirtualMachine]{}, invalid("unknown state")
	}
	if f.HypervisorAssetID != "" {
		if err := checkIDs(f.HypervisorAssetID); err != nil {
			return Result[VirtualMachine]{}, err
		}
		f.HypervisorAssetID = strings.ToLower(f.HypervisorAssetID)
	}
	f.Query = strings.TrimSpace(f.Query)
	if len(f.Query) > maxName {
		return Result[VirtualMachine]{}, invalid("query must be at most %d characters", maxName)
	}
	f.Page = f.Page.Normalize()
	return s.store.ListVMs(ctx, f)
}

// GetVM returns one Virtual Machine. Requires infrastructure.view.
func (s *Service) GetVM(ctx context.Context, p Principal, id string) (VirtualMachine, error) {
	if err := p.require(false); err != nil {
		return VirtualMachine{}, err
	}
	return s.store.GetVM(ctx, id)
}
