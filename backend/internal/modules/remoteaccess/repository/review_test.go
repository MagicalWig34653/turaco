package repository_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
)

// handle issues a launch handle for an authorized session.
func (e *env) handle(s application.Session) string {
	e.t.Helper()
	h, err := e.svc.Launch(context.Background(), e.caller(e.tech), e.tech, s.ID, &s.Version)
	if err != nil {
		e.t.Fatalf("launch: %v", err)
	}
	return h.Token
}

func (e *env) exchange(token string) error {
	_, err := e.svc.Exchange(context.Background(), e.caller(e.tech), e.tech, token)
	return err
}

func (e *env) approve(f fixture) application.Session {
	e.t.Helper()
	s, err := e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket,
		Provider: provider, Approver: application.Approver{UserID: &e.admin.UserID}})
	if err != nil || s.Status != application.StatusPendingApproval {
		e.t.Fatalf("pending request: %v %+v", err, s)
	}
	return s
}

func (e *env) auditMeta(targetID, action string) []map[string]any {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT metadata FROM platform.audit_events WHERE target_id = $1 AND action = $2`, targetID, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var m map[string]any
		_ = rows.Scan(&raw)
		_ = json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

func TestMappingChangeEndsOpenSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Authorized with a live handle: a remap cancels the session, the handle is useless.
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	token := e.handle(s)
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, f.device, provider, newPeer(), "correction"); err != nil {
		t.Fatal(err)
	}
	cur := e.get(s.ID)
	if cur.Status != application.StatusCancelled || cur.StatusReason == nil || *cur.StatusReason != application.ReasonMappingChanged {
		t.Fatalf("remap must cancel: %+v", cur)
	}
	if err := e.exchange(token); err == nil {
		t.Fatal("exchange after remap succeeded")
	}
	assertAudit(t, e, s.ID, "remoteaccess.session.cancelled")
	// Launched: unmapping closes it with the same reason.
	g := e.fixture("corporate")
	x, _ := e.launchAndExchange(e.mustRequest(g))
	if err := e.svc.UnmapPeer(ctx, e.caller(e.admin), e.admin, g.device, provider, "wrong_device"); err != nil {
		t.Fatal(err)
	}
	if c := e.get(x.SessionID); c.Status != application.StatusClosed || *c.StatusReason != application.ReasonMappingChanged {
		t.Fatalf("unmap must close a launched session: %+v", c)
	}
	// Pending approval: the approval is cancelled with it.
	e2 := newEnv(t, "personal")
	pf := e2.fixture("personal")
	pend := e2.approve(pf)
	if _, err := e2.svc.MapPeer(ctx, e2.caller(e2.admin), e2.admin, pf.device, provider, newPeer(), "correction"); err != nil {
		t.Fatal(err)
	}
	if c := e2.get(pend.ID); c.Status != application.StatusCancelled || len(e2.approvals.cancelled) == 0 {
		t.Fatalf("pending session on remap: %+v cancelled=%v", c, e2.approvals.cancelled)
	}
	// Repeating the current mapping changes nothing.
	h := e.fixture("corporate")
	hs := e.mustRequest(h)
	if _, err := e.svc.MapPeer(ctx, e.caller(e.admin), e.admin, h.device, provider, h.peer, "correction"); err != nil || e.get(hs.ID).Status != application.StatusAuthorized {
		t.Fatalf("same mapping: %v", err)
	}
}

func TestExchangeRefusesStalePeer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	token := e.handle(s)
	// Simulate a remap that did not end the session (defence in depth): close the mapping and map another peer.
	if _, err := e.pool.Exec(ctx, `UPDATE remoteaccess.peer_mappings SET closed_at = now(), close_reason = 'replaced' WHERE device_id = $1::uuid AND closed_at IS NULL`, f.device); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO remoteaccess.peer_mappings(device_id, provider, peer_id, source, mapped_by) VALUES ($1::uuid, $2, $3, 'manual', $4::uuid)`,
		f.device, provider, newPeer(), e.admin.UserID); err != nil {
		t.Fatal(err)
	}
	refusal(t, e.exchange(token), application.RefMappingChanged)
	if c := e.get(s.ID); c.Status != application.StatusFailed || *c.StatusReason != application.RefMappingChanged || c.LaunchedAt != nil {
		t.Fatalf("session after stale peer: %+v", c)
	}
	if e.fake.BuildCalls != 0 {
		t.Fatal("a launch link was built for a stale peer")
	}
}

func TestExchangeAndApprovalRerunGates(t *testing.T) {
	cases := []struct {
		name   string
		change func(e *env, f fixture)
		code   string
	}{
		{"ticket closed", func(e *env, f fixture) { t := e.tickets.m[f.ticket]; t.Open = false; e.tickets.m[f.ticket] = t }, application.RefTicketUnavailable},
		{"initiator lost ticket authority", func(e *env, f fixture) { delete(e.approvers.perms, e.tech.UserID) }, application.RefTicketUnavailable},
		{"device retired", func(e *env, f fixture) {
			d := e.devices.m[f.device]
			d.RetiredAt = ptr(e.clk.now())
			e.devices.m[f.device] = d
		}, application.RefDeviceRetired},
		{"device stale", func(e *env, f fixture) {
			d := e.devices.m[f.device]
			d.LastCheckinAt = nil
			e.devices.m[f.device] = d
		}, application.RefStaleDevice},
		{"holder changed", func(e *env, f fixture) { e.holders.m[*e.devices.m[f.device].AssetID] = newID() }, application.RefHolderMismatch},
		{"nobody to notify", func(e *env, f fixture) { e.dir.deactivate(f.holder) }, application.RefNoRecipient},
	}
	for _, c := range cases {
		t.Run("exchange "+c.name, func(t *testing.T) {
			e := newEnv(t)
			f := e.fixture("corporate")
			s := e.mustRequest(f)
			token := e.handle(s)
			c.change(e, f)
			refusal(t, e.exchange(token), c.code)
			if cur := e.get(s.ID); cur.Status != application.StatusFailed || *cur.StatusReason != c.code {
				t.Fatalf("session: %+v", cur)
			}
			if e.fake.BuildCalls != 0 {
				t.Fatal("link built after refusal")
			}
		})
		t.Run("approval "+c.name, func(t *testing.T) {
			e := newEnv(t, "corporate")
			f := e.fixture("corporate")
			s := e.approve(f)
			c.change(e, f)
			e.approvals.decide(*s.ApprovalID, "approved", e.admin.UserID)
			if err := e.decide(s.ID, *s.ApprovalID, "approve"); err != nil {
				t.Fatal(err)
			}
			if cur := e.get(s.ID); cur.Status != application.StatusRejected || *cur.StatusReason != c.code {
				t.Fatalf("approval must not authorize: %+v", cur)
			}
		})
	}
	t.Run("exchange approval now required", func(t *testing.T) {
		e := newEnv(t, "personal")
		f := e.fixture("corporate")
		s := e.mustRequest(f)
		token := e.handle(s)
		d := e.devices.m[f.device]
		d.Ownership = "personal"
		e.devices.m[f.device] = d
		refusal(t, e.exchange(token), application.RefApprovalRequired)
	})
	t.Run("exchange passes when nothing changed", func(t *testing.T) {
		e := newEnv(t)
		f := e.fixture("corporate")
		if x, _ := e.launchAndExchange(e.mustRequest(f)); x.Session.Status != application.StatusLaunched {
			t.Fatalf("%+v", x.Session)
		}
	})
}

func TestStaleGateUsesLastCheckin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	d := e.devices.m[f.device]
	// A fresh sync of an old check-in is stale; an old sync of a fresh check-in is not.
	d.ObservedAt, d.LastCheckinAt = e.clk.now(), ptr(e.clk.now().Add(-application.DeviceFreshness-time.Minute))
	e.devices.m[f.device] = d
	_, err := e.request(f, e.tech)
	refusal(t, err, application.RefStaleDevice)
	d.ObservedAt, d.LastCheckinAt = e.clk.now().Add(-30*24*time.Hour), ptr(e.clk.now().Add(-time.Hour))
	e.devices.m[f.device] = d
	if _, err := e.request(f, e.tech); err != nil {
		t.Fatalf("fresh check-in: %v", err)
	}
	d.LastCheckinAt = nil
	e.devices.m[f.device] = d
	if c, _ := e.svc.Capabilities(ctx, e.tech, f.device); !c.Stale || c.LastCheckinAt != nil {
		t.Fatalf("unknown check-in must be stale: %+v", c)
	}
}

func TestTicketGateBoundToCaller(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	other := principal(newID(), true, false)
	req := func(p application.Principal) error {
		_, err := e.svc.Request(ctx, e.caller(p), p, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider})
		return err
	}
	// No tickets.manage and not the assignee: the single code, same as unknown and closed tickets.
	refusal(t, req(other), application.RefTicketUnavailable)
	// The assignee may.
	tk := e.tickets.m[f.ticket]
	tk.AssigneeUserID = other.UserID
	e.tickets.m[f.ticket] = tk
	if err := req(other); err != nil {
		t.Fatalf("assignee: %v", err)
	}
	// The reporter never may, even with tickets.manage and as assignee.
	rep := principal(tk.ReporterUserID, true, false)
	e.approvers.perms[rep.UserID] = map[string]struct{}{"tickets.manage": {}}
	g := e.fixture("corporate")
	tk = e.tickets.m[g.ticket]
	tk.ReporterUserID, tk.AssigneeUserID = rep.UserID, rep.UserID
	e.tickets.m[g.ticket] = tk
	_, err := e.svc.Request(ctx, e.caller(rep), rep, application.NewSession{DeviceID: g.device, TicketID: g.ticket, Provider: provider})
	refusal(t, err, application.RefTicketUnavailable)
	// Unknown, closed and unauthorized are indistinguishable.
	_, e1 := e.svc.Request(ctx, e.caller(other), other, application.NewSession{DeviceID: g.device, TicketID: newID(), Provider: provider})
	tk.Open = false
	e.tickets.m[g.ticket] = tk
	_, e2 := e.svc.Request(ctx, e.caller(e.tech), e.tech, application.NewSession{DeviceID: g.device, TicketID: g.ticket, Provider: provider})
	if e1 == nil || e2 == nil || e1.Error() != e2.Error() {
		t.Fatalf("oracle: %v vs %v", e1, e2)
	}
}

func TestMismatchAlwaysNeedsApproval(t *testing.T) {
	e := newEnv(t) // no ownership policy
	f := e.fixture("corporate")
	tk := e.tickets.m[f.ticket]
	tk.AffectedUserID = newID()
	e.tickets.m[f.ticket] = tk
	_, err := e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider, MismatchReason: "shared_device"})
	refusal(t, err, application.RefApproverRequired)
	s, err := e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider,
		MismatchReason: "shared_device", Approver: application.Approver{UserID: &e.admin.UserID}})
	if err != nil || s.Status != application.StatusPendingApproval {
		t.Fatalf("mismatch with approver: %v %+v", err, s)
	}
}

func TestRefusedAttemptsCountAndAreAudited(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	tk := e.tickets.m[f.ticket]
	tk.Open = false
	e.tickets.m[f.ticket] = tk
	for i := 0; i < application.RateLimit; i++ {
		_, err := e.request(f, e.tech)
		refusal(t, err, application.RefTicketUnavailable)
	}
	if _, err := e.request(f, e.tech); !errors.Is(err, application.ErrRateLimited) {
		t.Fatalf("refused attempts must count: %v", err)
	}
	if m := e.auditMeta(f.device, "remoteaccess.session.request_refused"); len(m) != application.RateLimit || m[0]["reason"] != application.RefTicketUnavailable {
		t.Fatalf("refusal audit: %v", m)
	}
	// Attempts older than the retention are pruned by the expiry job.
	e.clk.advance(application.AttemptRetention + time.Hour)
	if _, err := e.svc.ExpireSessions(context.Background(), "job:prune"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM remoteaccess.request_attempts WHERE user_id = $1::uuid AND created_at < now() - interval '1 hour'`, e.tech.UserID).Scan(&n)
	if n != 0 {
		t.Fatalf("old attempts kept: %d", n)
	}
}

func TestNoRecipientRefused(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	e.dir.deactivate(f.holder) // holder and affected user are the same inactive user
	_, err := e.request(f, e.tech)
	refusal(t, err, application.RefNoRecipient)
	assertAudit(t, e, f.device, "remoteaccess.session.request_refused")
	// The technician himself is no recipient.
	g := e.fixture("corporate")
	tk := e.tickets.m[g.ticket]
	tk.AffectedUserID = e.tech.UserID
	e.tickets.m[g.ticket] = tk
	delete(e.holders.m, *e.devices.m[g.device].AssetID)
	_, err = e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: g.device, TicketID: g.ticket, Provider: provider, MismatchReason: "on_behalf",
		Approver: application.Approver{UserID: &e.admin.UserID}})
	refusal(t, err, application.RefNoRecipient)
	// The affected user alone is enough when the Device has no holder.
	h := e.fixture("corporate")
	delete(e.holders.m, *e.devices.m[h.device].AssetID)
	if _, err := e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: h.device, TicketID: h.ticket, Provider: provider, MismatchReason: "holder_changed",
		Approver: application.Approver{UserID: &e.admin.UserID}}); err != nil {
		t.Fatalf("affected user as recipient: %v", err)
	}
}

func TestApprovalPolicyUnknownOwnership(t *testing.T) {
	e := newEnv(t, "personal")
	for ownership, pending := range map[string]bool{"corporate": false, "personal": true, "unknown": true, "": true, "weird": true} {
		f := e.fixture(ownership)
		s, err := e.svc.Request(context.Background(), e.caller(e.tech), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider,
			Approver: application.Approver{UserID: &e.admin.UserID}})
		if err != nil || (s.Status == application.StatusPendingApproval) != pending {
			t.Fatalf("ownership %q: %v %s", ownership, err, s.Status)
		}
	}
	off := newEnv(t)
	if s := off.mustRequest(off.fixture("unknown")); s.Status != application.StatusAuthorized {
		t.Fatalf("no policy, unknown ownership: %s", s.Status)
	}
}

func TestMappingAuditHashesPeerID(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	next := newPeer()
	if _, err := e.svc.MapPeer(context.Background(), e.caller(e.admin), e.admin, f.device, provider, next, "correction"); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(next))
	metas := e.auditMeta(f.device, "remoteaccess.peer_mapping.mapped")
	if len(metas) != 2 {
		t.Fatalf("mapping audits: %v", metas)
	}
	found := false
	for _, m := range metas {
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), f.peer) || strings.Contains(string(raw), next) {
			t.Fatalf("peer id in audit: %s", raw)
		}
		found = found || m["peerIdHash"] == hex.EncodeToString(sum[:])
	}
	if !found {
		t.Fatalf("hash missing: %v", metas)
	}
	if err := e.svc.UnmapPeer(context.Background(), e.caller(e.admin), e.admin, f.device, provider, "wrong_device"); err != nil {
		t.Fatal(err)
	}
	if m := e.auditMeta(f.device, "remoteaccess.peer_mapping.unmapped"); len(m) != 1 || m[0]["peerIdHash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("unmap audit: %v", m)
	}
}

// ---- observations ----

func (e *env) rec(id, peer string, start time.Time) remoteaccess.ObservedSession {
	return remoteaccess.ObservedSession{ProviderSessionID: id, PeerID: peer, StartedAt: start, Source: "rustdesk.audit", ObservedAt: e.clk.now().Add(time.Minute)}
}

func (e *env) summary() application.ObservationSummary {
	e.t.Helper()
	s, err := e.svc.ObservationSummary(context.Background(), application.Principal{UserID: e.admin.UserID, ViewSessions: true})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) importRecords(rs ...remoteaccess.ObservedSession) application.ImportResult {
	e.t.Helper()
	res, err := e.svc.ImportObservations(context.Background(), "job:"+newID(), provider, rs)
	if err != nil {
		e.t.Fatalf("import: %v", err)
	}
	return res
}

func TestObservationAttribution(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	x, _ := e.launchAndExchange(e.mustRequest(f))
	launched := *x.Session.LaunchedAt
	other := e.mustRequest(e.fixture("corporate")) // authorized, never launched
	before := e.summary().UnattributedRecords
	uid := newID()
	end := launched.Add(3 * time.Minute)
	good := e.rec("g-"+uid, f.peer, launched.Add(30*time.Second))
	good.EndedAt, good.Operator = &end, "tech@example.org"
	wrongPeer := e.rec("w-"+uid, newPeer(), launched)
	early := e.rec("e-"+uid, f.peer, launched.Add(-time.Hour))
	res := e.importRecords(wrongPeer, early)
	if res.Records != 2 || res.Flagged != 2 || res.SessionsUpdated != 0 {
		t.Fatalf("unmatched: %+v", res)
	}
	if c := e.get(x.SessionID); c.ObservedSource != nil || c.ObservedConnectedAt != nil {
		t.Fatalf("observed facts invented: %+v", c)
	}
	res = e.importRecords(wrongPeer, early, good)
	if res.SessionsUpdated != 1 || res.Flagged != 0 {
		t.Fatalf("match: %+v", res)
	}
	c := e.get(x.SessionID)
	if c.ObservedSource == nil || *c.ObservedSource != "rustdesk.audit" || !c.ObservedConnectedAt.Equal(good.StartedAt) || c.ObservedEndedAt == nil || !c.ObservedEndedAt.Equal(end) {
		t.Fatalf("observed: %+v", c)
	}
	if c.Status != application.StatusLaunched || c.ClosedAt != nil || c.Consent != application.ConsentUnknown {
		t.Fatalf("observation changed authorized facts: %+v", c)
	}
	if o := e.get(other.ID); o.ObservedSource != nil {
		t.Fatalf("unlaunched session touched: %+v", o)
	}
	var attributed int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM remoteaccess.provider_session_records WHERE session_id = $1::uuid AND flag IS NULL AND operator = 'tech@example.org'`, x.SessionID).Scan(&attributed)
	if attributed != 1 {
		t.Fatalf("attributed records: %d", attributed)
	}
	if sum := e.summary(); sum.UnattributedRecords-before != 2 || sum.ByReason[application.FlagUnattributed] < 2 {
		t.Fatalf("summary: %+v (before %d)", sum, before)
	}
	// Idempotent: nothing changes, nothing is flagged again, no audit run entry.
	v := c.Version
	if res = e.importRecords(wrongPeer, early, good); res.SessionsUpdated != 0 || res.Flagged != 0 || e.get(x.SessionID).Version != v {
		t.Fatalf("repeat: %+v", res)
	}
	// A second record in the same session is a duplicate and does not feed the session.
	dup := e.rec("d-"+uid, f.peer, launched.Add(5*time.Minute))
	if res = e.importRecords(dup); res.Flagged != 1 || res.SessionsUpdated != 0 {
		t.Fatalf("duplicate: %+v", res)
	}
	if got := e.get(x.SessionID); !got.ObservedConnectedAt.Equal(good.StartedAt) {
		t.Fatalf("duplicate overwrote the session: %+v", got)
	}
	var flag string
	_ = e.pool.QueryRow(ctx, `SELECT flag FROM remoteaccess.provider_session_records WHERE provider_session_id = $1`, dup.ProviderSessionID).Scan(&flag)
	if flag != application.FlagDuplicate {
		t.Fatalf("duplicate flag: %q", flag)
	}
	// The job reads the provider and never creates sessions.
	e.fake.Observed = []remoteaccess.ObservedSession{good}
	if err := e.svc.HandleObserve(ctx, jobWithID("1")); err != nil || e.fake.SessionCall != 1 {
		t.Fatalf("job: %v %d", err, e.fake.SessionCall)
	}
	e.fake.FailSession = errors.New("provider down")
	if err := e.svc.HandleObserve(ctx, jobWithID("2")); err == nil {
		t.Fatal("provider failure swallowed")
	}
	// A closed session still receives the provider's end time of its attributed record.
	if _, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, x.SessionID, &c.Version, "completed"); err != nil {
		t.Fatal(err)
	}
	later := good
	later.EndedAt = ptr(end.Add(30 * time.Second))
	if res = e.importRecords(later); res.SessionsUpdated != 1 {
		t.Fatalf("end update after close: %+v", res)
	}
}

func TestObservationAfterCloseAndTie(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	x, _ := e.launchAndExchange(e.mustRequest(f))
	if _, err := e.svc.Close(context.Background(), e.caller(e.tech), e.tech, x.SessionID, &x.Session.Version, "completed"); err != nil {
		t.Fatal(err)
	}
	closedAt := *e.get(x.SessionID).ClosedAt
	uid := newID()
	// Within the slack but after the close: flagged, never fed to the session.
	late := e.rec("l-"+uid, f.peer, closedAt.Add(time.Minute))
	if res := e.importRecords(late); res.Flagged != 1 || res.SessionsUpdated != 0 {
		t.Fatalf("after close: %+v", res)
	}
	var flag string
	var sid *string
	_ = e.pool.QueryRow(context.Background(), `SELECT flag, session_id::text FROM remoteaccess.provider_session_records WHERE provider_session_id = $1`, late.ProviderSessionID).Scan(&flag, &sid)
	if flag != application.FlagAfterClose || sid == nil || *sid != x.SessionID || e.get(x.SessionID).ObservedSource != nil {
		t.Fatalf("after close: %q %v", flag, sid)
	}
	// Far after the close: no session qualifies at all.
	far := e.rec("f-"+uid, f.peer, closedAt.Add(application.ObservationSlack+time.Minute))
	if res := e.importRecords(far); res.Flagged != 1 {
		t.Fatalf("far: %+v", res)
	}
	// A tie: the peer moves to another Device whose session launches inside the first session's window.
	if err := e.svc.UnmapPeer(context.Background(), e.caller(e.admin), e.admin, f.device, provider, "wrong_device"); err != nil {
		t.Fatal(err)
	}
	g := e.fixture("corporate")
	if _, err := e.svc.MapPeer(context.Background(), e.caller(e.admin), e.admin, g.device, provider, f.peer, "correction"); err != nil {
		t.Fatal(err)
	}
	y, _ := e.launchAndExchange(e.mustRequest(g))
	tie := e.rec("t-"+uid, f.peer, y.Session.LaunchedAt.Add(10*time.Second))
	if res := e.importRecords(tie); res.Flagged != 1 || res.SessionsUpdated != 0 {
		t.Fatalf("tie: %+v", res)
	}
	if e.get(y.SessionID).ObservedSource != nil {
		t.Fatal("an ambiguous record fed a session")
	}
}

func TestObservationInvalidRecordsAreSkipped(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	x, _ := e.launchAndExchange(e.mustRequest(f))
	start := x.Session.LaunchedAt.Add(time.Minute)
	uid := newID()
	mod := func(id string, fn func(*remoteaccess.ObservedSession)) remoteaccess.ObservedSession {
		r := e.rec(id+"-"+uid, f.peer, start)
		fn(&r)
		return r
	}
	bad := []remoteaccess.ObservedSession{
		mod("src", func(r *remoteaccess.ObservedSession) { r.Source = "evil.source" }),
		mod("nosrc", func(r *remoteaccess.ObservedSession) { r.Source = "" }),
		mod("zero", func(r *remoteaccess.ObservedSession) { r.StartedAt = time.Time{} }),
		mod("future", func(r *remoteaccess.ObservedSession) { r.StartedAt = e.clk.now().Add(time.Hour) }),
		mod("ancient", func(r *remoteaccess.ObservedSession) { r.StartedAt = e.clk.now().Add(-365 * 24 * time.Hour) }),
		mod("endbefore", func(r *remoteaccess.ObservedSession) { r.EndedAt = ptr(start.Add(-time.Minute)) }),
		mod("endfuture", func(r *remoteaccess.ObservedSession) { r.EndedAt = ptr(e.clk.now().Add(time.Hour)) }),
		mod("peer", func(r *remoteaccess.ObservedSession) { r.PeerID = "not;valid" }),
		mod("id", func(r *remoteaccess.ObservedSession) { r.ProviderSessionID = "bad id;" }),
		mod("noobs", func(r *remoteaccess.ObservedSession) { r.ObservedAt = time.Time{} }),
		mod("op", func(r *remoteaccess.ObservedSession) { r.Operator = strings.Repeat("x", 101) }),
		mod("opctl", func(r *remoteaccess.ObservedSession) { r.Operator = "bad\x00op" }),
	}
	good := e.rec("ok-"+uid, f.peer, start)
	res, err := e.svc.ImportObservations(context.Background(), "job:bad", provider, append(bad, good))
	if err != nil || res.Skipped != len(bad) || res.Records != 1 || res.SessionsUpdated != 1 {
		t.Fatalf("invalid records: %+v %v", res, err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM remoteaccess.provider_session_records WHERE provider_session_id LIKE '%' || $1`, uid).Scan(&n)
	if n != 1 {
		t.Fatalf("stored records: %d", n)
	}
	// A record whose identity changed at the provider is a conflict: skipped, the stored one is kept.
	changed := good
	changed.StartedAt = start.Add(time.Minute)
	if res = e.importRecords(changed); res.Skipped != 1 || res.Records != 0 {
		t.Fatalf("conflict: %+v", res)
	}
	// The job never aborts on a bad record.
	e.fake.Observed = append(append([]remoteaccess.ObservedSession{}, bad...), good)
	if err := e.svc.HandleObserve(context.Background(), jobWithID("bad")); err != nil {
		t.Fatalf("job aborted on bad records: %v", err)
	}
}

func TestObservationSummaryNeedsViewSessions(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.ObservationSummary(context.Background(), e.tech); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("summary without view_sessions: %v", err)
	}
}

func TestHardeningTriggers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	e.handle(s)
	mustFail := func(name, sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err == nil {
			t.Errorf("%s: statement succeeded", name)
		}
	}
	mustFail("mapping peer rewrite", `UPDATE remoteaccess.peer_mappings SET peer_id = '999999999' WHERE device_id = $1::uuid`, f.device)
	mustFail("mapping reopen/close without reason", `UPDATE remoteaccess.peer_mappings SET closed_at = now() WHERE device_id = $1::uuid`, f.device)
	mustFail("mapping device rewrite", `UPDATE remoteaccess.peer_mappings SET device_id = $2::uuid, closed_at = now(), close_reason = 'x' WHERE device_id = $1::uuid`, f.device, newID())
	mustFail("handle expiry rewrite", `UPDATE remoteaccess.launch_handles SET expires_at = expires_at + interval '1 day' WHERE session_id = $1::uuid`, s.ID)
	mustFail("handle user rewrite", `UPDATE remoteaccess.launch_handles SET user_id = $2::uuid, used_at = now() WHERE session_id = $1::uuid`, s.ID, newID())
	mustFail("session device rewrite", `UPDATE remoteaccess.sessions SET device_id = $2::uuid WHERE id = $1::uuid`, s.ID, newID())
	mustFail("session peer rewrite", `UPDATE remoteaccess.sessions SET peer_id = '999999999' WHERE id = $1::uuid`, s.ID)
	mustFail("session initiator rewrite", `UPDATE remoteaccess.sessions SET initiated_by = $2::uuid WHERE id = $1::uuid`, s.ID, newID())
	mustFail("session excluded users rewrite", `UPDATE remoteaccess.sessions SET excluded_user_ids = '{}' WHERE id = $1::uuid`, s.ID)
	mustFail("session reference rewrite", `UPDATE remoteaccess.sessions SET reference = 'RAS-X' WHERE id = $1::uuid`, s.ID)
	mustFail("truncate sessions", `TRUNCATE remoteaccess.sessions CASCADE`)
	mustFail("truncate mappings", `TRUNCATE remoteaccess.peer_mappings`)
	mustFail("truncate handles", `TRUNCATE remoteaccess.launch_handles`)
	mustFail("truncate records", `TRUNCATE remoteaccess.provider_session_records`)
	mustFail("record delete", `DELETE FROM remoteaccess.provider_session_records`)
	// The legitimate transitions still work: used_at once, closing a mapping, status changes.
	if _, err := e.pool.Exec(ctx, `UPDATE remoteaccess.launch_handles SET used_at = now() WHERE session_id = $1::uuid`, s.ID); err != nil {
		t.Errorf("use handle: %v", err)
	}
	mustFail("handle used twice", `UPDATE remoteaccess.launch_handles SET used_at = now() WHERE session_id = $1::uuid`, s.ID)
	if _, err := e.pool.Exec(ctx, `UPDATE remoteaccess.peer_mappings SET closed_at = now(), close_reason = 'replaced' WHERE device_id = $1::uuid`, f.device); err != nil {
		t.Errorf("close mapping: %v", err)
	}
	mustFail("closed mapping reopened", `UPDATE remoteaccess.peer_mappings SET closed_at = NULL, close_reason = NULL WHERE device_id = $1::uuid`, f.device)
	// One open session per Device, whatever the provider.
	mustFail("second open session, other provider", `INSERT INTO remoteaccess.sessions(device_id, ticket_id, provider, peer_id, mode, status, initiated_by, expires_at)
		VALUES ($1::uuid, $2::uuid, 'anydesk', '123456789', 'attended', 'authorized', $3::uuid, now())`, f.device, f.ticket, e.tech.UserID)
}
