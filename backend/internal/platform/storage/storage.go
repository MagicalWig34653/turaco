// Package storage is the platform file storage (ADR-0037). Backend is the small blob port with a filesystem and an
// S3 adapter; Vault wraps a Backend with per-object envelope encryption (ADR-0014). Objects are addressed by
// generated ids only: user-supplied names never reach a path or object key.
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

var (
	// ErrNotFound means the object does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidID means the value is not a well-formed object id.
	ErrInvalidID = errors.New("storage: invalid object id")
	// ErrTooLarge means the plaintext exceeds the size limit of the call.
	ErrTooLarge = errors.New("storage: object exceeds the size limit")
	// ErrCorrupt means the stored object failed authentication (tampered, truncated or damaged).
	ErrCorrupt = errors.New("storage: object failed authentication")
	// ErrWrongKey means the object was sealed with a different master key.
	ErrWrongKey = errors.New("storage: object was sealed with a different master key")
)

// IDLength is the length of an object id: 128 random bits in lower-case hex.
const IDLength = 32

// NewID generates an object id.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidID reports whether id is a well-formed object id. Adapters check it before touching a path or a key.
func ValidID(id string) bool {
	if len(id) != IDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Backend is the blob port. It stores opaque bytes under generated ids; it knows nothing about encryption.
type Backend interface {
	// Put stores the whole stream under id atomically: readers see the complete object or none. An existing
	// object with the same id is replaced.
	Put(ctx context.Context, id string, r io.Reader) error
	// Open returns the object's stream, or ErrNotFound.
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	// Delete removes the object; a missing object is not an error (idempotent).
	Delete(ctx context.Context, id string) error
	// Check verifies that the backend is reachable and writable by this process without storing user data.
	Check(ctx context.Context) error
	// Driver names the adapter ("filesystem" or "s3").
	Driver() string
}
