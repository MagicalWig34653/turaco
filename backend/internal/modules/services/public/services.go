// Package public holds the adapters that connect the Services module to the
// public contracts of the modules it depends on, so the application depends
// only on small interfaces of its own.
package public

import (
	"context"

	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	infrapublic "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
)

// Infrastructure adapts the Infrastructure contract.
type Infrastructure struct{ i *infrapublic.Infrastructure }

func NewInfrastructure(i *infrapublic.Infrastructure) *Infrastructure { return &Infrastructure{i: i} }

func (x *Infrastructure) VMs(ctx context.Context, ids []string) (map[string]application.VMInfo, error) {
	found, err := x.i.VMs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.VMInfo, len(found))
	for id, v := range found {
		out[id] = application.VMInfo{ID: v.ID, Name: v.Name, State: v.State, HypervisorAssetID: v.HypervisorAssetID}
	}
	return out, nil
}

// Assets adapts the Assets contract.
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
