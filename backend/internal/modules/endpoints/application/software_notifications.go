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

// SoftwareApprovalCategory tells the holders of software.approve that a Software Version awaits approval.
const SoftwareApprovalCategory = "software.approval_requested"

// NotificationCategories are the notification categories Endpoints creates. software.approval_requested carries
// the product name and version as its only content and goes only to Users who hold software.approve when it is
// created (never to the person who registered or requested the version, who may not approve it).
func NotificationCategories() []notifications.Category {
	return []notifications.Category{{
		Name: SoftwareApprovalCategory, Owner: "endpoints", LinkType: "software_version", LinkPath: "/software/versions/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Software version awaits approval: %s", Intro: "A software version awaits your approval:", Action: "Open version"},
			"de": {Subject: "Softwareversion wartet auf Freigabe: %s", Intro: "Eine Softwareversion wartet auf Ihre Freigabe:", Action: "Version öffnen"},
		},
	}, {
		// deployment.attention carries only the Deployment reference and goes to the Deployment owner (F9 G4).
		Name: DeploymentAttentionCategory, Owner: "endpoints", LinkType: "deployment", LinkPath: "/deployments/{id}",
		Email: map[string]notifications.EmailText{
			"en": {Subject: "Deployment needs attention: %s", Intro: "A software deployment you own needs a decision:", Action: "Open deployment"},
			"de": {Subject: "Verteilung benötigt Aufmerksamkeit: %s", Intro: "Eine Softwareverteilung in Ihrer Verantwortung braucht eine Entscheidung:", Action: "Verteilung öffnen"},
		},
	}}
}

// SoftwareDirectory reports which Users are active (Organization work directory).
type SoftwareDirectory interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
}

// SoftwarePermissionHolders lists candidate recipients by id after the cursor (Organization contract).
type SoftwarePermissionHolders interface {
	ActiveUsers(ctx context.Context, after string, limit int) ([]string, error)
}

// SoftwarePermissionResolver returns the effective permissions of a User (platform/authorization/roles).
type SoftwarePermissionResolver interface {
	Permissions(ctx context.Context, userID string) (map[string]struct{}, error)
}

// SoftwareNotifier creates notifications inside the caller's transaction.
type SoftwareNotifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// softwareNotifyChunk is how many candidates one consumer run handles; the rest follows in a fan-out event.
const softwareNotifyChunk = 500

// SoftwareNotifications turns SoftwareVersionApprovalRequested into software.approval_requested notifications.
// It runs in the outbox dispatcher's claim transaction, writes only through tx and is idempotent (dedupe key
// per source event and recipient).
type SoftwareNotifications struct {
	store    Store
	dir      SoftwareDirectory
	holders  SoftwarePermissionHolders
	perms    SoftwarePermissionResolver
	notifier SoftwareNotifier
	chunk    int
}

func NewSoftwareNotifications(store Store, dir SoftwareDirectory, holders SoftwarePermissionHolders, perms SoftwarePermissionResolver, notifier SoftwareNotifier) *SoftwareNotifications {
	return &SoftwareNotifications{store: store, dir: dir, holders: holders, perms: perms, notifier: notifier, chunk: softwareNotifyChunk}
}

// WithChunk overrides the number of candidates per consumer run (tests).
func (n *SoftwareNotifications) WithChunk(chunk int) *SoftwareNotifications {
	n.chunk = chunk
	return n
}

// OnApprovalRequested notifies the holders of software.approve. It consumes SoftwareVersionApprovalRequested
// and its continuation SoftwareVersionApprovalRequestedFanOut (payload "after"). A stale event (the version is
// no longer pending) notifies nobody.
func (n *SoftwareNotifications) OnApprovalRequested(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		VersionID string `json:"versionId"`
		After     string `json:"after"`
		SourceID  string `json:"sourceEventId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	if !validUUID(p.VersionID) || p.After != "" && !validUUID(p.After) {
		return events.Permanent(fmt.Errorf("invalid ids in %s", ev.EventType))
	}
	v, err := n.store.GetSoftwareVersion(ctx, strings.ToLower(p.VersionID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if v.ApprovalStatus != VersionPending {
		return nil
	}
	source := ev.ID
	if p.SourceID != "" {
		source = p.SourceID
	}
	candidates, err := n.holders.ActiveUsers(ctx, strings.ToLower(p.After), n.chunk+1)
	if err != nil {
		return fmt.Errorf("list notification candidates: %w", err)
	}
	if len(candidates) > n.chunk {
		candidates = candidates[:n.chunk]
		if err := events.Publish(ctx, tx, events.Publication{Type: EventSoftwareVersionApprovalRequestedFanOut, ActorID: ev.ActorID, CorrelationID: ev.CorrelationID,
			Payload: map[string]any{"versionId": v.ID, "after": candidates[len(candidates)-1], "sourceEventId": source}}); err != nil {
			return fmt.Errorf("continue fan-out: %w", err)
		}
	}
	var ids []string
	for _, id := range candidates {
		excluded := id == v.RegisteredBy || (v.RequestedBy != nil && *v.RequestedBy == id) || (ev.ActorID != nil && *ev.ActorID == id)
		if !excluded {
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
		if _, ok := perms[PermSoftwareApprove]; !ok {
			continue
		}
		if _, err := n.notifier.Create(ctx, tx, notifications.Intent{
			RecipientUserID: id, Category: SoftwareApprovalCategory, Params: map[string]any{"title": v.ProductName + " " + v.ProductVersion},
			LinkType: "software_version", LinkID: v.ID, DedupeKey: source + ":" + id,
		}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}
