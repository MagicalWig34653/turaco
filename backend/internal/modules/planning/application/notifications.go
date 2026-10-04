package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories Planning creates:
// initiative.state tells the owner that their Initiative changed status. The
// owner may always read their Initiative, so the title never reaches someone
// who cannot open it.
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
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !uuidPattern.MatchString(p.InitiativeID) {
		return events.Permanent(fmt.Errorf("invalid initiative id in %s", ev.EventType))
	}
	i, err := n.store.Get(ctx, strings.ToLower(p.InitiativeID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if ev.ActorID != nil && *ev.ActorID == i.OwnerID {
		return nil
	}
	active, err := n.dir.ActiveUsers(ctx, []string{i.OwnerID})
	if err != nil {
		return fmt.Errorf("check recipient: %w", err)
	}
	if !active[i.OwnerID] {
		return nil
	}
	if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
		RecipientUserID: i.OwnerID, Category: NotificationCategory,
		Params:   map[string]any{"title": i.Reference + " · " + i.Title, "status": p.Status},
		LinkType: "initiative", LinkID: i.ID, DedupeKey: ev.ID + ":" + i.OwnerID,
	}); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}
