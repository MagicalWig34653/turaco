package public

import (
	"context"
)

// TeamsIdentityStore is the part of the Organization repository the personal Teams cards need.
type TeamsIdentityStore interface {
	EntraIdentityOf(ctx context.Context, userID string) (tenantID, objectID string, found bool, err error)
}

// TeamsIdentities answers the Teams identity of a User for the Notification service (ADR-0036): the Entra identity
// link of an active internal User. There is no matching by email or name; a User without a link gets no Teams card.
type TeamsIdentities struct{ store TeamsIdentityStore }

// NewTeamsIdentities creates the contract.
func NewTeamsIdentities(store TeamsIdentityStore) *TeamsIdentities {
	return &TeamsIdentities{store: store}
}

// TeamsIdentity implements notifications.TeamsIdentityResolver.
func (t *TeamsIdentities) TeamsIdentity(ctx context.Context, userID string) (string, string, bool, error) {
	return t.store.EntraIdentityOf(ctx, userID)
}
