package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories the Assets module creates.
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: "asset.assigned", Owner: "assets", LinkType: "asset", LinkPath: "/assets/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Equipment assigned: %s", Intro: "Equipment was assigned to you:", Action: "Open equipment"},
			"de": {Subject: "Ausstattung zugewiesen: %s", Intro: "Dir wurde Ausstattung zugewiesen:", Action: "Ausstattung öffnen"},
		},
	}}
}

// Notifier creates notifications inside the consumer's transaction.
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// Consumers turn asset events into notifications. They run in the outbox
// dispatcher's claim transaction, write only through tx and are idempotent.
type Consumers struct {
	store    Store
	dir      Directory
	notifier Notifier
}

func NewConsumers(store Store, dir Directory, notifier Notifier) *Consumers {
	return &Consumers{store: store, dir: dir, notifier: notifier}
}

// OnAssetAssigned tells a User that equipment was assigned to them. The effect
// is derived from the asset's current assignment, so a stale event (the asset
// was returned or reassigned meanwhile) notifies nobody, and the User who made
// the assignment is not notified about their own action.
func (c *Consumers) OnAssetAssigned(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		AssetID      string `json:"assetId"`
		AssigneeType string `json:"assigneeType"`
		AssigneeID   string `json:"assigneeId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode AssetAssigned payload: %w", err))
	}
	if p.AssigneeType != AssigneeUser || (ev.ActorID != nil && *ev.ActorID == p.AssigneeID) {
		return nil
	}
	asset, err := c.store.Get(ctx, p.AssetID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	assignments, err := c.store.Assignments(ctx, asset.ID)
	if err != nil {
		return err
	}
	current := false
	for _, a := range assignments {
		if a.ReturnedAt == nil && a.AssigneeType == AssigneeUser && a.AssigneeID == p.AssigneeID {
			current = true
		}
	}
	if !current {
		return nil
	}
	active, err := c.dir.ActiveUsers(ctx, []string{p.AssigneeID})
	if err != nil {
		return fmt.Errorf("check recipient: %w", err)
	}
	if !active[p.AssigneeID] {
		return nil
	}
	if _, err := c.notifier.Create(ctx, tx, notifications.Intent{
		RecipientUserID: p.AssigneeID, Category: "asset.assigned", Params: map[string]any{"title": asset.Reference},
		LinkType: "asset", LinkID: asset.ID, DedupeKey: ev.ID + ":" + p.AssigneeID,
	}); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}
