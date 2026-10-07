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

// NotificationCategories are the categories Remote Access creates: remoteaccess.session_started tells the
// Device's holder that a technician started a session (ADR-0026: the end user always sees that a session is
// active). Approvers are notified by the Approvals module; the notification carries the reference only and no link
// (end users cannot open sessions).
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: NotificationCategory, Owner: "remoteaccess",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "A technician started a remote session on your device: %s", Intro: "A remote support session was started on a device assigned to you:", Action: "Open Turaco"},
			"de": {Subject: "Ein Techniker hat eine Fernwartungssitzung auf deinem Gerät gestartet: %s", Intro: "Auf einem dir zugewiesenen Gerät wurde eine Fernwartungssitzung gestartet:", Action: "Turaco öffnen"},
		},
	}}
}

// Notifications turns RemoteAccessSessionLaunched into a notification for the Device's holder. It runs in the
// outbox dispatcher's claim transaction, writes only through tx and is idempotent (dedupe key per event).
type Notifications struct {
	store    Store
	devices  Devices
	holders  Holders
	dir      Directory
	notifier Notifier
}

func NewNotifications(store Store, devices Devices, holders Holders, dir Directory, notifier Notifier) *Notifications {
	return &Notifications{store: store, devices: devices, holders: holders, dir: dir, notifier: notifier}
}

// OnSessionLaunched tells the current holder of the Device (an active User, not the technician) that a session
// was started. No holder means nobody to tell.
func (n *Notifications) OnSessionLaunched(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		SessionID string `json:"sessionId"`
		DeviceID  string `json:"deviceId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !uuidPattern.MatchString(p.SessionID) || !uuidPattern.MatchString(p.DeviceID) {
		return events.Permanent(fmt.Errorf("invalid session or device id in %s", ev.EventType))
	}
	dev, ok, err := n.devices.Device(ctx, strings.ToLower(p.DeviceID))
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	if !ok || dev.AssetID == nil {
		return nil
	}
	holders, err := n.holders.UserHolders(ctx, []string{*dev.AssetID})
	if err != nil {
		return fmt.Errorf("read holder: %w", err)
	}
	holder := holders[*dev.AssetID]
	if holder == "" || (ev.ActorID != nil && *ev.ActorID == holder) {
		return nil
	}
	active, err := n.dir.ActiveUsers(ctx, []string{holder})
	if err != nil {
		return fmt.Errorf("check recipient: %w", err)
	}
	if !active[holder] {
		return nil
	}
	sess, err := n.store.GetSession(ctx, strings.ToLower(p.SessionID))
	if err != nil {
		return fmt.Errorf("load session reference: %w", err)
	}
	if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
		RecipientUserID: holder, Category: NotificationCategory, Params: map[string]any{"title": sess.Reference},
		DedupeKey: ev.ID + ":" + holder,
	}); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}
