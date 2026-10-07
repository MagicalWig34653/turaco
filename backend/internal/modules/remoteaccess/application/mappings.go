package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
)

// peerHash is the SHA-256 of a peer id as lower-case hex: audit records name a peer by this hash, never by the id.
func peerHash(peerID string) string {
	h := sha256.Sum256([]byte(peerID))
	return hex.EncodeToString(h[:])
}

// endSessionsOnMappingChange ends the open sessions of a Device and provider whose peer mapping was replaced or
// removed: launched ones are closed, the others cancelled (a pending Approval with them), always with the reason
// mapping_changed and an audit entry. The mapping is locked by the caller (lock order: mapping, then sessions).
func (s *Service) endSessionsOnMappingChange(ctx context.Context, tx pgx.Tx, c Caller, deviceID, provider string) error {
	open, err := s.store.OpenSessionsOfDeviceTx(ctx, tx, deviceID, provider)
	if err != nil {
		return err
	}
	for _, cur := range open {
		now := s.now()
		next := cur
		reason := ReasonMappingChanged
		op := "cancelled"
		next.Status, next.StatusReason, next.ClosedAt = StatusCancelled, &reason, &now
		switch cur.Status {
		case StatusLaunched:
			op, next.Status = "closed", StatusClosed
		case StatusPendingApproval:
			if err := s.approvals.CancelBySubjectInTx(ctx, tx, c.Actor, c.CorrelationID, cur.ID); err != nil {
				return err
			}
		}
		if _, err := s.commit(ctx, tx, c, cur, next, op, reason, map[string]any{"cause": "peer_mapping_changed"}); err != nil {
			return err
		}
	}
	return nil
}

// MapPeer maps a Device to its peer id at a provider by an explicit, audited manual mapping (never by hostname).
// An existing active mapping of the Device and provider is closed with the reason "replaced"; its history stays.
// The Device's open sessions of that provider are ended with the reason mapping_changed in the same transaction.
// Repeating the current mapping changes nothing. Requires remote_access.admin.
func (s *Service) MapPeer(ctx context.Context, c Caller, p Principal, deviceID, provider, peerID, reason string) (PeerMapping, error) {
	if err := c.validate(); err != nil {
		return PeerMapping{}, err
	}
	if !p.Admin || p.UserID == "" || p.UserID != c.Actor.UserID {
		return PeerMapping{}, ErrForbidden
	}
	deviceID, err := checkID(deviceID)
	if err != nil {
		return PeerMapping{}, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !providerKey.MatchString(provider) {
		return PeerMapping{}, invalid("provider is not a valid provider key")
	}
	if !oneOf(reason, MapReasons) {
		return PeerMapping{}, invalid("reason must be one of %s", strings.Join(MapReasons, ", "))
	}
	if _, ok := s.providers.Get(provider); !ok {
		return PeerMapping{}, refused(RefProviderDisabled)
	}
	if !remoteaccess.ValidPeerID(provider, peerID) {
		return PeerMapping{}, invalid("the peer id is not valid for this provider")
	}
	if _, ok, err := s.devices.Device(ctx, deviceID); err != nil {
		return PeerMapping{}, fmt.Errorf("read device: %w", err)
	} else if !ok {
		return PeerMapping{}, refused(RefDeviceUnknown)
	}
	var out PeerMapping
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		old, had, err := s.store.LockActiveMappingTx(ctx, tx, deviceID, provider)
		if err != nil {
			return err
		}
		if had && old.PeerID == peerID {
			out = old
			return nil
		}
		if had {
			if err := s.store.CloseMappingTx(ctx, tx, old.ID, &p.UserID, reasonReplaced, now); err != nil {
				return err
			}
			if err := s.endSessionsOnMappingChange(ctx, tx, c, deviceID, provider); err != nil {
				return err
			}
		}
		by := p.UserID
		out, err = s.store.InsertMappingTx(ctx, tx, PeerMapping{DeviceID: deviceID, Provider: provider, PeerID: peerID, Source: "manual",
			Reason: &reason, MappedBy: &by, MappedAt: now})
		if err != nil {
			return err
		}
		meta := map[string]any{"deviceId": deviceID, "provider": provider, "source": "manual", "reason": reason, "replaced": had,
			"mappingId": out.ID, "peerIdHash": peerHash(peerID)}
		if had {
			meta["previousPeerIdHash"] = peerHash(old.PeerID)
		}
		return recordAudit(ctx, tx, c, "peer_mapping.mapped", "device", deviceID, nil, nil, meta)
	})
	if errors.Is(err, ErrPeerTaken) {
		return PeerMapping{}, ErrPeerTaken
	}
	return out, err
}

// UnmapPeer closes the active mapping of a Device and provider with a reason code. Requires remote_access.admin.
func (s *Service) UnmapPeer(ctx context.Context, c Caller, p Principal, deviceID, provider, reason string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Admin || p.UserID == "" || p.UserID != c.Actor.UserID {
		return ErrForbidden
	}
	deviceID, err := checkID(deviceID)
	if err != nil {
		return err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !providerKey.MatchString(provider) {
		return invalid("provider is not a valid provider key")
	}
	if !oneOf(reason, UnmapReasons) {
		return invalid("reason must be one of %s", strings.Join(UnmapReasons, ", "))
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		old, had, err := s.store.LockActiveMappingTx(ctx, tx, deviceID, provider)
		if err != nil {
			return err
		}
		if !had {
			return ErrNotFound
		}
		if err := s.store.CloseMappingTx(ctx, tx, old.ID, &p.UserID, reason, s.now()); err != nil {
			return err
		}
		if err := s.endSessionsOnMappingChange(ctx, tx, c, deviceID, provider); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "peer_mapping.unmapped", "device", deviceID, nil, nil,
			map[string]any{"deviceId": deviceID, "provider": provider, "reason": reason, "mappingId": old.ID, "peerIdHash": peerHash(old.PeerID)})
	})
}
