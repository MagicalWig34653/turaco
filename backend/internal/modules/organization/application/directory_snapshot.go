package application

import "context"

// DirectorySnapshot is one complete, vendor-neutral enumeration of a directory
// provider. A source returns a snapshot only when every user and group search
// finished; partial results are an error, never a snapshot, because absence
// from a snapshot marks objects as no longer observed.
//
// Distinguished names are resolved by the source: member and manager
// references are stable external IDs of objects in the same snapshot.
type DirectorySnapshot struct {
	Users  []SnapshotUser
	Groups []SnapshotGroup
}

// SnapshotUser is one directory account. Only directory-owned User fields are
// carried; see docs/integrations/ldap-ad.md for the source-of-truth table.
type SnapshotUser struct {
	// ExternalID is the stable opaque identifier (AD objectGUID, LDAP
	// entryUUID) in canonical lowercase UUID form. Never a DN or username.
	ExternalID        string
	Username          string // sAMAccountName / uid
	DistinguishedName string // informational only; never used for matching
	DisplayName       string
	GivenName         *string
	FamilyName        *string
	Email             *string
	EmployeeNumber    *string
	// ManagerExternalID is nil when the account has no manager. When the
	// manager DN could not be resolved inside the snapshot, ManagerUnresolved
	// is true and ManagerExternalID is nil.
	ManagerExternalID *string
	ManagerUnresolved bool
	Enabled           bool
	// Invalid marks an account whose identity-relevant attributes (username,
	// email, employee number) are not valid text. The account is still
	// observed (it must not be swept as missing) and Enabled is still
	// authoritative, but its attribute values must not be applied: an
	// existing User keeps its stored values and a new account is skipped as a
	// conflict. Free-text display attributes are sanitized instead of
	// invalidating the entry. Organization also sets this itself when it finds
	// invalid identity-relevant text in the snapshot.
	Invalid bool
}

// SnapshotGroup is one directory group with its direct members.
type SnapshotGroup struct {
	ExternalID  string
	DisplayName string
	Description *string
	// Direct members resolved to external IDs of snapshot users/groups.
	MemberUserIDs  []string
	MemberGroupIDs []string
	// UnresolvedMembers counts direct members outside the snapshot, such as
	// foreign security principals or objects outside the configured base DNs.
	UnresolvedMembers int
}

// DirectorySource fetches snapshots from one configured directory provider.
// Implementations live in backend/internal/integrations and must not log or
// return attribute values in errors.
type DirectorySource interface {
	// ProviderKey identifies the provider, for example "ad". It is stored as
	// external_identities.provider_key and directory_groups.provider_key.
	ProviderKey() string
	Fetch(ctx context.Context) (DirectorySnapshot, error)
}

// SyncTrigger records why a run started.
type SyncTrigger string

const (
	SyncTriggerScheduled SyncTrigger = "scheduled"
	SyncTriggerManual    SyncTrigger = "manual"
)
