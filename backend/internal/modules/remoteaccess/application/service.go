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
	approvals Approvals
	approvers Approvers
	providers Providers
	// approvalOwnership are the Device ownerships (corporate, personal, unknown) whose sessions need an Approval.
	approvalOwnership []string
	now               func() time.Time
}

// NewService wires the use cases over the other modules' public contracts.
func NewService(store Store, devices Devices, tickets Tickets, holders Holders, approvals Approvals, approvers Approvers, providers Providers) *Service {
	return &Service{store: store, devices: devices, tickets: tickets, holders: holders, approvals: approvals, approvers: approvers,
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

// Request evaluates the policy gates and creates a session: authorized at once, or pending_approval when the
// Device's ownership requires a second approver. Requires remote_access.start_attended.
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
	if _, ok := s.providers.Get(provider); !ok {
		return Session{}, refused(RefProviderDisabled)
	}
	dev, ok, err := s.devices.Device(ctx, deviceID)
	if err != nil {
		return Session{}, fmt.Errorf("read device: %w", err)
	}
	if !ok {
		return Session{}, refused(RefDeviceUnknown)
	}
	if dev.RetiredAt != nil {
		return Session{}, refused(RefDeviceRetired)
	}
	now := s.now()
	if now.Sub(dev.ObservedAt) > DeviceFreshness {
		return Session{}, refused(RefStaleDevice)
	}
	mapping, ok, err := s.store.ActiveMapping(ctx, deviceID, provider)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, refused(RefNoPeerMapping)
	}
	t, ok, err := s.tickets.Ticket(ctx, ticketID)
	if err != nil {
		return Session{}, fmt.Errorf("read ticket: %w", err)
	}
	if !ok {
		return Session{}, refused(RefTicketUnknown)
	}
	if !t.Open {
		return Session{}, refused(RefTicketNotOpen)
	}
	var mismatch *string
	holder := ""
	if dev.AssetID != nil {
		holders, err := s.holders.UserHolders(ctx, []string{*dev.AssetID})
		if err != nil {
			return Session{}, fmt.Errorf("read holder: %w", err)
		}
		holder = holders[*dev.AssetID]
	}
	if holder == "" || !strings.EqualFold(holder, t.AffectedUserID) {
		if in.MismatchReason == "" {
			return Session{}, refused(RefHolderMismatch)
		}
		mismatch = strPtr(in.MismatchReason)
	}
	excluded := []string{p.UserID, t.ReporterUserID}
	slices.Sort(excluded)
	excluded = slices.Compact(excluded)
	needsApproval := slices.Contains(s.approvalOwnership, dev.Ownership)
	var approver Approver
	if needsApproval {
		if approver, err = normalizeApprover(in.Approver); err != nil {
			return Session{}, err
		}
		if err := s.checkApprover(ctx, approver, excluded); err != nil {
			return Session{}, err
		}
	}
	sess := Session{DeviceID: deviceID, TicketID: ticketID, Provider: provider, PeerID: mapping.PeerID, Mode: "attended",
		Status: StatusRequested, InitiatedBy: p.UserID, ExcludedUserIDs: excluded, Consent: ConsentUnknown, MismatchReason: mismatch,
		ExpiresAt: now.Add(PendingTTL), Note: note}
	var out Session
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := s.store.LockUserTx(ctx, tx, p.UserID); err != nil {
			return err
		}
		n, err := s.store.CountRequestsTx(ctx, tx, p.UserID, now.Add(-RateWindow))
		if err != nil {
			return err
		}
		if n >= RateLimit {
			return ErrRateLimited
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
		if mismatch != nil {
			meta["reason"] = *mismatch
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
		next.Status, next.StatusReason, next.ExpiresAt = StatusAuthorized, nil, now.Add(AuthorizedTTL)
		_, err := s.commit(ctx, tx, c, cur, next, "authorized", "", meta)
		return err
	}
	next.Status, next.StatusReason, next.ClosedAt = StatusRejected, strPtr(ReasonApprovalRejected), &now
	_, err = s.commit(ctx, tx, c, cur, next, "approval_rejected", ReasonApprovalRejected, meta)
	return err
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
