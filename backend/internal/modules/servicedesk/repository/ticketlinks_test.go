package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
)

func TestSetDeviceIsGuardedAuditedAndInTheHistory(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	asset, gone := e.uuid(), e.uuid()
	active := map[string]bool{}
	for _, id := range []string{e.alice, e.bob, e.u1, e.u2, e.admin.UserID} {
		active[id] = true
	}
	// The asset is held by somebody else: staff may still pick it.
	e.svc = application.NewService(repository.New(e.pool), dir{active: active}, device{asset: e.bob}).WithMemberships(e.mem)
	q := e.queue(application.QueueInternal)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.global, q)
	staff := application.Principal{UserID: e.u1, AssetsView: true}
	noAssets := e.employee(e.u1)

	if _, err := e.svc.SetDevice(ctx, e.c(e.u2), application.Principal{UserID: e.u2, AssetsView: true}, tk.ID, &tk.Version, &asset); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("a view grant must not set the device: %v", err)
	}
	if _, err := e.svc.SetDevice(ctx, e.c(e.u1), noAssets, tk.ID, &tk.Version, &asset); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("staff without assets.view must not pick a device: %v", err)
	}
	if _, err := e.svc.SetDevice(ctx, e.c(e.u1), staff, tk.ID, nil, &asset); err == nil {
		t.Error("the expected version is required")
	}
	stale := tk.Version + 5
	if _, err := e.svc.SetDevice(ctx, e.c(e.u1), staff, tk.ID, &stale, &asset); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.SetDevice(ctx, e.c(e.u1), staff, tk.ID, &tk.Version, &gone); !errors.Is(err, application.ErrDeviceInvalid) {
		t.Errorf("an unknown device must be refused: %v", err)
	}
	if _, err := e.svc.SetDevice(ctx, e.c(e.alice), application.Principal{UserID: e.alice, AssetsView: true}, tk.ID, &tk.Version, &asset); !errors.Is(err, application.ErrForbidden) && !errors.Is(err, application.ErrNotFound) {
		t.Errorf("the reporter must not set the device: %v", err)
	}
	out, err := e.svc.SetDevice(ctx, e.c(e.u1), staff, tk.ID, &tk.Version, &asset)
	if err != nil {
		t.Fatal(err)
	}
	if out.AssetID == nil || *out.AssetID != asset || out.Version != tk.Version+1 || out.DeviceSnapshot["reference"] != "AST-1" {
		t.Fatalf("device = %v %v version %d", out.AssetID, out.DeviceSnapshot, out.Version)
	}
	d, err := e.svc.Get(ctx, staff, tk.ID)
	if err != nil || !d.Abilities.SetDevice {
		t.Fatalf("ability = %+v %v", d.Abilities, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.device_changed' AND target_id = $2`, e.corr, tk.ID) != 1 {
		t.Error("the change must be audited once")
	}
	entries, _, err := e.svc.History(ctx, e.admin, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, en := range entries {
		if en.Kind == application.HistoryDeviceChanged && en.ToDevice == "AST-1" && en.ActorID == e.u1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no device_changed history entry in %+v", entries)
	}
	again, err := e.svc.SetDevice(ctx, e.c(e.u1), staff, tk.ID, &out.Version, &asset)
	if err != nil || again.Version != out.Version {
		t.Errorf("repeat = version %d, %v", again.Version, err)
	}
	cleared, err := e.svc.SetDevice(ctx, e.c(e.u1), noAssets, tk.ID, &again.Version, nil)
	if err != nil || cleared.AssetID != nil || cleared.DeviceSnapshot != nil {
		t.Errorf("clear = %v %v, %v", cleared.AssetID, cleared.DeviceSnapshot, err)
	}
}

func TestDuplicatesAndRelatedTicketsOnTheSurvivingTicket(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	reg := relationships.NewRegistry()
	reg.Register(application.Triples...)
	e.svc = e.svc.WithChanges(fakeChanges{}, relationships.New(reg), e.pool)
	q1, q2 := e.queue(application.QueueInternal), e.queue(application.QueueInternal)
	e.grant(q1, user(e.u1, "work"), user(e.u2, "view"))
	e.grant(q2, user(e.u2, "work"))
	a, dup, b, c := e.raise(e.global, q1), e.raise(e.global, q1), e.raise(e.global, q1), e.raise(e.global, q2)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.relationships WHERE source_id IN ($1::uuid, $2::uuid, $3::uuid)`, a.ID, b.ID, c.ID)
	})
	lead, viewer := e.employee(e.u1), e.employee(e.u2)

	if _, err := e.svc.MarkDuplicate(ctx, e.c(e.u1), lead, dup.ID, &dup.Version, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Duplicates(ctx, lead, a.ID)
	if err != nil || len(got) != 1 || got[0].TicketID != dup.ID || got[0].Reference != dup.Reference {
		t.Fatalf("duplicates = %+v %v", got, err)
	}
	if _, err := e.svc.Duplicates(ctx, e.employee(e.alice), a.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a person without staff access gets not found: %v", err)
	}

	if _, err := e.svc.LinkTicket(ctx, e.c(e.u1), lead, a.ID, a.ID); err == nil {
		t.Error("a ticket cannot be linked to itself")
	}
	if _, err := e.svc.LinkTicket(ctx, e.c(e.u2), viewer, a.ID, b.ID); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("a view grant must not link: %v", err)
	}
	if _, err := e.svc.LinkTicket(ctx, e.c(e.u1), lead, a.ID, c.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a ticket in a Queue the caller cannot see is not found: %v", err)
	}
	if _, err := e.svc.LinkTicket(ctx, e.c(e.u1), lead, a.ID, e.uuid()); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown ticket: %v", err)
	}
	l1, err := e.svc.LinkTicket(ctx, e.c(e.u1), lead, a.ID, b.ID)
	if err != nil || l1.TicketID != b.ID {
		t.Fatalf("link = %+v %v", l1, err)
	}
	// Linking again, also from the other side, is idempotent and creates no second link.
	l2, err := e.svc.LinkTicket(ctx, e.c(e.u1), lead, b.ID, a.ID)
	if err != nil || l2.ID != l1.ID {
		t.Fatalf("the reverse link must reuse the existing one: %+v %v", l2, err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.ticket_linked'`, e.corr) != 1 {
		t.Error("one audit event for the link")
	}
	for _, id := range []string{a.ID, b.ID} {
		rel, err := e.svc.RelatedTickets(ctx, lead, id)
		if err != nil || len(rel) != 1 || rel[0].Hidden {
			t.Fatalf("related of %s = %+v %v", id, rel, err)
		}
	}

	// A link to a ticket in a Queue the caller cannot see is a hidden placeholder.
	if _, err := e.svc.LinkTicket(ctx, e.c(e.u2), viewer, c.ID, a.ID); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("u2 only views ticket a, so linking is forbidden: %v", err)
	}
	e.grant(q1, user(e.u1, "work"), user(e.u2, "work"))
	if _, err := e.svc.LinkTicket(ctx, e.c(e.u2), viewer, c.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	rel, err := e.svc.RelatedTickets(ctx, lead, a.ID)
	if err != nil || len(rel) != 2 {
		t.Fatalf("related = %+v %v", rel, err)
	}
	hidden := 0
	for _, r := range rel {
		if r.Hidden {
			hidden++
			if r.TicketID != "" || r.Title != "" {
				t.Errorf("a hidden link carries data: %+v", r)
			}
		}
	}
	if hidden != 1 {
		t.Errorf("hidden = %d, want 1", hidden)
	}

	// Unlinking works from either side and is idempotent.
	if err := e.svc.UnlinkTicket(ctx, e.c(e.u1), lead, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UnlinkTicket(ctx, e.c(e.u1), lead, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.ticket_unlinked'`, e.corr) != 1 {
		t.Error("one audit event for the unlink")
	}
	if rel, err = e.svc.RelatedTickets(ctx, lead, b.ID); err != nil || len(rel) != 0 {
		t.Errorf("related after unlink = %+v %v", rel, err)
	}
}
