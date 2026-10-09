// Package public is the Infrastructure module's contract for other modules and
// the adapters that connect Infrastructure to the contracts it depends on
// (Assets), so the application depends only on small interfaces of its own.
package public

import (
	"context"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
)

// ErrNotFound is returned by WhereIs for an Asset without an active placement.
var ErrNotFound = application.ErrNotFound

// Location is where an Asset physically is: its active Rack Placement with the
// Rack, Room, Building and Site (Organization Location id) above it.
type Location = application.AssetLocation

// Infrastructure is the module's public service.
type Infrastructure struct{ svc *application.Service }

func New(svc *application.Service) *Infrastructure { return &Infrastructure{svc: svc} }

// WhereIs returns the active placement of an Asset, or ErrNotFound when it is
// not placed in a rack. It authorizes nothing: the caller decides who may see it.
func (i *Infrastructure) WhereIs(ctx context.Context, assetID string) (Location, error) {
	loc, err := i.svc.PlacementOf(ctx, assetID)
	if err != nil {
		return Location{}, err
	}
	if loc == nil {
		return Location{}, ErrNotFound
	}
	return *loc, nil
}

// VM is the minimal view of a Virtual Machine other modules get.
type VM struct {
	ID                string
	Name              string
	State             string
	HypervisorAssetID *string
}

// Decommissioned reports that the VM is a tombstone.
func (v VM) Decommissioned() bool { return v.State == application.VMDecommissioned }

// VMs returns id -> VM for the existing Virtual Machines among the ids (at
// most 500 ids; ids that are not UUIDs are ignored). It authorizes nothing: the
// caller decides who may see the names.
func (i *Infrastructure) VMs(ctx context.Context, ids []string) (map[string]VM, error) {
	found, err := i.svc.VMsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]VM, len(found))
	for _, v := range found {
		out[v.ID] = VM{ID: v.ID, Name: v.Name, State: v.State, HypervisorAssetID: v.HypervisorAssetID}
	}
	return out, nil
}

// VMIDsWithHypervisor lists the ids (ascending, after afterID, at most 500) of
// Virtual Machines that have a hypervisor Asset, for modules that derive links
// from them. It authorizes nothing.
func (i *Infrastructure) VMIDsWithHypervisor(ctx context.Context, afterID string, limit int) ([]string, error) {
	return i.svc.VMIDsWithHypervisor(ctx, afterID, limit)
}

// Assets adapts the Assets contract to the questions Infrastructure asks.
type Assets struct{ a *assetspublic.Assets }

func NewAssets(a *assetspublic.Assets) *Assets { return &Assets{a: a} }

func (x *Assets) Assets(ctx context.Context, ids []string) (map[string]application.AssetInfo, error) {
	found, err := x.a.Assets(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.AssetInfo, len(found))
	for id, a := range found {
		out[id] = application.AssetInfo{ID: a.ID, Reference: a.Reference, Status: a.Status}
	}
	return out, nil
}

// SearchVMs finds Virtual Machines that are not decommissioned by name (at most limit, 50 at most). It authorizes
// nothing: the caller decides who may see the names.
func (i *Infrastructure) SearchVMs(ctx context.Context, text string, limit int) ([]VM, error) {
	found, err := i.svc.SearchVMs(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]VM, 0, len(found))
	for _, v := range found {
		out = append(out, VM{ID: v.ID, Name: v.Name, State: v.State, HypervisorAssetID: v.HypervisorAssetID})
	}
	return out, nil
}
