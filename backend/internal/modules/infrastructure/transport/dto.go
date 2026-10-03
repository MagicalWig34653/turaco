package transport

import "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"

type buildingDTO struct {
	ID             string  `json:"id"`
	SiteLocationID string  `json:"siteLocationId"`
	Name           string  `json:"name"`
	AddressNote    *string `json:"addressNote"`
	Active         bool    `json:"active"`
	Version        int     `json:"version"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func toBuilding(b application.Building) buildingDTO {
	return buildingDTO{ID: b.ID, SiteLocationID: b.SiteLocationID, Name: b.Name, AddressNote: b.AddressNote, Active: b.Active,
		Version: b.Version, CreatedAt: ts(b.CreatedAt), UpdatedAt: ts(b.UpdatedAt)}
}

type roomDTO struct {
	ID         string  `json:"id"`
	BuildingID string  `json:"buildingId"`
	Name       string  `json:"name"`
	Floor      *string `json:"floor"`
	Active     bool    `json:"active"`
	Version    int     `json:"version"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

func toRoom(r application.Room) roomDTO {
	return roomDTO{ID: r.ID, BuildingID: r.BuildingID, Name: r.Name, Floor: r.Floor, Active: r.Active,
		Version: r.Version, CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt)}
}

type rackDTO struct {
	ID        string `json:"id"`
	RoomID    string `json:"roomId"`
	Name      string `json:"name"`
	HeightU   int    `json:"heightU"`
	Active    bool   `json:"active"`
	Version   int    `json:"version"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func toRack(r application.Rack) rackDTO {
	return rackDTO{ID: r.ID, RoomID: r.RoomID, Name: r.Name, HeightU: r.HeightU, Active: r.Active,
		Version: r.Version, CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt)}
}

type placementDTO struct {
	ID                  string  `json:"id"`
	RackID              string  `json:"rackId"`
	AssetID             string  `json:"assetId"`
	AssetReference      *string `json:"assetReference,omitempty"`
	UPosition           int     `json:"uPosition"`
	HeightU             int     `json:"heightU"`
	Face                string  `json:"face"`
	PlacedBy            *string `json:"placedBy"`
	PlacedAt            string  `json:"placedAt"`
	RemovedAt           *string `json:"removedAt"`
	RemovalReason       *string `json:"removalReason"`
	PreviousPlacementID *string `json:"previousPlacementId"`
	Version             int     `json:"version"`
}

func toPlacement(p application.Placement) placementDTO {
	return placementDTO{ID: p.ID, RackID: p.RackID, AssetID: p.AssetID, UPosition: p.UPosition, HeightU: p.HeightU, Face: p.Face,
		PlacedBy: p.PlacedBy, PlacedAt: ts(p.PlacedAt), RemovedAt: tsPtr(p.RemovedAt), RemovalReason: p.RemovalReason,
		PreviousPlacementID: p.PreviousID, Version: p.Version}
}

type rackDetailDTO struct {
	rackDTO
	Placements []placementDTO `json:"placements"`
}

type vmDTO struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	State              string  `json:"state"`
	HypervisorAssetID  *string `json:"hypervisorAssetId"`
	HypervisorRef      *string `json:"hypervisorAssetReference,omitempty"`
	VCPU               int     `json:"vcpu"`
	MemoryMB           int     `json:"memoryMb"`
	ManagementAddress  *string `json:"managementAddress"`
	NetworkNote        *string `json:"networkNote"`
	Notes              *string `json:"notes"`
	DecommissionReason *string `json:"decommissionReason"`
	DecommissionedAt   *string `json:"decommissionedAt"`
	Version            int     `json:"version"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

func toVM(v application.VirtualMachine) vmDTO {
	return vmDTO{ID: v.ID, Name: v.Name, State: v.State, HypervisorAssetID: v.HypervisorAssetID, VCPU: v.VCPU, MemoryMB: v.MemoryMB,
		ManagementAddress: v.ManagementAddress, NetworkNote: v.NetworkNote, Notes: v.Notes, DecommissionReason: v.DecommissionReason,
		DecommissionedAt: tsPtr(v.DecommissionedAt), Version: v.Version, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt)}
}

type locationDTO struct {
	Placed         bool    `json:"placed"`
	PlacementID    string  `json:"placementId,omitempty"`
	RackID         string  `json:"rackId,omitempty"`
	RackName       string  `json:"rackName,omitempty"`
	RoomID         string  `json:"roomId,omitempty"`
	RoomName       string  `json:"roomName,omitempty"`
	Floor          *string `json:"floor,omitempty"`
	BuildingID     string  `json:"buildingId,omitempty"`
	BuildingName   string  `json:"buildingName,omitempty"`
	SiteLocationID string  `json:"siteLocationId,omitempty"`
	SiteName       string  `json:"siteName,omitempty"`
	UPosition      int     `json:"uPosition,omitempty"`
	HeightU        int     `json:"heightU,omitempty"`
	Face           string  `json:"face,omitempty"`
	PlacedAt       string  `json:"placedAt,omitempty"`
}

type buildingSummaryDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Rooms   int    `json:"rooms"`
	Racks   int    `json:"racks"`
	Placed  int    `json:"placedAssets"`
	Version int    `json:"version"`
}

type siteDTO struct {
	LocationID string               `json:"locationId"`
	Name       string               `json:"name"`
	Buildings  int                  `json:"buildings"`
	Rooms      int                  `json:"rooms"`
	Racks      int                  `json:"racks"`
	Placed     int                  `json:"placedAssets"`
	Items      []buildingSummaryDTO `json:"buildingItems"`
}
