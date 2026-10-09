package authentication

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// POST /auth/local-login and POST /auth/credential-tokens/redeem (ADR-0034). Both answer 404 while
// AUTH_LOCAL_LOGIN_ENABLED is false.

type localLoginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

var errLocalCredentialChanged = errors.New("authentication: local credential changed")

func (h *loginHandler) localEnabled() bool {
	return h.cfg.LocalEnabled && h.localDir != nil
}

func (h *loginHandler) localLogin(w http.ResponseWriter, r *http.Request) {
	if !h.localEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	var req localLoginRequest
	if !decodeLoginBody(w, r, &req) {
		return
	}
	identifier := strings.TrimSpace(req.Identifier)
	if !validLoginField(identifier, maxIdentifierBytes) || !validLoginField(req.Password, maxLoginPasswordBytes) {
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "Identifier and password are required.")
		return
	}
	a := h.begin(r)
	// Source budget first, then the account (or, for unknown identifiers, the identifier) budget.
	if !h.reserve(w, r, a, a.ipKey, true) {
		return
	}
	userID, found, err := h.localDir.FindLocalAccount(r.Context(), identifier)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	a.subjectKey = IdentifierKey(identifier)
	if found {
		a.subjectKey = AccountKey(userID)
	}
	if !h.reserve(w, r, a, a.subjectKey, false) {
		return
	}
	var cred localCredential
	haveCred := false
	if found {
		if cred, haveCred, err = findLocalCredentialOf(r.Context(), h.pool, userID); err != nil {
			writeInternal(h.logger, w, r, err)
			return
		}
	}
	// Always spend one verification so unknown, locked and not yet activated accounts cost the same.
	hash := localDummyHash()
	if haveCred && cred.PasswordHash != nil {
		hash = *cred.PasswordHash
	}
	match, err := VerifyLocalPasswordHash(r.Context(), hash, req.Password, h.cfg.LocalHashWait)
	if errors.Is(err, ErrHashBusy) {
		h.logger.WarnContext(r.Context(), "local login: no hash capacity", "request_id", httpx.RequestID(w), "client_ip", a.ip)
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, http.StatusTooManyRequests, "auth.too_many_attempts", "Too many failed attempts. Try again later.")
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "local credential unusable", "request_id", httpx.RequestID(w), "user_id", userID, "error", err)
		match = false
	}
	reason := ""
	switch {
	case !found || !haveCred:
		reason = "unknown_account"
	case cred.PasswordHash == nil:
		reason = "not_activated"
	case !cred.Enabled:
		reason = "disabled"
	case cred.Locked:
		reason = "locked"
	case !match:
		reason = "wrong_password"
	}
	if reason != "" {
		if reason == "wrong_password" {
			h.recordLocalFailure(r.Context(), userID, httpx.RequestID(w), a.ip)
		}
		h.credentialFailure(w, r, a, ActionLocalLoginFailed, methodLocal, userID, reason)
		return
	}

	corr := httpx.RequestID(w)
	token, sess, err := h.createSession(r, LoginSession{
		UserID: userID, AuthMethod: methodLocal, CorrelationID: corr, Locker: h.users,
		AfterCreate: func(ctx context.Context, tx pgx.Tx, s Session) error {
			// The credential must still be exactly what was verified and the account must not be locked.
			tag, err := tx.Exec(ctx, `
				UPDATE platform.local_credentials SET last_used_at = now(), failed_attempts = 0, locked_until = NULL
				WHERE user_id = $1 AND kind = 'local' AND enabled AND password_hash = $2 AND (locked_until IS NULL OR locked_until <= now())`,
				userID, cred.PasswordHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errLocalCredentialChanged
			}
			return audit.Record(ctx, tx, audit.Change{
				Action: ActionLocalLoginSucceeded, TargetType: "user", TargetID: userID, Actor: audit.UserActor(userID),
				CorrelationID: corr, OccurredAt: s.CreatedAt,
				Metadata: map[string]any{"method": methodLocal, "clientIp": a.ip, "sessionId": s.ID},
			})
		},
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.credentialFailure(w, r, a, ActionLocalLoginFailed, methodLocal, userID, "user_inactive")
		return
	case errors.Is(err, errLocalCredentialChanged):
		h.credentialFailure(w, r, a, ActionLocalLoginFailed, methodLocal, userID, "credential_changed")
		return
	case errors.Is(err, ErrTemporarilyUnavailable):
		h.unavailable(w, r)
		return
	case err != nil:
		writeInternal(h.logger, w, r, err)
		return
	}
	h.finishLogin(w, r, a, token, sess)
}

// recordLocalFailure counts a wrong password on the credential and locks the account after repeated failures
// (audited as auth.local_login.locked). Best effort: a failure here must not change the answer.
func (h *loginHandler) recordLocalFailure(ctx context.Context, userID, corr, ip string) {
	ctx = context.WithoutCancel(ctx)
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var attempts int
		var lockedUntil *time.Time
		err := tx.QueryRow(ctx, `
			UPDATE platform.local_credentials
			SET failed_attempts = CASE WHEN locked_until IS NOT NULL AND locked_until <= now() THEN 1 ELSE failed_attempts + 1 END,
			    locked_until = CASE WHEN locked_until IS NOT NULL AND locked_until <= now() THEN NULL ELSE locked_until END
			WHERE user_id = $1 AND kind = 'local' AND enabled
			RETURNING failed_attempts, locked_until`, userID).Scan(&attempts, &lockedUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if attempts >= maxLocalFailures && lockedUntil == nil {
			if _, err := tx.Exec(ctx, `UPDATE platform.local_credentials SET locked_until = now() + $2::bigint * interval '1 microsecond' WHERE user_id = $1`,
				userID, localLockout.Microseconds()); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Change{Action: ActionLocalLoginLocked, TargetType: "user", TargetID: userID,
				Actor: audit.SystemActor("login"), CorrelationID: corr, Metadata: map[string]any{"clientIp": ip, "lockedForSeconds": int(localLockout.Seconds())}})
		}
		return nil
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "record local login failure failed", "request_id", corr, "error", err)
	}
}

type redeemRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// redeem sets the password of a local account with an invitation or reset token. It never logs in: the person
// signs in afterwards. Failures about the token are uniform (400 auth.invalid_token) and throttled by source.
func (h *loginHandler) redeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.localEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	var req redeemRequest
	if !decodeLoginBody(w, r, &req) {
		return
	}
	if !validLoginField(req.Password, maxLoginPasswordBytes) || req.Token == "" {
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "Token and password are required.")
		return
	}
	a := h.begin(r)
	if !h.reserve(w, r, a, a.ipKey, true) {
		return
	}
	invalidToken := func() {
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_token", "The link is invalid or has expired.")
	}
	if !validTokenFormat(req.Token) {
		invalidToken()
		return
	}
	corr := httpx.RequestID(w)
	var policy bool
	err := pgx.BeginFunc(r.Context(), h.pool, func(tx pgx.Tx) error {
		var id, userID, purpose string
		var live bool
		var used *time.Time
		err := tx.QueryRow(r.Context(), `
			SELECT id::text, user_id::text, purpose, expires_at > now(), used_at FROM platform.credential_tokens WHERE token_hash = $1 FOR UPDATE`,
			HashToken(req.Token)).Scan(&id, &userID, &purpose, &live, &used)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if used != nil || !live {
			return ErrInvalidToken
		}
		info, err := h.localDir.LocalAccountInfo(r.Context(), tx, userID)
		if err != nil {
			return err
		}
		if !info.Exists || !info.Local || !info.Active {
			return ErrInvalidToken
		}
		var loginName string
		var hadPassword bool
		err = tx.QueryRow(r.Context(), `SELECT login_name, password_hash IS NOT NULL FROM platform.local_credentials WHERE user_id = $1 AND kind = 'local' FOR UPDATE`, userID).Scan(&loginName, &hadPassword)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		// An invitation only activates; a reset only changes an activated account.
		if (purpose == PurposeInvitation) == hadPassword {
			return ErrInvalidToken
		}
		hash, err := HashLocalPassword(r.Context(), req.Password, info.DisplayName, info.Email, loginName)
		if errors.Is(err, ErrLocalPasswordPolicy) {
			policy = true
			return err
		}
		if err != nil {
			return err
		}
		at := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.Exec(r.Context(), `
			UPDATE platform.local_credentials
			SET password_hash = $2, enabled = true, failed_attempts = 0, locked_until = NULL, password_changed_at = $3, updated_at = $3
			WHERE user_id = $1`, userID, hash, at); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE platform.credential_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, userID, at); err != nil {
			return err
		}
		actor := audit.UserActor(userID)
		if _, err := RevokeUserSessions(r.Context(), tx, userID, "local_password_set", actor, corr, at); err != nil {
			return err
		}
		return audit.Record(r.Context(), tx, audit.Change{Action: ActionLocalPasswordSet, TargetType: "user", TargetID: userID, Actor: actor,
			CorrelationID: corr, OccurredAt: at, Metadata: map[string]any{"purpose": purpose, "clientIp": a.ip}})
	})
	switch {
	case err == nil:
		// A successful redemption gives the source budget back.
		if rerr := h.throttle.Refund(context.WithoutCancel(r.Context()), a.ipKey); rerr != nil {
			h.logger.WarnContext(r.Context(), "refund redeem throttle", "request_id", corr, "error", rerr)
		}
		w.WriteHeader(http.StatusNoContent)
	case policy:
		httpx.WriteError(w, http.StatusBadRequest, "auth.password_policy", "The password does not meet the requirements.")
	case errors.Is(err, ErrInvalidToken):
		invalidToken()
	case errors.Is(err, ErrHashBusy):
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, http.StatusTooManyRequests, "auth.too_many_attempts", "Too many requests. Try again later.")
	default:
		writeInternal(h.logger, w, r, err)
	}
}
