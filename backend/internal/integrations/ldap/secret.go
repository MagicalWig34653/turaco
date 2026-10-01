package ldap

import (
	"fmt"
	"io"
	"log/slog"
)

const redacted = "[REDACTED]"

// secret holds a credential so that formatting, logging or marshalling a
// struct that contains it never reveals it. The value is behind a pointer on
// purpose: fmt cannot call methods on unexported struct fields, and for a
// plain string-kind field it would print the content, while a pointer field is
// printed as an address.
type secret struct{ value *string }

func newSecret(v string) secret { return secret{value: &v} }

// reveal returns the credential. Call it only where it is handed to the LDAP
// bind operation.
func (s secret) reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

func (secret) String() string   { return redacted }
func (secret) GoString() string { return redacted }

// Format implements fmt.Formatter so that every verb, including %+v and %#v,
// prints the placeholder.
func (secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue implements slog.LogValuer.
func (secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText also covers encoding/json and other text-based encoders.
func (secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
