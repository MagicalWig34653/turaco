package authentication

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Local accounts (ADR-0034, F14 slice A-A2): Users outside the directory sign in with a password they set
// themselves through a single-use invitation or reset token. The emergency (break-glass) account is a different
// credential kind with its own endpoint, policy and hashing pool; nothing here authenticates it and nothing in
// emergency.go authenticates a local account.

// Credential kinds of platform.local_credentials.
const (
	CredentialKindEmergency = "emergency"
	CredentialKindLocal     = "local"
)

// Token purposes and lifetimes.
const (
	PurposeInvitation = "invitation"
	PurposeReset      = "reset"

	InvitationTTL = 7 * 24 * time.Hour
	ResetTTL      = 24 * time.Hour

	methodLocal = "local-account"
)

// Local-account audit actions.
const (
	ActionLocalInvited        = "auth.local_credential.invited"
	ActionLocalResetRequested = "auth.local_credential.reset_requested"
	ActionLocalPasswordSet    = "auth.local_credential.password_set"
	ActionTokenSuperseded     = "auth.credential_token.superseded"
	ActionTokenRevoked        = "auth.credential_token.revoked"
	ActionLocalLoginSucceeded = "auth.local_login.succeeded"
	ActionLocalLoginFailed    = "auth.local_login.failed"
	ActionLocalLoginLocked    = "auth.local_login.locked"
	maxLocalFailures          = 5
	localLockout              = 15 * time.Minute
	localArgonMemoryKiB       = 64 * 1024
	localArgonTime            = 3
	localArgonThreads         = 1
	MinLocalPasswordLength    = 12
	defaultLocalHashWait      = time.Second
)

// localHashSlots is the argon2 slot pool of local accounts. It is separate from the break-glass pool, so a flood
// of local logins cannot starve the emergency account (review rule R3).
var localHashSlots = make(chan struct{}, 4)

// Local password errors.
var (
	ErrLocalPasswordPolicy = errors.New("authentication: password does not meet the local account policy")
	ErrInvalidToken        = errors.New("authentication: invalid or expired token")
)

// LocalAccountInfo is what authentication needs to know about a User from Organization.
type LocalAccountInfo struct {
	// Exists and Local: the User exists and was created in Turaco (origin "local"); directory-linked and
	// emergency Users never have a local credential.
	Exists bool
	Local  bool
	Active bool
	// DisplayName and Email feed the password policy (a password may not contain them).
	DisplayName string
	Email       string
}

// LocalAccountDirectory is the Organization contract of local accounts; organization/public implements it.
type LocalAccountDirectory interface {
	// FindLocalAccount resolves a login identifier (the primary email address) to the User id of a local account.
	FindLocalAccount(ctx context.Context, identifier string) (userID string, ok bool, err error)
	// LocalAccountInfo reads the User inside tx (FOR SHARE).
	LocalAccountInfo(ctx context.Context, tx pgx.Tx, userID string) (LocalAccountInfo, error)
}

// ---- password policy and hashing ----

var commonPasswords = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`passwordpassword password1234 password12345 passwort1234 passwort12345 qwertzuiop12 qwertyuiop12
		1234567890ab 123456789012 1234567890123 12345678901234 abcdefghijkl abcdefgh1234 iloveyou1234 letmein12345 welcome12345 welcome1234!
		administrator changeme1234 changemenow12 summer2024!! winter2024!! sommer2024!! winter2025!! sommer2025!! frühling2025!
		krankenhaus1 krankenhaus12 klinikum1234 mustermann123 trustno1trustno1 monkeymonkey1 footballfootball qazwsxedcrfv
		1q2w3e4r5t6y 1qaz2wsx3edc zaq12wsxcde3 asdfghjkl123 asdfghjklasdf passw0rdpassw0rd p@ssw0rd1234 p@ssword1234 turaco123456
		turacoturaco1`) {
		commonPasswords[p] = true
	}
}

// squash lowercases and keeps letters and digits, so "P@ss-W0rd" and "pass word" compare like the plain forms.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case '@':
			b.WriteRune('a')
		case '0':
			b.WriteRune('o')
		case '1', '!':
			b.WriteRune('i')
		case '3':
			b.WriteRune('e')
		case '$', '5':
			b.WriteRune('s')
		default:
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 && r != ' ' {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// ValidateLocalPassword checks the policy of local accounts: at least 12 characters, at most 1024 bytes, no
// composition rules, not a known common password, not a single repeated or sequential run, and not containing the
// User's display name, email local part or login name. Distinct from the emergency policy.
func ValidateLocalPassword(password string, personal ...string) error {
	if len(password) > MaxPasswordLength || utf8.RuneCountInString(password) < MinLocalPasswordLength || !utf8.ValidString(password) || strings.ContainsRune(password, 0) {
		return ErrLocalPasswordPolicy
	}
	sq := squash(password)
	if commonPasswords[strings.ToLower(password)] || commonPasswords[sq] {
		return ErrLocalPasswordPolicy
	}
	for _, w := range []string{"password", "passwort", "qwerty", "qwertz", "letmein", "welcome", "iloveyou", "administrator", "1234567", "9876543", "abcdefg"} {
		if len(sq) < len(w)+12 && strings.Contains(sq, squash(w)) {
			return ErrLocalPasswordPolicy
		}
	}
	// One repeated character or a repeated short block ("abababababab").
	runes := []rune(password)
	for block := 1; block <= 4 && block*3 <= len(runes); block++ {
		repeated := true
		for i := block; i < len(runes); i++ {
			if runes[i] != runes[i%block] {
				repeated = false
				break
			}
		}
		if repeated {
			return ErrLocalPasswordPolicy
		}
	}
	for _, p := range personal {
		for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == ' ' || r == '@' || r == '.' || r == '-' || r == '_' || r == ',' }) {
			if sp := squash(part); utf8.RuneCountInString(sp) >= 4 && strings.Contains(sq, sp) {
				return ErrLocalPasswordPolicy
			}
		}
	}
	return nil
}

// HashLocalPassword validates the local policy and returns the argon2id PHC string, computed under the local slot
// pool and parameters. It waits at most defaultLocalHashWait for a slot (ErrHashBusy).
func HashLocalPassword(ctx context.Context, password string, personal ...string) (string, error) {
	if err := ValidateLocalPassword(password, personal...); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	release, err := acquireSlot(ctx, localHashSlots, defaultLocalHashWait)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, localArgonTime, localArgonMemoryKiB, localArgonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, localArgonMemoryKiB, localArgonTime, localArgonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyLocalPasswordHash is VerifyPasswordHashWithin under the local slot pool.
func VerifyLocalPasswordHash(ctx context.Context, phc, password string, wait time.Duration) (bool, error) {
	h, err := parseArgonHash(phc)
	if err != nil {
		return false, err
	}
	if len(password) > MaxPasswordLength {
		return false, nil
	}
	release, err := acquireSlot(ctx, localHashSlots, wait)
	if err != nil {
		return false, fmt.Errorf("verify password: %w", err)
	}
	defer release()
	key := argon2.IDKey([]byte(password), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(key, h.key) == 1, nil
}

var localDummyHash = sync.OnceValue(func() string {
	salt := make([]byte, argonSaltLen)
	key := argon2.IDKey([]byte("turaco-local-dummy-password"), salt, localArgonTime, localArgonMemoryKiB, localArgonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, localArgonMemoryKiB, localArgonTime, localArgonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
})

// ---- credentials and tokens ----

// LocalCredentials issues credentials and tokens of local accounts inside the caller's transaction. Organization
// calls it through its CredentialIssuer port; platform code never reads Organization tables.
type LocalCredentials struct{ now func() time.Time }

// NewLocalCredentials creates the service. now may be nil.
func NewLocalCredentials(now func() time.Time) *LocalCredentials {
	if now == nil {
		now = time.Now
	}
	return &LocalCredentials{now: now}
}

// EnsureLocalCredential creates the disabled, password-less credential row of a local account when it does not
// exist and reports whether the account was activated (a password was set) before.
func (l *LocalCredentials) EnsureLocalCredential(ctx context.Context, tx pgx.Tx, userID string) (activated bool, err error) {
	if !uuidPattern.MatchString(userID) {
		return false, errors.New("ensure local credential: user id must be a UUID")
	}
	loginName := "l-" + strings.ReplaceAll(strings.ToLower(userID), "-", "")[:24]
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.local_credentials (user_id, login_name, kind, password_hash, enabled)
		VALUES ($1, $2, 'local', NULL, false) ON CONFLICT (user_id) DO NOTHING`, userID, loginName); err != nil {
		return false, fmt.Errorf("ensure local credential: %w", err)
	}
	var kind string
	var hash *string
	if err := tx.QueryRow(ctx, `SELECT kind, password_hash FROM platform.local_credentials WHERE user_id = $1 FOR UPDATE`, userID).Scan(&kind, &hash); err != nil {
		return false, fmt.Errorf("ensure local credential: %w", err)
	}
	if kind != CredentialKindLocal {
		return false, errors.New("ensure local credential: the user has a credential of another kind")
	}
	return hash != nil, nil
}

// IssueToken creates the single-use token of purpose for the User (256-bit random, only its SHA-256 hash is
// stored) and invalidates every earlier open token of the same User and purpose. The raw token is returned once.
func (l *LocalCredentials) IssueToken(ctx context.Context, tx pgx.Tx, userID, purpose string, actor audit.Actor, correlationID string) (string, time.Time, error) {
	var ttl time.Duration
	var action string
	switch purpose {
	case PurposeInvitation:
		ttl, action = InvitationTTL, ActionLocalInvited
	case PurposeReset:
		ttl, action = ResetTTL, ActionLocalResetRequested
	default:
		return "", time.Time{}, errors.New("issue credential token: unknown purpose")
	}
	if err := actor.Validate(); err != nil {
		return "", time.Time{}, err
	}
	now := l.now().UTC().Truncate(time.Microsecond)
	tag, err := tx.Exec(ctx, `UPDATE platform.credential_tokens SET used_at = $3 WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`, userID, purpose, now)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("supersede credential tokens: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if err := audit.Record(ctx, tx, audit.Change{Action: ActionTokenSuperseded, TargetType: "user", TargetID: userID, Actor: actor,
			CorrelationID: correlationID, OccurredAt: now, Metadata: map[string]any{"purpose": purpose, "count": tag.RowsAffected()}}); err != nil {
			return "", time.Time{}, err
		}
	}
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(ttl)
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.credential_tokens (user_id, purpose, token_hash, expires_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, purpose, HashToken(token), expires, actorJSON(actor), now); err != nil {
		return "", time.Time{}, fmt.Errorf("insert credential token: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "user", TargetID: userID, Actor: actor,
		CorrelationID: correlationID, OccurredAt: now, Metadata: map[string]any{"expiresAt": expires.Format(time.RFC3339)}}); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func actorJSON(a audit.Actor) []byte {
	if a.UserID != "" {
		return []byte(`{"userId":"` + a.UserID + `"}`)
	}
	return []byte(`{"actor":"` + a.System + `"}`)
}

// RevokeCredentialTokens marks every open token of the User used and audits it when there were any.
func RevokeCredentialTokens(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, at time.Time) (int, error) {
	at = at.UTC().Truncate(time.Microsecond)
	tag, err := tx.Exec(ctx, `UPDATE platform.credential_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, userID, at)
	if err != nil {
		return 0, fmt.Errorf("revoke credential tokens: %w", err)
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		if err := audit.Record(ctx, tx, audit.Change{Action: ActionTokenRevoked, TargetType: "user", TargetID: userID, Actor: actor,
			CorrelationID: correlationID, OccurredAt: at, Metadata: map[string]any{"reason": reason, "count": n}}); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// SessionRevoker implements Organization's SessionRevoker port over the session and token stores.
type SessionRevoker struct{}

// RevokeSessions ends all sessions of the User.
func (SessionRevoker) RevokeSessions(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, at time.Time) (int, error) {
	return RevokeUserSessions(ctx, tx, userID, reason, actor, correlationID, at)
}

// RevokeCredentialTokens invalidates the open invitation and reset tokens of the User.
func (SessionRevoker) RevokeCredentialTokens(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, at time.Time) (int, error) {
	return RevokeCredentialTokens(ctx, tx, userID, reason, actor, correlationID, at)
}

// localCredential is the stored credential of a local account.
type localCredential struct {
	UserID       string
	LoginName    string
	PasswordHash *string
	Enabled      bool
	// Locked is evaluated by the database clock (locked_until > now()).
	Locked bool
}

func findLocalCredentialOf(ctx context.Context, pool *pgxpool.Pool, userID string) (localCredential, bool, error) {
	var c localCredential
	err := pool.QueryRow(ctx, `
		SELECT user_id::text, login_name, password_hash, enabled, coalesce(locked_until > now(), false)
		FROM platform.local_credentials WHERE user_id = $1 AND kind = 'local'`, userID).
		Scan(&c.UserID, &c.LoginName, &c.PasswordHash, &c.Enabled, &c.Locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return localCredential{}, false, nil
	}
	if err != nil {
		return localCredential{}, false, fmt.Errorf("find local account credential: %w", err)
	}
	return c, true, nil
}
