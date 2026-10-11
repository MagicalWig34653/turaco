package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

func TestSponsoredPartiesShowOnlyOwn(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	other := f.local("OtherSponsor")
	stranger := f.local("Stranger")
	party := f.newParty(sponsor.ID)
	otherParty := f.newParty(other.ID)
	end := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	mine := f.external(party.ID, "Mine", end)
	theirs := f.external(otherParty.ID, "Theirs", end)

	list, err := f.repo.ListSponsoredParties(ctx, sponsor.ID)
	if err != nil || len(list) != 1 || list[0].ID != party.ID || len(list[0].Accounts) != 1 || list[0].Accounts[0].UserID != mine.ID {
		t.Fatalf("sponsor list = %+v %v", list, err)
	}
	if a := list[0].Accounts[0]; a.Version != mine.Version || !a.AccessExpiresAt.Equal(end) || a.Status != application.StatusActive {
		t.Errorf("account = %+v", a)
	}
	for _, c := range list {
		for _, a := range c.Accounts {
			if a.UserID == theirs.ID {
				t.Error("an account of another party is listed")
			}
		}
	}
	// A stranger, an administrator holding the manage permission and an unknown id see nothing of this party.
	for name, id := range map[string]string{"stranger": stranger.ID, "unknown": missingID, "malformed": "not-a-uuid"} {
		got, err := f.repo.ListSponsoredParties(ctx, id)
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	// After the hand-over the previous sponsor sees nothing and the new one sees the party.
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: party.Version, SponsorUserID: &stranger.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.repo.ListSponsoredParties(ctx, sponsor.ID); len(got) != 0 {
		t.Errorf("previous sponsor still sees %+v", got)
	}
	if got, _ := f.repo.ListSponsoredParties(ctx, stranger.ID); len(got) != 1 {
		t.Errorf("new sponsor sees %+v", got)
	}
}

func TestApplicationListSponsoredNeedsASession(t *testing.T) {
	f := newPartyFix(t)
	svc := application.NewExternalParties(f.repo)
	// No user behind the call (a system or CLI caller) is refused instead of listing something.
	if _, err := svc.ListSponsored(context.Background(), f.caller("")); err == nil {
		t.Error("a caller without a user id may not list sponsored parties")
	}
	sponsor := f.local("Sponsor")
	f.newParty(sponsor.ID)
	got, err := svc.ListSponsored(context.Background(), f.caller(sponsor.ID))
	if err != nil || len(got) != 1 {
		t.Errorf("sponsor: %+v %v", got, err)
	}
}

func (f *partyFix) outbox(eventType, key, value string) []map[string]any {
	f.t.Helper()
	ctx := context.Background()
	rows, err := f.pool.Query(ctx, `SELECT payload, actor_id::text FROM platform.outbox_events WHERE event_type = $1 AND payload->>$2 = $3`, eventType, key, value)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var actor *string
		if err := rows.Scan(&raw, &actor); err != nil {
			f.t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			f.t.Fatal(err)
		}
		if actor != nil {
			m["_actor"] = *actor
		}
		out = append(out, m)
	}
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE event_type = $1 AND payload->>$2 = $3`, eventType, key, value)
	})
	return out
}

func TestPartyOperationsPublishEvents(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	next := f.local("Next")
	party := f.newParty(sponsor.ID)
	until := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	ext := f.external(party.ID, "Ext", until)

	if _, err := f.repo.ExtendAccess(ctx, f.caller(sponsor.ID), ext.ID, ext.Version, until.Add(24*time.Hour), "contract_renewed"); err != nil {
		t.Fatal(err)
	}
	ev := f.outbox("ExternalAccessExtended", "userId", ext.ID)
	if len(ev) != 1 || ev[0]["_actor"] != sponsor.ID || ev[0]["externalPartyId"] != party.ID {
		t.Errorf("ExternalAccessExtended = %+v", ev)
	}

	got, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: party.Version, SponsorUserID: &next.ID})
	if err != nil {
		t.Fatal(err)
	}
	ev = f.outbox("ExternalPartySponsorChanged", "partyId", party.ID)
	if len(ev) != 1 || ev[0]["sponsorUserId"] != next.ID || ev[0]["previousSponsorUserId"] != sponsor.ID {
		t.Errorf("ExternalPartySponsorChanged = %+v", ev)
	}
	// A rename does not announce a sponsor change.
	name := f.pfx + " Renamed"
	got, err = f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: got.Version, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if ev = f.outbox("ExternalPartySponsorChanged", "partyId", party.ID); len(ev) != 1 {
		t.Errorf("sponsor changed events after a rename = %d", len(ev))
	}
	off := false
	if _, err = f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: got.Version, Active: &off}); err != nil {
		t.Fatal(err)
	}
	if ev = f.outbox("ExternalPartyDeactivated", "partyId", party.ID); len(ev) != 1 {
		t.Errorf("ExternalPartyDeactivated = %+v", ev)
	}
}

func TestPartyConsumersNotifyTheSponsor(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	next := f.local("Next")
	gone := f.user(f.pfx+" Departed", "inactive")
	party := f.newParty(sponsor.ID)
	until := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	ext := f.external(party.ID, "Ext", until)
	n := &recordingNotifier{}
	consumers := application.NewPartyConsumers(f.repo, n)

	run := func(fn func(context.Context, pgx.Tx, events.OutboxEvent) error, id, eventType string, actor *string, payload map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
			return fn(ctx, tx, events.OutboxEvent{ID: id, EventType: eventType, ActorID: actor, Payload: raw})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	take := func() []string {
		var out []string
		for _, in := range n.intents {
			out = append(out, in.Category+">"+in.RecipientUserID)
		}
		n.intents = nil
		return out
	}

	// Expiry: the sponsor is told, the intent carries the account name and links to the sponsor's page.
	run(consumers.OnAccessExpired, "ev-1", "AccessExpired", nil, map[string]any{"userId": ext.ID, "externalPartyId": party.ID})
	if len(n.intents) != 1 || n.intents[0].RecipientUserID != sponsor.ID || n.intents[0].Category != application.NotificationAccessExpired ||
		n.intents[0].LinkType != application.LinkSponsoredAccount || n.intents[0].LinkID != ext.ID || n.intents[0].DedupeKey != "ev-1:"+sponsor.ID ||
		n.intents[0].Params["title"] != ext.DisplayName {
		t.Fatalf("expired intents = %+v", n.intents)
	}
	n.intents = nil

	// Extension by somebody else notifies the sponsor; the sponsor extending it themselves notifies nobody.
	payload := map[string]any{"userId": ext.ID, "externalPartyId": party.ID, "accessExpiresAt": until.Format(time.RFC3339)}
	admin := f.local("Admin").ID
	adminPtr := &admin
	run(consumers.OnAccessExtended, "ev-2", "ExternalAccessExtended", adminPtr, payload)
	if got := take(); len(got) != 1 || got[0] != application.NotificationAccessExtended+">"+sponsor.ID {
		t.Errorf("extended by admin = %v", got)
	}
	self := sponsor.ID
	run(consumers.OnAccessExtended, "ev-3", "ExternalAccessExtended", &self, payload)
	if got := take(); len(got) != 0 {
		t.Errorf("extended by the sponsor = %v", got)
	}
	// An unknown account notifies nobody and does not fail the event.
	run(consumers.OnAccessExtended, "ev-4", "ExternalAccessExtended", adminPtr, map[string]any{"userId": missingID, "accessExpiresAt": until.Format(time.RFC3339)})
	if got := take(); len(got) != 0 {
		t.Errorf("unknown account = %v", got)
	}

	// Hand-over: the new sponsor and the previous one are told, nobody else.
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: party.Version, SponsorUserID: &next.ID}); err != nil {
		t.Fatal(err)
	}
	change := map[string]any{"partyId": party.ID, "sponsorUserId": next.ID, "previousSponsorUserId": sponsor.ID}
	run(consumers.OnSponsorChanged, "ev-5", "ExternalPartySponsorChanged", adminPtr, change)
	got := take()
	if len(got) != 2 || got[0] != application.NotificationSponsorAssigned+">"+next.ID || got[1] != application.NotificationSponsorRemoved+">"+sponsor.ID {
		t.Errorf("hand-over = %v", got)
	}
	// A stale event (the sponsor is somebody else by now) does not tell the supposed new sponsor.
	stale := map[string]any{"partyId": party.ID, "sponsorUserId": sponsor.ID, "previousSponsorUserId": gone}
	run(consumers.OnSponsorChanged, "ev-6", "ExternalPartySponsorChanged", adminPtr, stale)
	if got := take(); len(got) != 0 { // previous sponsor "gone" is inactive
		t.Errorf("stale hand-over = %v", got)
	}

	// Deactivation tells the current sponsor, never the person who did it.
	run(consumers.OnPartyDeactivated, "ev-7", "ExternalPartyDeactivated", adminPtr, map[string]any{"partyId": party.ID})
	if got := take(); len(got) != 1 || got[0] != application.NotificationPartyDeactivated+">"+next.ID {
		t.Errorf("deactivated = %v", got)
	}
	run(consumers.OnPartyDeactivated, "ev-8", "ExternalPartyDeactivated", &next.ID, map[string]any{"partyId": party.ID})
	if got := take(); len(got) != 0 {
		t.Errorf("deactivated by the sponsor = %v", got)
	}

	// A sponsor who is no longer an active employee receives nothing.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET status = 'inactive' WHERE id = $1::uuid`, next.ID); err != nil {
		t.Fatal(err)
	}
	run(consumers.OnPartyDeactivated, "ev-9", "ExternalPartyDeactivated", adminPtr, map[string]any{"partyId": party.ID})
	if got := take(); len(got) != 0 {
		t.Errorf("inactive sponsor = %v", got)
	}
	// A malformed payload is a permanent failure, not a retry loop.
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		return consumers.OnPartyDeactivated(ctx, tx, events.OutboxEvent{ID: "ev-10", Payload: []byte(`[`)})
	})
	if err == nil || !events.IsPermanent(err) {
		t.Errorf("malformed payload: %v", err)
	}
}
