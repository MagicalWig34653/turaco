package public

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
)

// EntraProviderKey is the external identity provider key of an Entra tenant.
func EntraProviderKey(tenantID string) string { return "entra:" + strings.ToLower(tenantID) }

// ExternalIdentityStore is the part of the Organization repository the Entra sign-in needs.
type ExternalIdentityStore interface {
	FindUserByExternalIdentity(ctx context.Context, providerKey, subject string) (string, bool, error)
}

// EntraIdentities implements authentication.EntraIdentityDirectory on tenant id + object id.
type EntraIdentities struct{ store ExternalIdentityStore }

var _ authentication.EntraIdentityDirectory = (*EntraIdentities)(nil)

// NewEntraIdentities creates the contract.
func NewEntraIdentities(store ExternalIdentityStore) *EntraIdentities {
	return &EntraIdentities{store: store}
}

// FindEntraUser implements authentication.EntraIdentityDirectory.
func (e *EntraIdentities) FindEntraUser(ctx context.Context, tenantID, objectID string) (string, bool, error) {
	return e.store.FindUserByExternalIdentity(ctx, EntraProviderKey(tenantID), strings.ToLower(objectID))
}

// LockEntraLink implements authentication.EntraLinkLocker: it locks the identity link of userID inside tx.
func (e *EntraIdentities) LockEntraLink(ctx context.Context, tx pgx.Tx, userID, tenantID, objectID string) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM organization.external_identities
		WHERE provider_key = $1 AND external_subject = $2 AND user_id = $3::uuid AND enabled FOR SHARE`,
		EntraProviderKey(tenantID), strings.ToLower(objectID), userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// EntraSignInStore is the part of the Organization repository the hybrid match and the provisioning need.
type EntraSignInStore interface {
	LinkEntraBySourceAnchor(ctx context.Context, directoryProviderKey, tenantID, objectID, anchor, correlationID string) (string, error)
	ProvisionEntraEmployee(ctx context.Context, tenantID, objectID, displayName, email, correlationID string) (string, error)
}

// EntraSignIn implements authentication.EntraAnchorLinker and authentication.EntraProvisioner. The hybrid match
// uses the directory provider key the Entra tenant is bound to (ENTRA_LINK_DIRECTORY_PROVIDER_KEY).
type EntraSignIn struct {
	store                EntraSignInStore
	directoryProviderKey string
}

var (
	_ authentication.EntraAnchorLinker = (*EntraSignIn)(nil)
	_ authentication.EntraProvisioner  = (*EntraSignIn)(nil)
)

// NewEntraSignIn creates the contract. directoryProviderKey may be empty when only provisioning is used.
func NewEntraSignIn(store EntraSignInStore, directoryProviderKey string) *EntraSignIn {
	return &EntraSignIn{store: store, directoryProviderKey: directoryProviderKey}
}

// LinkBySourceAnchor implements authentication.EntraAnchorLinker.
func (e *EntraSignIn) LinkBySourceAnchor(ctx context.Context, tenantID, objectID, anchor, correlationID string) (string, error) {
	if e.directoryProviderKey == "" {
		return "", authentication.ErrAnchorNoMatch
	}
	id, err := e.store.LinkEntraBySourceAnchor(ctx, e.directoryProviderKey, strings.ToLower(tenantID), strings.ToLower(objectID), strings.ToLower(anchor), correlationID)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, application.ErrAnchorNoMatch):
		return "", authentication.ErrAnchorNoMatch
	case errors.Is(err, application.ErrAnchorAmbiguous):
		return "", authentication.ErrAnchorAmbiguous
	case errors.Is(err, application.ErrAnchorRefused):
		return "", authentication.ErrAnchorRefused
	default:
		return "", err
	}
}

// ProvisionEmployee implements authentication.EntraProvisioner.
func (e *EntraSignIn) ProvisionEmployee(ctx context.Context, tenantID, objectID, displayName, email, correlationID string) (string, error) {
	id, err := e.store.ProvisionEntraEmployee(ctx, strings.ToLower(tenantID), strings.ToLower(objectID), displayName, email, correlationID)
	var invalid *application.InvalidInputError
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, application.ErrProvisionEmailConflict):
		return "", authentication.ErrProvisionEmailConflict
	case errors.As(err, &invalid):
		return "", authentication.ErrProvisionRefused
	default:
		return "", err
	}
}
