package authentication

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Sign-in with Microsoft Entra ID (ADR-0035, slice E-A). The protocol work (authorization URL, code exchange, ID
// token validation) is behind OIDCProvider (integrations/entra); this file owns the browser flow: server-side
// transaction, browser binding, throttling, identity mapping, session creation and uniform audited failures. The
// identity key is tenant id + object id; an email address never links or creates a User.

const (
	methodEntra = "entra"

	// oidcTransactionTTL is how long a started sign-in may be completed.
	oidcTransactionTTL = 10 * time.Minute
	// maxPendingOIDCTransactions bounds the table against start floods.
	maxPendingOIDCTransactions = 20000
	maxReturnToBytes           = 512

	entraFailedCode      = "entra_failed"
	entraNotLinkedCode   = "entra_not_linked"
	entraUnavailableCode = "entra_unavailable"
	// loginPagePath is where failures go; the page shows a generic message for the code.
	loginPagePath = "/login"
)

var (
	// ErrOIDCInvalid is returned for any token, state or exchange problem (the details are never shown to the user).
	ErrOIDCInvalid = errors.New("authentication: oidc sign-in invalid")
	// ErrOIDCTenantNotAllowed is returned when the tenant is not allowed.
	ErrOIDCTenantNotAllowed = errors.New("authentication: oidc tenant not allowed")
	// ErrOIDCGuestRefused is returned for B2B guests while guests are refused.
	ErrOIDCGuestRefused = errors.New("authentication: oidc guest refused")
	// ErrOIDCUnavailable is returned when the identity provider cannot be reached or its keys cannot be fetched.
	ErrOIDCUnavailable = errors.New("authentication: oidc provider unavailable")

	// ErrAnchorNoMatch: no active directory-synchronized User has the source anchor (the person may be provisioned).
	ErrAnchorNoMatch = errors.New("authentication: no user matches the source anchor")
	// ErrAnchorRefused: the anchor names exactly one User that cannot be linked (inactive, not an employee, not
	// directory-owned, already has an identity of the tenant or the identity is taken). Provisioning does not follow.
	ErrAnchorRefused = errors.New("authentication: the matched user cannot be linked")
	// ErrAnchorAmbiguous: more than one User matches the anchor. Nothing is linked and nothing is provisioned.
	ErrAnchorAmbiguous = errors.New("authentication: the source anchor matches more than one user")
	// ErrProvisionEmailConflict: a User already has the primary email address; never a link by email.
	ErrProvisionEmailConflict = errors.New("authentication: a user already has this email address")
	// ErrProvisionRefused: the profile in the token cannot be used to create a User (missing or unusable name or email).
	ErrProvisionRefused = errors.New("authentication: the token profile cannot create a user")
)

// VerifiedIdentity is what a validated ID token proves. TenantID and ObjectID are lower-case GUIDs.
type VerifiedIdentity struct {
	TenantID    string
	ObjectID    string
	Guest       bool
	DisplayName string
	Email       string
	// SourceAnchor is the objectGUID (canonical lower-case text) decoded from Graph onPremisesImmutableId. It is set
	// only for a non-guest member of the home tenant with onPremisesSyncEnabled=true and only when the hybrid match is
	// configured; otherwise it is empty.
	SourceAnchor string
	// MemberConfirmed is true only when the token affirmatively identifies a member of the tenant (optional claim
	// acct = 0 and no foreign idp). Provisioning requires it, because the guest claims are optional and a guest
	// token without them is indistinguishable from a member token.
	MemberConfirmed bool
}

// OIDCProvider is the identity provider side of the flow.
type OIDCProvider interface {
	// AuthorizeURL returns the authorization endpoint URL for the transaction values.
	AuthorizeURL(state, nonce, codeChallenge string) string
	// Exchange trades the code for tokens and validates the ID token against nonce. It returns one of the
	// ErrOIDC* sentinels on failure.
	Exchange(ctx context.Context, code, codeVerifier, nonce string) (VerifiedIdentity, error)
}

// EntraIdentityDirectory maps a verified identity to a User. It matches tenant id + object id only.
type EntraIdentityDirectory interface {
	FindEntraUser(ctx context.Context, tenantID, objectID string) (userID string, found bool, err error)
}

// EntraLinkLocker is an optional capability of the identity directory: it locks the exact identity link inside
// the session-creation transaction (FOR SHARE), so an administrator unlinking at the same moment either waits for
// the new session, which the unlink then revokes, or the sign-in sees the link gone. Without it a link removed
// between the lookup and the session creation could still produce a session.
type EntraLinkLocker interface {
	LockEntraLink(ctx context.Context, tx pgx.Tx, userID, tenantID, objectID string) (bool, error)
}

// errEntraLinkGone aborts session creation when the identity link no longer exists.
var errEntraLinkGone = errors.New("authentication: entra identity link removed during sign-in")

// EntraAnchorLinker links an Entra identity to the User that directory synchronization created for the same
// objectGUID (hybrid match, ADR-0035 point 3). It links in one transaction and audits `via: source_anchor`; it
// returns ErrAnchorNoMatch, ErrAnchorRefused or ErrAnchorAmbiguous and never matches by email, UPN or name.
type EntraAnchorLinker interface {
	LinkBySourceAnchor(ctx context.Context, tenantID, objectID, anchor, correlationID string) (userID string, err error)
}

// EntraProvisioner creates an internal employee (no roles, no teams) for a first Entra sign-in when the runtime
// setting auth.entra_provisioning is auto_employee. It returns ErrProvisionEmailConflict or ErrProvisionRefused.
type EntraProvisioner interface {
	ProvisionEmployee(ctx context.Context, tenantID, objectID, displayName, email, correlationID string) (userID string, err error)
}

// EntraProvisioningAuto is the value of the setting auth.entra_provisioning that enables provisioning.
const EntraProvisioningAuto = "auto_employee"

// EntraLoginDeps enables the Entra endpoints in LoginDeps.
type EntraLoginDeps struct {
	Provider   OIDCProvider
	Identities EntraIdentityDirectory
	// HomeTenantID is ENTRA_TENANT_ID. The hybrid match and the provisioning apply to this tenant only.
	HomeTenantID string
	// Anchors enables the hybrid source-anchor match (ENTRA_LINK_DIRECTORY_PROVIDER_KEY); nil disables it.
	Anchors EntraAnchorLinker
	// Provisioner and Provisioning enable automatic provisioning: Provisioning returns the setting value and
	// anything other than EntraProvisioningAuto keeps the default (link_only).
	Provisioner  EntraProvisioner
	Provisioning func(ctx context.Context) string
	// SessionMaxAge caps the absolute lifetime of an Entra session (ENTRA_SESSION_MAX_AGE).
	SessionMaxAge time.Duration
	// SessionCap, when set, returns the administration setting; the smaller positive value wins.
	SessionCap func(ctx context.Context) time.Duration
}

func (h *loginHandler) entraEnabled() bool {
	return h.entra != nil && h.entra.Provider != nil && h.entra.Identities != nil
}

func oidcBindingCookieName(secure bool) string {
	if secure {
		return "__Host-turaco_oidc"
	}
	return "turaco_oidc"
}

func randomURLToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Bytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// safeReturnTo accepts only a relative in-app path (open-redirect guard).
func safeReturnTo(raw string) string {
	if raw == "" || len(raw) > maxReturnToBytes || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.Contains(raw, `\`) {
		return "/"
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "/"
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return raw
}

func (h *loginHandler) entraStart(w http.ResponseWriter, r *http.Request) {
	if !h.entraEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	// Starts have their own client budget, taken before any database work: an anonymous caller must not be able
	// to fill the table of pending transactions and lock everyone out.
	a := h.begin(r)
	if !h.reserve(w, r, a, "ip:entra-start/"+strings.TrimPrefix(a.ipKey, "ip:"), true) {
		return
	}
	state, err1 := randomURLToken()
	nonce, err2 := randomURLToken()
	verifier, err3 := randomURLToken()
	binding, err4 := randomURLToken()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	returnTo := safeReturnTo(r.URL.Query().Get("returnTo"))
	shared := r.URL.Query().Get("shared") == "1"
	ok, err := h.createOIDCTransaction(r.Context(), sha256Bytes(state), sha256Bytes(binding), nonce, verifier, returnTo, shared)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	if !ok {
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth.temporarily_unavailable", "Login is temporarily unavailable. Try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcBindingCookieName(h.cfg.SecureCookie), Value: binding, Path: "/", MaxAge: int(oidcTransactionTTL.Seconds()),
		HttpOnly: true, Secure: h.cfg.SecureCookie, SameSite: http.SameSiteLaxMode,
	})
	challenge := base64.RawURLEncoding.EncodeToString(sha256Bytes(verifier))
	http.Redirect(w, r, h.entra.Provider.AuthorizeURL(state, nonce, challenge), http.StatusFound)
}

func (h *loginHandler) entraCallback(w http.ResponseWriter, r *http.Request) {
	if !h.entraEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	a := h.begin(r)
	// The callback has its own client budget (like Kerberos) and is reserved before any database or network work.
	a.ipKey = "ip:entra/" + strings.TrimPrefix(a.ipKey, "ip:")
	if !h.reserve(w, r, a, a.ipKey, true) {
		return
	}
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	clearBinding := func() {
		http.SetCookie(w, &http.Cookie{Name: oidcBindingCookieName(h.cfg.SecureCookie), Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, Secure: h.cfg.SecureCookie, SameSite: http.SameSiteLaxMode})
	}
	// Cleared before anything is written: cookies set after the response header are lost.
	clearBinding()
	if len(state) == 0 || len(state) > 128 {
		h.entraFailure(w, r, a, "", "state_mismatch", entraFailedCode)
		return
	}
	// The transaction is consumed first, so a replayed or failed callback can never be retried.
	tx, found, err := h.consumeOIDCTransaction(r.Context(), sha256Bytes(state))
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	cookie, cerr := r.Cookie(oidcBindingCookieName(h.cfg.SecureCookie))
	if !found || cerr != nil || subtle.ConstantTimeCompare(sha256Bytes(cookie.Value), tx.bindingHash) != 1 {
		h.entraFailure(w, r, a, "", "state_mismatch", entraFailedCode)
		return
	}
	if q.Get("error") != "" || code == "" || len(code) > 4096 {
		h.entraFailure(w, r, a, "", "provider_error", entraFailedCode)
		return
	}
	ident, err := h.entra.Provider.Exchange(r.Context(), code, tx.verifier, tx.nonce)
	switch {
	case err == nil:
	case errors.Is(err, ErrOIDCTenantNotAllowed):
		h.entraFailure(w, r, a, "", "tenant_not_allowed", entraFailedCode)
		return
	case errors.Is(err, ErrOIDCGuestRefused):
		h.entraFailure(w, r, a, "", "guest_refused", entraFailedCode)
		return
	case errors.Is(err, ErrOIDCUnavailable):
		h.entraFailure(w, r, a, "", "provider_unavailable", entraUnavailableCode)
		return
	default:
		h.entraFailure(w, r, a, "", "invalid_token", entraFailedCode)
		return
	}
	if ident.Guest {
		h.entraFailure(w, r, a, "", "guest_refused", entraFailedCode)
		return
	}
	userID, linked, err := h.entra.Identities.FindEntraUser(r.Context(), ident.TenantID, ident.ObjectID)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	if !linked {
		var reason string
		userID, reason, err = h.resolveUnlinkedEntra(r.Context(), ident, httpx.RequestID(w))
		if err != nil {
			writeInternal(h.logger, w, r, err)
			return
		}
		if userID == "" {
			h.entraFailure(w, r, a, "", reason, entraNotLinkedCode)
			return
		}
	}
	lifetime := h.entra.SessionMaxAge
	if h.entra.SessionCap != nil {
		if c := h.entra.SessionCap(r.Context()); c > 0 && (lifetime <= 0 || c < lifetime) {
			lifetime = c
		}
	}
	txShared := tx.shared
	session := LoginSession{UserID: userID, AuthMethod: methodEntra, CorrelationID: httpx.RequestID(w), Locker: h.users, MaxLifetime: lifetime}
	locker, hasLocker := h.entra.Identities.(EntraLinkLocker)
	session.AfterCreate = func(ctx context.Context, tx pgx.Tx, created Session) error {
		if hasLocker {
			held, err := locker.LockEntraLink(ctx, tx, userID, ident.TenantID, ident.ObjectID)
			if err != nil {
				return err
			}
			if !held {
				return errEntraLinkGone
			}
		}
		if tx2 := tx; txShared {
			// The sign-in page said "shared computer": logging out ends the Entra session too.
			_, err := tx2.Exec(ctx, `UPDATE platform.sessions SET shared_workstation = true WHERE id = $1::uuid`, created.ID)
			return err
		}
		return nil
	}
	sessionToken, sess, err := h.createSession(r, session)
	switch {
	case errors.Is(err, errEntraLinkGone):
		h.entraFailure(w, r, a, userID, "link_removed", entraNotLinkedCode)
		return
	case errors.Is(err, ErrUserInactive):
		h.entraFailure(w, r, a, userID, "user_inactive", entraFailedCode)
		return
	case errors.Is(err, ErrTemporarilyUnavailable):
		h.unavailable(w, r)
		return
	case err != nil:
		writeInternal(h.logger, w, r, err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if err := h.throttle.Refund(ctx, a.ipKey); err != nil {
		h.logger.ErrorContext(ctx, "refund login throttle failed", "request_id", httpx.RequestID(w), "error", err)
	}
	expires := sess.IdleExpiresAt
	if sess.AbsoluteExpiresAt.Before(expires) {
		expires = sess.AbsoluteExpiresAt
	}
	SetCookie(w, sessionToken, expires, h.cfg.SecureCookie)
	http.Redirect(w, r, tx.returnTo, http.StatusFound)
}

// resolveUnlinkedEntra applies steps 2 and 3 of the identity resolution (ADR-0035 point 3) to an identity without a
// link: the hybrid source-anchor match, then provisioning. It returns the User id, or an empty id and the audit reason.
// The email address is never used to find or link a User.
func (h *loginHandler) resolveUnlinkedEntra(ctx context.Context, ident VerifiedIdentity, correlationID string) (string, string, error) {
	d := h.entra
	home := d.HomeTenantID != "" && ident.TenantID == d.HomeTenantID && !ident.Guest
	if home && d.Anchors != nil && ident.SourceAnchor != "" {
		id, err := d.Anchors.LinkBySourceAnchor(ctx, ident.TenantID, ident.ObjectID, ident.SourceAnchor, correlationID)
		switch {
		case err == nil:
			return id, "", nil
		case errors.Is(err, ErrAnchorAmbiguous):
			return "", "source_anchor_ambiguous", nil
		case errors.Is(err, ErrAnchorRefused):
			return "", "source_anchor_refused", nil
		case errors.Is(err, ErrAnchorNoMatch):
			// The person is not synchronized from the directory: provisioning may still apply.
		default:
			return "", "", err
		}
	}
	if !home || !ident.MemberConfirmed || d.Provisioner == nil || d.Provisioning == nil || d.Provisioning(ctx) != EntraProvisioningAuto {
		return "", "not_linked", nil
	}
	id, err := d.Provisioner.ProvisionEmployee(ctx, ident.TenantID, ident.ObjectID, ident.DisplayName, ident.Email, correlationID)
	switch {
	case err == nil:
		return id, "", nil
	case errors.Is(err, ErrProvisionEmailConflict):
		return "", "email_conflict", nil
	case errors.Is(err, ErrProvisionRefused):
		return "", "provisioning_refused", nil
	default:
		return "", "", err
	}
}

// entraFailure audits the failure (never claims, codes or tokens), waits the minimum response time and sends the
// browser back to the login page with a generic code.
func (h *loginHandler) entraFailure(w http.ResponseWriter, r *http.Request, a attempt, userID, reason, code string) {
	ctx := context.WithoutCancel(r.Context())
	targetType, targetID := "login", "unknown"
	if userID != "" {
		targetType, targetID = "user", userID
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{
			Action: "auth.login.failed", TargetType: targetType, TargetID: targetID,
			Actor: audit.SystemActor("login"), CorrelationID: httpx.RequestID(w),
			Metadata: map[string]any{"method": methodEntra, "reason": reason, "clientIp": a.ip},
		})
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "audit login failure failed", "request_id", httpx.RequestID(w), "error", err)
	}
	if rest := h.cfg.MinFailureTime - h.now().Sub(a.start); rest > 0 {
		h.sleep(r.Context(), rest)
	}
	http.Redirect(w, r, loginPagePath+"?error="+url.QueryEscape(code), http.StatusFound)
}

type oidcTransaction struct {
	bindingHash []byte
	nonce       string
	verifier    string
	returnTo    string
	shared      bool
}

// createOIDCTransaction stores a new transaction after pruning expired ones. It returns false when too many are
// pending (a flood), without storing anything.
func (h *loginHandler) createOIDCTransaction(ctx context.Context, stateHash, bindingHash []byte, nonce, verifier, returnTo string, shared bool) (bool, error) {
	var stored bool
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.oidc_login_transactions WHERE created_at < now() - $1::interval`,
			(oidcTransactionTTL + time.Minute).String()); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.oidc_login_transactions`).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingOIDCTransactions {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.oidc_login_transactions (state_hash, binding_hash, nonce, code_verifier, return_to, shared)
			VALUES ($1, $2, $3, $4, $5, $6)`, stateHash, bindingHash, nonce, verifier, returnTo, shared); err != nil {
			return err
		}
		stored = true
		return nil
	})
	return stored, err
}

// consumeOIDCTransaction deletes and returns the transaction (single use). A transaction older than the TTL counts as not found.
func (h *loginHandler) consumeOIDCTransaction(ctx context.Context, stateHash []byte) (oidcTransaction, bool, error) {
	var t oidcTransaction
	var created time.Time
	err := h.pool.QueryRow(ctx, `DELETE FROM platform.oidc_login_transactions WHERE state_hash = $1
		RETURNING binding_hash, nonce, code_verifier, return_to, shared, created_at`, stateHash).Scan(&t.bindingHash, &t.nonce, &t.verifier, &t.returnTo, &t.shared, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return oidcTransaction{}, false, nil
	}
	if err != nil {
		return oidcTransaction{}, false, err
	}
	if h.now().Sub(created) > oidcTransactionTTL {
		return oidcTransaction{}, false, nil
	}
	return t, true, nil
}
