package repository_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
)

func refusal(t *testing.T, err error, code string) {
	t.Helper()
	var r *application.RefusedError
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("want refusal %q, got %v", code, err)
	}
}

func TestRequestPolicyGates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := func(f fixture, mod func(*application.NewSession)) error {
		in := application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider}
		if mod != nil {
			mod(&in)
		}
		_, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
		return err
	}
	// Permission is required.
	f := e.fixture("corporate")
	noStart := principal(e.tech.UserID, false, false)
	if _, err := e.svc.Request(ctx, e.caller(noStart), noStart, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("without start_attended: %v", err)
	}
	// A caller cannot act as someone else.
	if _, err := e.svc.Request(ctx, e.caller(e.admin), e.tech, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider}); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("principal and actor differ: %v", err)
	}
	refusal(t, req(f, func(n *application.NewSession) { n.Provider = "anydesk" }), application.RefProviderDisabled)
	refusal(t, req(f, func(n *application.NewSession) { n.DeviceID = newID() }), application.RefDeviceUnknown)
	refusal(t, req(f, func(n *application.NewSession) { n.TicketID = newID() }), application.RefTicketUnknown)
	for _, bad := range []string{"", "not-a-uuid", "'; drop table x;--"} {
		if err := req(f, func(n *application.NewSession) { n.DeviceID = bad }); err == nil {
			t.Fatalf("device id %q accepted", bad)
		}
	}
	if err := req(f, func(n *application.NewSession) { n.Provider = "Rust Desk;" }); err == nil {
		t.Fatal("provider key accepted")
	}
	if err := req(f, func(n *application.NewSession) { n.MismatchReason = "because" }); err == nil {
		t.Fatal("unknown mismatch reason accepted")
	}
	if err := req(f, func(n *application.NewSession) { n.Note = "bad‮note" }); err == nil {
		t.Fatal("unsafe note accepted")
	}
	if err := req(f, func(n *application.NewSession) { n.Note = strings.Repeat("x", 501) }); err == nil {
		t.Fatal("long note accepted")
	}

	// Stale and retired Devices.
	stale := e.fixture("corporate")
	d := e.devices.m[stale.device]
	d.ObservedAt = e.clk.now().Add(-application.DeviceFreshness - time.Minute)
	e.devices.m[stale.device] = d
	refusal(t, req(stale, nil), application.RefStaleDevice)
	retired := e.fixture("corporate")
	d = e.devices.m[retired.device]
	now := e.clk.now()
	d.RetiredAt = &now
	e.devices.m[retired.device] = d
	refusal(t, req(retired, nil), application.RefDeviceRetired)

	// No mapping.
	unmapped := e.fixture("corporate")
	if err := e.svc.UnmapPeer(ctx, e.caller(e.admin), e.admin, unmapped.device, provider, "no_longer_valid"); err != nil {
		t.Fatal(err)
	}
	refusal(t, req(unmapped, nil), application.RefNoPeerMapping)

	// Ticket closed.
	closed := e.fixture("corporate")
	tk := e.tickets.m[closed.ticket]
	tk.Open = false
	e.tickets.m[closed.ticket] = tk
	refusal(t, req(closed, nil), application.RefTicketNotOpen)

	// Holder mismatch needs a reason; with a reason the session records it.
	mm := e.fixture("corporate")
	tk = e.tickets.m[mm.ticket]
	tk.AffectedUserID = newID()
	e.tickets.m[mm.ticket] = tk
	refusal(t, req(mm, nil), application.RefHolderMismatch)
	s, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, application.NewSession{DeviceID: mm.device, TicketID: mm.ticket, Provider: provider, MismatchReason: "on_behalf", Note: "  called by the manager "})
	if err != nil || s.MismatchReason == nil || *s.MismatchReason != "on_behalf" || s.Note == nil || *s.Note != "called by the manager" {
		t.Fatalf("mismatch with reason: %v %+v", err, s)
	}
	// A Device without a holder is a mismatch too.
	nh := e.fixture("corporate")
	delete(e.holders.m, *e.devices.m[nh.device].AssetID)
	refusal(t, req(nh, nil), application.RefHolderMismatch)

	// The happy path: authorized at once, consent unknown, attended.
	ok := e.fixture("corporate")
	s, err = e.request(ok, e.tech)
	if err != nil || s.Status != application.StatusAuthorized || s.Consent != application.ConsentUnknown || s.Mode != "attended" || s.MismatchReason != nil ||
		s.PeerID != ok.peer || !strings.HasPrefix(s.Reference, "RAS-") || s.InitiatedBy != e.tech.UserID {
		t.Fatalf("authorized session: %v %+v", err, s)
	}
	if !s.ExpiresAt.Equal(e.clk.now().Add(application.AuthorizedTTL)) {
		t.Fatalf("expiry %v", s.ExpiresAt)
	}
}

func TestOneOpenSessionPerDeviceAndProvider(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	_, err := e.request(f, e.tech)
	refusal(t, err, application.RefSessionOpen)
	// After it ended a new one can start.
	if _, err := e.svc.Cancel(context.Background(), e.caller(e.tech), e.tech, s.ID, &s.Version, "no_longer_needed"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.request(f, e.tech); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestLifecycleLaunchConsentClose(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	// expectedVersion is required.
	if _, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, nil); err == nil {
		t.Fatal("launch without expectedVersion")
	}
	wrong := s.Version + 5
	if _, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &wrong); !errors.Is(err, application.ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// Consent and close are not possible before launch.
	if _, err := e.svc.RecordConsent(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version, "granted"); err == nil {
		t.Fatal("consent before launch")
	}
	if _, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version, "completed"); err == nil {
		t.Fatal("close before launch")
	}
	x, _ := e.launchAndExchange(s)
	if x.URI.Reveal() != "fake://"+provider+"/"+f.peer || x.Session.Status != application.StatusLaunched || x.Session.LaunchedAt == nil {
		t.Fatalf("exchange: %+v", x.Session)
	}
	if e.fake.BuildCalls != 1 {
		t.Fatalf("provider build calls %d", e.fake.BuildCalls)
	}
	cur := e.get(s.ID)
	if !cur.ExpiresAt.Equal(e.clk.now().Add(application.LaunchedTTL)) || cur.Consent != application.ConsentUnknown {
		t.Fatalf("launched: %+v", cur)
	}
	// A launched session cannot be cancelled; the wrong version, a stranger and bad reasons are refused.
	if _, err := e.svc.Cancel(ctx, e.caller(e.tech), e.tech, s.ID, &cur.Version, "no_longer_needed"); err == nil {
		t.Fatal("cancel after launch")
	}
	if _, err := e.svc.RecordConsent(ctx, e.caller(e.tech), e.tech, s.ID, &cur.Version, "maybe"); err == nil {
		t.Fatal("bad consent decision")
	}
	other := principal(newID(), true, false)
	if _, err := e.svc.RecordConsent(ctx, e.caller(other), other, s.ID, &cur.Version, "granted"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger consent: %v", err)
	}
	cur2, err := e.svc.RecordConsent(ctx, e.caller(e.tech), e.tech, s.ID, &cur.Version, "granted")
	if err != nil || cur2.Consent != application.ConsentGranted || cur2.ConsentRecordedAt == nil || cur2.Status != application.StatusLaunched {
		t.Fatalf("consent: %v %+v", err, cur2)
	}
	if _, err := e.svc.RecordConsent(ctx, e.caller(e.tech), e.tech, s.ID, &cur2.Version, "declined"); !errors.Is(err, application.ErrConsentRecorded) {
		t.Fatalf("second consent: %v", err)
	}
	if _, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, s.ID, &cur2.Version, "because"); err == nil {
		t.Fatal("bad close reason")
	}
	closed, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, s.ID, &cur2.Version, "completed")
	if err != nil || closed.Status != application.StatusClosed || closed.StatusReason == nil || *closed.StatusReason != "completed" || closed.ClosedAt == nil {
		t.Fatalf("close: %v %+v", err, closed)
	}
	// Turaco infers nothing from the provider: observed facts stay unknown.
	if closed.ObservedConnectedAt != nil || closed.ObservedEndedAt != nil || closed.ObservedSource != nil {
		t.Fatalf("observed facts invented: %+v", closed)
	}
	d, err := e.svc.GetSession(ctx, e.tech, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, tr := range d.Transitions {
		ops = append(ops, tr.Operation)
	}
	if strings.Join(ops, ",") != "request,authorized,launched,closed" {
		t.Fatalf("transitions: %v", ops)
	}
	// The audit trail names every step.
	assertAudit(t, e, s.ID, "remoteaccess.session.requested", "remoteaccess.session.authorized", "remoteaccess.session.launch_handle_issued",
		"remoteaccess.session.launched", "remoteaccess.session.consent_recorded", "remoteaccess.session.closed")
	// After the end a new close is refused.
	if _, err := e.svc.Close(ctx, e.caller(e.tech), e.tech, s.ID, &closed.Version, "completed"); err == nil {
		t.Fatal("second close")
	}
}

func assertAudit(t *testing.T, e *env, targetID string, actions ...string) {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT action FROM platform.audit_events WHERE target_id = $1 ORDER BY occurred_at, id`, targetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		got[a] = true
	}
	for _, a := range actions {
		if !got[a] {
			t.Errorf("audit action %s missing for %s (have %v)", a, targetID, got)
		}
	}
}

func TestConsentDeclinedEndsSession(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	x, _ := e.launchAndExchange(s)
	out, err := e.svc.RecordConsent(context.Background(), e.caller(e.tech), e.tech, s.ID, &x.Session.Version, "declined")
	if err != nil || out.Status != application.StatusClosed || out.Consent != application.ConsentDeclined || *out.StatusReason != application.ReasonConsentDeclined {
		t.Fatalf("declined: %v %+v", err, out)
	}
}

func TestLaunchHandleRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	// Only the initiator can launch; a stranger sees nothing, whatever the version.
	other := principal(newID(), true, false)
	if _, err := e.svc.Launch(ctx, e.caller(other), other, s.ID, &s.Version); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger launch: %v", err)
	}
	wrong := 99
	if _, err := e.svc.Launch(ctx, e.caller(other), other, s.ID, &wrong); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("stranger launch with wrong version must not reveal the session: %v", err)
	}
	noStart := principal(e.tech.UserID, false, false)
	if _, err := e.svc.Launch(ctx, e.caller(noStart), noStart, s.ID, &s.Version); !errors.Is(err, application.ErrForbidden) {
		t.Fatalf("launch needs start_attended: %v", err)
	}
	h, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version)
	if err != nil || len(h.Token) != 43 || !h.ExpiresAt.Equal(e.clk.now().Add(application.LaunchHandleTTL)) {
		t.Fatalf("launch: %v %+v", err, h)
	}
	// At most one valid handle.
	if _, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version); !errors.Is(err, application.ErrHandleActive) {
		t.Fatalf("second handle: %v", err)
	}
	// Wrong user, garbage and unknown tokens look the same.
	if _, err := e.svc.Exchange(ctx, e.caller(other), other, h.Token); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("wrong user: %v", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 43), strings.Repeat("!", 43), h.Token + "A", "'; select 1;--"} {
		if _, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, bad); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}
	if e.get(s.ID).Status != application.StatusAuthorized {
		t.Fatal("failed exchanges must not launch")
	}
	// Expiry after 60 seconds: 410, the session stays authorized, a new handle can be issued.
	e.clk.advance(application.LaunchHandleTTL + time.Second)
	if _, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h.Token); !errors.Is(err, application.ErrHandleExpired) {
		t.Fatalf("expired: %v", err)
	}
	h2, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version)
	if err != nil {
		t.Fatalf("new handle after expiry: %v", err)
	}
	x, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h2.Token)
	if err != nil || x.Session.Status != application.StatusLaunched {
		t.Fatalf("exchange: %v", err)
	}
	// Single use.
	if _, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h2.Token); !errors.Is(err, application.ErrHandleUsed) {
		t.Fatalf("second use: %v", err)
	}
	if e.fake.BuildCalls != 1 {
		t.Fatalf("the link must be built once, got %d", e.fake.BuildCalls)
	}
}

func TestExchangeAfterCancelAndSessionExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.mustRequest(e.fixture("corporate"))
	h, _ := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version)
	if _, err := e.svc.Cancel(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version, "wrong_device"); err != nil {
		t.Fatal(err)
	}
	var tr *application.InvalidTransitionError
	if _, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h.Token); !errors.As(err, &tr) {
		t.Fatalf("exchange after cancel: %v", err)
	}
	// The session's own 15 minute window also stops an exchange even with a fresh handle.
	s2 := e.mustRequest(e.fixture("corporate"))
	e.clk.advance(application.AuthorizedTTL + time.Second)
	if _, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s2.ID, &s2.Version); !errors.Is(err, application.ErrHandleExpired) {
		t.Fatalf("launch after the session expired: %v", err)
	}
}

func TestLaunchFailureEndsSession(t *testing.T) {
	e := newEnv(t)
	s := e.mustRequest(e.fixture("corporate"))
	h, _ := e.svc.Launch(context.Background(), e.caller(e.tech), e.tech, s.ID, &s.Version)
	e.fake.FailBuild = errors.New("provider down")
	if _, err := e.svc.Exchange(context.Background(), e.caller(e.tech), e.tech, h.Token); !errors.Is(err, application.ErrLaunchFailed) {
		t.Fatalf("failure: %v", err)
	}
	cur := e.get(s.ID)
	if cur.Status != application.StatusFailed || *cur.StatusReason != application.ReasonLaunchFailed || cur.ClosedAt == nil || cur.LaunchedAt != nil {
		t.Fatalf("failed session: %+v", cur)
	}
	// The handle is spent.
	e.fake.FailBuild = nil
	if _, err := e.svc.Exchange(context.Background(), e.caller(e.tech), e.tech, h.Token); !errors.Is(err, application.ErrHandleUsed) {
		t.Fatalf("spent handle: %v", err)
	}
}

func TestLaunchLinkIsNeverPersistedLoggedOrAudited(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	s := e.mustRequest(f)
	x, token := e.launchAndExchange(s)
	if _, err := e.svc.Close(context.Background(), e.caller(e.tech), e.tech, s.ID, &x.Session.Version, "completed"); err != nil {
		t.Fatal(err)
	}
	link := x.URI.Reveal()
	ctx := context.Background()
	for _, q := range []string{
		`SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND (coalesce(before_data::text,'') || coalesce(after_data::text,'') || coalesce(metadata::text,'')) LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM platform.outbox_events WHERE correlation_id IN (SELECT correlation_id FROM platform.audit_events WHERE target_id = $1) AND payload::text LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM remoteaccess.sessions s WHERE s.id = $1::uuid AND s::text LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM remoteaccess.session_transitions t WHERE t.session_id = $1::uuid AND t::text LIKE '%' || $2 || '%'`,
		`SELECT count(*) FROM remoteaccess.launch_handles h WHERE h.session_id = $1::uuid AND h::text LIKE '%' || $2 || '%'`,
	} {
		for _, secret := range []string{link, token, "fake://"} {
			var n int
			if err := e.pool.QueryRow(ctx, q, s.ID, secret).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("secret %q found by %s", secret[:min(len(secret), 8)], q[:40])
			}
		}
	}
	// Only the hash of the token is stored.
	var hashLen int
	if err := e.pool.QueryRow(ctx, `SELECT length(token_hash) FROM remoteaccess.launch_handles WHERE session_id = $1::uuid`, s.ID).Scan(&hashLen); err != nil || hashLen != 32 {
		t.Fatalf("token hash: %v %d", err, hashLen)
	}
}

func TestPendingApprovalRoundTripAndSeparationOfDuties(t *testing.T) {
	e := newEnv(t, "personal")
	ctx := context.Background()
	f := e.fixture("personal")
	in := application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider}
	if _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); err == nil {
		t.Fatal("approver is required")
	} else {
		refusal(t, err, application.RefApproverRequired)
	}
	both := application.Approver{UserID: &e.admin.UserID, TeamID: &e.admin.UserID}
	in.Approver = both
	refusal(t, func() error { _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); return err }(), application.RefApproverRequired)
	// An approver without remote_access.admin and the initiator are no eligible approvers.
	plain := newID()
	in.Approver = application.Approver{UserID: &plain}
	if _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("approver without admin: %v", err)
	}
	e.approvers.perms[e.tech.UserID] = map[string]struct{}{application.PermAdmin: {}}
	in.Approver = application.Approver{UserID: &e.tech.UserID}
	if _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("initiator as approver: %v", err)
	}
	// The Ticket requester is excluded as well.
	rep := e.tickets.m[f.ticket].ReporterUserID
	e.approvers.perms[rep] = map[string]struct{}{application.PermAdmin: {}}
	in.Approver = application.Approver{UserID: &rep}
	if _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("ticket requester as approver: %v", err)
	}
	// A Team needs a member with the permission.
	team := newID()
	e.approvers.teams[team] = []string{plain}
	in.Approver = application.Approver{TeamID: &team}
	if _, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in); !errors.Is(err, application.ErrNoEligibleApprover) {
		t.Fatalf("team without eligible member: %v", err)
	}
	e.approvers.teams[team] = []string{plain, e.admin.UserID}
	s, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
	if err != nil || s.Status != application.StatusPendingApproval || s.ApprovalID == nil {
		t.Fatalf("pending: %v %+v", err, s)
	}
	// A pending session cannot be launched.
	if _, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version); err == nil {
		t.Fatal("launch while pending")
	}
	// A decision by someone who is excluded rejects the session; so does one for another approval.
	e.approvals.decide(*s.ApprovalID, "approved", e.tech.UserID)
	if err := e.decide(s.ID, *s.ApprovalID, "approve"); err != nil {
		t.Fatal(err)
	}
	cur := e.get(s.ID)
	if cur.Status != application.StatusRejected || *cur.StatusReason != application.ReasonApproverNotAuthorized {
		t.Fatalf("excluded decider: %+v", cur)
	}
	// Idempotent: replaying the event changes nothing.
	if err := e.decide(s.ID, *s.ApprovalID, "approve"); err != nil || e.get(s.ID).Version != cur.Version {
		t.Fatalf("replay: %v", err)
	}
	// Fresh session: a valid approval authorizes; the event for another approval id is refused.
	f2 := e.fixture("personal")
	in.DeviceID, in.TicketID = f2.device, f2.ticket
	in.Approver = application.Approver{UserID: &e.admin.UserID}
	s2, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
	if err != nil {
		t.Fatal(err)
	}
	e.approvals.decide(*s2.ApprovalID, "approved", e.admin.UserID)
	if err := e.decide(s2.ID, *s2.ApprovalID, "approve"); err != nil {
		t.Fatal(err)
	}
	cur = e.get(s2.ID)
	if cur.Status != application.StatusAuthorized || !cur.ExpiresAt.Equal(e.clk.now().Add(application.AuthorizedTTL)) {
		t.Fatalf("approved: %+v", cur)
	}
	if x, _ := e.launchAndExchange(cur); x.Session.Status != application.StatusLaunched {
		t.Fatal("approved session must launch")
	}
	f3 := e.fixture("personal")
	in.DeviceID, in.TicketID = f3.device, f3.ticket
	s3, _ := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
	e.approvals.decide(*s3.ApprovalID, "approved", e.admin.UserID)
	if err := e.decide(s3.ID, newID(), "approve"); err != nil {
		t.Fatal(err)
	}
	if cur = e.get(s3.ID); cur.Status != application.StatusRejected {
		t.Fatalf("approval id mismatch must not authorize: %+v", cur)
	}
	// Rejection by an eligible approver.
	f4 := e.fixture("personal")
	in.DeviceID, in.TicketID = f4.device, f4.ticket
	s4, _ := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
	e.approvals.decide(*s4.ApprovalID, "rejected", e.admin.UserID)
	if err := e.decide(s4.ID, *s4.ApprovalID, "reject"); err != nil {
		t.Fatal(err)
	}
	if cur = e.get(s4.ID); cur.Status != application.StatusRejected || *cur.StatusReason != application.ReasonApprovalRejected {
		t.Fatalf("rejected: %+v", cur)
	}
	// Decisions on other subjects are ignored; garbage is permanent.
	if err := e.decide(newID(), newID(), "approve"); err != nil {
		t.Fatalf("unknown session: %v", err)
	}
	// Cancelling a pending session cancels its Approval.
	f5 := e.fixture("personal")
	in.DeviceID, in.TicketID = f5.device, f5.ticket
	s5, _ := e.svc.Request(ctx, e.caller(e.tech), e.tech, in)
	if _, err := e.svc.Cancel(ctx, e.caller(e.tech), e.tech, s5.ID, &s5.Version, "no_longer_needed"); err != nil {
		t.Fatal(err)
	}
	if e.approvals.byID[*s5.ApprovalID].Status != "cancelled" {
		t.Fatal("approval not cancelled")
	}
	// Corporate devices need no approval under this policy.
	if s := e.mustRequest(e.fixture("corporate")); s.Status != application.StatusAuthorized {
		t.Fatalf("corporate: %+v", s)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < application.RateLimit; i++ {
		if _, err := e.request(e.fixture("corporate"), e.tech); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := e.request(e.fixture("corporate"), e.tech); !errors.Is(err, application.ErrRateLimited) {
		t.Fatalf("21st request: %v", err)
	}
	// The limit is per user.
	other := principal(newID(), true, false)
	if _, err := e.request(e.fixture("corporate"), other); err != nil {
		t.Fatalf("other user: %v", err)
	}
}

func TestConcurrentRequestsForOneDevice(t *testing.T) {
	e := newEnv(t)
	f := e.fixture("corporate")
	var wg sync.WaitGroup
	results := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = e.request(f, e.tech)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else {
			var r *application.RefusedError
			if !errors.As(err, &r) || r.Code != application.RefSessionOpen {
				t.Fatalf("unexpected: %v", err)
			}
		}
	}
	if ok != 1 {
		t.Fatalf("%d sessions opened for one device", ok)
	}
}

func TestConcurrentExchangeAndCancel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		s := e.mustRequest(e.fixture("corporate"))
		h, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var exErr, exErr2, cancelErr error
		wg.Add(3)
		go func() { defer wg.Done(); _, exErr = e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h.Token) }()
		go func() { defer wg.Done(); _, exErr2 = e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h.Token) }()
		go func() {
			defer wg.Done()
			_, cancelErr = e.svc.Cancel(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version, "no_longer_needed")
		}()
		wg.Wait()
		wins := 0
		for _, err := range []error{exErr, exErr2} {
			if err == nil {
				wins++
			}
		}
		cur := e.get(s.ID)
		switch {
		case wins == 1 && cur.Status == application.StatusLaunched && cancelErr != nil:
		case wins == 0 && cur.Status == application.StatusCancelled && cancelErr == nil:
		default:
			t.Fatalf("round %d: wins=%d cancel=%v exchange=%v/%v status=%s", round, wins, cancelErr, exErr, exErr2, cur.Status)
		}
		if wins == 1 && e.get(s.ID).LaunchedAt == nil {
			t.Fatal("launched without launched_at")
		}
	}
}

func TestExpiryJob(t *testing.T) {
	e := newEnv(t, "personal")
	ctx := context.Background()
	unlaunched := e.mustRequest(e.fixture("corporate"))
	launched, _ := e.launchAndExchange(e.mustRequest(e.fixture("corporate")))
	fresh := e.mustRequest(e.fixture("corporate"))
	pf := e.fixture("personal")
	pending, err := e.svc.Request(ctx, e.caller(e.tech), e.tech, application.NewSession{DeviceID: pf.device, TicketID: pf.ticket, Provider: provider,
		Approver: application.Approver{UserID: &e.admin.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is due yet.
	if n, err := e.svc.ExpireSessions(ctx, "job:1"); err != nil || n != 0 {
		t.Fatalf("early run: %d %v", n, err)
	}
	e.clk.advance(application.AuthorizedTTL + time.Minute)
	if n, err := e.svc.ExpireSessions(ctx, "job:2"); err != nil || n < 2 {
		t.Fatalf("authorized expiry: %d %v", n, err)
	}
	for _, id := range []string{unlaunched.ID, fresh.ID} {
		if c := e.get(id); c.Status != application.StatusExpired || *c.StatusReason != application.ReasonNotLaunched || c.ClosedAt == nil {
			t.Fatalf("authorized not launched: %+v", c)
		}
	}
	if c := e.get(launched.SessionID); c.Status != application.StatusLaunched {
		t.Fatalf("launched session must stay open for 8 hours: %+v", c)
	}
	if c := e.get(pending.ID); c.Status != application.StatusPendingApproval {
		t.Fatalf("pending for less than 24h: %+v", c)
	}
	e.clk.advance(application.PendingTTL)
	if _, err := e.svc.ExpireSessions(ctx, "job:3"); err != nil {
		t.Fatal(err)
	}
	if c := e.get(pending.ID); c.Status != application.StatusExpired || *c.StatusReason != application.ReasonApprovalTimeout {
		t.Fatalf("pending expiry: %+v", c)
	}
	if e.approvals.byID[*pending.ApprovalID].Status != "cancelled" {
		t.Fatal("approval of the expired session not cancelled")
	}
	if c := e.get(launched.SessionID); c.Status != application.StatusClosed || *c.StatusReason != application.ReasonExpiredOpen || c.Consent != application.ConsentUnknown {
		t.Fatalf("launched expiry: %+v", c)
	}
	// Idempotent.
	if n, err := e.svc.ExpireSessions(ctx, "job:4"); err != nil || n != 0 {
		t.Fatalf("second run: %d %v", n, err)
	}
	d, _ := e.svc.GetSession(ctx, e.tech, launched.SessionID)
	last := d.Transitions[len(d.Transitions)-1]
	if last.ActorSystem == nil || last.Operation != "closed" {
		t.Fatalf("expiry transition: %+v", last)
	}
}
