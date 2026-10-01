package public

import (
	"context"
	"errors"
)

// DirectorySnapshot is one complete, vendor-neutral enumeration of a directory
// provider. A source returns a snapshot only when every user and group search
// finished; partial results are an error, never a snapshot, because absence
// from a snapshot marks objects as no longer observed.
//
// Distinguished names are resolved by the source: member and manager
// references are stable external IDs of objects in the same snapshot.
type DirectorySnapshot struct {
	Users  []DirectoryUser
	Groups []DirectoryGroup
}

// DirectoryUser is one directory account. Only directory-owned User fields are
// carried; see docs/integrations/ldap-ad.md for the source-of-truth table.
type DirectoryUser struct {
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
}

// DirectoryGroup is one directory group with its direct members.
type DirectoryGroup struct {
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

// ErrSyncAlreadyRunning is returned when another run for the provider is in progress.
var ErrSyncAlreadyRunning = errors.New("organization: directory sync already running")

// ErrSyncSafeguard is returned when a run was aborted because it would
// deactivate more users than the configured safeguard allows. It is
// permanent: retrying the same directory state gives the same result.
var ErrSyncSafeguard = errors.New("organization: directory sync aborted by deactivation safeguard")

// DirectorySyncJobType is the platform job type that runs one directory sync.
// Its payload is DirectorySyncJobPayload.
const DirectorySyncJobType = "organization.directory_sync"

// DirectorySyncJobPayload is the JSON payload of a DirectorySyncJobType job.
type DirectorySyncJobPayload struct {
	ProviderKey string      `json:"providerKey"`
	Trigger     SyncTrigger `json:"trigger"`
}

// DirectorySyncDedupeKey collapses scheduled and manual requests for one provider.
func DirectorySyncDedupeKey(providerKey string) string {
	return DirectorySyncJobType + ":" + providerKey
}
