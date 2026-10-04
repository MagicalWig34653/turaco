package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories Planning creates:
// initiative.state tells the owner that their Initiative changed status. The
// reference is the only Initiative content included in the notification.
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: NotificationCategory, Owner: "planning", LinkType: "initiative", LinkPath: "/initiatives/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Initiative status updated: %s", Intro: "The status of an initiative you own changed:", Action: "Open initiative"},
			"de": {Subject: "Status einer Initiative aktualisiert: %s", Intro: "Der Status einer Initiative, die du verantwortest, hat sich geändert:", Action: "Initiative öffnen"},
		},
	}}
}

// Notifications turns InitiativeStatusChanged into a notification for the
// owner. It runs in the outbox dispatcher's claim transaction, writes only
// through tx and is idempotent (dedupe key per event and recipient).
type Notifications struct {
	store    Store
	dir      Directory
	notifier Notifier
}

func NewNotifications(store Store, dir Directory, notifier Notifier) *Notifications {
	return &Notifications{store: store, dir: dir, notifier: notifier}
}

// OnStatusChanged tells the owner of the Initiative about its new status,
// unless the owner caused the change or is no longer active.
func (n *Notifications) OnStatusChanged(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		InitiativeID string `json:"initiativeId"`
		OwnerID      string `json:"ownerId"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !uuidPattern.MatchString(p.InitiativeID) || !uuidPattern.MatchString(p.OwnerID) {
		return events.Permanent(fmt.Errorf("invalid initiative or owner id in %s", ev.EventType))
	}
	p.InitiativeID, p.OwnerID = strings.ToLower(p.InitiativeID), strings.ToLower(p.OwnerID)
	if ev.ActorID != nil && *ev.ActorID == p.OwnerID {
		return nil
	}
	active, err := n.dir.ActiveUsers(ctx, []string{p.OwnerID})
	if err != nil {
		return fmt.Errorf("check recipient: %w", err)
	}
	if !active[p.OwnerID] {
		return nil
	}
	i, err := n.store.Get(ctx, p.InitiativeID)
	if err != nil {
		return fmt.Errorf("load initiative reference: %w", err)
	}
	if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
		RecipientUserID: p.OwnerID, Category: NotificationCategory,
		Params:   map[string]any{"title": i.Reference, "status": p.Status},
		LinkType: "initiative", LinkID: i.ID, DedupeKey: ev.ID + ":" + p.OwnerID,
	}); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}
