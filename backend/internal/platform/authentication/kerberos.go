package authentication

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// GET /auth/kerberos: login with a Kerberos/SPNEGO ticket
// (docs/security/identity-access-design.md section 8). The ticket is
// validated by an injected KerberosValidator (integrations/kerberos); the
// authenticated principal is mapped to a synced directory account exactly like
// a password login, so Kerberos never creates Users.

const (
	methodKerberos = "kerberos"
	// maxNegotiateHeaderBytes bounds the Authorization header value that is
	// decoded and parsed.
	maxNegotiateHeaderBytes = 64 << 10
	negotiateScheme         = "Negotiate"
)

// kerberosEnabled reports whether the Kerberos endpoint is active: it needs a
// validator and the directory mapping that password login also uses.
func (h *loginHandler) kerberosEnabled() bool {
	return h.krb != nil && h.directory != nil && h.cfg.ProviderKey != ""
}

func (h *loginHandler) kerberos(w http.ResponseWriter, r *http.Request) {
	if !h.kerberosEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	// A GET that creates a session must not be triggerable by another site
	// (login CSRF): browsers attach Negotiate credentials to cross-site
	// requests to intranet hosts. Requests without Fetch Metadata (curl, old
	// browsers) are accepted; they cannot be forged cross-site with a ticket.
	if !kerberosSameSite(r) {
		httpx.WriteError(w, http.StatusForbidden, "platform.csrf_rejected", "The request origin is not allowed.")
		return
	}
	token, outcome := parseNegotiate(r.Header.Values("Authorization"))
	switch outcome {
	case negotiateChallenge:
		// The normal first round trip: no work, not counted as a failure and
		// not audited.
		h.negotiateChallenge(w)
		return
	case negotiateMalformed:
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "The Authorization header is invalid.")
		return
	}

	raw, decodeErr := base64.StdEncoding.DecodeString(token)
	// Browsers on machines without a usable Kerberos ticket (not domain
	// joined, wrong host name) fall back to NTLM inside Negotiate. That is
	// not an attack and not a Kerberos failure: answer the challenge without
	// counting or auditing, so the UI falls back to the password form.
	if decodeErr == nil && isNTLMToken(raw) {
		h.negotiateChallenge(w)
		return
	}

	a := h.begin(r)
	// Kerberos has its own client budget so that broken Kerberos (stale
	// keytab, unjoined devices behind one NAT) cannot exhaust the budget the
	// password fallback needs. No account key: a forged token must not lock a
	// user out. Reserved before any validation work.
	a.ipKey = "ip:krb/" + strings.TrimPrefix(a.ipKey, "ip:")
	if !h.reserve(w, r, a, a.ipKey, true) {
		return
	}

	if decodeErr != nil {
		h.kerberosFailure(w, r, a, "", "invalid_ticket")
		return
	}
	principal, err := h.krb.Validate(r.Context(), raw)
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidTicket):
		h.kerberosFailure(w, r, a, "", "invalid_ticket")
		return
	default:
		writeInternal(h.logger, w, r, err)
		return
	}
	// The validator already guarantees this; the directory lookup interprets
	// `DOMAIN\user` and `user@host`, so a name containing them must never
	// reach it, whatever the validator implementation.
	if !plainKerberosUsername(principal.Username) {
		h.kerberosFailure(w, r, a, "", "invalid_ticket")
		return
	}

	acct, found, err := h.directory.FindDirectoryAccount(r.Context(), h.cfg.ProviderKey, principal.Username)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	// For Kerberos the lookup is the identity decision (no bind proves it),
	// so the folded SQL match is not enough: require the principal name to
	// equal the synced username exactly, ignoring ASCII case only (AD keeps
	// the case the user typed). Non-ASCII names must match byte for byte, so
	// Unicode folding (Kelvin sign, dotted I) never maps a different
	// principal onto an account.
	if !found || !kerberosNameMatches(principal.Username, acct.Username) {
		h.kerberosFailure(w, r, a, "", "unknown_account")
		return
	}

	sessionToken, sess, err := h.createSession(r, LoginSession{
		UserID: acct.UserID, AuthMethod: methodKerberos, CorrelationID: httpx.RequestID(w), Locker: h.users,
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.kerberosFailure(w, r, a, acct.UserID, "user_inactive")
		return
	case errors.Is(err, ErrTemporarilyUnavailable):
		h.unavailable(w, r)
		return
	case err != nil:
		writeInternal(h.logger, w, r, err)
		return
	}
	h.finishLogin(w, r, a, sessionToken, sess)
}

func (h *loginHandler) negotiateChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", negotiateScheme)
	httpx.WriteError(w, http.StatusUnauthorized, "auth.invalid_credentials", "Invalid credentials.")
}

// kerberosFailure audits the failure (never the token) and answers the
// uniform 401 with the Negotiate challenge after the minimum response time.
func (h *loginHandler) kerberosFailure(w http.ResponseWriter, r *http.Request, a attempt, userID, reason string) {
	w.Header().Set("WWW-Authenticate", negotiateScheme)
	h.credentialFailure(w, r, a, "auth.login.failed", methodKerberos, userID, reason)
}

type negotiateOutcome int

const (
	// negotiateToken: a Negotiate token was supplied.
	negotiateToken negotiateOutcome = iota
	// negotiateChallenge: no usable credentials, send the challenge.
	negotiateChallenge
	// negotiateMalformed: a bad request (several or oversized headers).
	negotiateMalformed
)

// parseNegotiate extracts the base64 token of `Authorization: Negotiate
// <token>`. Other schemes and a bare `Negotiate` get the challenge; several
// Authorization headers or one over maxNegotiateHeaderBytes are malformed.
// The returned token is not decoded or validated.
func parseNegotiate(values []string) (string, negotiateOutcome) {
	switch len(values) {
	case 0:
		return "", negotiateChallenge
	case 1:
	default:
		return "", negotiateMalformed
	}
	v := values[0]
	if len(v) > maxNegotiateHeaderBytes {
		return "", negotiateMalformed
	}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
	if !strings.EqualFold(scheme, negotiateScheme) {
		return "", negotiateChallenge
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", negotiateChallenge
	}
	return rest, negotiateToken
}

// plainKerberosUsername accepts a bare login name: it must survive
// FindDirectoryAccount's identifier rules unchanged (no DOMAIN\ prefix, no
// e-mail form, no instance part, no surrounding space).
func plainKerberosUsername(s string) bool {
	return validLoginField(s, maxIdentifierBytes) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, `@\/`)
}

// kerberosSameSite rejects cross-site requests to the session-creating GET:
// Fetch Metadata when present, and otherwise an Origin header naming another
// host. Requests with neither (curl, very old browsers) are accepted; they
// cannot be forged cross-site with the victim's ticket by a modern browser.
func kerberosSameSite(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// isNTLMToken reports whether a Negotiate token is a raw NTLMSSP message.
func isNTLMToken(raw []byte) bool {
	return len(raw) >= 8 && string(raw[:8]) == "NTLMSSP\x00"
}

// kerberosNameMatches compares a Kerberos principal name with the synced
// directory username: ASCII case-insensitive for ASCII names, byte-exact
// otherwise. strings.EqualFold is deliberately not used (it folds Unicode).
func kerberosNameMatches(principal, synced string) bool {
	if principal == synced {
		return principal != ""
	}
	if len(principal) != len(synced) || !isASCII(principal) || !isASCII(synced) {
		return false
	}
	for i := 0; i < len(principal); i++ {
		if asciiLower(principal[i]) != asciiLower(synced[i]) {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func asciiLower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
