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

func (x *Infrastructure) VMIDsWithHypervisor(ctx context.Context, afterID string, limit int) ([]string, error) {
	return x.i.VMIDsWithHypervisor(ctx, afterID, limit)
}

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
	found, err := x.a.AssetsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]application.AssetInfo, len(found))
	for id, a := range found {
		out[id] = application.AssetInfo{ID: a.ID, Reference: a.Reference, Status: a.Status}
	}
	return out, nil
}

// ServiceInfo is what other modules may know about a Service: identity,
// lifecycle and the people responsible for it.
type ServiceInfo struct {
	ID            string
	Reference     string
	Name          string
	Status        string
	Criticality   string
	OwnerUserID   *string
	OwnerTeamID   *string
	SupportTeamID *string
}

// Retired reports that the Service is a tombstone.
func (s ServiceInfo) Retired() bool { return s.Status == application.StatusRetired }

// Impact types are the Services impact traversal as other modules see it.
type (
	// ImpactCaller says what the caller may see, as the Services transport derives it.
	ImpactCaller = application.Principal
	ImpactInput  = application.ImpactInput
	ImpactResult = application.ImpactResult
	ImpactNode   = application.ImpactNode
	NodeInfo     = application.NodeInfo
	PathEdge     = application.PathEdge
)

// Impact errors a caller may need to recognize.
var (
	ErrImpactBusy = application.ErrImpactBusy
	ErrNotFound   = application.ErrNotFound
)

// Services is the Services module's public service for other modules.
type Services struct{ app *application.App }

func New(app *application.App) *Services { return &Services{app: app} }

// Lookup returns id -> Service information for the existing Services among ids
// (at most 500), retired ones included. It performs no permission check.
func (s *Services) Lookup(ctx context.Context, ids []string) (map[string]ServiceInfo, error) {
	found, err := s.app.Lookup(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ServiceInfo, len(found))
	for id, v := range found {
		out[id] = ServiceInfo{ID: v.ID, Reference: v.Reference, Name: v.Name, Status: v.Status, Criticality: v.Criticality,
			OwnerUserID: v.OwnerUserID, OwnerTeamID: v.OwnerTeamID, SupportTeamID: v.SupportTeamID}
	}
	return out, nil
}

// Impact runs the bounded impact traversal for the caller (see application.App.Impact);
// the caller's Services, infrastructure and assets visibility decides what is
// shown, so the redaction rules are those of the Services impact view.
func (s *Services) Impact(ctx context.Context, p ImpactCaller, in ImpactInput) (ImpactResult, error) {
	return s.app.Impact(ctx, p, in)
}

// Search finds active Services by name or reference (at most limit, 50 at most). It performs no permission check.
func (s *Services) Search(ctx context.Context, text string, limit int) ([]ServiceInfo, error) {
	found, err := s.app.Search(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceInfo, 0, len(found))
	for _, v := range found {
		out = append(out, ServiceInfo{ID: v.ID, Reference: v.Reference, Name: v.Name, Status: v.Status, Criticality: v.Criticality,
			OwnerUserID: v.OwnerUserID, OwnerTeamID: v.OwnerTeamID, SupportTeamID: v.SupportTeamID})
	}
	return out, nil
}
