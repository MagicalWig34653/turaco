package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type versionBody struct {
	ExpectedVersion *int `json:"expectedVersion"`
}

// ---- tree ----

func (h *handler) tree(w http.ResponseWriter, r *http.Request) {
	sites, err := h.svc.Tree(r.Context(), principal(r), flag(r, "includeArchived"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items []siteDTO `json:"items"`
	}{Items: make([]siteDTO, 0, len(sites))}
	for _, s := range sites {
		d := siteDTO{LocationID: s.LocationID, Name: s.Name, Buildings: len(s.Buildings), Items: make([]buildingSummaryDTO, 0, len(s.Buildings))}
		for _, b := range s.Buildings {
			d.Rooms, d.Racks, d.Placed = d.Rooms+b.Rooms, d.Racks+b.Racks, d.Placed+b.Placed
			d.Items = append(d.Items, buildingSummaryDTO{ID: b.ID, Name: b.Name, Active: b.Active, Rooms: b.Rooms, Racks: b.Racks, Placed: b.Placed, Version: b.Version})
		}
		out.Items = append(out.Items, d)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- buildings ----

func (h *handler) listBuildings(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListBuildings(r.Context(), principal(r), r.URL.Query().Get("siteLocationId"), flag(r, "includeArchived"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toBuilding))
}

func (h *handler) getBuilding(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetBuilding(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBuilding(out))
}

func (h *handler) createBuilding(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SiteLocationID string `json:"siteLocationId"`
		Name           string `json:"name"`
		AddressNote    string `json:"addressNote"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateBuilding(r.Context(), caller(w, r), principal(r), b.SiteLocationID, b.Name, b.AddressNote)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toBuilding(out))
}

func (h *handler) updateBuilding(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            *string `json:"name"`
		AddressNote     *string `json:"addressNote"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) || !needVersion(w, b.ExpectedVersion) {
		return
	}
	out, err := h.svc.UpdateBuilding(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion,
		application.BuildingUpdate{Name: b.Name, AddressNote: b.AddressNote})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBuilding(out))
}

func (h *handler) buildingArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetBuildingArchived(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, archived)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toBuilding(out))
	}
}

// ---- rooms ----

func (h *handler) listRooms(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListRooms(r.Context(), principal(r), r.PathValue("id"), flag(r, "includeArchived"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toRoom))
}

func (h *handler) getRoom(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetRoom(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoom(out))
}

func (h *handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name  string `json:"name"`
		Floor string `json:"floor"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateRoom(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Name, b.Floor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRoom(out))
}

func (h *handler) updateRoom(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            *string `json:"name"`
		Floor           *string `json:"floor"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) || !needVersion(w, b.ExpectedVersion) {
		return
	}
	out, err := h.svc.UpdateRoom(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, application.RoomUpdate{Name: b.Name, Floor: b.Floor})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRoom(out))
}

func (h *handler) roomArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetRoomArchived(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, archived)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRoom(out))
	}
}

// ---- racks ----

func (h *handler) listRacks(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListRacks(r.Context(), principal(r), r.PathValue("id"), flag(r, "includeArchived"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toRack))
}

func (h *handler) getRack(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetRack(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ids := make([]string, 0, len(d.Placements))
	for _, p := range d.Placements {
		ids = append(ids, p.AssetID)
	}
	refs, err := h.svc.AssetReferences(r.Context(), ids)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := rackDetailDTO{rackDTO: toRack(d.Rack), Placements: make([]placementDTO, 0, len(d.Placements))}
	for _, p := range d.Placements {
		dto := toPlacement(p)
		if ref, ok := refs[p.AssetID]; ok {
			dto.AssetReference = &ref
		}
		out.Placements = append(out.Placements, dto)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) createRack(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		HeightU int    `json:"heightU"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateRack(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.Name, b.HeightU)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRack(out))
}

func (h *handler) renameRack(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name            string `json:"name"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) || !needVersion(w, b.ExpectedVersion) {
		return
	}
	out, err := h.svc.RenameRack(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, b.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRack(out))
}

func (h *handler) rackArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := h.svc.SetRackArchived(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, archived)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRack(out))
	}
}

// ---- placements ----

func (h *handler) listPlacements(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListPlacements(r.Context(), principal(r), r.PathValue("id"), flag(r, "includeRemoved"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toPlacement)
	ids := make([]string, 0, len(res.Items))
	for _, p := range res.Items {
		ids = append(ids, p.AssetID)
	}
	if out.Names, err = h.svc.AssetReferences(r.Context(), ids); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) getPlacement(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetPlacement(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPlacement(out))
}

type placementBody struct {
	RackID    string `json:"rackId"`
	AssetID   string `json:"assetId"`
	UPosition int    `json:"uPosition"`
	HeightU   int    `json:"heightU"`
	Face      string `json:"face"`
}

func (h *handler) place(w http.ResponseWriter, r *http.Request) {
	var b placementBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.PlaceAsset(r.Context(), caller(w, r), principal(r),
		application.PlacementInput{RackID: b.RackID, AssetID: b.AssetID, UPosition: b.UPosition, HeightU: b.HeightU, Face: b.Face})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toPlacement(out))
}

func (h *handler) move(w http.ResponseWriter, r *http.Request) {
	var b struct {
		placementBody
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.MoveAsset(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion,
		application.PlacementInput{RackID: b.RackID, UPosition: b.UPosition, HeightU: b.HeightU, Face: b.Face})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPlacement(out))
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.RemoveAsset(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPlacement(out))
}

func (h *handler) assetLocation(w http.ResponseWriter, r *http.Request) {
	loc, err := h.svc.WhereIs(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if loc == nil {
		httpx.JSON(w, http.StatusOK, locationDTO{Placed: false})
		return
	}
	name := ""
	if sites, err := h.svc.SiteNames(r.Context(), []string{loc.SiteLocationID}); err == nil {
		name = sites[loc.SiteLocationID]
	}
	httpx.JSON(w, http.StatusOK, locationDTO{Placed: true, PlacementID: loc.PlacementID, RackID: loc.RackID, RackName: loc.RackName,
		RoomID: loc.RoomID, RoomName: loc.RoomName, Floor: loc.Floor, BuildingID: loc.BuildingID, BuildingName: loc.BuildingName,
		SiteLocationID: loc.SiteLocationID, SiteName: name, UPosition: loc.UPosition, HeightU: loc.HeightU, Face: loc.Face, PlacedAt: ts(loc.PlacedAt)})
}

// ---- virtual machines ----

func (h *handler) listVMs(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListVMs(r.Context(), principal(r), application.VMFilter{State: v.Get("state"), HypervisorAssetID: v.Get("hypervisorAssetId"), Query: v.Get("q"), Page: page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := toList(res, toVM)
	var ids []string
	for _, vm := range res.Items {
		if vm.HypervisorAssetID != nil {
			ids = append(ids, *vm.HypervisorAssetID)
		}
	}
	if out.Names, err = h.svc.AssetReferences(r.Context(), ids); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) getVM(w http.ResponseWriter, r *http.Request) {
	vm, err := h.svc.GetVM(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusOK, vm)
}

// respondVM adds the hypervisor's Asset reference to a single VM response.
func (h *handler) respondVM(w http.ResponseWriter, r *http.Request, status int, vm application.VirtualMachine) {
	dto := toVM(vm)
	if vm.HypervisorAssetID != nil {
		refs, err := h.svc.AssetReferences(r.Context(), []string{*vm.HypervisorAssetID})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if ref, ok := refs[*vm.HypervisorAssetID]; ok {
			dto.HypervisorRef = &ref
		}
	}
	httpx.JSON(w, status, dto)
}

func (h *handler) createVM(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name              string  `json:"name"`
		State             string  `json:"state"`
		HypervisorAssetID *string `json:"hypervisorAssetId"`
		VCPU              int     `json:"vcpu"`
		MemoryMB          int     `json:"memoryMb"`
		ManagementAddress string  `json:"managementAddress"`
		NetworkNote       string  `json:"networkNote"`
		Notes             string  `json:"notes"`
	}
	if !decode(w, r, &b) {
		return
	}
	vm, err := h.svc.CreateVM(r.Context(), caller(w, r), principal(r), application.VMInput{Name: b.Name, State: b.State, HypervisorAssetID: b.HypervisorAssetID,
		VCPU: b.VCPU, MemoryMB: b.MemoryMB, ManagementAddress: b.ManagementAddress, NetworkNote: b.NetworkNote, Notes: b.Notes})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusCreated, vm)
}

func (h *handler) updateVM(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name              *string `json:"name"`
		VCPU              *int    `json:"vcpu"`
		MemoryMB          *int    `json:"memoryMb"`
		ManagementAddress *string `json:"managementAddress"`
		NetworkNote       *string `json:"networkNote"`
		Notes             *string `json:"notes"`
		ExpectedVersion   *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) || !needVersion(w, b.ExpectedVersion) {
		return
	}
	vm, err := h.svc.UpdateVMDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion, application.VMDetails{
		Name: b.Name, VCPU: b.VCPU, MemoryMB: b.MemoryMB, ManagementAddress: b.ManagementAddress, NetworkNote: b.NetworkNote, Notes: b.Notes})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusOK, vm)
}

func (h *handler) vmState(w http.ResponseWriter, r *http.Request) {
	var b struct {
		State           string `json:"state"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	vm, err := h.svc.ChangeVMState(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.State)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusOK, vm)
}

func (h *handler) vmHypervisor(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AssetID         *string `json:"assetId"`
		ExpectedVersion *int    `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	vm, err := h.svc.AssignVMHypervisor(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.AssetID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusOK, vm)
}

func (h *handler) vmDecommission(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	vm, err := h.svc.DecommissionVM(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.respondVM(w, r, http.StatusOK, vm)
}
