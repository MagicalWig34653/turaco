package application

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Notifications about the lifecycle of External Parties and their accounts (F14 slice A-G follow-up). They are
// outbox consumers on the existing notification platform (ADR-0024): the event commits with the state change, the
// consumer runs in the dispatcher's transaction, writes only through tx and is idempotent (durable dedupe key per
// event and recipient). The recipient is the sponsor of the party, read at delivery time (on a hand-over also the
// previous sponsor), and never the person who caused the event. Preferences and the email channel are the platform's.

// Notification categories of Organization. The notifications carry the display name of the account or the party
// and, for access, the end date; nothing else.
const (
	// NotificationAccessExpired: the end of access of an external account of the sponsor's party passed.
	NotificationAccessExpired = "organization.access_expired"
	// NotificationAccessExtended: someone else extended the access of an account of the sponsor's party.
	NotificationAccessExtended = "organization.access_extended"
	// NotificationSponsorAssigned: the recipient became the sponsor of a party.
	NotificationSponsorAssigned = "organization.party_sponsor_assigned"
	// NotificationSponsorRemoved: the recipient is no longer the sponsor of a party (no link: the page no longer
	// shows the party to them).
	NotificationSponsorRemoved = "organization.party_sponsor_removed"
	// NotificationPartyDeactivated: someone else deactivated a party the recipient sponsors.
	NotificationPartyDeactivated = "organization.party_deactivated"

	// Link types of those notifications: the sponsor's own page, which needs no permission.
	LinkSponsoredAccount = "sponsored_account"
	LinkSponsoredParty   = "sponsored_party"
)

// NotificationCategories are the notification categories of Organization.
func NotificationCategories() []notifications.Category {
	cat := func(name, linkType, linkPath, enSubject, enIntro, deSubject, deIntro string) notifications.Category {
		en := notifications.EmailText{Subject: enSubject, Intro: enIntro, Action: "Open sponsored parties"}
		de := notifications.EmailText{Subject: deSubject, Intro: deIntro, Action: "Betreute Partner öffnen"}
		if linkType == "" {
			en.Action, de.Action = "Open Turaco", "Turaco öffnen"
		}
		return notifications.Category{Name: name, Owner: "organization", LinkType: linkType, LinkPath: linkPath,
			Email: map[string]notifications.EmailText{"en": en, "de": de}}
	}
	const account, party = "/sponsored-parties?account={id}", "/sponsored-parties?party={id}"
	return []notifications.Category{
		cat(NotificationAccessExpiring, LinkSponsoredAccount, account,
			"External access ends soon: %s", "The access of an external account ends soon:",
			"Externer Zugang endet bald: %s", "Der Zugang eines externen Kontos endet bald:"),
		cat(NotificationAccessExpired, LinkSponsoredAccount, account,
			"External access ended: %s", "The access of an external account you sponsor ended:",
			"Externer Zugang beendet: %s", "Der Zugang eines externen Kontos, für das Sie verantwortlich sind, ist beendet:"),
		cat(NotificationAccessExtended, LinkSponsoredAccount, account,
			"External access extended: %s", "The access of an external account you sponsor was extended:",
			"Externer Zugang verlängert: %s", "Der Zugang eines externen Kontos, für das Sie verantwortlich sind, wurde verlängert:"),
		cat(NotificationSponsorAssigned, LinkSponsoredParty, party,
			"You are now the sponsor of: %s", "You became the sponsor of an external party:",
			"Sie betreuen jetzt: %s", "Sie sind jetzt verantwortlich für einen externen Partner:"),
		cat(NotificationSponsorRemoved, "", "",
			"You are no longer the sponsor of: %s", "You are no longer the sponsor of an external party:",
			"Sie betreuen nicht mehr: %s", "Sie sind nicht mehr verantwortlich für einen externen Partner:"),
		cat(NotificationPartyDeactivated, LinkSponsoredParty, party,
			"External party deactivated: %s", "An external party you sponsor was deactivated:",
			"Externer Partner deaktiviert: %s", "Ein externer Partner, für den Sie verantwortlich sind, wurde deaktiviert:"),
		locationMoveCategory(),
	}
}

// PartyNotificationStore reads what a consumer needs, inside the consumer's transaction.
type PartyNotificationStore interface {
	// AccountContextTx returns the display name of an external account, the name of its party and the party's
	// current sponsor. found is false for an unknown account.
	AccountContextTx(ctx context.Context, tx pgx.Tx, accountID string) (c PartyNotificationContext, found bool, err error)
	// PartyContextTx returns the name, current sponsor and active state of a party.
	PartyContextTx(ctx context.Context, tx pgx.Tx, partyID string) (c PartyNotificationContext, found bool, err error)
	// IsActiveEmployeeTx reports whether the user is an active internal employee account.
	IsActiveEmployeeTx(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
}

// PartyNotificationContext is the data of a notification about an account or a party.
type PartyNotificationContext struct {
	AccountName string
	PartyName   string
	SponsorID   string
	PartyActive bool
}

// PartyNotifier creates notifications inside the consumer's transaction (platform/notifications.Service).
type PartyNotifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// PartyConsumers turn External Party events into notifications for the sponsor.
type PartyConsumers struct {
	store    PartyNotificationStore
	notifier PartyNotifier
}

func NewPartyConsumers(store PartyNotificationStore, notifier PartyNotifier) *PartyConsumers {
	return &PartyConsumers{store: store, notifier: notifier}
}

func decodeEvent(ev events.OutboxEvent, dst any) error {
	if err := json.Unmarshal(ev.Payload, dst); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	return nil
}

// notify tells the recipients, skipping the actor, duplicates and Users who are not active employees.
func (c *PartyConsumers) notify(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent, category, linkType, linkID string, params map[string]any, recipients ...string) error {
	var done []string
	for _, id := range recipients {
		if id == "" || slices.Contains(done, id) || (ev.ActorID != nil && *ev.ActorID == id) {
			continue
		}
		done = append(done, id)
		active, err := c.store.IsActiveEmployeeTx(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("check recipient: %w", err)
		}
		if !active {
			continue
		}
		if _, err := c.notifier.Create(ctx, tx, notifications.Intent{RecipientUserID: id, Category: category, Params: params,
			LinkType: linkType, LinkID: linkID, DedupeKey: ev.ID + ":" + id}); err != nil {
			return fmt.Errorf("create notification: %w", err)
		}
	}
	return nil
}

// OnAccessExpired (event AccessExpired) tells the sponsor that an account of the party ended.
func (c *PartyConsumers) OnAccessExpired(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		UserID string `json:"userId"`
	}
	if err := decodeEvent(ev, &p); err != nil {
		return err
	}
	pc, found, err := c.store.AccountContextTx(ctx, tx, p.UserID)
	if err != nil || !found {
		return err
	}
	return c.notify(ctx, tx, ev, NotificationAccessExpired, LinkSponsoredAccount, p.UserID,
		map[string]any{"title": pc.AccountName, "partyName": pc.PartyName}, pc.SponsorID)
}

// OnAccessExtended (event ExternalAccessExtended) tells the sponsor when somebody else extended an account.
func (c *PartyConsumers) OnAccessExtended(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		UserID          string    `json:"userId"`
		AccessExpiresAt time.Time `json:"accessExpiresAt"`
	}
	if err := decodeEvent(ev, &p); err != nil {
		return err
	}
	pc, found, err := c.store.AccountContextTx(ctx, tx, p.UserID)
	if err != nil || !found {
		return err
	}
	return c.notify(ctx, tx, ev, NotificationAccessExtended, LinkSponsoredAccount, p.UserID,
		map[string]any{"title": pc.AccountName, "partyName": pc.PartyName, "accessExpiresAt": p.AccessExpiresAt.UTC().Format(time.RFC3339)},
		pc.SponsorID)
}

// OnSponsorChanged (event ExternalPartySponsorChanged) tells the new sponsor and the previous one only while
// each message still describes the party's current sponsorship.
func (c *PartyConsumers) OnSponsorChanged(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		PartyID           string `json:"partyId"`
		SponsorUserID     string `json:"sponsorUserId"`
		PreviousSponsorID string `json:"previousSponsorUserId"`
	}
	if err := decodeEvent(ev, &p); err != nil {
		return err
	}
	pc, found, err := c.store.PartyContextTx(ctx, tx, p.PartyID)
	if err != nil || !found {
		return err
	}
	params := map[string]any{"title": pc.PartyName}
	if pc.SponsorID == p.SponsorUserID {
		if err := c.notify(ctx, tx, ev, NotificationSponsorAssigned, LinkSponsoredParty, p.PartyID, params, p.SponsorUserID); err != nil {
			return err
		}
	}
	if pc.SponsorID != p.PreviousSponsorID {
		return c.notify(ctx, tx, ev, NotificationSponsorRemoved, "", "", params, p.PreviousSponsorID)
	}
	return nil
}

// OnPartyDeactivated (event ExternalPartyDeactivated) tells the sponsor when somebody else deactivated the party.
func (c *PartyConsumers) OnPartyDeactivated(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		PartyID string `json:"partyId"`
	}
	if err := decodeEvent(ev, &p); err != nil {
		return err
	}
	pc, found, err := c.store.PartyContextTx(ctx, tx, p.PartyID)
	if err != nil || !found {
		return err
	}
	if pc.PartyActive {
		return nil
	}
	return c.notify(ctx, tx, ev, NotificationPartyDeactivated, LinkSponsoredParty, p.PartyID,
		map[string]any{"title": pc.PartyName}, pc.SponsorID)
}
