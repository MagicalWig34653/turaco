package application

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Location moves that affect Location-scoped role assignments (ADR-0040, F16 design L10, slice L-C). MoveLocation
// publishes EventScopedLocationMoved in the move's transaction when the count is not zero; this outbox consumer tells
// every other platform administrator (the actor is never notified). It runs in the dispatcher's transaction, writes
// only through tx and is idempotent (durable dedupe key per event and recipient). The notification carries the
// Location name and the count, nothing about the assignments themselves.

const (
	// EventScopedLocationMoved is published by MoveLocation when the move affected Location-scoped role assignments.
	EventScopedLocationMoved = "ScopedLocationMoved"
	// NotificationScopedLocationMoved: a Location move changed what Location-scoped role assignments cover.
	NotificationScopedLocationMoved = "organization.scoped_location_moved"
	// LinkLocationAdmin points at the Location administration.
	LinkLocationAdmin = "location_admin"
)

// locationMoveCategory is the notification category of administrator notices about Location moves.
func locationMoveCategory() notifications.Category {
	return notifications.Category{Name: NotificationScopedLocationMoved, Owner: "organization", LinkType: LinkLocationAdmin,
		LinkPath: "/admin/locations?location={id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Location moved with scoped access: %s", Intro: "A Location was moved; this changes what Location-scoped role assignments cover:",
				Action: "Open Locations"},
			"de": {Subject: "Standort mit standortbezogenen Rechten verschoben: %s", Intro: "Ein Standort wurde verschoben; das ändert, was standortbezogene Rollenzuweisungen abdecken:",
				Action: "Standorte öffnen"},
		}}
}

// AdministratorDirectory lists the platform administrators (platform/authorization/roles.Guards).
type AdministratorDirectory interface {
	AdministratorUserIDs(ctx context.Context, tx pgx.Tx) ([]string, error)
}

// LocationMoveStore reads what the consumer needs, inside the consumer's transaction.
type LocationMoveStore interface {
	// LocationNameTx returns the current name of a Location; found is false for an unknown id.
	LocationNameTx(ctx context.Context, tx pgx.Tx, id string) (name string, found bool, err error)
	// IsActiveEmployeeTx reports whether the user is an active internal employee account.
	IsActiveEmployeeTx(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
}

// LocationMoveConsumers turn ScopedLocationMoved events into notifications for the other platform administrators.
type LocationMoveConsumers struct {
	store    LocationMoveStore
	admins   AdministratorDirectory
	notifier PartyNotifier
}

func NewLocationMoveConsumers(store LocationMoveStore, admins AdministratorDirectory, notifier PartyNotifier) *LocationMoveConsumers {
	return &LocationMoveConsumers{store: store, admins: admins, notifier: notifier}
}

// OnScopedLocationMoved (event ScopedLocationMoved) notifies every active administrator except the actor.
func (c *LocationMoveConsumers) OnScopedLocationMoved(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		LocationID                string `json:"locationId"`
		ScopedAssignmentsAffected int    `json:"scopedAssignmentsAffected"`
	}
	if err := decodeEvent(ev, &p); err != nil {
		return err
	}
	name, found, err := c.store.LocationNameTx(ctx, tx, p.LocationID)
	if err != nil || !found {
		return err
	}
	admins, err := c.admins.AdministratorUserIDs(ctx, tx)
	if err != nil {
		return fmt.Errorf("list administrators: %w", err)
	}
	for _, id := range admins {
		if ev.ActorID != nil && *ev.ActorID == id {
			continue
		}
		active, err := c.store.IsActiveEmployeeTx(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("check recipient: %w", err)
		}
		if !active {
			continue
		}
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{RecipientUserID: id, Category: NotificationScopedLocationMoved,
			Params:   map[string]any{"title": name, "count": p.ScopedAssignmentsAffected},
			LinkType: LinkLocationAdmin, LinkID: p.LocationID, DedupeKey: ev.ID + ":" + id}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
