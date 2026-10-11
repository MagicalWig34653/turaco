package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// External Parties (F14 slice A-G, ADR-0034 point 4, design section 6). An External Party is the company or person
// group external accounts belong to. It names an internal sponsor who answers for the accounts and is notified
// before access ends. The record carries no address or contract data.

// External Party kinds.
const (
	PartyVendor     = "vendor"
	PartyContractor = "contractor"
	PartyPartner    = "partner"

	maxPartyNameLength  = 200
	maxPartyNotesLength = 500
)

var partyKinds = []string{PartyVendor, PartyContractor, PartyPartner}

// PartyKinds returns the allowed External Party kinds.
func PartyKinds() []string { return slices.Clone(partyKinds) }

// Errors of the External Party operations.
var (
	// ErrSponsorInvalid: the sponsor must be an active internal employee account.
	ErrSponsorInvalid = errors.New("organization: the sponsor must be an active employee")
	// ErrPartyInactive: new accounts and extensions need an active External Party.
	ErrPartyInactive = errors.New("organization: the external party is not active")
	// ErrPartyNameTaken: party names are unique.
	ErrPartyNameTaken = errors.New("organization: an external party with this name exists")
)

// ExternalParty is the company or person group of external accounts.
type ExternalParty struct {
	ID            string
	Kind          string
	Name          string
	SponsorUserID string
	// SponsorName is filled by reads only.
	SponsorName string
	Active      bool
	Notes       string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// AccountCount is the number of external accounts of the party; filled by reads.
	AccountCount int
}

// PartyAccount is an external account as shown with its party.
type PartyAccount struct {
	UserID          string
	DisplayName     string
	Status          string
	AccessExpiresAt time.Time
}

// NewExternalPartyInput creates an External Party.
type NewExternalPartyInput struct {
	Kind          string
	Name          string
	SponsorUserID string
	Notes         string
}

// ExternalPartyChange changes the named fields; ExpectedVersion is required.
type ExternalPartyChange struct {
	ExpectedVersion int
	Kind            *string
	Name            *string
	SponsorUserID   *string
	Notes           *string
	Active          *bool
}

// ExternalPartyStore is the storage port of the External Party operations. Every write locks the row, authorizes
// inside the transaction and audits in the same transaction.
type ExternalPartyStore interface {
	ListExternalParties(ctx context.Context) ([]ExternalParty, error)
	GetExternalParty(ctx context.Context, id string) (ExternalParty, []PartyAccount, error)
	CreateExternalParty(ctx context.Context, c Caller, in NewExternalPartyInput) (ExternalParty, error)
	UpdateExternalParty(ctx context.Context, c Caller, id string, in ExternalPartyChange) (ExternalParty, error)
	// ListSponsoredParties returns the External Parties whose sponsor is sponsorUserID, with their accounts. The
	// filter is part of the query: nobody sees a party they do not sponsor.
	ListSponsoredParties(ctx context.Context, sponsorUserID string) ([]SponsoredParty, error)
}

// SponsoredAccount is an external account as its sponsor sees it: no email address, no department, nothing but what
// the sponsor needs to renew access.
type SponsoredAccount struct {
	UserID          string
	DisplayName     string
	Status          string
	AccessExpiresAt time.Time
	// Version is the optimistic-lock version the extend-access operation expects.
	Version int
}

// SponsoredParty is an External Party with its accounts, as shown on the sponsor's own page.
type SponsoredParty struct {
	ID       string
	Kind     string
	Name     string
	Active   bool
	Accounts []SponsoredAccount
}

// ExternalParties validates and performs External Party operations. All of them need
// organization.external_parties.manage (checked here from the caller and by the route).
type ExternalParties struct{ store ExternalPartyStore }

func NewExternalParties(store ExternalPartyStore) *ExternalParties {
	return &ExternalParties{store: store}
}

func (e *ExternalParties) List(ctx context.Context, c Caller) ([]ExternalParty, error) {
	if !c.ExternalPartiesManage {
		return nil, ErrForbidden
	}
	return e.store.ListExternalParties(ctx)
}

func (e *ExternalParties) Get(ctx context.Context, c Caller, id string) (ExternalParty, []PartyAccount, error) {
	if !c.ExternalPartiesManage {
		return ExternalParty{}, nil, ErrForbidden
	}
	return e.store.GetExternalParty(ctx, id)
}

// ListSponsored returns the External Parties the caller sponsors. It needs no permission (being the sponsor is the
// authority, as for extend-access); an active employee session is required and the store filters by the caller's id,
// so a caller who sponsors nothing gets an empty list and learns nothing about other parties.
func (e *ExternalParties) ListSponsored(ctx context.Context, c Caller) ([]SponsoredParty, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.Actor.UserID == "" || !validUUID(c.Actor.UserID) {
		return nil, ErrForbidden
	}
	return e.store.ListSponsoredParties(ctx, strings.ToLower(c.Actor.UserID))
}

func cleanPartyKind(kind string) (string, error) {
	if !slices.Contains(partyKinds, kind) {
		return "", invalid("kind must be one of: %s", strings.Join(partyKinds, ", "))
	}
	return kind, nil
}

func cleanPartyNotes(notes string) (string, error) {
	notes = strings.TrimSpace(notes)
	if utf8.RuneCountInString(notes) > maxPartyNotesLength || !utf8.ValidString(notes) || safetext.ContainsUnsafe(notes, true) {
		return "", invalid("notes must be at most %d characters without control characters", maxPartyNotesLength)
	}
	return notes, nil
}

func (e *ExternalParties) Create(ctx context.Context, c Caller, in NewExternalPartyInput) (ExternalParty, error) {
	if err := c.validate(); err != nil {
		return ExternalParty{}, err
	}
	if !c.ExternalPartiesManage {
		return ExternalParty{}, ErrForbidden
	}
	var err error
	if in.Kind, err = cleanPartyKind(in.Kind); err != nil {
		return ExternalParty{}, err
	}
	if in.Name, err = cleanText("name", in.Name, maxPartyNameLength); err != nil {
		return ExternalParty{}, err
	}
	in.SponsorUserID = strings.ToLower(in.SponsorUserID)
	if !validUUID(in.SponsorUserID) {
		return ExternalParty{}, invalid("sponsorUserId must be a user id")
	}
	if in.Notes, err = cleanPartyNotes(in.Notes); err != nil {
		return ExternalParty{}, err
	}
	return e.store.CreateExternalParty(ctx, c, in)
}

func (e *ExternalParties) Update(ctx context.Context, c Caller, id string, in ExternalPartyChange) (ExternalParty, error) {
	if err := c.validate(); err != nil {
		return ExternalParty{}, err
	}
	if !c.ExternalPartiesManage {
		return ExternalParty{}, ErrForbidden
	}
	if err := requireVersion(in.ExpectedVersion); err != nil {
		return ExternalParty{}, err
	}
	if in.Kind != nil {
		k, err := cleanPartyKind(*in.Kind)
		if err != nil {
			return ExternalParty{}, err
		}
		in.Kind = &k
	}
	if in.Name != nil {
		n, err := cleanText("name", *in.Name, maxPartyNameLength)
		if err != nil {
			return ExternalParty{}, err
		}
		in.Name = &n
	}
	if in.SponsorUserID != nil {
		s := strings.ToLower(*in.SponsorUserID)
		if !validUUID(s) {
			return ExternalParty{}, invalid("sponsorUserId must be a user id")
		}
		in.SponsorUserID = &s
	}
	if in.Notes != nil {
		n, err := cleanPartyNotes(*in.Notes)
		if err != nil {
			return ExternalParty{}, err
		}
		in.Notes = &n
	}
	if in.Kind == nil && in.Name == nil && in.SponsorUserID == nil && in.Notes == nil && in.Active == nil {
		return ExternalParty{}, invalid("at least one field is required")
	}
	return e.store.UpdateExternalParty(ctx, c, id, in)
}

// Access expiry (ADR-0034 point 5): the job that deactivates external accounts past their end of access and the
// reminders to the sponsor.
const (
	ExpireAccessJobType    = "organization.expire_external_access"
	ExpireAccessJobTimeout = time.Minute
	ExpireAccessInterval   = 5 * time.Minute

	// NotificationAccessExpiring is the category of the reminder to the sponsor.
	NotificationAccessExpiring = "organization.access_expiring"

	// ExpireAccessBatch bounds one run of the job; the next tick continues.
	ExpireAccessBatch = 200
)

// AccessReminderLeadDays are the days before the end of access on which the sponsor is reminded.
var AccessReminderLeadDays = []int{14, 3}

// ExpiryStore is the storage port of the expiry job.
type ExpiryStore interface {
	// ExpireAccess deactivates active external accounts whose access ended at or before now, revokes their sessions
	// and credential tokens, audits as actor "system:access-expiry" and publishes AccessExpired. It processes at
	// most limit accounts, is idempotent and returns the number of accounts expired.
	ExpireAccess(ctx context.Context, now time.Time, limit int) (int, error)
	// RemindSponsors notifies the sponsor of every active external account whose access ends within a lead time. One
	// notification per sponsor, account, lead time and end date (a durable dedupe key). It returns the number of
	// notifications created.
	RemindSponsors(ctx context.Context, now time.Time, leadDays []int, limit int) (int, error)
}

// NewExpireHandler is the handler of ExpireAccessJobType: it deactivates external accounts past their end of access
// (the audit and cleanup path; the request path already treats them as inactive) and reminds sponsors. A backlog
// larger than one batch is worked off by the following runs. Both parts run even when the first fails, and the
// errors are joined so the job is retried.
func NewExpireHandler(store ExpiryStore, now func() time.Time) jobs.Handler {
	return func(ctx context.Context, _ jobs.Job) error {
		at := now().UTC()
		_, expireErr := store.ExpireAccess(ctx, at, ExpireAccessBatch)
		_, remindErr := store.RemindSponsors(ctx, at, AccessReminderLeadDays, ExpireAccessBatch)
		return errors.Join(expireErr, remindErr)
	}
}
