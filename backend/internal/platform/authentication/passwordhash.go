package authentication

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Emergency-account password hashing: argon2id with fixed parameters and PHC
// string encoding ($argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>, unpadded
// standard base64). docs/security/identity-access-design.md §7.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32

	// MinPasswordLength is the minimum length (characters) of an emergency
	// password; MaxPasswordLength bounds the bytes hashed per request.
	MinPasswordLength = 16
	MaxPasswordLength = 1024

	// Verification bounds so a tampered stored hash cannot demand unbounded
	// memory or time.
	maxArgonMemoryKiB = 256 * 1024
	maxArgonTime      = 10
	maxArgonThreads   = 16
)

// ErrPasswordTooShort and ErrPasswordTooLong reject passwords outside policy.
var (
	ErrPasswordTooShort = fmt.Errorf("authentication: password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("authentication: password must be at most %d bytes", MaxPasswordLength)
)

var b64 = base64.RawStdEncoding

// hashSlots bounds concurrent argon2 computations (64 MiB each), so a flood of
// attempts from many addresses cannot exhaust memory.
var hashSlots = make(chan struct{}, 4)

// ErrHashBusy means no hash slot became free within the allowed wait; nothing
// was hashed.
var ErrHashBusy = errors.New("authentication: password hashing is busy")

// acquireHashSlot waits for a slot until ctx ends or, when wait is positive,
// for at most wait (then ErrHashBusy).
func acquireHashSlot(ctx context.Context, wait time.Duration) (func(), error) {
	release := func() { <-hashSlots }
	select {
	case hashSlots <- struct{}{}:
		return release, nil
	default:
	}
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case hashSlots <- struct{}{}:
		return release, nil
	case <-timeout:
		return nil, ErrHashBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ValidatePasswordPolicy checks the emergency password policy.
func ValidatePasswordPolicy(password string) error {
	if len(password) > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// HashPassword returns the argon2id PHC string of password after checking the
// password policy.
func HashPassword(ctx context.Context, password string) (string, error) {
	if err := ValidatePasswordPolicy(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	release, err := acquireHashSlot(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

type argonHash struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

func parseArgonHash(phc string) (argonHash, error) {
	bad := errors.New("authentication: malformed password hash")
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return argonHash{}, bad
	}
	var h argonHash
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return argonHash{}, bad
	}
	var m, t, p uint64
	var err error
	if !strings.HasPrefix(params[0], "m=") || !strings.HasPrefix(params[1], "t=") || !strings.HasPrefix(params[2], "p=") {
		return argonHash{}, bad
	}
	if m, err = strconv.ParseUint(params[0][2:], 10, 32); err != nil {
		return argonHash{}, bad
	}
	if t, err = strconv.ParseUint(params[1][2:], 10, 32); err != nil {
		return argonHash{}, bad
	}
	if p, err = strconv.ParseUint(params[2][2:], 10, 8); err != nil {
		return argonHash{}, bad
	}
	if m == 0 || m > maxArgonMemoryKiB || t == 0 || t > maxArgonTime || p == 0 || p > maxArgonThreads {
		return argonHash{}, bad
	}
	h.memory, h.time, h.threads = uint32(m), uint32(t), uint8(p)
	if h.salt, err = b64.Strict().DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return argonHash{}, bad
	}
	if h.key, err = b64.Strict().DecodeString(parts[5]); err != nil || len(h.key) < 16 {
		return argonHash{}, bad
	}
	return h, nil
}

// VerifyPasswordHash reports whether password matches the PHC string. The
// comparison is constant-time. A malformed hash is an error, a mismatch is
// (false, nil).
func VerifyPasswordHash(ctx context.Context, phc, password string) (bool, error) {
	return VerifyPasswordHashWithin(ctx, phc, password, 0)
}

// VerifyPasswordHashWithin is VerifyPasswordHash that waits at most wait for a
// hash slot (wait <= 0: until ctx ends) and returns ErrHashBusy, without
// hashing, when none became free.
func VerifyPasswordHashWithin(ctx context.Context, phc, password string, wait time.Duration) (bool, error) {
	h, err := parseArgonHash(phc)
	if err != nil {
		return false, err
	}
	if len(password) > MaxPasswordLength {
		return false, nil
	}
	release, err := acquireHashSlot(ctx, wait)
	if err != nil {
		return false, fmt.Errorf("verify password: %w", err)
	}
	defer release()
	key := argon2.IDKey([]byte(password), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(key, h.key) == 1, nil
}

// dummyHash is verified against when an account does not exist or is
// disabled, so that the response time does not reveal it.
var dummyHash = sync.OnceValue(func() string {
	salt := make([]byte, argonSaltLen)
	key := argon2.IDKey([]byte("turaco-dummy-password"), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
})
