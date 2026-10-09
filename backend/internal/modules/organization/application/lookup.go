package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// People lookup: the minimal directory read that every signed-in employee has, so that a support ticket or a
// service request can be raised for another person without organization.view. It returns an id, a display name
// and the department name of active internal employees only (never external or emergency accounts, never e-mail
// addresses, managers or locations), needs at least LookupMinChars characters, returns at most LookupMaxResults and
// is rate limited by the transport. The search text is never audited.
const (
	LookupMinChars   = 3
	LookupMaxChars   = 100
	LookupMaxResults = 10
)

// ErrLookupQueryTooShort means the text is shorter than LookupMinChars; the transport reports the minimum.
var ErrLookupQueryTooShort = errors.New("organization: lookup text too short")

// ErrLookupQueryInvalid means the text is too long or contains control characters.
var ErrLookupQueryInvalid = errors.New("organization: lookup text invalid")

// PersonRef is one lookup result.
type PersonRef struct {
	ID          string
	DisplayName string
	Department  string
}

// PeopleLookupStore is the persistence port of the lookup.
type PeopleLookupStore interface {
	LookupPeople(ctx context.Context, actor audit.Actor, correlationID, text string, limit int) ([]PersonRef, error)
}

// PeopleLookup performs the lookup.
type PeopleLookup struct{ store PeopleLookupStore }

func NewPeopleLookup(store PeopleLookupStore) *PeopleLookup { return &PeopleLookup{store: store} }

// Find returns up to LookupMaxResults colleagues whose name or e-mail contains the text.
func (l *PeopleLookup) Find(ctx context.Context, actor audit.Actor, correlationID, text string) ([]PersonRef, error) {
	text = strings.TrimSpace(text)
	n := utf8.RuneCountInString(text)
	switch {
	case n < LookupMinChars:
		return nil, ErrLookupQueryTooShort
	case n > LookupMaxChars || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return nil, ErrLookupQueryInvalid
	}
	return l.store.LookupPeople(ctx, actor, correlationID, text, LookupMaxResults)
}
