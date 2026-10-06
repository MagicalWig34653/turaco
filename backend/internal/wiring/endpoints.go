package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	assetspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/public"
	endpointsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	endpointsrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/repository"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// assetLookup adapts the Assets public contract to the questions Endpoints ask.
type assetLookup struct{ a *assetspublic.Assets }

func (l assetLookup) FindBySerial(ctx context.Context, serial string) (endpointsapp.AssetInfo, error) {
	a, err := l.a.FindBySerial(ctx, serial)
	switch {
	case errors.Is(err, assetspublic.ErrNotFound):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetNotFound
	case errors.Is(err, assetspublic.ErrConflict):
		return endpointsapp.AssetInfo{}, endpointsapp.ErrAssetAmbiguous
	case err != nil:
		return endpointsapp.AssetInfo{}, err
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, nil
}

func (l assetLookup) ByID(ctx context.Context, assetID string) (endpointsapp.AssetInfo, bool, error) {
	found, err := l.a.Assets(ctx, []string{assetID})
	if err != nil {
		return endpointsapp.AssetInfo{}, false, err
	}
	a, ok := found[assetID]
	if !ok {
		return endpointsapp.AssetInfo{}, false, nil
	}
	return endpointsapp.AssetInfo{ID: a.ID, SerialNumber: a.SerialNumber, Status: a.Status}, true, nil
}

// directoryLookup adapts the Organization directory graph and work directory contracts to the questions the
// management views ask. All lookups are bounded by the Organization contract and report truncation.
type directoryLookup struct {
	graph *orgpublic.DirectoryGraph
	names *orgpublic.WorkDirectory
}

func (d directoryLookup) groups(in []orgpublic.DirectoryGroupRef) []endpointsapp.DirectoryGroup {
	out := make([]endpointsapp.DirectoryGroup, 0, len(in))
	for _, g := range in {
		out = append(out, endpointsapp.DirectoryGroup{ID: g.ID, ExternalID: g.ExternalID, Name: g.DisplayName, ObservedAt: g.LastObservedAt})
	}
	return out
}

func (d directoryLookup) GroupsByExternalIDs(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.DirectoryGroup, bool, error) {
	g, cut, err := d.graph.GroupsByExternalIDs(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return d.groups(g), cut, err
}

func (d directoryLookup) GroupsByIDs(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.DirectoryGroup, bool, error) {
	g, cut, err := d.graph.GroupsByIDs(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return d.groups(g), cut, err
}

func edges(in []orgpublic.GroupNestingEdge) []endpointsapp.NestingEdge {
	out := make([]endpointsapp.NestingEdge, 0, len(in))
	for _, e := range in {
		out = append(out, endpointsapp.NestingEdge{ChildID: e.ChildID, ParentID: e.ParentID})
	}
	return out
}

func (d directoryLookup) NestingUp(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.NestingEdge, bool, error) {
	e, cut, err := d.graph.NestingUp(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return edges(e), cut, err
}

func (d directoryLookup) NestingDown(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.NestingEdge, bool, error) {
	e, cut, err := d.graph.NestingDown(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return edges(e), cut, err
}

func memberships(in []orgpublic.GroupUserMembership) []endpointsapp.UserMembership {
	out := make([]endpointsapp.UserMembership, 0, len(in))
	for _, m := range in {
		out = append(out, endpointsapp.UserMembership{UserID: m.UserID, GroupID: m.GroupID, ObservedAt: m.LastObservedAt})
	}
	return out
}

func (d directoryLookup) UserMemberships(ctx context.Context, providerKey string, ids []string) ([]endpointsapp.UserMembership, bool, error) {
	m, cut, err := d.graph.UserMemberships(ctx, providerKey, ids, orgpublic.MaxGraphRows)
	return memberships(m), cut, err
}

func (d directoryLookup) GroupMembers(ctx context.Context, providerKey string, ids []string, limit int) ([]endpointsapp.UserMembership, bool, error) {
	m, cut, err := d.graph.GroupMembers(ctx, providerKey, ids, limit)
	return memberships(m), cut, err
}

func (d directoryLookup) UsersWithIdentity(ctx context.Context, providerKey string, ids []string) (map[string]bool, error) {
	return d.graph.UsersWithIdentity(ctx, providerKey, ids)
}

func (d directoryLookup) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	return d.names.UserNames(ctx, ids)
}

// Endpoints builds the Endpoints service over the Assets public contract, the Organization directory graph,
// the endpoint provider and the Software Management Provider. syncEnabled switches POST /api/v1/endpoint-sync
// on; softwareSync switches the Software Package synchronization on.
func Endpoints(pool *pgxpool.Pool, provider intune.Provider, syncEnabled bool, software softwaremgmt.Provider, softwareSync bool) *endpointsapp.Service {
	org := orgrepository.New(pool)
	assets := assetspublic.New(Assets(pool))
	dir := directoryLookup{graph: orgpublic.NewDirectoryGraph(org), names: orgpublic.NewWorkDirectory(org)}
	return endpointsapp.NewService(endpointsrepository.New(pool), assetLookup{assets}, provider, syncEnabled, nil).
		WithViews(dir, assets).WithSoftware(software, softwareSync)
}

// SoftwareNotifications builds the consumer that tells software.approve holders about approval requests.
func SoftwareNotifications(pool *pgxpool.Pool, notifier *notifications.Service) *endpointsapp.SoftwareNotifications {
	org := orgrepository.New(pool)
	return endpointsapp.NewSoftwareNotifications(endpointsrepository.New(pool), orgpublic.NewWorkDirectory(org),
		orgpublic.NewNotificationRecipients(org), roles.NewEvaluator(pool, orgpublic.NewAuthorizationSubjects(org)), notifier)
}
