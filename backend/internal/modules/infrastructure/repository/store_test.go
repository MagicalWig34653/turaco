package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type dir struct{ sites map[string]string }

func (d dir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		_, out[id] = d.sites[id]
	}
	return out, nil
}
func (d dir) LocationNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := d.sites[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

type assets map[string]application.AssetInfo

func (a assets) Assets(_ context.Context, ids []string) (map[string]application.AssetInfo, error) {
	out := map[string]application.AssetInfo{}
	for _, id := range ids {
		if v, ok := a[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *application.Service
	corr    string
	site    string
	manager string
	assets  assets
	manage  application.Principal
	view    application.Principal
}

func (e *env) uuid() string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) newAsset(status string) string {
	id := e.uuid()
	e.assets[id] = application.AssetInfo{ID: id, Reference: "AST-" + id[:8], Status: status}
	return id
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	e := &env{t: t, pool: pool, corr: "infra-" + hex.EncodeToString(b), assets: assets{}}
	e.site, e.manager = e.uuid(), e.uuid()
	e.svc = application.NewService(repository.New(pool), dir{sites: map[string]string{e.site: "Headquarters"}}, e.assets)
	e.manage = application.Principal{UserID: e.manager, Manage: true, AssetsView: true}
	e.view = application.Principal{UserID: e.uuid(), View: true, AssetsView: true}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, e.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.virtual_machines WHERE name LIKE $1`, e.corr+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_unit_occupancy WHERE rack_id IN (SELECT k.id FROM infrastructure.racks k JOIN infrastructure.rooms m ON m.id = k.room_id JOIN infrastructure.buildings b ON b.id = m.building_id WHERE b.site_location_id = $1::uuid)`, e.site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_placements WHERE rack_id IN (SELECT k.id FROM infrastructure.racks k JOIN infrastructure.rooms m ON m.id = k.room_id JOIN infrastructure.buildings b ON b.id = m.building_id WHERE b.site_location_id = $1::uuid) AND previous_placement_id IS NOT NULL`, e.site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rack_placements WHERE rack_id IN (SELECT k.id FROM infrastructure.racks k JOIN infrastructure.rooms m ON m.id = k.room_id JOIN infrastructure.buildings b ON b.id = m.building_id WHERE b.site_location_id = $1::uuid)`, e.site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.racks WHERE room_id IN (SELECT m.id FROM infrastructure.rooms m JOIN infrastructure.buildings b ON b.id = m.building_id WHERE b.site_location_id = $1::uuid)`, e.site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.rooms WHERE building_id IN (SELECT id FROM infrastructure.buildings WHERE site_location_id = $1::uuid)`, e.site)
		_, _ = pool.Exec(ctx, `DELETE FROM infrastructure.buildings WHERE site_location_id = $1::uuid`, e.site)
	})
	return e
}

func (e *env) caller() application.Caller {
	return application.Caller{Actor: audit.UserActor(e.manager), CorrelationID: e.corr}
}

// rack builds Building > Room > Rack of the given height.
func (e *env) rack(height int) (application.Building, application.Room, application.Rack) {
	e.t.Helper()
	ctx := context.Background()
	b, err := e.svc.CreateBuilding(ctx, e.caller(), e.manage, e.site, "B"+e.uuid()[28:], "Main street")
	if err != nil {
		e.t.Fatalf("create building: %v", err)
	}
	r, err := e.svc.CreateRoom(ctx, e.caller(), e.manage, b.ID, "R1", "1")
	if err != nil {
		e.t.Fatalf("create room: %v", err)
	}
	k, err := e.svc.CreateRack(ctx, e.caller(), e.manage, r.ID, "K1", height)
	if err != nil {
		e.t.Fatalf("create rack: %v", err)
	}
	return b, r, k
}

func (e *env) place(rack, asset string, u, h int, face string) (application.Placement, error) {
	return e.svc.PlaceAsset(context.Background(), e.caller(), e.manage, application.PlacementInput{RackID: rack, AssetID: asset, UPosition: u, HeightU: h, Face: face})
}

// ver returns the current version of a record, for operations that require expectedVersion.
func (e *env) ver(table, id string) *int {
	e.t.Helper()
	n := e.count(`SELECT version FROM infrastructure.`+table+` WHERE id = $1::uuid`, id)
	return &n
}

func (e *env) count(sql string, args ...any) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestTopologyLifecycleAndArchive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, r, k := e.rack(42)
	if _, err := e.svc.CreateBuilding(ctx, e.caller(), e.manage, e.uuid(), "Ghost", ""); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Fatalf("unknown site: %v", err)
	}
	if _, err := e.svc.CreateBuilding(ctx, e.caller(), e.view, e.site, "X", ""); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("view-only create: %v", err)
	}
	if _, err := e.svc.CreateBuilding(ctx, e.caller(), e.manage, e.site, b.Name, ""); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("duplicate building name: %v", err)
	}
	if _, err := e.svc.CreateRack(ctx, e.caller(), e.manage, r.ID, "tall", 61); err == nil {
		t.Fatal("rack height 61 accepted")
	}
	if _, err := e.svc.CreateRack(ctx, e.caller(), e.manage, r.ID, "k1", 10); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("duplicate rack name (case-insensitive): %v", err)
	}
	renamed, err := e.svc.RenameRack(ctx, e.caller(), e.manage, k.ID, k.Version, "K2")
	if err != nil || renamed.Version != k.Version+1 {
		t.Fatalf("rename: %v %+v", err, renamed)
	}
	if _, err := e.svc.RenameRack(ctx, e.caller(), e.manage, k.ID, k.Version, "K3"); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// Archive order: rack, room, building; unarchive order reversed.
	if _, err := e.svc.SetRoomArchived(ctx, e.caller(), e.manage, r.ID, e.ver("rooms", r.ID), true); !errors.Is(err, application.ErrNotEmpty) {
		t.Fatalf("archive room with active rack: %v", err)
	}
	if _, err := e.svc.SetBuildingArchived(ctx, e.caller(), e.manage, b.ID, e.ver("buildings", b.ID), true); !errors.Is(err, application.ErrNotEmpty) {
		t.Fatalf("archive building with active room: %v", err)
	}
	asset := e.newAsset("available")
	pl, err := e.place(k.ID, asset, 1, 2, "front")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetRackArchived(ctx, e.caller(), e.manage, k.ID, e.ver("racks", k.ID), true); !errors.Is(err, application.ErrNotEmpty) {
		t.Fatalf("archive rack with placement: %v", err)
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, pl.ID, e.ver("rack_placements", pl.ID), "relocated"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error {
			_, err := e.svc.SetRackArchived(ctx, e.caller(), e.manage, k.ID, e.ver("racks", k.ID), true)
			return err
		},
		func() error {
			_, err := e.svc.SetRoomArchived(ctx, e.caller(), e.manage, r.ID, e.ver("rooms", r.ID), true)
			return err
		},
		func() error {
			_, err := e.svc.SetBuildingArchived(ctx, e.caller(), e.manage, b.ID, e.ver("buildings", b.ID), true)
			return err
		},
	} {
		if err := step(); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}
	if _, err := e.svc.CreateRoom(ctx, e.caller(), e.manage, b.ID, "R2", ""); !errors.Is(err, application.ErrArchived) {
		t.Fatalf("room in archived building: %v", err)
	}
	if _, err := e.place(k.ID, asset, 1, 1, "front"); !errors.Is(err, application.ErrArchived) {
		t.Fatalf("place into archived rack: %v", err)
	}
	if _, err := e.svc.SetRoomArchived(ctx, e.caller(), e.manage, r.ID, e.ver("rooms", r.ID), false); !errors.Is(err, application.ErrArchived) {
		t.Fatalf("unarchive room below archived building: %v", err)
	}
	if _, err := e.svc.SetBuildingArchived(ctx, e.caller(), e.manage, b.ID, e.ver("buildings", b.ID), false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetRoomArchived(ctx, e.caller(), e.manage, r.ID, e.ver("rooms", r.ID), false); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'infrastructure.building.archived'`, e.corr) != 1 {
		t.Fatal("archive not audited")
	}
}

func TestPlacementBoundsOverlapAndFaces(t *testing.T) {
	e := newEnv(t)
	_, _, k := e.rack(10)
	a1, a2, a3, a4 := e.newAsset("available"), e.newAsset("available"), e.newAsset("assigned"), e.newAsset("available")
	if _, err := e.place(k.ID, a1, 10, 2, "front"); err == nil {
		t.Fatal("placement beyond rack height accepted")
	}
	if _, err := e.place(k.ID, a1, 0, 1, "front"); err == nil {
		t.Fatal("u=0 accepted")
	}
	if _, err := e.place(k.ID, a1, 1, 1, "side"); err == nil {
		t.Fatal("bad face accepted")
	}
	if _, err := e.place(k.ID, a1, 10, 1, "front"); err != nil {
		t.Fatalf("top unit: %v", err)
	}
	if _, err := e.place(k.ID, a2, 3, 4, "front"); err != nil {
		t.Fatal(err)
	}
	// 6..7 front is free, 5 overlaps U3-U6.
	if _, err := e.place(k.ID, a3, 6, 2, "front"); !errors.Is(err, application.ErrOccupied) {
		t.Fatalf("overlap: %v", err)
	}
	// The same units on the rear face are free.
	if _, err := e.place(k.ID, a3, 3, 4, "rear"); err != nil {
		t.Fatalf("rear face: %v", err)
	}
	// One active placement per asset.
	if _, err := e.place(k.ID, a2, 7, 1, "front"); !errors.Is(err, application.ErrAssetPlaced) {
		t.Fatalf("second placement of one asset: %v", err)
	}
	for _, status := range []string{"disposed", "lost", "retired"} {
		if _, err := e.place(k.ID, e.newAsset(status), 8, 1, "front"); !errors.Is(err, application.ErrAssetUnusable) {
			t.Fatalf("%s asset: %v", status, err)
		}
	}
	if _, err := e.place(k.ID, e.uuid(), 8, 1, "front"); !errors.Is(err, application.ErrAssetUnusable) {
		t.Fatalf("unknown asset: %v", err)
	}
	if _, err := e.place(k.ID, a4, 8, 1, "front"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM infrastructure.rack_unit_occupancy WHERE rack_id = $1::uuid`, k.ID); n != 1+4+4+1 {
		t.Fatalf("occupied units = %d", n)
	}
	// The database refuses a double occupancy even if application checks are bypassed.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO infrastructure.rack_unit_occupancy(rack_id, face, u, placement_id)
		SELECT rack_id, face, u_position, id FROM infrastructure.rack_placements WHERE asset_id = $1::uuid`, a2); err == nil {
		t.Fatal("database accepted a double occupancy")
	}
}

func TestConcurrentPlacementsToSameUnitOneWins(t *testing.T) {
	e := newEnv(t)
	_, _, k := e.rack(20)
	const n = 8
	var wins, occupied, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		asset := e.newAsset("available")
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Overlapping ranges: every one of them includes unit 5.
			_, err := e.place(k.ID, asset, 4+i%2, 2, "front")
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, application.ErrOccupied):
				occupied.Add(1)
			default:
				other.Add(1)
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || occupied.Load() != n-1 {
		t.Fatalf("wins=%d occupied=%d other=%d", wins.Load(), occupied.Load(), other.Load())
	}
	if c := e.count(`SELECT count(*) FROM infrastructure.rack_placements WHERE rack_id = $1::uuid AND removed_at IS NULL`, k.ID); c != 1 {
		t.Fatalf("active placements = %d", c)
	}
}

func TestMoveAndRemoveKeepHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _, k1 := e.rack(10)
	_, _, k2 := e.rack(5)
	asset, other := e.newAsset("available"), e.newAsset("available")
	p, err := e.place(k1.ID, asset, 2, 3, "front")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.place(k1.ID, other, 8, 1, "front"); err != nil {
		t.Fatal(err)
	}
	// Moving onto its own units (shifting by one) works: the old units are freed first.
	moved, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, p.ID, &p.Version, application.PlacementInput{RackID: k1.ID, UPosition: 3, HeightU: 3, Face: "front"})
	if err != nil {
		t.Fatalf("shift: %v", err)
	}
	if moved.ID == p.ID || moved.PreviousID == nil || *moved.PreviousID != p.ID {
		t.Fatalf("move did not create a linked placement: %+v", moved)
	}
	if _, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, p.ID, e.ver("rack_placements", p.ID), application.PlacementInput{RackID: k1.ID, UPosition: 1, HeightU: 1, Face: "front"}); !errors.Is(err, application.ErrPlacementClosed) {
		t.Fatalf("move closed placement: %v", err)
	}
	if _, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, moved.ID, e.ver("rack_placements", moved.ID), application.PlacementInput{RackID: k1.ID, UPosition: 8, HeightU: 1, Face: "front"}); !errors.Is(err, application.ErrOccupied) {
		t.Fatalf("move onto other asset: %v", err)
	}
	// A failed move leaves the old placement active.
	if c := e.count(`SELECT count(*) FROM infrastructure.rack_placements WHERE asset_id = $1::uuid AND removed_at IS NULL`, asset); c != 1 {
		t.Fatalf("active placements after failed move = %d", c)
	}
	if _, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, moved.ID, e.ver("rack_placements", moved.ID), application.PlacementInput{RackID: k2.ID, UPosition: 4, HeightU: 3, Face: "front"}); err == nil {
		t.Fatal("move beyond the height of the target rack accepted")
	}
	to2, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, moved.ID, &moved.Version, application.PlacementInput{RackID: k2.ID, UPosition: 1, HeightU: 3, Face: "rear"})
	if err != nil || to2.RackID != k2.ID {
		t.Fatalf("move to other rack: %v", err)
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, e.ver("rack_placements", to2.ID), "moved"); err == nil {
		t.Fatal("reason 'moved' is internal and must be refused")
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, e.ver("rack_placements", to2.ID), ""); err == nil {
		t.Fatal("empty reason accepted")
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, &to2.Version, "replaced"); err != nil {
		t.Fatal(err)
	}
	// A retried remove with the same reason is idempotent (no second audit entry);
	// a different reason is refused.
	auditBefore := e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'infrastructure.placement.removed'`, e.corr)
	again, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, &to2.Version, "replaced")
	if err != nil || again.RemovedAt == nil {
		t.Fatalf("retried remove: %v %+v", err, again)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'infrastructure.placement.removed'`, e.corr) != auditBefore {
		t.Fatal("retried remove audited twice")
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, &to2.Version, "other"); !errors.Is(err, application.ErrPlacementClosed) {
		t.Fatalf("remove twice with another reason: %v", err)
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, to2.ID, nil, "replaced"); err == nil {
		t.Fatal("remove without expectedVersion accepted")
	}
	hist, err := e.svc.ListPlacements(ctx, e.view, k1.ID, true, application.Page{})
	if err != nil || len(hist.Items) != 3 { // original (moved), shifted (moved), other
		t.Fatalf("history of k1: %v %d", err, len(hist.Items))
	}
	active, err := e.svc.ListPlacements(ctx, e.view, k1.ID, false, application.Page{})
	if err != nil || len(active.Items) != 1 || active.Items[0].AssetID != other {
		t.Fatalf("active of k1: %v %+v", err, active.Items)
	}
	// Removed asset can be placed again and units are free.
	if _, err := e.place(k2.ID, asset, 1, 5, "rear"); err != nil {
		t.Fatalf("re-place: %v", err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action LIKE 'infrastructure.placement.%'`, e.corr) < 6 {
		t.Fatal("placement operations not audited")
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'RackPlacementChanged'`, e.corr) < 6 {
		t.Fatal("RackPlacementChanged not published")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND (before_data::text LIKE '%K1%' OR after_data::text LIKE '%Main street%')`, e.corr) != 0 {
		t.Fatal("names or notes leaked into the audit log")
	}
}

func TestWhereIsAndTree(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, _, k := e.rack(12)
	asset := e.newAsset("available")
	loc, err := e.svc.WhereIs(ctx, e.view, asset)
	if err != nil || loc != nil {
		t.Fatalf("unplaced asset: %v %+v", err, loc)
	}
	if _, err := e.place(k.ID, asset, 5, 2, "rear"); err != nil {
		t.Fatal(err)
	}
	loc, err = e.svc.WhereIs(ctx, e.view, asset)
	if err != nil || loc == nil || loc.RackName != "K1" || loc.BuildingID != b.ID || loc.SiteLocationID != e.site || loc.UPosition != 5 || loc.Face != "rear" {
		t.Fatalf("where is: %v %+v", err, loc)
	}
	pub := public.New(e.svc)
	if got, err := pub.WhereIs(ctx, asset); err != nil || got.RackID != k.ID {
		t.Fatalf("public where is: %v %+v", err, got)
	}
	if _, err := pub.WhereIs(ctx, e.newAsset("available")); !errors.Is(err, public.ErrNotFound) {
		t.Fatalf("public where is, unplaced: %v", err)
	}
	if _, err := e.svc.WhereIs(ctx, e.view, e.uuid()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown asset: %v", err)
	}
	if _, err := e.svc.WhereIs(ctx, e.view, "not-a-uuid"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("invalid id: %v", err)
	}
	noAssets := e.view
	noAssets.AssetsView = false
	if _, err := e.svc.WhereIs(ctx, noAssets, asset); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("without assets.view: %v", err)
	}
	if _, err := e.svc.WhereIs(ctx, application.Principal{AssetsView: true}, asset); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("without infrastructure.view: %v", err)
	}
	tree, err := e.svc.Tree(ctx, e.view, false)
	if err != nil {
		t.Fatal(err)
	}
	sites := tree.Sites
	var found *application.SiteSummary
	for i := range sites {
		if sites[i].LocationID == e.site {
			found = &sites[i]
		}
	}
	if found == nil || found.Name != "Headquarters" || len(found.Buildings) != 1 {
		t.Fatalf("tree: %+v", sites)
	}
	if bs := found.Buildings[0]; bs.Rooms != 1 || bs.Racks != 1 || bs.Placed != 1 {
		t.Fatalf("building counts: %+v", bs)
	}
	if _, err := e.svc.Tree(ctx, application.Principal{}, false); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("tree without permission: %v", err)
	}
	d, err := e.svc.GetRack(ctx, e.view, k.ID)
	if err != nil || len(d.Placements) != 1 {
		t.Fatalf("rack detail: %v %+v", err, d)
	}
	if _, err := e.svc.GetRack(ctx, e.view, e.uuid()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown rack: %v", err)
	}
	if _, err := e.svc.GetRack(ctx, application.Principal{}, k.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("rack without permission: %v", err)
	}
}

func TestVirtualMachineOperations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	host, gone := e.newAsset("assigned"), e.newAsset("disposed")
	in := application.VMInput{Name: e.corr + "-vm1", State: "running", HypervisorAssetID: &host, VCPU: 4, MemoryMB: 8192, ManagementAddress: "10.0.0.5", NetworkNote: "VLAN 20", Notes: "line1\nline2"}
	vm, err := e.svc.CreateVM(ctx, e.caller(), e.manage, in)
	if err != nil || vm.State != "running" || vm.Version != 1 {
		t.Fatalf("create: %v %+v", err, vm)
	}
	bad := []application.VMInput{
		{Name: "", VCPU: 1, MemoryMB: 1},
		{Name: e.corr + "-x", VCPU: 0, MemoryMB: 1},
		{Name: e.corr + "-x", VCPU: 1, MemoryMB: application.MaxMemoryMB + 1},
		{Name: e.corr + "-x", VCPU: 1, MemoryMB: 1, State: "decommissioned"},
		{Name: e.corr + "-x", VCPU: 1, MemoryMB: 1, ManagementAddress: "bad host!"},
		{Name: e.corr + "-x", VCPU: 1, MemoryMB: 1, ManagementAddress: "-bad.example"},
		{Name: e.corr + "-x", VCPU: 1, MemoryMB: 1, Notes: "bad\x00note"},
	}
	for i, b := range bad {
		if _, err := e.svc.CreateVM(ctx, e.caller(), e.manage, b); err == nil {
			t.Errorf("invalid input %d accepted", i)
		}
	}
	for _, addr := range []string{"host-1.example.org", "2001:db8::1", "vm01"} {
		if _, err := e.svc.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-a-" + addr, VCPU: 1, MemoryMB: 1, ManagementAddress: addr}); err != nil {
			t.Errorf("address %q: %v", addr, err)
		}
	}
	if _, err := e.svc.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-VM1", VCPU: 1, MemoryMB: 1}); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("duplicate live name: %v", err)
	}
	in.HypervisorAssetID, in.Name = &gone, e.corr+"-vm2"
	if _, err := e.svc.CreateVM(ctx, e.caller(), e.manage, in); !errors.Is(err, application.ErrReferenceInvalid) {
		t.Fatalf("disposed hypervisor: %v", err)
	}
	if _, err := e.svc.CreateVM(ctx, e.caller(), e.view, in); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("view-only create: %v", err)
	}
	cpu := 8
	empty := ""
	upd, err := e.svc.UpdateVMDetails(ctx, e.caller(), e.manage, vm.ID, vm.Version, application.VMDetails{VCPU: &cpu, NetworkNote: &empty})
	if err != nil || upd.VCPU != 8 || upd.NetworkNote != nil || upd.Version != 2 {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if _, err := e.svc.UpdateVMDetails(ctx, e.caller(), e.manage, vm.ID, vm.Version, application.VMDetails{VCPU: &cpu}); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	st, err := e.svc.ChangeVMState(ctx, e.caller(), e.manage, vm.ID, &upd.Version, "stopped")
	if err != nil || st.State != "stopped" {
		t.Fatalf("state: %v %+v", err, st)
	}
	if _, err := e.svc.ChangeVMState(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), "decommissioned"); err == nil {
		t.Fatal("state decommissioned must go through DecommissionVM")
	}
	cleared, err := e.svc.AssignVMHypervisor(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), nil)
	if err != nil || cleared.HypervisorAssetID != nil {
		t.Fatalf("clear hypervisor: %v", err)
	}
	if _, err := e.svc.AssignVMHypervisor(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), &host); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.ListVMs(ctx, e.view, application.VMFilter{State: "stopped", HypervisorAssetID: host, Query: "VM1"})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != vm.ID {
		t.Fatalf("filtered list: %v %+v", err, res.Items)
	}
	if res, _ := e.svc.ListVMs(ctx, e.view, application.VMFilter{Query: "100%_"}); len(res.Items) != 0 {
		t.Fatal("LIKE wildcards in q must be escaped")
	}
	if _, err := e.svc.ListVMs(ctx, e.view, application.VMFilter{State: "weird"}); err == nil {
		t.Fatal("unknown state filter accepted")
	}
	page, err := e.svc.ListVMs(ctx, e.view, application.VMFilter{Query: e.corr, Page: application.Page{Limit: 2}})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page 1: %v %d", err, len(page.Items))
	}
	if _, err := e.svc.ListVMs(ctx, e.view, application.VMFilter{Page: application.Page{Cursor: "x"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("cursor: %v", err)
	}
	if _, err := e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), "because"); err == nil {
		t.Fatal("free-text decommission reason accepted")
	}
	dec, err := e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), "retired")
	if err != nil || dec.State != "decommissioned" || dec.DecommissionedAt == nil || *dec.DecommissionReason != "retired" {
		t.Fatalf("decommission: %v %+v", err, dec)
	}
	if _, err := e.svc.ChangeVMState(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), "running"); !errors.Is(err, application.ErrDecommissioned) {
		t.Fatalf("change decommissioned: %v", err)
	}
	if again, err := e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, &dec.Version, "retired"); err != nil || again.Version != dec.Version {
		t.Fatalf("retried decommission must be idempotent: %v", err)
	}
	if _, err := e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), "migrated"); !errors.Is(err, application.ErrDecommissioned) {
		t.Fatalf("decommission twice with another reason: %v", err)
	}
	if _, err := e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, nil, "retired"); err == nil {
		t.Fatal("decommission without expectedVersion accepted")
	}
	// The name is free again after the tombstone.
	in.HypervisorAssetID, in.Name = &host, e.corr+"-vm1"
	if _, err := e.svc.CreateVM(ctx, e.caller(), e.manage, in); err != nil {
		t.Fatalf("name reuse after decommission: %v", err)
	}
	if _, err := e.svc.GetVM(ctx, e.view, e.uuid()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unknown vm: %v", err)
	}
	if _, err := e.svc.GetVM(ctx, application.Principal{}, vm.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("vm without permission: %v", err)
	}
	if e.count(`SELECT count(*) FROM platform.outbox_events WHERE correlation_id = $1 AND event_type = 'VirtualMachineChanged'`, e.corr) < 6 {
		t.Fatal("VirtualMachineChanged not published")
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND (after_data::text LIKE '%10.0.0.5%' OR metadata::text LIKE '%line1%')`, e.corr) != 0 {
		t.Fatal("address or notes leaked into the audit log")
	}
}

type failingAssets struct{}

func (failingAssets) Assets(context.Context, []string) (map[string]application.AssetInfo, error) {
	return nil, errors.New("assets backend down")
}

func TestHypervisorLookupFailurePropagatesAndIdsAreLowercased(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	host := e.newAsset("assigned")
	upper := strings.ToUpper(host)
	vm, err := e.svc.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-case", VCPU: 1, MemoryMB: 1, HypervisorAssetID: &upper})
	if err != nil || vm.HypervisorAssetID == nil || *vm.HypervisorAssetID != host {
		t.Fatalf("upper-case hypervisor id must be stored lower-case: %v %+v", err, vm)
	}
	broken := application.NewService(repository.New(e.pool), dir{}, failingAssets{})
	_, err = broken.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-broken", VCPU: 1, MemoryMB: 1, HypervisorAssetID: &host})
	if err == nil || errors.Is(err, application.ErrReferenceInvalid) {
		t.Fatalf("an Assets lookup failure must not look like an invalid reference: %v", err)
	}
	if _, err := broken.AssignVMHypervisor(ctx, e.caller(), e.manage, vm.ID, e.ver("virtual_machines", vm.ID), &host); err == nil || errors.Is(err, application.ErrReferenceInvalid) {
		t.Fatalf("assign: %v", err)
	}
}

func TestMoveRefusesUnusableAssetAndWarningsListIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _, k := e.rack(10)
	good, bad := e.newAsset("available"), e.newAsset("available")
	if _, err := e.place(k.ID, good, 1, 1, "front"); err != nil {
		t.Fatal(err)
	}
	pl, err := e.place(k.ID, bad, 3, 2, "front")
	if err != nil {
		t.Fatal(err)
	}
	// Disposed while placed: the placement keeps its units but cannot be moved.
	e.assets[bad] = application.AssetInfo{ID: bad, Reference: "AST-bad", Status: "disposed"}
	if _, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, pl.ID, &pl.Version, application.PlacementInput{RackID: k.ID, UPosition: 6, HeightU: 2, Face: "front"}); !errors.Is(err, application.ErrAssetUnusable) {
		t.Fatalf("move of a disposed asset: %v", err)
	}
	if e.count(`SELECT count(*) FROM infrastructure.rack_unit_occupancy WHERE placement_id = $1::uuid`, pl.ID) != 2 {
		t.Fatal("a refused move must keep the units")
	}
	w, err := e.svc.PlacementWarnings(ctx, e.view)
	if err != nil {
		t.Fatal(err)
	}
	var found *application.PlacementWarning
	for i := range w.Items {
		if w.Items[i].Placement.ID == pl.ID {
			found = &w.Items[i]
		}
		if w.Items[i].Placement.AssetID == good {
			t.Fatal("usable asset listed as warning")
		}
	}
	if found == nil || found.AssetStatus != "disposed" || found.AssetRef != "AST-bad" || found.RackName != "K1" {
		t.Fatalf("warnings: %+v", w)
	}
	// Removing the placement clears the warning.
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, pl.ID, e.ver("rack_placements", pl.ID), "error_correction"); err != nil {
		t.Fatal(err)
	}
	w, _ = e.svc.PlacementWarnings(ctx, e.view)
	for _, it := range w.Items {
		if it.Placement.ID == pl.ID {
			t.Fatal("removed placement still warned")
		}
	}
	noAssets := e.view
	noAssets.AssetsView = false
	if _, err := e.svc.PlacementWarnings(ctx, noAssets); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("warnings without assets.view: %v", err)
	}
	if _, err := e.svc.PlacementWarnings(ctx, application.Principal{AssetsView: true}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("warnings without infrastructure.view: %v", err)
	}
}

func TestStateChangesRequireExpectedVersion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, r, k := e.rack(10)
	pl, err := e.place(k.ID, e.newAsset("available"), 1, 1, "front")
	if err != nil {
		t.Fatal(err)
	}
	vm, err := e.svc.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-v", VCPU: 1, MemoryMB: 1})
	if err != nil {
		t.Fatal(err)
	}
	in := application.PlacementInput{RackID: k.ID, UPosition: 2, HeightU: 1, Face: "front"}
	checks := map[string]error{}
	_, checks["move"] = e.svc.MoveAsset(ctx, e.caller(), e.manage, pl.ID, nil, in)
	_, checks["remove"] = e.svc.RemoveAsset(ctx, e.caller(), e.manage, pl.ID, nil, "other")
	_, checks["state"] = e.svc.ChangeVMState(ctx, e.caller(), e.manage, vm.ID, nil, "running")
	_, checks["hypervisor"] = e.svc.AssignVMHypervisor(ctx, e.caller(), e.manage, vm.ID, nil, nil)
	_, checks["decommission"] = e.svc.DecommissionVM(ctx, e.caller(), e.manage, vm.ID, nil, "retired")
	_, checks["archive rack"] = e.svc.SetRackArchived(ctx, e.caller(), e.manage, k.ID, nil, true)
	_, checks["unarchive rack"] = e.svc.SetRackArchived(ctx, e.caller(), e.manage, k.ID, nil, false)
	_, checks["archive room"] = e.svc.SetRoomArchived(ctx, e.caller(), e.manage, r.ID, nil, true)
	_, checks["archive building"] = e.svc.SetBuildingArchived(ctx, e.caller(), e.manage, b.ID, nil, true)
	for name, err := range checks {
		var inv *application.InvalidInputError
		if !errors.As(err, &inv) {
			t.Errorf("%s without expectedVersion: %v", name, err)
		}
	}
	if e.count(`SELECT version FROM infrastructure.virtual_machines WHERE id = $1::uuid`, vm.ID) != 1 {
		t.Fatal("a refused request changed the record")
	}
}

func TestDatabaseEnforcesFitFaceAndParentState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, r, k := e.rack(10)
	asset := e.newAsset("available")
	pl, err := e.place(k.ID, asset, 9, 2, "front")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error { _, err := e.pool.Exec(ctx, sql, args...); return err }

	// Rack fit: a placement beyond the rack height or with a wrong height copy is refused.
	const ins = `INSERT INTO infrastructure.rack_placements(rack_id, asset_id, u_position, height_u, face, rack_height_u)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'front', $5)`
	if exec(ins, k.ID, e.uuid(), 10, 2, 10) == nil {
		t.Fatal("database accepted a placement beyond the rack")
	}
	if exec(ins, k.ID, e.uuid(), 10, 2, 60) == nil {
		t.Fatal("database accepted a forged rack height")
	}
	// Occupancy must match the placement's rack and face.
	if exec(`INSERT INTO infrastructure.rack_unit_occupancy(rack_id, face, u, placement_id) VALUES ($1::uuid, 'rear', 1, $2::uuid)`, k.ID, pl.ID) == nil {
		t.Fatal("database accepted an occupancy with the wrong face")
	}
	_, _, other := e.rack(10)
	if exec(`INSERT INTO infrastructure.rack_unit_occupancy(rack_id, face, u, placement_id) VALUES ($1::uuid, 'front', 1, $2::uuid)`, other.ID, pl.ID) == nil {
		t.Fatal("database accepted an occupancy in another rack")
	}
	// The rack height cannot change under a placement.
	if exec(`UPDATE infrastructure.racks SET height_u = 5 WHERE id = $1::uuid`, k.ID) == nil {
		t.Fatal("database accepted shrinking a rack with placements")
	}

	// Parent state: nothing active below an archived parent, no archive over active children.
	if exec(`UPDATE infrastructure.racks SET active = false WHERE id = $1::uuid`, k.ID) == nil {
		t.Fatal("database archived a rack with an active placement")
	}
	if exec(`UPDATE infrastructure.rooms SET active = false WHERE id = $1::uuid`, r.ID) == nil {
		t.Fatal("database archived a room with an active rack")
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, pl.ID, e.ver("rack_placements", pl.ID), "other"); err != nil {
		t.Fatal(err)
	}
	if err := exec(`UPDATE infrastructure.racks SET active = false WHERE id = $1::uuid`, k.ID); err != nil {
		t.Fatal(err)
	}
	if exec(ins, k.ID, e.uuid(), 1, 1, 10) == nil {
		t.Fatal("database accepted an active placement on an archived rack")
	}
	if err := exec(`UPDATE infrastructure.rooms SET active = false WHERE id = $1::uuid`, r.ID); err != nil {
		t.Fatal(err)
	}
	if exec(`UPDATE infrastructure.racks SET active = true WHERE id = $1::uuid`, k.ID) == nil {
		t.Fatal("database activated a rack in an archived room")
	}
	if exec(`INSERT INTO infrastructure.racks(room_id, name, height_u) VALUES ($1::uuid, 'late', 5)`, r.ID) == nil {
		t.Fatal("database accepted an active rack in an archived room")
	}
}

// Every active placement occupies exactly height_u units and a closed one none.
func TestOccupancyMatchesPlacements(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _, k1 := e.rack(20)
	_, _, k2 := e.rack(20)
	var open []application.Placement
	for i := 0; i < 4; i++ {
		p, err := e.place(k1.ID, e.newAsset("available"), 1+i*3, 1+i, "front")
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, p)
	}
	if _, err := e.svc.MoveAsset(ctx, e.caller(), e.manage, open[0].ID, &open[0].Version, application.PlacementInput{RackID: k2.ID, UPosition: 5, HeightU: 3, Face: "rear"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RemoveAsset(ctx, e.caller(), e.manage, open[1].ID, &open[1].Version, "relocated"); err != nil {
		t.Fatal(err)
	}
	const inScope = `rack_id IN ($1::uuid, $2::uuid)`
	bad := e.count(`SELECT count(*) FROM infrastructure.rack_placements p
		WHERE p.`+inScope+` AND (
		  (p.removed_at IS NULL AND (SELECT count(*) FROM infrastructure.rack_unit_occupancy o WHERE o.placement_id = p.id) <> p.height_u)
		  OR (p.removed_at IS NOT NULL AND EXISTS (SELECT 1 FROM infrastructure.rack_unit_occupancy o WHERE o.placement_id = p.id))
		  OR EXISTS (SELECT 1 FROM infrastructure.rack_unit_occupancy o WHERE o.placement_id = p.id AND (o.u < p.u_position OR o.u > p.u_position + p.height_u - 1)))`, k1.ID, k2.ID)
	if bad != 0 {
		t.Fatalf("%d placements violate the occupancy invariant", bad)
	}
	if e.count(`SELECT count(*) FROM infrastructure.rack_unit_occupancy WHERE `+inScope, k1.ID, k2.ID) != (3+4)+3 {
		t.Fatal("unexpected number of occupied units")
	}
}

func TestTreeIsBoundedAndReportsTruncation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO infrastructure.buildings(site_location_id, name)
		SELECT uuidv7(), 'tree-bound-' || g FROM generate_series(1, $1::int) g`, application.MaxTreeSites+1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM infrastructure.buildings WHERE name LIKE 'tree-bound-%'`)
	})
	tree, err := e.svc.Tree(ctx, e.view, false)
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Truncated || len(tree.Sites) != application.MaxTreeSites {
		t.Fatalf("truncated=%v sites=%d", tree.Truncated, len(tree.Sites))
	}
}

func TestPublicVMsLookup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	hv := e.newAsset("assigned")
	vm, err := e.svc.CreateVM(ctx, e.caller(), e.manage, application.VMInput{Name: e.corr + "-lookup", VCPU: 1, MemoryMB: 1, HypervisorAssetID: &hv})
	if err != nil {
		t.Fatal(err)
	}
	pub := public.New(e.svc)
	got, err := pub.VMs(ctx, []string{vm.ID, e.uuid(), "not-a-uuid"})
	if err != nil || len(got) != 1 || got[vm.ID].Name != vm.Name || got[vm.ID].HypervisorAssetID == nil || *got[vm.ID].HypervisorAssetID != hv {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if got[vm.ID].Decommissioned() {
		t.Fatal("running VM is not decommissioned")
	}
	if _, err := pub.VMs(ctx, make([]string, application.MaxLookupIDs+1)); err == nil {
		t.Fatal("lookup must be bounded")
	}
}

func TestPublicVMIDsWithHypervisorPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mine := map[string]bool{}
	for i, withHV := range []bool{true, true, true, false} {
		in := application.VMInput{Name: e.corr + "-page-" + string(rune('a'+i)), VCPU: 1, MemoryMB: 1}
		if withHV {
			hv := e.newAsset("assigned")
			in.HypervisorAssetID = &hv
		}
		vm, err := e.svc.CreateVM(ctx, e.caller(), e.manage, in)
		if err != nil {
			t.Fatal(err)
		}
		mine[vm.ID] = withHV
	}
	pub := public.New(e.svc)
	var got []string
	after := ""
	for {
		ids, err := pub.VMIDsWithHypervisor(ctx, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id <= after {
				t.Fatalf("ids must ascend: %s after %s", id, after)
			}
		}
		if len(ids) == 0 {
			break
		}
		got, after = append(got, ids...), ids[len(ids)-1]
	}
	found := 0
	for _, id := range got {
		if hv, ok := mine[id]; ok {
			if !hv {
				t.Fatalf("VM without hypervisor listed: %s", id)
			}
			found++
		}
	}
	if found != 3 {
		t.Fatalf("expected the 3 VMs with a hypervisor, found %d", found)
	}
	if _, err := pub.VMIDsWithHypervisor(ctx, "", 0); err == nil {
		t.Fatal("limit must be positive")
	}
	if _, err := pub.VMIDsWithHypervisor(ctx, "nope", 10); err == nil {
		t.Fatal("cursor must be a UUID")
	}
	if _, err := pub.VMIDsWithHypervisor(ctx, "", application.MaxLookupIDs+1); err == nil {
		t.Fatal("limit must be bounded")
	}
}
