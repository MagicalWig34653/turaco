package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Remote Access operations. Audit actions are remoteaccess.session.<operation> (requested,
// approval_requested, authorized, approval_rejected, launch_handle_issued, launched, consent_recorded, closed,
// cancelled, expired, launch_failed, observation_imported) and remoteaccess.peer_mapping.<mapped|unmapped>,
// written in the mutation's transaction with ids and reason codes only: notes, peer ids and launch links are
// never copied into audit, events or logs.
type Service struct {
	store     Store
	devices   Devices
	tickets   Tickets
	holders   Holders
	dir       Directory
	approvals Approvals
	approvers Approvers
	providers Providers
	// approvalOwnership are the Device ownerships (corporate, personal, unknown) whose sessions need an Approval.
	approvalOwnership []string
	now               func() time.Time
}

// NewService wires the use cases over the other modules' public contracts.
func NewService(store Store, devices Devices, tickets Tickets, holders Holders, dir Directory, approvals Approvals, approvers Approvers, providers Providers) *Service {
	return &Service{store: store, devices: devices, tickets: tickets, holders: holders, dir: dir, approvals: approvals, approvers: approvers,
		providers: providers, now: func() time.Time { return time.Now().UTC() }}
}

// WithApprovalOwnership sets the Device ownerships that require a second approver (REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP).
func (s *Service) WithApprovalOwnership(o []string) *Service {
	s.approvalOwnership = slices.Clone(o)
	return s
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// providerKey mirrors the provider column CHECK.
	providerKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)
	systemActor = audit.SystemActor("remoteaccess-workflow")
)

func checkID(id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", invalid("ids must be UUIDs")
	}
	return strings.ToLower(id), nil
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func oneOf(s string, set []string) bool { return slices.Contains(set, s) }

func cleanNote(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > maxNote || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return nil, invalid("note must be at most %d characters without control or invisible formatting characters", maxNote)
	}
	return &s, nil
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: "remoteaccess." + action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func sessionState(s *Session) any {
	if s == nil {
		return nil
	}
	return map[string]any{"status": s.Status, "consent": s.Consent, "version": s.Version}
}

func terminal(status string) bool {
	switch status {
	case StatusClosed, StatusRejected, StatusCancelled, StatusExpired, StatusFailed:
		return true
	}
	return false
}

func (s *Service) recordTransition(ctx context.Context, tx pgx.Tx, c Caller, id string, from *string, to, op, reason string) error {
	t := Transition{SessionID: id, FromStatus: from, ToStatus: to, Operation: op, Reason: strPtr(reason), CorrelationID: c.CorrelationID}
	if c.Actor.UserID != "" {
		t.ActorUserID = &c.Actor.UserID
	} else {
		name := c.Actor.System
		t.ActorSystem = &name
	}
	return s.store.InsertTransitionTx(ctx, tx, t)
}

// commit writes a changed session, its transition when the status moved, the events and the audit entry in the
// caller's transaction. The session must be locked. op names the audit action and the transition operation.
func (s *Service) commit(ctx context.Context, tx pgx.Tx, c Caller, cur, next Session, op, reason string, meta map[string]any) (Session, error) {
	out, err := s.store.UpdateSessionTx(ctx, tx, next)
	if err != nil {
		return Session{}, err
	}
	if cur.Status != out.Status {
		if err := s.recordTransition(ctx, tx, c, out.ID, &cur.Status, out.Status, op, reason); err != nil {
			return Session{}, err
		}
		payload := map[string]any{"sessionId": out.ID, "deviceId": out.DeviceID, "ticketId": out.TicketID, "provider": out.Provider,
			"status": out.Status, "previousStatus": cur.Status, "operation": op}
		if reason != "" {
			payload["reason"] = reason
		}
		switch {
		case out.Status == StatusAuthorized:
			err = publish(ctx, tx, c, EventAuthorized, payload)
		case out.Status == StatusLaunched:
			err = publish(ctx, tx, c, EventLaunched, payload)
		case terminal(out.Status):
			err = publish(ctx, tx, c, EventClosed, payload)
		}
		if err != nil {
			return Session{}, err
		}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["deviceId"], meta["ticketId"], meta["provider"] = out.DeviceID, out.TicketID, out.Provider
	if reason != "" {
		meta["reason"] = reason
	}
	if err := recordAudit(ctx, tx, c, "session."+op, "remote_access_session", out.ID, sessionState(&cur), sessionState(&out), meta); err != nil {
		return Session{}, err
	}
	return out, nil
}

// lock locks a session, checks the version and that the status is one of from.
// access (when not nil) is checked first: a caller who may not touch the session sees ErrNotFound, never a
// version or status refusal.
func (s *Service) lock(ctx context.Context, tx pgx.Tx, id string, access func(Session) bool, expected int, op string, from ...string) (Session, error) {
	if !uuidPattern.MatchString(id) {
		return Session{}, ErrNotFound
	}
	cur, err := s.store.LockSessionTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Session{}, err
	}
	if access != nil && !access(cur) {
		return Session{}, ErrNotFound
	}
	if expected != cur.Version {
		return Session{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.Status) {
		return Session{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

// own reports whether the caller may drive the session: its initiator, or an administrator.
func own(p Principal, cur Session) bool {
	return p.UserID != "" && (cur.InitiatedBy == p.UserID || p.Admin)
}

// NewSession describes a session to request.
type NewSession struct {
	DeviceID string
	TicketID string
	Provider string
	// MismatchReason is required when the Ticket's affected User is not the Device's current holder.
	MismatchReason string
	// Note is optional plain text (never audited).
	Note string
	// Approver is required when policy asks for an Approval (exactly one of User and Team).
	Approver Approver
}

// permTicketsManage lets a technician work any Ticket (servicedesk permission registry).
const permTicketsManage = "tickets.manage"

// Request evaluates the policy gates and creates a session: authorized at once, or pending_approval when the
// Device's ownership (or a recorded holder mismatch) requires a second approver. Every attempt after input
// validation, refused ones included, counts toward the per-user rate limit. Requires remote_access.start_attended.
func (s *Service) Request(ctx context.Context, c Caller, p Principal, in NewSession) (Session, error) {
	if err := c.validate(); err != nil {
		return Session{}, err
	}
	if !p.Start || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Session{}, ErrForbidden
	}
	deviceID, err := checkID(in.DeviceID)
	if err != nil {
		return Session{}, err
	}
	ticketID, err := checkID(in.TicketID)
	if err != nil {
		return Session{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !providerKey.MatchString(provider) {
		return Session{}, invalid("provider is not a valid provider key")
	}
	if in.MismatchReason != "" && !oneOf(in.MismatchReason, MismatchReasons) {
		return Session{}, invalid("reason must be one of %s", strings.Join(MismatchReasons, ", "))
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	if err := s.countAttempt(ctx, p.UserID, now); err != nil {
		return Session{}, err
	}
	res, mapping, err := s.requestChecks(ctx, p.UserID, deviceID, ticketID, provider, in.MismatchReason)
	if err != nil {
		return Session{}, s.auditRefusal(ctx, c, deviceID, ticketID, provider, err)
	}
	excluded := []string{p.UserID, res.Ticket.ReporterUserID}
	slices.Sort(excluded)
	excluded = slices.Compact(excluded)
	var approver Approver
	if res.NeedsApproval {
		if approver, err = normalizeApprover(in.Approver); err != nil {
			return Session{}, s.auditRefusal(ctx, c, deviceID, ticketID, provider, err)
		}
		if err := s.checkApprover(ctx, approver, excluded); err != nil {
			return Session{}, err
		}
	}
	sess := Session{DeviceID: deviceID, TicketID: ticketID, Provider: provider, PeerID: mapping.PeerID, Mode: "attended",
		Status: StatusRequested, InitiatedBy: p.UserID, ExcludedUserIDs: excluded, Consent: ConsentUnknown, MismatchReason: res.Mismatch,
		ExpiresAt: now.Add(PendingTTL), Note: note}
	needsApproval := res.NeedsApproval
	var out Session
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		// The mapping is locked while the session is inserted, so a concurrent remap cannot leave a session with
		// a stale peer id behind (MapPeer and UnmapPeer lock the mapping first, too).
		locked, ok, err := s.store.LockActiveMappingTx(ctx, tx, deviceID, provider)
		if err != nil {
			return err
		}
		if !ok || locked.PeerID != mapping.PeerID {
			return refused(RefMappingChanged)
		}
		cur, err := s.store.InsertSessionTx(ctx, tx, sess)
		if err != nil {
			return err
		}
		if err := s.recordTransition(ctx, tx, c, cur.ID, nil, cur.Status, "request", ""); err != nil {
			return err
		}
		if err := publish(ctx, tx, c, EventRequested, map[string]any{"sessionId": cur.ID, "deviceId": cur.DeviceID, "ticketId": cur.TicketID,
			"provider": cur.Provider, "approvalRequired": needsApproval}); err != nil {
			return err
		}
		meta := map[string]any{"deviceId": cur.DeviceID, "ticketId": cur.TicketID, "provider": cur.Provider, "approvalRequired": needsApproval}
		if res.Mismatch != nil {
			meta["reason"] = *res.Mismatch
		}
		if err := recordAudit(ctx, tx, c, "session.requested", "remote_access_session", cur.ID, nil, sessionState(&cur), meta); err != nil {
			return err
		}
		next := cur
		if needsApproval {
			label := cur.Reference
			approvalID, err := s.approvals.RequestInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID, label, approver, excluded)
			if err != nil {
				return err
			}
			next.Status, next.ApprovalID, next.ExpiresAt = StatusPendingApproval, &approvalID, now.Add(PendingTTL)
			out, err = s.commit(ctx, tx, c, cur, next, "approval_requested", "", nil)
			return err
		}
		next.Status, next.ExpiresAt = StatusAuthorized, now.Add(AuthorizedTTL)
		out, err = s.commit(ctx, tx, c, cur, next, "authorized", "", nil)
		return err
	})
	return out, err
}

// countAttempt records a request attempt under the user's advisory lock and refuses with ErrRateLimited beyond
// RateLimit attempts in RateWindow. It commits on its own, so refused attempts count, too.
func (s *Service) countAttempt(ctx context.Context, userID string, now time.Time) error {
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := s.store.LockUserTx(ctx, tx, userID); err != nil {
			return err
		}
		n, err := s.store.CountAttemptsTx(ctx, tx, userID, now.Add(-RateWindow))
		if err != nil {
			return err
		}
		if n >= RateLimit {
			return ErrRateLimited
		}
		return s.store.InsertAttemptTx(ctx, tx, userID, now)
	})
}

// auditRefusal records a refused Request (reason code only) and returns the original error; a failing audit write
// is returned instead, so a refusal is never silently unaudited. Errors other than refusals pass through.
func (s *Service) auditRefusal(ctx context.Context, c Caller, deviceID, ticketID, provider string, err error) error {
	var ref *RefusedError
	if !errors.As(err, &ref) {
		return err
	}
	aerr := s.store.InTx(ctx, func(tx pgx.Tx) error {
		return recordAudit(ctx, tx, c, "session.request_refused", "device", deviceID, nil, nil,
			map[string]any{"deviceId": deviceID, "ticketId": ticketID, "provider": provider, "reason": ref.Code})
	})
	if aerr != nil {
		return fmt.Errorf("audit refused request: %w", aerr)
	}
	return err
}

// gateResult is what the policy gates found.
type gateResult struct {
	Device        DeviceInfo
	Ticket        TicketInfo
	Mismatch      *string
	NeedsApproval bool
}

// requestChecks runs every gate of a new request: provider, Device, peer mapping, then the Ticket gates.
func (s *Service) requestChecks(ctx context.Context, initiator, deviceID, ticketID, provider, mismatch string) (gateResult, PeerMapping, error) {
	if _, ok := s.providers.Get(provider); !ok {
		return gateResult{}, PeerMapping{}, refused(RefProviderDisabled)
	}
	dev, err := s.checkDevice(ctx, deviceID)
	if err != nil {
		return gateResult{}, PeerMapping{}, err
	}
	mapping, ok, err := s.store.ActiveMapping(ctx, deviceID, provider)
	if err != nil {
		return gateResult{}, PeerMapping{}, err
	}
	if !ok {
		return gateResult{}, PeerMapping{}, refused(RefNoPeerMapping)
	}
	res, err := s.checkTicket(ctx, initiator, ticketID, mismatch, dev)
	return res, mapping, err
}

// stale reports a Device that did not check in at its management provider within DeviceFreshness; an unknown
// check-in is stale. Turaco's own sync time says nothing about the Device and is not used.
func stale(dev DeviceInfo, now time.Time) bool {
	return dev.LastCheckinAt == nil || now.Sub(*dev.LastCheckinAt) > DeviceFreshness
}

// checkDevice reads the Device again and refuses unknown, retired and stale ones.
func (s *Service) checkDevice(ctx context.Context, deviceID string) (DeviceInfo, error) {
	dev, ok, err := s.devices.Device(ctx, deviceID)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("read device: %w", err)
	}
	switch {
	case !ok:
		return DeviceInfo{}, refused(RefDeviceUnknown)
	case dev.RetiredAt != nil:
		return DeviceInfo{}, refused(RefDeviceRetired)
	case stale(dev, s.now()):
		return DeviceInfo{}, refused(RefStaleDevice)
	}
	return dev, nil
}

// ownershipNeedsApproval applies REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP: when the policy is set, an ownership
// that is neither corporate nor personal (unknown) needs an Approval, too.
func (s *Service) ownershipNeedsApproval(ownership string) bool {
	if len(s.approvalOwnership) == 0 {
		return false
	}
	return slices.Contains(s.approvalOwnership, ownership) || (ownership != "corporate" && ownership != "personal")
}

// checkTicket runs the Ticket-bound gates for the initiating user: the Ticket must be open and available to the
// initiator (unknown, closed, own-reported and unauthorized Tickets all give ticket_unavailable), the Device's
// holder must be the Ticket's affected user or a mismatch reason must be recorded (which always needs an
// Approval), the approval policy is evaluated on the current ownership and somebody must be notifiable.
func (s *Service) checkTicket(ctx context.Context, initiator, ticketID, mismatch string, dev DeviceInfo) (gateResult, error) {
	t, ok, err := s.tickets.Ticket(ctx, ticketID)
	if err != nil {
		return gateResult{}, fmt.Errorf("read ticket: %w", err)
	}
	if !ok || !t.Open || strings.EqualFold(t.ReporterUserID, initiator) {
		return gateResult{}, refused(RefTicketUnavailable)
	}
	if !strings.EqualFold(t.AssigneeUserID, initiator) {
		perms, err := s.approvers.Permissions(ctx, initiator)
		if err != nil {
			return gateResult{}, fmt.Errorf("read permissions: %w", err)
		}
		if _, ok := perms[permTicketsManage]; !ok {
			return gateResult{}, refused(RefTicketUnavailable)
		}
	}
	holder := ""
	if dev.AssetID != nil {
		holders, err := s.holders.UserHolders(ctx, []string{*dev.AssetID})
		if err != nil {
			return gateResult{}, fmt.Errorf("read holder: %w", err)
		}
		holder = holders[*dev.AssetID]
	}
	res := gateResult{Device: dev, Ticket: t}
	if holder == "" || !strings.EqualFold(holder, t.AffectedUserID) {
		if mismatch == "" {
			return gateResult{}, refused(RefHolderMismatch)
		}
		res.Mismatch = strPtr(mismatch)
	}
	res.NeedsApproval = res.Mismatch != nil || s.ownershipNeedsApproval(dev.Ownership)
	notify, err := s.hasRecipient(ctx, initiator, holder, t.AffectedUserID)
	if err != nil {
		return gateResult{}, err
	}
	if !notify {
		return gateResult{}, refused(RefNoRecipient)
	}
	return res, nil
}

// hasRecipient reports whether the Device's holder or the Ticket's affected user (other than the technician) is an
// active user who can be told that a session started: the end user must always be able to see it.
func (s *Service) hasRecipient(ctx context.Context, initiator string, candidates ...string) (bool, error) {
	var ids []string
	for _, id := range candidates {
		if id != "" && !strings.EqualFold(id, initiator) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	active, err := s.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("check recipients: %w", err)
	}
	for _, id := range ids {
		if active[id] {
			return true, nil
		}
	}
	return false, nil
}

func normalizeApprover(a Approver) (Approver, error) {
	if a.UserID == nil == (a.TeamID == nil) {
		return Approver{}, refused(RefApproverRequired)
	}
	var out Approver
	for _, x := range []struct {
		in  *string
		out **string
	}{{a.UserID, &out.UserID}, {a.TeamID, &out.TeamID}} {
		if x.in != nil {
			id, err := checkID(*x.in)
			if err != nil {
				return Approver{}, invalid("the approver must be a user or team id")
			}
			*x.out = &id
		}
	}
	return out, nil
}

func (s *Service) holdsAdmin(ctx context.Context, userID string) (bool, error) {
	perms, err := s.approvers.Permissions(ctx, userID)
	if err != nil {
		return false, err
	}
	_, ok := perms[PermAdmin]
	return ok, nil
}

// checkApprover refuses (ErrNoEligibleApprover) an approver User who is excluded or lacks remote_access.admin, and
// an approver Team without a current, not excluded member holding remote_access.admin.
func (s *Service) checkApprover(ctx context.Context, a Approver, excluded []string) error {
	candidates := []string{}
	if a.UserID != nil {
		candidates = append(candidates, *a.UserID)
	} else {
		members, err := s.approvers.TeamMemberIDs(ctx, *a.TeamID)
		if err != nil {
			return err
		}
		candidates = members
	}
	for _, u := range candidates {
		if slices.Contains(excluded, u) {
			continue
		}
		ok, err := s.holdsAdmin(ctx, u)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrNoEligibleApprover
}

// verifyDecision checks a decided Approval through the Approvals contract: it is the pending one, its status
// matches the decision and the decider holds remote_access.admin and is neither the initiator nor the Ticket
// requester. It returns the decider and a refusal code for the audit (empty when verified).
func (s *Service) verifyDecision(ctx context.Context, cur Session, approvalID, decision string) (string, string, error) {
	if cur.ApprovalID == nil || !strings.EqualFold(*cur.ApprovalID, approvalID) {
		return "", "approval_mismatch", nil
	}
	list, err := s.approvals.ForSubject(ctx, cur.ID)
	if err != nil {
		return "", "", err
	}
	want := "approved"
	if decision == "reject" {
		want = "rejected"
	}
	for _, a := range list {
		if !strings.EqualFold(a.ID, approvalID) {
			continue
		}
		if a.Status != want {
			return "", "status_mismatch", nil
		}
		if a.DecidedByUserID == nil {
			return "", "no_decider", nil
		}
		decider := *a.DecidedByUserID
		if slices.Contains(cur.ExcludedUserIDs, decider) {
			return decider, "decider_excluded", nil
		}
		ok, err := s.holdsAdmin(ctx, decider)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return decider, "decider_lacks_permission", nil
		}
		return decider, "", nil
	}
	return "", "approval_unknown", nil
}

// OnApprovalDecided moves a pending session whose Approval was decided: an approval authorizes it (it must still
// be launched within AuthorizedTTL), a rejection ends it with approval_rejected. A decision that cannot be
// verified rejects the session with approver_not_authorized and cancels its pending Approvals. It runs in the
// dispatcher's claim transaction and is idempotent: events of other subjects and stale events change nothing.
func (s *Service) OnApprovalDecided(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	var p struct {
		ApprovalID  string `json:"approvalId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Decision    string `json:"decision"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode ApprovalDecided payload: %w", err))
	}
	if p.SubjectType != SubjectType {
		return nil
	}
	if p.Decision != "approve" && p.Decision != "reject" {
		return events.Permanent(fmt.Errorf("approval decision %q of session %s is unknown", p.Decision, p.SubjectID))
	}
	if p.ApprovalID == "" {
		return events.Permanent(fmt.Errorf("approval decision of session %s carries no approval id", p.SubjectID))
	}
	if !uuidPattern.MatchString(p.SubjectID) {
		return events.Permanent(errors.New("approval decision names an invalid session id"))
	}
	cur, err := s.store.LockSessionTx(ctx, tx, strings.ToLower(p.SubjectID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.Status != StatusPendingApproval {
		return nil
	}
	c := Caller{Actor: systemActor, CorrelationID: ev.CorrelationID}
	decider, why, err := s.verifyDecision(ctx, cur, p.ApprovalID, p.Decision)
	if err != nil {
		return err
	}
	meta := map[string]any{"approvalId": cur.ApprovalID, "eventApprovalId": p.ApprovalID, "decision": p.Decision}
	if decider != "" {
		meta["decidedBy"] = decider
	}
	now := s.now()
	next := cur
	if why != "" {
		meta["refusal"] = why
		if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
			return err
		}
		next.Status, next.StatusReason, next.ClosedAt = StatusRejected, strPtr(ReasonApproverNotAuthorized), &now
		_, err := s.commit(ctx, tx, c, cur, next, "approval_rejected", ReasonApproverNotAuthorized, meta)
		return err
	}
	if p.Decision == "approve" {
		// The world may have moved while the approval was pending: run the gates again.
		if code, err := s.regate(ctx, cur); err != nil {
			return err
		} else if code != "" {
			meta["refusal"] = code
			next.Status, next.StatusReason, next.ClosedAt = StatusRejected, strPtr(code), &now
			_, err := s.commit(ctx, tx, c, cur, next, "approval_rejected", code, meta)
			return err
		}
		next.Status, next.StatusReason, next.ExpiresAt = StatusAuthorized, nil, now.Add(AuthorizedTTL)
		_, err := s.commit(ctx, tx, c, cur, next, "authorized", "", meta)
		return err
	}
	next.Status, next.StatusReason, next.ClosedAt = StatusRejected, strPtr(ReasonApprovalRejected), &now
	_, err = s.commit(ctx, tx, c, cur, next, "approval_rejected", ReasonApprovalRejected, meta)
	return err
}

// regate re-runs the Device and Ticket gates for an existing session (the initiator and the recorded mismatch
// reason are the session's own). It returns the refusal code, or "" when the gates pass.
func (s *Service) regate(ctx context.Context, cur Session) (string, error) {
	dev, err := s.checkDevice(ctx, cur.DeviceID)
	if err == nil {
		var res gateResult
		mismatch := ""
		if cur.MismatchReason != nil {
			mismatch = *cur.MismatchReason
		}
		if res, err = s.checkTicket(ctx, cur.InitiatedBy, cur.TicketID, mismatch, dev); err == nil && res.NeedsApproval && cur.ApprovalID == nil {
			err = refused(RefApprovalRequired)
		}
	}
	var ref *RefusedError
	if errors.As(err, &ref) {
		return ref.Code, nil
	}
	return "", err
}

// move runs one status transition by the session's initiator (or an administrator) that needs expectedVersion.
func (s *Service) move(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, from []string, reason string,
	apply func(tx pgx.Tx, cur Session, next *Session) error) (Session, error) {
	if err := c.validate(); err != nil {
		return Session{}, err
	}
	if !p.Start && !p.Admin {
		return Session{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Session{}, err
	}
	var out Session
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lock(ctx, tx, id, func(x Session) bool { return own(p, x) }, exp, op, from...)
		if err != nil {
			return err
		}
		next := cur
		if err := apply(tx, cur, &next); err != nil {
			return err
		}
		out, err = s.commit(ctx, tx, c, cur, next, op, reason, nil)
		return err
	})
	return out, err
}

// Cancel ends a session that has not been launched (pending_approval or authorized) with a reason code; a pending
// Approval is cancelled. Initiator or administrator; requires expectedVersion.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Session, error) {
	if !oneOf(reason, CancelReasons) {
		return Session{}, invalid("reason must be one of %s", strings.Join(CancelReasons, ", "))
	}
	return s.move(ctx, c, p, id, expected, "cancelled", []string{StatusPendingApproval, StatusAuthorized}, reason,
		func(tx pgx.Tx, cur Session, next *Session) error {
			if cur.Status == StatusPendingApproval {
				if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
					return err
				}
			}
			now := s.now()
			next.Status, next.StatusReason, next.ClosedAt = StatusCancelled, &reason, &now
			return nil
		})
}

// Close ends a launched session with a reason code. Turaco never infers an end: this is an explicit close.
func (s *Service) Close(ctx context.Context, c Caller, p Principal, id string, expected *int, reason string) (Session, error) {
	if !oneOf(reason, CloseReasons) {
		return Session{}, invalid("reason must be one of %s", strings.Join(CloseReasons, ", "))
	}
	return s.move(ctx, c, p, id, expected, "closed", []string{StatusLaunched}, reason, func(_ pgx.Tx, _ Session, next *Session) error {
		now := s.now()
		next.Status, next.StatusReason, next.ClosedAt = StatusClosed, &reason, &now
		return nil
	})
}

// RecordConsent records the end user's consent as stated by the technician. Declined ends the session with
// consent_declined. Only the initiator, once, while the session is launched; requires expectedVersion.
func (s *Service) RecordConsent(ctx context.Context, c Caller, p Principal, id string, expected *int, decision string) (Session, error) {
	if decision != ConsentGranted && decision != ConsentDeclined {
		return Session{}, invalid("decision must be granted or declined")
	}
	if !p.Start {
		return Session{}, ErrForbidden
	}
	return s.move(ctx, c, Principal{UserID: p.UserID, Start: true}, id, expected, "consent_recorded", []string{StatusLaunched}, "",
		func(_ pgx.Tx, cur Session, next *Session) error {
			if cur.Consent != ConsentUnknown {
				return ErrConsentRecorded
			}
			now := s.now()
			next.Consent, next.ConsentRecordedAt, next.ConsentRecordedBy = decision, &now, &p.UserID
			if decision == ConsentDeclined {
				next.Status, next.StatusReason, next.ClosedAt = StatusClosed, strPtr(ReasonConsentDeclined), &now
			}
			return nil
		})
}

// ---- expiry ----

// ExpireSessions ends open sessions whose time ran out: an authorized session not launched in time expires, a
// pending approval older than PendingTTL expires (its Approval is cancelled) and a launched session nobody closed
// is closed with expired_open. Each session is handled in its own transaction and rechecked under the lock, so
// the job is idempotent. It returns the number of sessions it ended.
func (s *Service) ExpireSessions(ctx context.Context, correlationID string) (int, error) {
	c := Caller{Actor: audit.SystemActor("remoteaccess-expiry"), CorrelationID: correlationID}
	now := s.now()
	due, err := s.store.DueSessions(ctx, now, maxBatch)
	if err != nil {
		return 0, err
	}
	if err := s.store.PruneAttempts(ctx, now.Add(-AttemptRetention)); err != nil {
		return 0, err
	}
	ended := 0
	for _, d := range due {
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			cur, err := s.store.LockSessionTx(ctx, tx, d.ID)
			if err != nil {
				return err
			}
			if !cur.ExpiresAt.Before(now) && !cur.ExpiresAt.Equal(now) {
				return nil
			}
			next := cur
			next.ClosedAt = &now
			var op, reason string
			switch cur.Status {
			case StatusPendingApproval:
				if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
					return err
				}
				next.Status, op, reason = StatusExpired, "expired", ReasonApprovalTimeout
			case StatusAuthorized:
				next.Status, op, reason = StatusExpired, "expired", ReasonNotLaunched
			case StatusLaunched:
				next.Status, op, reason = StatusClosed, "closed", ReasonExpiredOpen
			default:
				return nil
			}
			next.StatusReason = &reason
			if _, err := s.commit(ctx, tx, c, cur, next, op, reason, nil); err != nil {
				return err
			}
			ended++
			return nil
		})
		if err != nil {
			return ended, err
		}
	}
	return ended, nil
}
