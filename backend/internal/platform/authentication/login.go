package authentication

import (
	"context"
	"errors"
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
	// defaultMinFailureTime is the minimum duration of every credential
	// failure response, so unknown, disabled and wrong-password accounts
	// cannot be told apart by timing.
	defaultMinFailureTime = 400 * time.Millisecond
	// emergencySessionMax caps the absolute lifetime of emergency sessions.
	emergencySessionMax = time.Hour
	// verifyTimeout bounds one directory password verification.
	verifyTimeout = 15 * time.Second
	// defaultEmergencyHashWait is how long an emergency login waits for a
	// hash slot before it is answered 429 without hashing.
	defaultEmergencyHashWait = time.Second
	// hashBusyRetryAfter is the Retry-After (seconds) of that answer.
	hashBusyRetryAfter = 5

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
	// EmergencyHashWait bounds the wait for a password hash slot of an
	// emergency login; defaults to one second.
	EmergencyHashWait time.Duration
}

// LoginDeps are the collaborators of the login endpoints. Users is required
// (it serializes session creation with user status changes, also for the
// emergency account); Directory and Verifier may be nil when directory
// password login is not configured.
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

// errEmergencyCredentialChanged aborts emergency session creation when the
// credential was disabled, replaced or removed after it was verified.
var errEmergencyCredentialChanged = errors.New("authentication: emergency credential changed")

// RegisterLogin mounts the login endpoints under /api/v1/auth. Unsafe
// methods are protected by the same-origin guard. It panics when d.Users is
// nil: without the user lock a login could race a deactivation.
func RegisterLogin(mux *http.ServeMux, d LoginDeps, cfg LoginConfig) {
	if d.Users == nil {
		panic("authentication: RegisterLogin requires LoginDeps.Users")
	}
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
	if h.cfg.EmergencyHashWait <= 0 {
		h.cfg.EmergencyHashWait = defaultEmergencyHashWait
	}
	mux.Handle("GET /api/v1/auth/methods", httpx.NoStore(http.HandlerFunc(h.methods)))
	mux.Handle("POST /api/v1/auth/login", httpx.NoStore(RequireSameOrigin(http.HandlerFunc(h.login))))
	mux.Handle("POST /api/v1/auth/emergency-login", httpx.NoStore(RequireSameOrigin(http.HandlerFunc(h.emergencyLogin))))
	mux.Handle("GET /api/v1/auth/kerberos", httpx.NoStore(http.HandlerFunc(h.kerberos)))
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
	return h.cfg.ProviderKey != "" && h.directory != nil && h.verifier != nil
}

func (h *loginHandler) methods(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]bool{
		"password":  h.passwordEnabled(),
		"kerberos":  false,
		"emergency": h.cfg.EmergencyEnabled,
	})
}

// kerberos is a placeholder until F1 slice 4.
func (h *loginHandler) kerberos(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
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
	if !h.passwordEnabled() {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
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
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "Identifier and password are required.")
		return
	}
	a := h.begin(r)
	// The client budget is reserved before any lookup, so a flood is refused
	// without touching the directory tables.
	if !h.reserve(w, r, a, a.ipKey, true) {
		return
	}

	acct, found, err := h.directory.FindDirectoryAccount(r.Context(), h.cfg.ProviderKey, identifier)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return
	}
	// One budget per account (all spellings of its identifiers share it) or,
	// for identifiers that resolve to nothing, per normalized identifier.
	userID := ""
	a.subjectKey = IdentifierKey(identifier)
	if found {
		userID = acct.UserID
		a.subjectKey = AccountKey(acct.UserID)
	}
	if !h.reserve(w, r, a, a.subjectKey, false) {
		return
	}
	if !found {
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, "", "unknown_account")
		return
	}
	vctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	err = h.verifier.VerifyPassword(vctx, acct.DistinguishedName, req.Password)
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidCredentials):
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, userID, "invalid_password")
		return
	case errors.Is(err, ErrProviderUnavailable):
		h.logger.WarnContext(r.Context(), "directory password verification unavailable", "request_id", httpx.RequestID(w), "error", err)
		// The password was never checked, so the attempt must not count
		// toward a lock: otherwise users retrying during a directory outage
		// would be locked out afterwards. No bind happened, so this cannot
		// help an attacker or trigger directory lockout.
		for _, key := range []string{a.subjectKey, a.ipKey} {
			if rerr := h.throttle.Refund(r.Context(), key); rerr != nil {
				h.logger.WarnContext(r.Context(), "refund login attempt", "request_id", httpx.RequestID(w), "error", rerr)
			}
		}
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth.provider_unavailable", "The identity provider is not available.")
		return
	default:
		writeInternal(h.logger, w, r, err)
		return
	}

	token, sess, err := h.createSession(r, LoginSession{
		UserID: acct.UserID, AuthMethod: methodLDAP, CorrelationID: httpx.RequestID(w), Locker: h.users,
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.credentialFailure(w, r, a, "auth.login.failed", methodLDAP, userID, "user_inactive")
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

func (h *loginHandler) emergencyLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EmergencyEnabled {
		httpx.WriteError(w, http.StatusNotFound, "auth.method_unavailable", "This login method is not available.")
		return
	}
	var req emergencyLoginRequest
	if !decodeLoginBody(w, r, &req) {
		return
	}
	login := strings.ToLower(strings.TrimSpace(req.Login))
	if !validLoginField(login, maxLoginNameBytes) || !validLoginField(req.Password, maxLoginPasswordBytes) {
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "Login and password are required.")
		return
	}
	a := h.begin(r)
	// No per-account key: strangers must not be able to lock the break-glass
	// account. Only the client budget applies, reserved before any hashing.
	if !h.reserve(w, r, a, a.ipKey, true) {
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
	match, err := VerifyPasswordHashWithin(r.Context(), hash, req.Password, h.cfg.EmergencyHashWait)
	if errors.Is(err, ErrHashBusy) {
		h.logger.WarnContext(r.Context(), "emergency login: no hash capacity", "request_id", httpx.RequestID(w), "client_ip", a.ip)
		w.Header().Set("Retry-After", strconv.Itoa(hashBusyRetryAfter))
		httpx.WriteError(w, http.StatusTooManyRequests, "auth.too_many_attempts", "Too many failed attempts. Try again later.")
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "emergency credential unusable", "request_id", httpx.RequestID(w), "user_id", cred.UserID, "error", err)
		match = false
	}
	reason, userID := "", ""
	if found {
		userID = cred.UserID
	}
	switch {
	case !found:
		reason = "unknown_account"
	case !cred.Enabled:
		reason = "disabled"
	case !match:
		reason = "wrong_password"
	}
	if reason != "" {
		h.credentialFailure(w, r, a, "auth.emergency_login.failed", methodEmergency, userID, reason)
		return
	}

	corr := httpx.RequestID(w)
	token, sess, err := h.createSession(r, LoginSession{
		UserID: cred.UserID, AuthMethod: methodEmergency, CorrelationID: corr,
		MaxLifetime: emergencySessionMax, Locker: h.users,
		AfterCreate: func(ctx context.Context, tx pgx.Tx, s Session) error {
			// The credential must still be exactly what was verified: a
			// concurrent disable, password change or removal makes this
			// update match nothing and rolls the session back.
			tag, err := tx.Exec(ctx, `
				UPDATE platform.local_credentials SET last_used_at = now()
				WHERE user_id = $1 AND enabled AND password_hash = $2`, cred.UserID, cred.PasswordHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errEmergencyCredentialChanged
			}
			return audit.Record(ctx, tx, audit.Change{
				Action: "auth.emergency_login.succeeded", TargetType: "user", TargetID: cred.UserID,
				Actor: audit.UserActor(cred.UserID), CorrelationID: corr, OccurredAt: s.CreatedAt,
				Metadata: map[string]any{"method": methodEmergency, "loginName": cred.LoginName, "clientIp": a.ip, "sessionId": s.ID},
			})
		},
	})
	switch {
	case errors.Is(err, ErrUserInactive):
		h.credentialFailure(w, r, a, "auth.emergency_login.failed", methodEmergency, userID, "user_inactive")
		return
	case errors.Is(err, errEmergencyCredentialChanged):
		h.credentialFailure(w, r, a, "auth.emergency_login.failed", methodEmergency, userID, "credential_changed")
		return
	case errors.Is(err, ErrTemporarilyUnavailable):
		h.unavailable(w, r)
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
	ipKey string
	// subjectKey is the account or identifier key reserved by /auth/login;
	// empty for emergency logins.
	subjectKey string
}

func (h *loginHandler) begin(r *http.Request) attempt {
	ip := ClientIP(r, h.cfg.TrustedProxies)
	return attempt{start: h.now(), ip: ip, ipKey: ClientKey(ip)}
}

// reserve reserves one attempt for key and returns true when the request may
// proceed. Otherwise it has answered 429 (or 500) and the caller must stop;
// throttled requests are logged but not audited.
func (h *loginHandler) reserve(w http.ResponseWriter, r *http.Request, a attempt, key string, client bool) bool {
	reserveFn := h.throttle.ReserveIdentifier
	if client {
		reserveFn = h.throttle.ReserveClient
	}
	allowed, retry, err := reserveFn(r.Context(), key)
	if err != nil {
		writeInternal(h.logger, w, r, err)
		return false
	}
	if allowed {
		return true
	}
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	h.logger.WarnContext(r.Context(), "login throttled", "request_id", httpx.RequestID(w), "client_ip", a.ip)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(w, http.StatusTooManyRequests, "auth.too_many_attempts", "Too many failed attempts. Try again later.")
	return false
}

func (h *loginHandler) unavailable(w http.ResponseWriter, r *http.Request) {
	h.logger.WarnContext(r.Context(), "login session creation timed out on a lock", "request_id", httpx.RequestID(w))
	httpx.WriteError(w, http.StatusServiceUnavailable, "auth.temporarily_unavailable", "Login is temporarily unavailable. Try again.")
}

// credentialFailure audits the failure and answers the uniform 401 after the
// minimum response time. The attempt was already counted by reserve. The
// audit never contains the raw identifier or any password: the target is the
// resolved user, or the fixed "login"/"unknown" when nothing resolved.
func (h *loginHandler) credentialFailure(w http.ResponseWriter, r *http.Request, a attempt, action, method, userID, reason string) {
	ctx := context.WithoutCancel(r.Context())
	targetType, targetID := "login", "unknown"
	if userID != "" {
		targetType, targetID = "user", userID
	}
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{
			Action: action, TargetType: targetType, TargetID: targetID,
			Actor: audit.SystemActor("login"), CorrelationID: httpx.RequestID(w),
			Metadata: map[string]any{"method": method, "reason": reason, "clientIp": a.ip},
		})
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "audit login failure failed", "request_id", httpx.RequestID(w), "error", err)
	}
	if rest := h.cfg.MinFailureTime - h.now().Sub(a.start); rest > 0 {
		h.sleep(r.Context(), rest)
	}
	httpx.WriteError(w, http.StatusUnauthorized, "auth.invalid_credentials", "Invalid credentials.")
}

// createSession adds the request's current session (if any) to be revoked and
// creates the login session.
func (h *loginHandler) createSession(r *http.Request, p LoginSession) (string, Session, error) {
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

// finishLogin gives the successful attempt back: the account counter is
// forgotten and the client budget refunded by one.
func (h *loginHandler) finishLogin(w http.ResponseWriter, r *http.Request, a attempt, token string, sess Session) {
	ctx := context.WithoutCancel(r.Context())
	if a.subjectKey != "" {
		if err := h.throttle.Clear(ctx, a.subjectKey); err != nil {
			h.logger.ErrorContext(ctx, "clear login throttle failed", "request_id", httpx.RequestID(w), "error", err)
		}
	}
	if err := h.throttle.Refund(ctx, a.ipKey); err != nil {
		h.logger.ErrorContext(ctx, "refund login throttle failed", "request_id", httpx.RequestID(w), "error", err)
	}
	expires := sess.IdleExpiresAt
	if sess.AbsoluteExpiresAt.Before(expires) {
		expires = sess.AbsoluteExpiresAt
	}
	SetCookie(w, token, expires, h.cfg.SecureCookie)
	w.WriteHeader(http.StatusNoContent)
}

// decodeLoginBody reads one JSON object of at most 8 KiB with no unknown
// fields. Bodies are never logged.
func decodeLoginBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxLoginBodyBytes); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "auth.invalid_request", "The request body is invalid.")
		return false
	}
	return true
}

// validLoginField accepts 1..max bytes of valid UTF-8 without NUL.
func validLoginField(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
