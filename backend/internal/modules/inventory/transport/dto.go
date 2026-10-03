package transport

import "github.com/MagicalWig34653/turaco/backend/internal/modules/inventory/application"

type warehouseDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	LocationID *string `json:"locationId"`
	Active     bool    `json:"active"`
	Version    int     `json:"version"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

func toWarehouse(w application.Warehouse) warehouseDTO {
	return warehouseDTO{ID: w.ID, Name: w.Name, LocationID: w.LocationID, Active: w.Active, Version: w.Version, CreatedAt: ts(w.CreatedAt), UpdatedAt: ts(w.UpdatedAt)}
}

type locationDTO struct {
	ID          string `json:"id"`
	WarehouseID string `json:"warehouseId"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func toLocation(l application.StorageLocation) locationDTO {
	return locationDTO{ID: l.ID, WarehouseID: l.WarehouseID, Name: l.Name, Active: l.Active, Version: l.Version, CreatedAt: ts(l.CreatedAt), UpdatedAt: ts(l.UpdatedAt)}
}

type balanceDTO struct {
	ProductID         string `json:"productId"`
	StorageLocationID string `json:"storageLocationId"`
	WarehouseID       string `json:"warehouseId"`
	OnHand            int    `json:"onHand"`
	Reserved          int    `json:"reserved"`
	Available         int    `json:"available"`
	UpdatedAt         string `json:"updatedAt"`
}

func toBalance(b application.Balance) balanceDTO {
	return balanceDTO{ProductID: b.ProductID, StorageLocationID: b.StorageLocationID, WarehouseID: b.WarehouseID,
		OnHand: b.OnHand, Reserved: b.Reserved, Available: b.Available(), UpdatedAt: ts(b.UpdatedAt)}
}

type transactionDTO struct {
	ID                string  `json:"id"`
	Type              string  `json:"type"`
	ProductID         string  `json:"productId"`
	StorageLocationID string  `json:"storageLocationId"`
	OnHandDelta       int     `json:"onHandDelta"`
	ReservedDelta     int     `json:"reservedDelta"`
	GroupID           string  `json:"groupId"`
	ReservationID     *string `json:"reservationId"`
	ContextType       *string `json:"contextType"`
	ContextID         *string `json:"contextId"`
	Reason            *string `json:"reason"`
	ActorUserID       *string `json:"actorUserId"`
	CreatedAt         string  `json:"createdAt"`
}

func toTransaction(t application.Transaction) transactionDTO {
	return transactionDTO{ID: t.ID, Type: t.Type, ProductID: t.ProductID, StorageLocationID: t.StorageLocationID, OnHandDelta: t.OnHandDelta,
		ReservedDelta: t.ReservedDelta, GroupID: t.GroupID, ReservationID: t.ReservationID, ContextType: t.ContextType, ContextID: t.ContextID,
		Reason: t.Reason, ActorUserID: t.ActorUserID, CreatedAt: ts(t.CreatedAt)}
}

type reservationDTO struct {
	ID                string  `json:"id"`
	Kind              string  `json:"kind"`
	ProductID         string  `json:"productId"`
	StorageLocationID *string `json:"storageLocationId"`
	Quantity          *int    `json:"quantity"`
	AssetID           *string `json:"assetId"`
	Status            string  `json:"status"`
	ContextType       *string `json:"contextType"`
	ContextID         *string `json:"contextId"`
	Reason            *string `json:"reason"`
	ClosedAt          *string `json:"closedAt"`
	CreatedBy         *string `json:"createdBy"`
	Version           int     `json:"version"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
}

func toReservation(x application.Reservation) reservationDTO {
	return reservationDTO{ID: x.ID, Kind: x.Kind, ProductID: x.ProductID, StorageLocationID: x.StorageLocationID, Quantity: x.Quantity,
		AssetID: x.AssetID, Status: x.Status, ContextType: x.ContextType, ContextID: x.ContextID, Reason: x.Reason, ClosedAt: tsPtr(x.ClosedAt),
		CreatedBy: x.CreatedBy, Version: x.Version, CreatedAt: ts(x.CreatedAt), UpdatedAt: ts(x.UpdatedAt)}
}
