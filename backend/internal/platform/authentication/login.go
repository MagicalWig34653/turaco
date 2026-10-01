package authentication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Login endpoints: POST /auth/login (directory password), POST
// /auth/emergency-login (local break-glass account), GET /auth/methods and the
// Kerberos placeholder. Behaviour: docs/security/identity-access-design.md §6, §7.

const (
	maxLoginBodyBytes     = 8 << 10
	maxIdentifierBytes    = 256
	maxLoginNameBytes     = 64
	maxLoginPasswordBytes = 1024
	maxAuditIdentifier    = 128
	// defaultMinFailureTime is the minimum duration of every credential
	// failure response, so unknown, disabled and wrong-password accounts
	// cannot be told apart by timing.
	defaultMinFailureTime = 400 * time.Millisecond
	// emergencySessionMax caps the absolute lifetime of emergency sessions.
	emergencySessionMax = time.Hour
	// verifyTimeout bounds one directory password verification.
	verifyTimeout = 15 * time.Second

	methodLDAP      = "ldap"
	methodEmergency = "emergency"
)

// LoginConfig configures the login endpoints.
type LoginConfig struct {
	// ProviderKey is the directory provider key used to resolve accounts. It
	// is set only when directory password login is configured.
	ProviderKey string
	// EmergencyEnabled mirrors AUTH_EMERGENCY_LOGIN_ENABLED.
	EmergencyEnabled bool
	SecureCookie     bool
	// TrustedProxies are the networks whose X-Forwarded-For is honoured.
	TrustedProxies []netip.Prefix
	// MinFailureTime defaults to 400 ms.
	MinFailureTime time.Duration
}

// LoginDeps are the collaborators of the login endpoints. Directory, Verifier
// and Users may be nil when directory password login is not configured.
type LoginDeps struct {
	Pool      *pgxpool.Pool
	Sessions  *Service
	Throttle  *Throttle
	Directory AccountDirectory
	Verifier  PasswordVerifier
	Users     UserLocker
	Logger    *slog.Logger
	// Now and Sleep are injectable for tests; defaults are time.Now and a
	// context-aware sleep.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

type loginSessions interface {
	Authenticate(ctx context.Context, token string) (Session, error)
	CreateLogin(ctx context.Context, p LoginSession) (string, Session, error)
}

type loginHandler struct {
	pool      *pgxpool.Pool
	sessions  loginSessions
	throttle  *Throttle
	directory AccountDirectory
	verifier  PasswordVerifier
	users     UserLocker
	cfg       LoginConfig
	logger    *slog.Logger
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration)
}

// RegisterLogin mounts the login endpoints under /api/v1/auth. Unsafe
// methods are protected by the same-origin guard.
func RegisterLogin(mux *http.ServeMux, d LoginDeps, cfg LoginConfig) {
	h := &loginHandler{
		pool: d.Pool, sessions: d.Sessions, throttle: d.Throttle,
		directory: d.Directory, verifier: d.Verifier, users: d.Users,
		cfg: cfg, logger: d.Logger, now: d.Now, sleep: d.Sleep,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.sleep == nil {
		h.sleep = sleepContext
	}
	if h.cfg.MinFailureTime <= 0 {
		h.cfg.MinFailureTime = defaultMinFailureTime
	}
	mux.HandleFunc("GET /api/v1/auth/methods", h.methods)
	mux.Handle("POST /api/v1/auth/login", RequireSameOrigin(http.HandlerFunc(h.login)))
	mux.Handle("POST /api/v1/auth/emergency-login", RequireSameOrigin(http.HandlerFunc(h.emergencyLogin)))
	mux.HandleFunc("GET /api/v1/auth/kerberos", h.kerberos)
}

func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (h *loginHandler) passwordEnabled() bool {
	return h.cfg.ProviderKey != "" && h.directory != nil && h.verifier != nil && h.users != nil
}

func (h *loginHandler) methods(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]bool{
		"password":  h.passwordEnabled(),
		"kerberos":  false,
		"emergency": h.cfg.EmergencyEnabled,
	})
}

// kerberos is a placeholder until F1 slice 4.
func (h *loginHandler) kerberos(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
}

type passwordLoginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type emergencyLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (h *loginHandler) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.passwordEnabled() {
		writeError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	var req passwordLoginRequest
	if !decodeLoginBody(w, r, &req) {
		return
	}
	identifier := strings.TrimSpace(req.Identifier)
	// An empty password is rejected before anything else: an empty simple
	// bind is an unauthenticated bind and would succeed.
	if !validLoginField(identifier, maxIdentifierBytes) || !validLoginField(req.Password, maxLoginPasswordBytes) {
		writeError(w, http.StatusBadRequest, "auth.invalid_request", "Identifier and password are required.")
		return
	}
	a := h.begin(r, IdentifierKey(identifier))
	if h.rejectIfThrottled(w, r, a) {
		return
	}

	acct, found, err := h.directory.FindDirectoryAccount(r.Context(), h.cfg.ProviderKey, identifier)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	if !found {
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, identifier, "unknown_account")
		return
	}
	vctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	err = h.verifier.VerifyPassword(vctx, acct.DistinguishedName, req.Password)
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidCredentials):
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, identifier, "invalid_password")
		return
	case errors.Is(err, ErrProviderUnavailable):
		h.logger.WarnContext(r.Context(), "directory password verification unavailable", "request_id", requestID(w), "error", err)
		writeError(w, http.StatusServiceUnavailable, "auth.provider_unavailable", "The identity provider is not available.")
		return
	default:
		writeInternal(h.logger, w, r, err)
		return
	}

	token, sess, err := h.createSession(w, r, LoginSession{
		UserID: acct.UserID, AuthMethod: methodLDAP, CorrelationID: requestID(w), Locker: h.users,
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, identifier, "user_inactive")
		return
	case err != nil:
		writeInternal(h.logger, w, r, err)
		return
	}
	h.finishLogin(w, r, a, token, sess)
}

func (h *loginHandler) emergencyLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.cfg.EmergencyEnabled {
		writeError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	var req emergencyLoginRequest
	if !decodeLoginBody(w, r, &req) {
		return
	}
	login := strings.ToLower(strings.TrimSpace(req.Login))
	if !validLoginField(login, maxLoginNameBytes) || !validLoginField(req.Password, maxLoginPasswordBytes) {
		writeError(w, http.StatusBadRequest, "auth.invalid_request", "Login and password are required.")
		return
	}
	// The emergency key space is separate from directory identifiers, so
	// failures against a directory user of the same name cannot lock the
	// emergency account (and vice versa).
	a := h.begin(r, IdentifierKey(methodEmergency+":"+login))
	if h.rejectIfThrottled(w, r, a) {
		return
	}

	cred, found, err := FindLocalCredential(r.Context(), h.pool, login)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	// Always spend one hash verification so unknown and disabled accounts
	// cost the same as real ones.
	hash := cred.PasswordHash
	if !found {
		hash = dummyHash()
	}
	match, err := VerifyPasswordHash(r.Context(), hash, req.Password)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "emergency credential unusable", "request_id", requestID(w), "login", login, "error", err)
		match = false
	}
	reason := ""
	switch {
	case !found:
		reason = "unknown_account"
	case !cred.Enabled:
		reason = "disabled"
	case !match:
		reason = "wrong_password"
	}
	if reason != "" {
		h.credentialFailure(w, r, a, "auth.emergency_login.failed", methodEmergency, login, reason)
		return
	}

	corr := requestID(w)
	token, sess, err := h.createSession(w, r, LoginSession{
		UserID: cred.UserID, AuthMethod: methodEmergency, CorrelationID: corr,
		MaxLifetime: emergencySessionMax, Locker: h.users,
		AfterCreate: func(ctx context.Context, tx pgx.Tx, s Session) error {
			if _, err := tx.Exec(ctx, `UPDATE platform.local_credentials SET last_used_at = $2 WHERE user_id = $1`, cred.UserID, s.CreatedAt); err != nil {
				return err
			}
			return h.insertAudit(ctx, tx, "auth.emergency_login.succeeded", "user", cred.UserID, &cred.UserID, corr, s.CreatedAt,
				map[string]any{"method": methodEmergency, "loginName": cred.LoginName, "clientIp": a.ip, "sessionId": s.ID})
		},
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.credentialFailure(w, r, a, "auth.emergency_login.failed", methodEmergency, login, "user_inactive")
		return
	case err != nil:
		writeInternal(h.logger, w, r, err)
		return
	}
	// Loud by design: operators alert on this line.
	h.logger.ErrorContext(r.Context(), "emergency account used", "login", cred.LoginName, "user_id", cred.UserID, "client_ip", a.ip, "request_id", corr)
	h.finishLogin(w, r, a, token, sess)
}

// attempt carries the throttle keys and timing of one login request.
type attempt struct {
	start time.Time
	ip    string
	idKey string
	ipKey string
}

func (h *loginHandler) begin(r *http.Request, idKey string) attempt {
	ip := ClientIP(r, h.cfg.TrustedProxies)
	return attempt{start: h.now(), ip: ip, idKey: idKey, ipKey: ClientKey(ip)}
}

// rejectIfThrottled answers 429 and returns true when the identifier or the
// client is locked. It runs before any directory or hash work.
func (h *loginHandler) rejectIfThrottled(w http.ResponseWriter, r *http.Request, a attempt) bool {
	retry, locked, err := h.throttle.Check(r.Context(), a.idKey, a.ipKey)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return true
	}
	if !locked {
		return false
	}
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	h.logger.WarnContext(r.Context(), "login throttled", "request_id", requestID(w), "client_ip", a.ip)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "auth.too_many_attempts", "Too many failed attempts. Try again later.")
	return true
}

// credentialFailure counts the failure, audits it and answers the uniform 401
// after the minimum response time. The password is never part of any of it.
func (h *loginHandler) credentialFailure(w http.ResponseWriter, r *http.Request, a attempt, action, method, identifier, reason string) {
	ctx := context.WithoutCancel(r.Context())
	if err := h.throttle.RecordFailures(ctx, a.idKey, a.ipKey); err != nil {
		h.logger.ErrorContext(ctx, "record login failure failed", "request_id", requestID(w), "error", err)
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		return h.insertAudit(ctx, tx, action, "login", a.idKey, nil, requestID(w), h.now(), map[string]any{
			"method": method, "identifier": truncateIdentifier(identifier), "reason": reason, "clientIp": a.ip,
		})
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "audit login failure failed", "request_id", requestID(w), "error", err)
	}
	if rest := h.cfg.MinFailureTime - h.now().Sub(a.start); rest > 0 {
		h.sleep(r.Context(), rest)
	}
	writeError(w, http.StatusUnauthorized, "auth.invalid_credentials", "Invalid credentials.")
}

// createSession adds the request's current session (if any) to be revoked and
// creates the login session.
func (h *loginHandler) createSession(w http.ResponseWriter, r *http.Request, p LoginSession) (string, Session, error) {
	if token, ok := tokenFromRequest(r, h.cfg.SecureCookie); ok {
		switch old, err := h.sessions.Authenticate(r.Context(), token); {
		case err == nil:
			p.ReplaceSessionID = old.ID
		case errors.Is(err, ErrInvalidSession):
		default:
			return "", Session{}, err
		}
	}
	return h.sessions.CreateLogin(r.Context(), p)
}

func (h *loginHandler) finishLogin(w http.ResponseWriter, r *http.Request, a attempt, token string, sess Session) {
	if err := h.throttle.Clear(r.Context(), a.idKey); err != nil {
		h.logger.ErrorContext(r.Context(), "clear login throttle failed", "request_id", requestID(w), "error", err)
	}
	expires := sess.IdleExpiresAt
	if sess.AbsoluteExpiresAt.Before(expires) {
		expires = sess.AbsoluteExpiresAt
	}
	SetCookie(w, token, expires, h.cfg.SecureCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (h *loginHandler) insertAudit(ctx context.Context, tx pgx.Tx, action, targetType, targetID string, actor *string, corr string, at time.Time, metadata map[string]any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		return err
	}
	return audit.Insert(ctx, tx, audit.Entry{
		ID: id, OccurredAt: at.UTC().Truncate(time.Microsecond), ActorID: actor, Action: action,
		TargetType: targetType, TargetID: targetID, CorrelationID: corr, Metadata: meta,
	})
}

// decodeLoginBody reads one JSON object of at most 8 KiB with no unknown
// fields. Bodies are never logged.
func decodeLoginBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "auth.invalid_request", "The request body is invalid.")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "auth.invalid_request", "The request body is invalid.")
		return false
	}
	return true
}

// validLoginField accepts 1..max bytes of valid UTF-8 without NUL.
func validLoginField(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// truncateIdentifier limits an identifier for audit metadata to 128 bytes
// without splitting a character.
func truncateIdentifier(s string) string {
	if len(s) <= maxAuditIdentifier {
		return s
	}
	cut := maxAuditIdentifier
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func requestID(w http.ResponseWriter) string { return w.Header().Get("X-Request-ID") }
