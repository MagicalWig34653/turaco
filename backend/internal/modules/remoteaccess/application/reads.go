package application

import (
	"context"
	"slices"
	"strings"
	"time"
)

// MaskPeerID hides all but the last three characters of a peer id; administrators see it whole.
func MaskPeerID(id string, admin bool) string {
	if admin || len(id) <= 3 {
		if admin {
			return id
		}
		return "***"
	}
	return "***" + id[len(id)-3:]
}

// ProviderAvailability says whether and why a provider can start a session on a Device.
type ProviderAvailability struct {
	Provider     string
	Capabilities ProviderCapabilities
	Mapped       bool
	// PeerID is masked unless the caller holds remote_access.admin.
	PeerID    string
	Available bool
	// Reasons are refusal codes (Ref*) that currently block a session; empty when Available.
	Reasons []string
}

// ProviderCapabilities mirrors the connector's declaration.
type ProviderCapabilities struct {
	Attended        bool
	ObserveSessions bool
	CloseSessions   bool
}

// Capabilities is the answer for one Device.
type Capabilities struct {
	DeviceID   string
	Enabled    bool
	Known      bool
	ObservedAt *time.Time
	// LastCheckinAt is the Device's last check-in at its management provider; Stale is derived from it (nil is stale).
	LastCheckinAt *time.Time
	Stale         bool
	Providers     []ProviderAvailability
}

// Capabilities reports which enabled providers could start an attended session on the Device and why not. It
// does not allow starting one. Requires remote_access.view.
func (s *Service) Capabilities(ctx context.Context, p Principal, deviceID string) (Capabilities, error) {
	if !p.View && !p.Start && !p.Admin {
		return Capabilities{}, ErrForbidden
	}
	deviceID, err := checkID(deviceID)
	if err != nil {
		return Capabilities{}, err
	}
	keys := s.providers.Keys()
	out := Capabilities{DeviceID: deviceID, Enabled: len(keys) > 0, Providers: []ProviderAvailability{}}
	dev, ok, err := s.devices.Device(ctx, deviceID)
	if err != nil {
		return Capabilities{}, err
	}
	out.Known = ok
	if ok {
		at := dev.ObservedAt
		out.ObservedAt = &at
		out.LastCheckinAt = dev.LastCheckinAt
		out.Stale = stale(dev, s.now())
	}
	for _, key := range keys {
		prov, _ := s.providers.Get(key)
		caps := prov.Capabilities()
		a := ProviderAvailability{Provider: key, Reasons: []string{},
			Capabilities: ProviderCapabilities{Attended: caps.Attended, ObserveSessions: caps.ObserveSessions, CloseSessions: caps.CloseSessions}}
		switch {
		case !ok:
			a.Reasons = append(a.Reasons, RefDeviceUnknown)
		default:
			if dev.RetiredAt != nil {
				a.Reasons = append(a.Reasons, RefDeviceRetired)
			}
			if out.Stale {
				a.Reasons = append(a.Reasons, RefStaleDevice)
			}
			m, mapped, err := s.store.ActiveMapping(ctx, deviceID, key)
			if err != nil {
				return Capabilities{}, err
			}
			a.Mapped = mapped
			if mapped {
				a.PeerID = MaskPeerID(m.PeerID, p.Admin)
			} else {
				a.Reasons = append(a.Reasons, RefNoPeerMapping)
			}
		}
		a.Available = len(a.Reasons) == 0
		out.Providers = append(out.Providers, a)
	}
	return out, nil
}

// Mappings lists the peer mappings of a Device (history included on request). Peer ids are masked unless the
// caller holds remote_access.admin. Requires remote_access.view.
func (s *Service) Mappings(ctx context.Context, p Principal, deviceID string, includeClosed bool) ([]PeerMapping, error) {
	if !p.View && !p.Start && !p.Admin {
		return nil, ErrForbidden
	}
	deviceID, err := checkID(deviceID)
	if err != nil {
		return nil, err
	}
	list, err := s.store.Mappings(ctx, deviceID, includeClosed && p.Admin)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].PeerID = MaskPeerID(list[i].PeerID, p.Admin)
	}
	return list, nil
}

// ListSessions lists sessions newest first. Callers without remote_access.view_sessions see only their own.
func (s *Service) ListSessions(ctx context.Context, p Principal, f Filter) (Result[Session], error) {
	if f.Status != "" && !slices.Contains(Statuses, f.Status) {
		return Result[Session]{}, invalid("status must be one of %s", strings.Join(Statuses, ", "))
	}
	for _, id := range []*string{&f.DeviceID, &f.TicketID, &f.InitiatedBy} {
		if *id != "" {
			n, err := checkID(*id)
			if err != nil {
				return Result[Session]{}, err
			}
			*id = n
		}
	}
	if !p.ViewSessions {
		if !p.Start || p.UserID == "" {
			return Result[Session]{}, ErrForbidden
		}
		f.OnlyInitiator = p.UserID
	}
	return s.store.ListSessions(ctx, f)
}

// SessionDetail is a session with its transitions.
type SessionDetail struct {
	Session     Session
	Transitions []Transition
}

// isApprover reports whether the User is the approver User or the decider of one of the session's Approvals, or
// a member of the approver Team.
func (s *Service) isApprover(ctx context.Context, sessionID, userID string) (bool, error) {
	list, err := s.approvals.ForSubject(ctx, sessionID)
	if err != nil {
		return false, err
	}
	var teams []string
	loaded := false
	for _, a := range list {
		if (a.ApproverUserID != nil && *a.ApproverUserID == userID) || (a.DecidedByUserID != nil && *a.DecidedByUserID == userID) {
			return true, nil
		}
		if a.ApproverTeamID != nil {
			if !loaded {
				if teams, err = s.approvers.TeamIDsOfUser(ctx, userID); err != nil {
					return false, err
				}
				loaded = true
			}
			if slices.Contains(teams, *a.ApproverTeamID) {
				return true, nil
			}
		}
	}
	return false, nil
}

// GetSession returns a session with its transitions to its initiator, to remote_access.view_sessions holders and
// to its approvers; anyone else gets ErrNotFound.
func (s *Service) GetSession(ctx context.Context, p Principal, id string) (SessionDetail, error) {
	if !uuidPattern.MatchString(id) || p.UserID == "" {
		return SessionDetail{}, ErrNotFound
	}
	cur, err := s.store.GetSession(ctx, strings.ToLower(id))
	if err != nil {
		return SessionDetail{}, err
	}
	if !p.ViewSessions && cur.InitiatedBy != p.UserID {
		ok, err := s.isApprover(ctx, cur.ID, p.UserID)
		if err != nil {
			return SessionDetail{}, err
		}
		if !ok {
			return SessionDetail{}, ErrNotFound
		}
	}
	tr, err := s.store.Transitions(ctx, cur.ID, Page{Limit: MaxLimit})
	if err != nil {
		return SessionDetail{}, err
	}
	return SessionDetail{Session: cur, Transitions: tr.Items}, nil
}

// ObservationSummary counts the provider records of the last ObservationSummaryWindow that could not be attributed
// cleanly to one session (unattributed, duplicate, after close). It raises no finding and no briefing item.
// Requires remote_access.view_sessions.
func (s *Service) ObservationSummary(ctx context.Context, p Principal) (ObservationSummary, error) {
	if !p.ViewSessions {
		return ObservationSummary{}, ErrForbidden
	}
	since := s.now().Add(-ObservationSummaryWindow)
	by, err := s.store.RecordSummary(ctx, since)
	if err != nil {
		return ObservationSummary{}, err
	}
	out := ObservationSummary{ByReason: by, Since: since}
	for _, n := range by {
		out.UnattributedRecords += n
	}
	return out, nil
}
