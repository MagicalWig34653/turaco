package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

func TestPeerMappings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dev := newID()
	e.devices.m[dev] = application.DeviceInfo{ID: dev, Name: "pc", ObservedAt: e.clk.now()}
	peer := newPeer()
	// Only administrators map; validation is strict.
	if _, err := e.svc.MapPeer(ctx, e.caller(e.tech), e.tech, dev, provider, peer, "initial_mapping"); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("technician: %v", err)
	}
	for _, bad := range []string{"", "12", "123456789012345678901234567890123", "abc def ghi", "a;b;c;d;e;f", "$(whoami)1", "../../x", "123456789\n", "ünïcode123", "<a>b</a>xx", "rustdesk://x"} {
		if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, bad, "initial_mapping"); err == nil {
			t.Fatalf("peer id %q accepted", bad)
		}
	}
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, peer, "whatever"); err == nil {
		t.Fatal("bad reason accepted")
	}
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, "anydesk", peer, "initial_mapping"); err == nil {
		t.Fatal("provider not enabled")
	}
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, newID(), provider, peer, "initial_mapping"); err == nil {
		t.Fatal("unknown device")
	}
	m1, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, peer, "initial_mapping")
	if err != nil || m1.Source != "manual" || m1.PeerID != peer {
		t.Fatalf("map: %v %+v", err, m1)
	}
	// Repeating is idempotent (no new row).
	m1b, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, peer, "initial_mapping")
	if err != nil || m1b.ID != m1.ID {
		t.Fatalf("idempotent: %v", err)
	}
	// Another Device cannot take the same peer.
	other := newID()
	e.devices.m[other] = application.DeviceInfo{ID: other, Name: "pc2", ObservedAt: e.clk.now()}
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, other, provider, peer, "initial_mapping"); !errors.Is(err, application.ErrPeerTaken) {
		t.Fatalf("peer taken: %v", err)
	}
	// Remapping closes the old row and keeps history.
	peer2 := newPeer()
	m2, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, peer2, "correction")
	if err != nil || m2.ID == m1.ID {
		t.Fatalf("remap: %v", err)
	}
	all, err := e.svc.Mappings(ctx, e.admin, dev, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("history: %v %d", err, len(all))
	}
	var closed int
	for _, m := range all {
		if m.ClosedAt != nil && m.CloseReason != nil && *m.CloseReason == "replaced" && m.ClosedBy != nil {
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("closed rows: %d", closed)
	}
	// The released peer can be mapped elsewhere now.
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, other, provider, peer, "initial_mapping"); err != nil {
		t.Fatalf("released peer: %v", err)
	}
	// Masking: technicians never see a whole peer id, administrators do; history only for administrators.
	list, err := e.svc.Mappings(ctx, e.tech, dev, true)
	if err != nil || len(list) != 1 || list[0].PeerID != "***"+peer2[len(peer2)-3:] || strings.Contains(list[0].PeerID, peer2[:6]) {
		t.Fatalf("masked list: %v %+v", err, list)
	}
	if list, _ = e.svc.Mappings(ctx, e.admin, dev, false); list[0].PeerID != peer2 {
		t.Fatalf("admin list: %+v", list)
	}
	nobody := application.Principal{UserID: newID()}
	if _, err := e.svc.Mappings(ctx, nobody, dev, false); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
	// Unmap.
	if err := e.svc.UnmapPeer(ctx, e.caller(e.tech), e.tech, dev, provider, "wrong_device"); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("technician unmap: %v", err)
	}
	if err := e.svc.UnmapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, "bogus"); err == nil {
		t.Fatal("bad unmap reason")
	}
	if err := e.svc.UnmapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, "wrong_device"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UnmapPeer(ctx, e.caller(e.admin), e.admin, dev, provider, "wrong_device"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("unmap twice: %v", err)
	}
	assertAudit(t, e, dev, "remoteaccess.peer_mapping.mapped", "remoteaccess.peer_mapping.unmapped")
	// The audit never carries a peer id.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND metadata::text LIKE '%' || $2 || '%'`, dev, peer2).Scan(&n); err != nil || n != 0 {
		t.Fatalf("peer id in audit: %v %d", err, n)
	}
}

func TestCapabilitiesAndMasking(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	nobody := application.Principal{UserID: newID()}
	if _, err := e.svc.Capabilities(ctx, nobody, f.device); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("no permission: %v", err)
	}
	c, err := e.svc.Capabilities(ctx, e.tech, f.device)
	if err != nil || !c.Enabled || !c.Known || c.Stale || len(c.Providers) != 1 {
		t.Fatalf("capabilities: %v %+v", err, c)
	}
	p := c.Providers[0]
	if !p.Available || !p.Mapped || p.PeerID == f.peer || !strings.HasSuffix(p.PeerID, f.peer[len(f.peer)-3:]) || !p.Capabilities.Attended {
		t.Fatalf("provider: %+v", p)
	}
	if ca, _ := e.svc.Capabilities(ctx, e.admin, f.device); ca.Providers[0].PeerID != f.peer {
		t.Fatal("administrators see the peer id")
	}
	// Why not available.
	d := e.devices.m[f.device]
	d.LastCheckinAt = ptr(e.clk.now().Add(-8 * 24 * time.Hour))
	e.devices.m[f.device] = d
	if c, _ = e.svc.Capabilities(ctx, e.tech, f.device); c.Providers[0].Available || c.Providers[0].Reasons[0] != application.RefStaleDevice || !c.Stale {
		t.Fatalf("stale: %+v", c)
	}
	if c, _ = e.svc.Capabilities(ctx, e.tech, newID()); c.Known || c.Providers[0].Reasons[0] != application.RefDeviceUnknown {
		t.Fatalf("unknown device: %+v", c)
	}
	if _, err := e.svc.Capabilities(ctx, e.tech, "x"); err == nil {
		t.Fatal("bad id")
	}
	// Feature off: no providers.
	off := application.NewService(e.store, e.devices, e.tickets, e.holders, e.dir, e.approvals, e.approvers, remoteaccess.NewRegistryOf()).WithClock(e.clk.now)
	if c, _ = off.Capabilities(ctx, e.tech, f.device); c.Enabled || len(c.Providers) != 0 {
		t.Fatalf("off: %+v", c)
	}
	_, err = off.Request(ctx, e.caller(e.tech), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider})
	refusal(t, err, application.RefProviderDisabled)
}

func TestReadScopesAndIDOR(t *testing.T) {
	e := newEnv(t, "personal")
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	other := principal(newID(), true, false)
	if _, err := e.svc.GetSession(ctx, other, s.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("other technician: %v", err)
	}
	if _, err := e.svc.GetSession(ctx, application.Principal{UserID: f.holder}, s.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("the end user cannot open the session record: %v", err)
	}
	if _, err := e.svc.GetSession(ctx, e.tech, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Fatal("garbage id")
	}
	viewer := application.Principal{UserID: newID(), ViewSessions: true}
	if d, err := e.svc.GetSession(ctx, viewer, s.ID); err != nil || len(d.Transitions) != 2 {
		t.Fatalf("view_sessions: %v", err)
	}
	// Lists: own sessions without view_sessions, all with.
	_ = e.mustRequest(e.fixture("corporate"))
	mine, err := e.svc.ListSessions(ctx, other, application.Filter{})
	if err != nil || len(mine.Items) != 0 {
		t.Fatalf("other's list: %v %d", err, len(mine.Items))
	}
	if _, err := e.svc.ListSessions(ctx, application.Principal{UserID: newID(), View: true}, application.Filter{}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("view only: %v", err)
	}
	own, err := e.svc.ListSessions(ctx, e.tech, application.Filter{Page: application.Page{Limit: 1}})
	if err != nil || len(own.Items) != 1 || own.NextCursor == "" {
		t.Fatalf("own page: %v %+v", err, own)
	}
	next, err := e.svc.ListSessions(ctx, e.tech, application.Filter{Page: application.Page{Limit: 5, Cursor: own.NextCursor}})
	if err != nil || len(next.Items) != 1 {
		t.Fatalf("own next page: %v %d", err, len(next.Items))
	}
	// A filter cannot widen the scope.
	if got, _ := e.svc.ListSessions(ctx, e.tech, application.Filter{InitiatedBy: other.UserID}); len(got.Items) != 0 {
		t.Fatalf("initiator filter leaked: %d", len(got.Items))
	}
	all, err := e.svc.ListSessions(ctx, viewer, application.Filter{DeviceID: f.device, TicketID: f.ticket, Status: application.StatusAuthorized})
	if err != nil || len(all.Items) != 1 || all.Items[0].ID != s.ID {
		t.Fatalf("filters: %v %+v", err, all)
	}
	if _, err := e.svc.ListSessions(ctx, viewer, application.Filter{Status: "bogus"}); err == nil {
		t.Fatal("bad status")
	}
	if _, err := e.svc.ListSessions(ctx, viewer, application.Filter{Page: application.Page{Cursor: "zzz"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
	// Cancel/close by another technician: not found, whatever the version; administrators may.
	if _, err := e.svc.Cancel(ctx, e.caller(other), other, s.ID, &s.Version, "wrong_device"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger cancel: %v", err)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(e.admin), e.admin, s.ID, &s.Version, "wrong_device"); err != nil {
		t.Fatalf("admin cancel: %v", err)
	}
	// The approver can read a pending session.
	pf := e.fixture("personal")
	ps, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, application.NewSession{DeviceID: pf.device, TicketID: pf.ticket, Provider: provider, Approver: application.Approver{UserID: &e.admin.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	e.approvals.byID[*ps.ApprovalID].ApproverUserID = &e.admin.UserID
	if _, err := e.svc.GetSession(ctx, application.Principal{UserID: e.admin.UserID}, ps.ID); err != nil {
		t.Fatalf("approver read: %v", err)
	}
}

func TestSessionStartedNotification(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	x, _ := e.launchAndExchange(e.mustRequest(f))
	rec := &recordingNotifier{}
	dir := activeDir{}
	n := application.NewNotifications(e.store, e.devices, e.holders, e.tickets, dir, rec)
	payload, _ := json.Marshal(map[string]any{"sessionId": x.SessionID, "deviceId": f.device, "ticketId": f.ticket})
	tech := e.tech.UserID
	ev := events.OutboxEvent{ID: newID(), EventType: application.EventLaunched, ActorID: &tech, Payload: payload}
	run := func(ev events.OutboxEvent) error {
		return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return n.OnSessionLaunched(ctx, tx, ev) })
	}
	if err := run(ev); err != nil || len(rec.intents) != 1 {
		t.Fatalf("notify: %v %d", err, len(rec.intents))
	}
	in := rec.intents[0]
	if in.RecipientUserID != f.holder || in.Category != application.NotificationCategory || in.LinkType != "" || in.Params["title"] != x.Reference || in.DedupeKey == "" {
		t.Fatalf("intent: %+v", in)
	}
	// Only the reference, nothing about the provider, peer or technician.
	for k, v := range in.Params {
		if s, _ := v.(string); k != "title" || strings.Contains(s, f.peer) {
			t.Fatalf("param %s leaked", k)
		}
	}
	// The holder is the actor: nothing to tell.
	holder := f.holder
	rec.intents = nil
	ev.ActorID = &holder
	if err := run(ev); err != nil || len(rec.intents) != 0 {
		t.Fatalf("self: %v", err)
	}
	// A Device without a holder: the Ticket's affected user is still told.
	delete(e.holders.m, *e.devices.m[f.device].AssetID)
	ev.ActorID = &tech
	if err := run(ev); err != nil || len(rec.intents) != 1 || rec.intents[0].RecipientUserID != f.holder {
		t.Fatalf("no holder: %v %+v", err, rec.intents)
	}
	// Holder and affected user differ: both are told, once each; an inactive one is skipped.
	rec.intents = nil
	other := newID()
	e.holders.m[*e.devices.m[f.device].AssetID] = other
	if err := run(ev); err != nil || len(rec.intents) != 2 {
		t.Fatalf("holder and affected user: %v %+v", err, rec.intents)
	}
	rec.intents = nil
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return application.NewNotifications(e.store, e.devices, e.holders, e.tickets, inactiveDir{other}, rec).OnSessionLaunched(ctx, tx, ev)
	}); err != nil || len(rec.intents) != 1 || rec.intents[0].RecipientUserID != f.holder {
		t.Fatalf("inactive holder: %v %+v", err, rec.intents)
	}
	bad := events.OutboxEvent{ID: newID(), EventType: application.EventLaunched, Payload: json.RawMessage(`{"sessionId":"x"}`)}
	if err := run(bad); err == nil {
		t.Fatal("invalid payload must fail permanently")
	}
}

type inactiveDir struct{ id string }

func (d inactiveDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = id != d.id
	}
	return out, nil
}

type activeDir struct{}

func (activeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func TestDatabaseConstraintsAndTriggers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	x, _ := e.launchAndExchange(s)
	if _, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, s.ID, &x.Session.Version, "completed"); err != nil {
		t.Fatal(err)
	}
	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s: statement succeeded", name)
		}
	}
	mustFail("transition update", `UPDATE remoteaccess.session_transitions SET operation = 'x' WHERE session_id = $1::uuid`, s.ID)
	mustFail("transition delete", `DELETE FROM remoteaccess.session_transitions WHERE session_id = $1::uuid`, s.ID)
	mustFail("transition truncate", `TRUNCATE remoteaccess.session_transitions`)
	mustFail("session delete", `DELETE FROM remoteaccess.sessions WHERE id = $1::uuid`, s.ID)
	mustFail("handle delete", `DELETE FROM remoteaccess.launch_handles WHERE session_id = $1::uuid`, s.ID)
	mustFail("mapping delete", `DELETE FROM remoteaccess.peer_mappings WHERE device_id = $1::uuid`, f.device)
	mustFail("unattended", `UPDATE remoteaccess.sessions SET mode = 'unattended' WHERE id = $1::uuid`, s.ID)
	mustFail("closed without reason", `UPDATE remoteaccess.sessions SET status_reason = NULL WHERE id = $1::uuid`, s.ID)
	mustFail("closed without closed_at", `UPDATE remoteaccess.sessions SET closed_at = NULL WHERE id = $1::uuid`, s.ID)
	mustFail("closed never launched", `UPDATE remoteaccess.sessions SET launched_at = NULL WHERE id = $1::uuid`, s.ID)
	mustFail("bad status", `UPDATE remoteaccess.sessions SET status = 'running' WHERE id = $1::uuid`, s.ID)
	mustFail("bad consent", `UPDATE remoteaccess.sessions SET consent = 'yes' WHERE id = $1::uuid`, s.ID)
	mustFail("consent time without consent", `UPDATE remoteaccess.sessions SET consent_recorded_at = now() WHERE id = $1::uuid`, s.ID)
	mustFail("observed source without time", `UPDATE remoteaccess.sessions SET observed_source = 'a.b' WHERE id = $1::uuid`, s.ID)
	mustFail("bad reason code", `UPDATE remoteaccess.sessions SET status_reason = 'Not A Code' WHERE id = $1::uuid`, s.ID)
	mustFail("bad mismatch reason", `UPDATE remoteaccess.sessions SET mismatch_reason = 'x' WHERE id = $1::uuid`, s.ID)
	mustFail("short token hash", `INSERT INTO remoteaccess.launch_handles(session_id, user_id, token_hash, expires_at) VALUES ($1::uuid, $2::uuid, '\x00', now())`, s.ID, e.tech.UserID)
	mustFail("transition without actor", `INSERT INTO remoteaccess.session_transitions(session_id, to_status, operation, correlation_id) VALUES ($1::uuid, 'closed', 'x', 'c')`, s.ID)
	mustFail("peer id with shell characters", `INSERT INTO remoteaccess.peer_mappings(device_id, provider, peer_id, source, mapped_by) VALUES ($1::uuid, 'x', 'a;b', 'manual', $1::uuid)`, newID())
	mustFail("manual mapping without user", `INSERT INTO remoteaccess.peer_mappings(device_id, provider, peer_id, source) VALUES ($1::uuid, 'x', '123456789', 'manual')`, newID())
	// A second open session for one device and provider violates the unique index; a closed one does not.
	if _, err := e.pool.Exec(ctx, `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, '123456789', 'attended', 'requested', $4::uuid, now())`, f.device, f.ticket, provider, e.tech.UserID); err != nil {
		t.Fatalf("closed sessions must not block a new one: %v", err)
	}
	mustFail("second open session", `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, '123456789', 'attended', 'authorized', $4::uuid, now())`, f.device, f.ticket, provider, e.tech.UserID)
	mustFail("pending without approval", `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, '123456789', 'attended', 'pending_approval', $4::uuid, now())`, newID(), f.ticket, provider, e.tech.UserID)
	mustFail("launched without launched_at", `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, '123456789', 'attended', 'launched', $4::uuid, now())`, newID(), f.ticket, provider, e.tech.UserID)
	mustFail("declined consent while open", `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at, consent, consent_recorded_at, launched_at)
		VALUES ($1::uuid, $2::uuid, $3, '123456789', 'attended', 'launched', $4::uuid, now(), 'declined', now(), now())`, newID(), f.ticket, provider, e.tech.UserID)
}
