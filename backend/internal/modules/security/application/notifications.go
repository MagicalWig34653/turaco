package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// NotificationCategories are the notification categories Security creates: security.advisory tells the
// holders of security.manage that an Advisory became applicable. The reference and title are the only
// Advisory content included, and only Users who hold security.manage when the notification is created
// receive it.
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: NotificationCategory, Owner: "security", LinkType: "security_advisory", LinkPath: "/security/advisories/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Security advisory applicable: %s", Intro: "A security advisory was marked as applicable:", Action: "Open advisory"},
			"de": {Subject: "Sicherheitshinweis betrifft uns: %s", Intro: "Ein Sicherheitshinweis wurde als zutreffend markiert:", Action: "Hinweis öffnen"},
		},
	}}
}

// notifyChunk is how many candidate recipients one consumer run handles; the rest follows in a
// SecurityAdvisoryPublishedFanOut event.
const notifyChunk = 500

// Notifications turns SecurityAdvisoryPublished into security.advisory notifications. It runs in the
// outbox dispatcher's claim transaction, writes only through tx and is idempotent (dedupe key per event
// and recipient).
type Notifications struct {
	store    Store
	dir      Directory
	holders  PermissionHolders
	perms    PermissionResolver
	notifier Notifier
	chunk    int
}

func NewNotifications(store Store, dir Directory, holders PermissionHolders, perms PermissionResolver, notifier Notifier) *Notifications {
	return &Notifications{store: store, dir: dir, holders: holders, perms: perms, notifier: notifier, chunk: notifyChunk}
}

// WithChunk overrides the number of candidates per consumer run (tests).
func (n *Notifications) WithChunk(chunk int) *Notifications {
	n.chunk = chunk
	return n
}

// OnAdvisoryPublished tells security.manage holders (candidates: active Users, each checked against
// their effective permissions including Directory Group assignments) that an Advisory
// became applicable; the User who marked it is not told. It consumes SecurityAdvisoryPublished and its
// continuation SecurityAdvisoryPublishedFanOut (payload "after"). A stale event (the Advisory left the
// applicable states) notifies nobody.
func (n *Notifications) OnAdvisoryPublished(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		AdvisoryID string `json:"advisoryId"`
		After      string `json:"after"`
		SourceID   string `json:"sourceEventId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !uuidPattern.MatchString(p.AdvisoryID) || p.After != "" && !uuidPattern.MatchString(p.After) {
		return events.Permanent(fmt.Errorf("invalid ids in %s", ev.EventType))
	}
	a, err := n.store.GetAdvisory(ctx, strings.ToLower(p.AdvisoryID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !slices.Contains([]string{AdvisoryApplicable, AdvisoryRemediationPlanned, AdvisoryRemediating}, a.Status) {
		return nil
	}
	source := ev.ID
	if p.SourceID != "" {
		source = p.SourceID
	}
	candidates, err := n.holders.UsersWithPermission(ctx, PermManage, strings.ToLower(p.After), n.chunk+1)
	if err != nil {
		return fmt.Errorf("list security.manage holders: %w", err)
	}
	if len(candidates) > n.chunk {
		candidates = candidates[:n.chunk]
		if err := events.Publish(ctx, tx, events.Publication{Type: EventAdvisoryPublishedFanOut, ActorID: ev.ActorID, CorrelationID: ev.CorrelationID,
			Payload: map[string]any{"advisoryId": a.ID, "after": candidates[len(candidates)-1], "sourceEventId": source}}); err != nil {
			return fmt.Errorf("continue fan-out: %w", err)
		}
	}
	var ids []string
	for _, id := range candidates {
		if ev.ActorID == nil || *ev.ActorID != id {
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
	for _, id := range ids {
		if !active[id] {
			continue
		}
		perms, err := n.perms.Permissions(ctx, id)
		if err != nil {
			return fmt.Errorf("load recipient permissions: %w", err)
		}
		if _, ok := perms[PermManage]; !ok {
			continue
		}
		if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: NotificationCategory, Params: map[string]any{"title": a.Reference + " · " + a.Title},
			LinkType: "security_advisory", LinkID: a.ID, DedupeKey: source + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
