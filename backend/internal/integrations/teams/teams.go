// Package teams is the Microsoft Teams adapter port (ADR-0036, slice T-A): channel posts through Teams Workflows
// webhook destinations. Turaco owns the typed Card; the Adaptive Card JSON is rendered inside the adapter, so
// Microsoft types never reach the Notification service. A webhook URL is a bearer secret: it comes from the
// deployment secret file TEAMS_CHANNEL_DESTINATIONS_FILE, is addressed by a destination key everywhere else and
// never appears in errors, logs, health output or the API.
package teams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

// Card kinds of channel posts.
const (
	KindDeclared  = "declared"
	KindUpdated   = "updated"
	KindScheduled = "scheduled"
)

var (
	// ErrNotConfigured is returned by the NotConfigured adapter: no destination is configured.
	ErrNotConfigured = errors.New("teams: not configured")
	// ErrUnknownDestination means the destination key has no entry in the destinations file. It is permanent.
	ErrUnknownDestination = errors.New("teams: unknown destination")
	// ErrInvalidCard means the card violates the reference-only rules.
	ErrInvalidCard = errors.New("teams: invalid card")
)

// TransientError marks a failure worth retrying: a network error, a 5xx answer or rate limiting (429). RetryAfter
// carries the provider's Retry-After when it sent one.
type TransientError struct {
	// Code is a short machine code (for example rate_limited, http_503, network).
	Code       string
	RetryAfter time.Duration
}

func (e *TransientError) Error() string { return "teams: transient failure: " + e.Code }

// PermanentError marks a failure that retrying cannot fix (the flow was deleted, the URL was rejected).
type PermanentError struct {
	// Code is a short machine code (for example http_404, http_403).
	Code string
}

func (e *PermanentError) Error() string { return "teams: permanent failure: " + e.Code }

// ErrorCode reduces an adapter error to a short machine code that is safe to store and show.
func ErrorCode(err error) string {
	var t *TransientError
	var p *PermanentError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, ErrUnknownDestination):
		return "unknown_destination"
	case errors.Is(err, ErrInvalidCard):
		return "invalid_card"
	case errors.As(err, &t):
		return t.Code
	case errors.As(err, &p):
		return p.Code
	default:
		return "error"
	}
}

// Card is one channel post. It is reference-only by construction: category wording, the reference number and a
// link back to Turaco. It has no field for a title, description, name or comment (ADR-0036, data minimisation),
// and Validate refuses a reference that could carry free text.
type Card struct {
	// Kind is KindDeclared, KindUpdated or KindScheduled.
	Kind string
	// Category is the notification category the post belongs to.
	Category string
	// Reference is the number a reader may know, for example MI-000012. No spaces, no free text.
	Reference string
	// Headline is the generic wording of the category in Locale ("A major incident was declared").
	Headline string
	// Action is the label of the link ("Open in Turaco").
	Action string
	// LinkURL is the absolute https (or, for local development, http) URL of the record in Turaco.
	LinkURL string
	// Locale is "en" or "de"; it only selects the wording that Headline and Action already carry.
	Locale string
}

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

const maxTextRunes = 200

// ValidReference reports whether s is acceptable as a reference number.
func ValidReference(s string) bool { return referencePattern.MatchString(s) }

// Validate checks the card; a card that fails is never sent.
func (c Card) Validate() error {
	switch c.Kind {
	case KindDeclared, KindUpdated, KindScheduled:
	default:
		return fmt.Errorf("%w: unknown kind", ErrInvalidCard)
	}
	if c.Category == "" || !ValidReference(c.Reference) {
		return fmt.Errorf("%w: category and a reference number are required", ErrInvalidCard)
	}
	for name, text := range map[string]string{"headline": c.Headline, "action": c.Action} {
		if strings.TrimSpace(text) == "" || len([]rune(text)) > maxTextRunes || strings.ContainsFunc(text, unicode.IsControl) {
			return fmt.Errorf("%w: %s is empty, too long or has control characters", ErrInvalidCard, name)
		}
	}
	u, err := url.Parse(c.LinkURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(c.LinkURL) > 1000 {
		return fmt.Errorf("%w: link must be an absolute http(s) URL", ErrInvalidCard)
	}
	if c.Locale != "en" && c.Locale != "de" {
		return fmt.Errorf("%w: unknown locale", ErrInvalidCard)
	}
	return nil
}

// Sender is the port of the Teams channel. Implementations: Workflows (real), Fake (tests) and NotConfigured.
type Sender interface {
	// Mode reports real, fake or not_configured for health.
	Mode() health.Mode
	// DestinationKeys lists the configured destination keys, sorted. Never URLs.
	DestinationKeys() []string
	// PostToChannel posts the card to the destination. Errors are ErrUnknownDestination, ErrInvalidCard,
	// *TransientError or *PermanentError (or ErrNotConfigured).
	PostToChannel(ctx context.Context, destinationKey string, card Card) error
}
