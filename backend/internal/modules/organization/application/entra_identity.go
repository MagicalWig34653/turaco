package application

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
)

// Administrator linking of Microsoft Entra identities (ADR-0035 point 3c, F15 slice E-B). An Entra identity is an
// External Identity with provider key `entra:<tenant id>` and the object id as subject. Linking gives the person
// who controls that Entra account access to the User, so it is takeover-capable like directory linking: platform
// administrators only, never on oneself, dominance rule R1, the account must be active, and a local credential is
// replaced atomically (ADR-0034 R5). Turaco never searches Entra by name or email; the administrator enters the
// object id copied from the Entra portal.

// Errors of the Entra link operations. The transport maps each to one API error code.
var (
	// ErrEntraNotConfigured: Entra sign-in is not configured on this installation, so there is no tenant to link to.
	ErrEntraNotConfigured = errors.New("organization: Entra sign-in is not configured")
	// ErrEntraTenantNotAllowed: the tenant is not one of the tenants Turaco accepts for sign-in.
	ErrEntraTenantNotAllowed = errors.New("organization: the tenant is not allowed for Entra sign-in")
	// ErrEntraIdentityTaken: the Entra identity is already linked to a User.
	ErrEntraIdentityTaken = errors.New("organization: the Entra identity is already linked")
	// ErrEntraTenantAlreadyUsed: the User already has an Entra identity of this tenant.
	ErrEntraTenantAlreadyUsed = errors.New("organization: the user already has an Entra identity of this tenant")

	// Sign-in time operations (the hybrid match and the provisioning); organization/public maps them to the
	// authentication sentinels.

	// ErrAnchorNoMatch: no directory identity has the source anchor.
	ErrAnchorNoMatch = errors.New("organization: no user matches the source anchor")
	// ErrAnchorRefused: the matched User cannot be linked (inactive, not a directory-owned employee, identity taken).
	ErrAnchorRefused = errors.New("organization: the matched user cannot be linked")
	// ErrAnchorAmbiguous: more than one User matches the source anchor.
	ErrAnchorAmbiguous = errors.New("organization: the source anchor matches more than one user")
	// ErrProvisionEmailConflict: a User already has the primary email address; provisioning never links by email.
	ErrProvisionEmailConflict = errors.New("organization: a user already has this email address")
)

// EntraLinkResult is what a link or unlink returns.
type EntraLinkResult struct {
	User User
	// CredentialDeleted is true when linking replaced a local credential (R5).
	CredentialDeleted bool
	// NoticeSent is true when the target was told about the change by email.
	NoticeSent bool
}

// EntraStore is the persistence port of the Entra link operations. Each method runs in one transaction with its
// audit event and authorizes (platform administrator, not oneself, dominance, state) inside it.
type EntraStore interface {
	LinkEntraIdentity(ctx context.Context, c Caller, userID string, version int, tenantID, objectID string) (EntraLinkResult, error)
	UnlinkEntraIdentity(ctx context.Context, c Caller, userID, identityID string) (EntraLinkResult, error)
}

// IdentityNotifier tells the target of a link or unlink about it by email. It is wired only when mail is configured.
type IdentityNotifier interface {
	// NotifyIdentityChange sends the notice to the stored primary address; event is "linked" or "unlinked".
	NotifyIdentityChange(ctx context.Context, to, displayName, event string) error
}

// EntraLinking validates and performs the Entra link operations.
type EntraLinking struct {
	store    EntraStore
	tenants  []string
	notifier IdentityNotifier
}

// NewEntraLinking creates the service. tenants are the tenant ids accepted for sign-in (ENTRA_ALLOWED_TENANT_IDS,
// empty when Entra sign-in is not configured).
func NewEntraLinking(store EntraStore, tenants []string, notifier IdentityNotifier) *EntraLinking {
	lower := make([]string, 0, len(tenants))
	for _, t := range tenants {
		lower = append(lower, strings.ToLower(t))
	}
	return &EntraLinking{store: store, tenants: lower, notifier: notifier}
}

// Configured reports whether Entra sign-in is configured (otherwise linking is refused).
func (e *EntraLinking) Configured() bool { return len(e.tenants) > 0 }

// Tenants returns the accepted tenant ids.
func (e *EntraLinking) Tenants() []string { return slices.Clone(e.tenants) }

var entraGUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const consumerTenantGUID = "9188040d-6c67-4c5b-b112-36a304b66dad"

// Link links the Entra identity (tenant id + object id) to the User.
func (e *EntraLinking) Link(ctx context.Context, c Caller, userID string, version int, tenantID, objectID string) (EntraLinkResult, error) {
	if err := c.validate(); err != nil {
		return EntraLinkResult{}, err
	}
	if !e.Configured() {
		return EntraLinkResult{}, ErrEntraNotConfigured
	}
	if err := requireVersion(version); err != nil {
		return EntraLinkResult{}, err
	}
	tenantID, objectID = strings.ToLower(strings.TrimSpace(tenantID)), strings.ToLower(strings.TrimSpace(objectID))
	if !entraGUID.MatchString(tenantID) || !entraGUID.MatchString(objectID) {
		return EntraLinkResult{}, invalid("tenantId and objectId must be GUIDs")
	}
	if tenantID == consumerTenantGUID || !slices.Contains(e.tenants, tenantID) {
		return EntraLinkResult{}, ErrEntraTenantNotAllowed
	}
	res, err := e.store.LinkEntraIdentity(ctx, c, userID, version, tenantID, objectID)
	if err != nil {
		return EntraLinkResult{}, err
	}
	res.NoticeSent = e.notify(ctx, res.User, "linked")
	return res, nil
}

// Unlink removes an Entra identity of the User and ends the User's Entra sessions.
func (e *EntraLinking) Unlink(ctx context.Context, c Caller, userID, identityID string) (EntraLinkResult, error) {
	if err := c.validate(); err != nil {
		return EntraLinkResult{}, err
	}
	res, err := e.store.UnlinkEntraIdentity(ctx, c, userID, identityID)
	if err != nil {
		return EntraLinkResult{}, err
	}
	res.NoticeSent = e.notify(ctx, res.User, "unlinked")
	return res, nil
}

// notify is best effort: the change is committed and audited whether or not the mail leaves.
func (e *EntraLinking) notify(ctx context.Context, u User, event string) bool {
	if e.notifier == nil || u.PrimaryEmail == nil || *u.PrimaryEmail == "" {
		return false
	}
	return e.notifier.NotifyIdentityChange(ctx, *u.PrimaryEmail, u.DisplayName, event) == nil
}

// ValidateProvisionProfile validates the name and email of an Entra token before a User is created from them. Both
// are required: the email is what the collision check runs against.
func ValidateProvisionProfile(displayName, email string) (name string, address string, err error) {
	if name, err = cleanText("displayName", displayName, maxPersonNameLength); err != nil {
		return "", "", err
	}
	p, err := cleanEmail(&email)
	if err != nil {
		return "", "", err
	}
	if p == nil {
		return "", "", invalid("primaryEmail is required")
	}
	return name, *p, nil
}
