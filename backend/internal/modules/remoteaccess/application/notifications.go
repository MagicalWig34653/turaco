package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the categories Remote Access creates: remoteaccess.session_started tells the
// Device's holder and the Ticket's affected user that a technician started a session (ADR-0026: the end user always sees that a session is
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

// Notifications turns RemoteAccessSessionLaunched into notifications for the Device's holder and the Ticket's
// affected user. It runs in the outbox dispatcher's claim transaction, writes only through tx and is idempotent
// (dedupe key per event and recipient).
type Notifications struct {
	store    Store
	devices  Devices
	holders  Holders
	tickets  Tickets
	dir      Directory
	notifier Notifier
}

func NewNotifications(store Store, devices Devices, holders Holders, tickets Tickets, dir Directory, notifier Notifier) *Notifications {
	return &Notifications{store: store, devices: devices, holders: holders, tickets: tickets, dir: dir, notifier: notifier}
}

// OnSessionLaunched tells the Device's current holder and the Ticket's affected user (active users other than the
// technician, each once) that a session was started. The request gates guarantee that at least one of them can be
// told; a recipient that became inactive since is skipped.
func (n *Notifications) OnSessionLaunched(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		SessionID string `json:"sessionId"`
		DeviceID  string `json:"deviceId"`
		TicketID  string `json:"ticketId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !uuidPattern.MatchString(p.SessionID) || !uuidPattern.MatchString(p.DeviceID) || !uuidPattern.MatchString(p.TicketID) {
		return events.Permanent(fmt.Errorf("invalid session, device or ticket id in %s", ev.EventType))
	}
	var candidates []string
	dev, ok, err := n.devices.Device(ctx, strings.ToLower(p.DeviceID))
	if err != nil {
		return fmt.Errorf("read device: %w", err)
	}
	if ok && dev.AssetID != nil {
		holders, err := n.holders.UserHolders(ctx, []string{*dev.AssetID})
		if err != nil {
			return fmt.Errorf("read holder: %w", err)
		}
		candidates = append(candidates, holders[*dev.AssetID])
	}
	t, ok, err := n.tickets.Ticket(ctx, strings.ToLower(p.TicketID))
	if err != nil {
		return fmt.Errorf("read ticket: %w", err)
	}
	if ok {
		candidates = append(candidates, t.AffectedUserID)
	}
	var ids []string
	for _, id := range candidates {
		if id != "" && !(ev.ActorID != nil && *ev.ActorID == id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	active, err := n.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return fmt.Errorf("check recipients: %w", err)
	}
	sess, err := n.store.GetSession(ctx, strings.ToLower(p.SessionID))
	if err != nil {
		return fmt.Errorf("load session reference: %w", err)
	}
	for _, id := range ids {
		if !active[id] {
			continue
		}
		if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: NotificationCategory, Params: map[string]any{"title": sess.Reference},
			DedupeKey: ev.ID + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
