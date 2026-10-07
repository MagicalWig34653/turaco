package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
)

// tokenBytes is the size of a launch token (256 bit).
const tokenBytes = 32

// LaunchHandle is the one-time token returned by Launch. Token is shown exactly once and never stored: only its
// SHA-256 is.
type LaunchHandle struct {
	Token     string
	ExpiresAt time.Time
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// validToken accepts exactly the shape Launch produces (43 base64url characters), so garbage never reaches SQL.
func validToken(t string) bool {
	if len(t) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(t)
	return err == nil && len(b) == tokenBytes
}

// Launch issues a one-time launch handle for an authorized session: 60 seconds valid, bound to the initiating
// User, stored as a hash only. At most one handle is valid at a time. The session does not change; only the
// exchange moves it to launched. Initiator only; requires expectedVersion.
func (s *Service) Launch(ctx context.Context, c Caller, p Principal, id string, expected *int) (LaunchHandle, error) {
	if err := c.validate(); err != nil {
		return LaunchHandle{}, err
	}
	if !p.Start || p.UserID == "" || p.UserID != c.Actor.UserID {
		return LaunchHandle{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return LaunchHandle{}, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return LaunchHandle{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	h := Handle{UserID: p.UserID, ExpiresAt: now.Add(LaunchHandleTTL)}
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lock(ctx, tx, id, func(x Session) bool { return x.InitiatedBy == p.UserID }, exp, "launch", StatusAuthorized)
		if err != nil {
			return err
		}
		if !cur.ExpiresAt.After(now) {
			return ErrHandleExpired
		}
		live, err := s.store.LiveHandlesTx(ctx, tx, cur.ID, now)
		if err != nil {
			return err
		}
		if live > 0 {
			return ErrHandleActive
		}
		h.SessionID = cur.ID
		if err := s.store.InsertHandleTx(ctx, tx, h, hashToken(token)); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "session.launch_handle_issued", "remote_access_session", cur.ID, nil, nil,
			map[string]any{"deviceId": cur.DeviceID, "ticketId": cur.TicketID, "provider": cur.Provider, "expiresAt": h.ExpiresAt.Format(time.RFC3339)})
	})
	if err != nil {
		return LaunchHandle{}, err
	}
	return LaunchHandle{Token: token, ExpiresAt: h.ExpiresAt}, nil
}

// Exchanged is the result of a launch handle exchange. URI is the provider launch link: hand it to the
// technician in the response body only; it is never persisted, logged or audited.
type Exchanged struct {
	SessionID string
	Reference string
	Provider  string
	Session   Session
	URI       remoteaccess.LaunchURI
}

// Exchange resolves a launch token into the provider launch link, once, for the same User who requested it, and
// moves the session authorized to launched. Unknown token or another User: ErrNotFound; used before:
// ErrHandleUsed; expired: ErrHandleExpired. When the provider cannot build the link, the session fails with
// launch_failed and ErrLaunchFailed is returned.
func (s *Service) Exchange(ctx context.Context, c Caller, p Principal, token string) (Exchanged, error) {
	if err := c.validate(); err != nil {
		return Exchanged{}, err
	}
	if !p.Start || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Exchanged{}, ErrForbidden
	}
	if !validToken(token) {
		return Exchanged{}, ErrNotFound
	}
	now := s.now()
	var out Exchanged
	var failure error
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		h, err := s.store.LockHandleTx(ctx, tx, hashToken(token))
		if err != nil {
			return err
		}
		if h.UserID != p.UserID {
			return ErrNotFound
		}
		if h.UsedAt != nil {
			return ErrHandleUsed
		}
		if !h.ExpiresAt.After(now) {
			return ErrHandleExpired
		}
		cur, err := s.store.LockSessionTx(ctx, tx, h.SessionID)
		if err != nil {
			return err
		}
		if cur.Status != StatusAuthorized {
			return &InvalidTransitionError{Operation: "launch", From: cur.Status}
		}
		if !cur.ExpiresAt.After(now) {
			return ErrHandleExpired
		}
		if err := s.store.MarkHandleUsedTx(ctx, tx, h.ID, now); err != nil {
			return err
		}
		next := cur
		var uri remoteaccess.LaunchURI
		prov, ok := s.providers.Get(cur.Provider)
		if ok {
			uri, err = prov.BuildLaunch(remoteaccess.PeerRef{Provider: cur.Provider, ID: cur.PeerID}, remoteaccess.ModeAttended)
		}
		if !ok || err != nil {
			next.Status, next.StatusReason, next.ClosedAt = StatusFailed, strPtr(ReasonLaunchFailed), &now
			if _, err := s.commit(ctx, tx, c, cur, next, "launch_failed", ReasonLaunchFailed, nil); err != nil {
				return err
			}
			failure = ErrLaunchFailed
			return nil
		}
		next.Status, next.StatusReason, next.LaunchedAt, next.ExpiresAt = StatusLaunched, nil, &now, now.Add(LaunchedTTL)
		saved, err := s.commit(ctx, tx, c, cur, next, "launched", "", nil)
		if err != nil {
			return err
		}
		out = Exchanged{SessionID: saved.ID, Reference: saved.Reference, Provider: saved.Provider, Session: saved, URI: uri}
		return nil
	})
	if err != nil {
		return Exchanged{}, err
	}
	if failure != nil {
		return Exchanged{}, failure
	}
	return out, nil
}
