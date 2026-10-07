package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
)

// MapPeer maps a Device to its peer id at a provider by an explicit, audited manual mapping (never by hostname).
// An existing active mapping of the Device and provider is closed with the reason "replaced"; its history stays.
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
		}
		by := p.UserID
		out, err = s.store.InsertMappingTx(ctx, tx, PeerMapping{DeviceID: deviceID, Provider: provider, PeerID: peerID, Source: "manual",
			Reason: &reason, MappedBy: &by, MappedAt: now})
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "peer_mapping.mapped", "device", deviceID, nil, nil,
			map[string]any{"deviceId": deviceID, "provider": provider, "source": "manual", "reason": reason, "replaced": had, "mappingId": out.ID})
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
		return recordAudit(ctx, tx, c, "peer_mapping.unmapped", "device", deviceID, nil, nil,
			map[string]any{"deviceId": deviceID, "provider": provider, "reason": reason, "mappingId": old.ID})
	})
}
