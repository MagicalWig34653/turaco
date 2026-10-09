package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// People administration (F14 slices A-A and A-A2, docs/product/f14-administration-design.md section 1): explicit
// domain operations on Users, Locations and Departments. Every operation takes expectedVersion, locks the row,
// authorizes inside the transaction and audits in the same transaction. The rules that decide who may touch whom
// (field ownership, dominance, last administrator, emergency accounts) live in the repository next to the row
// locks; this file validates input and names the errors.

// Errors of the people operations. The transport maps each to one API error code.
var (
	// ErrVersionConflict: expectedVersion is not the current version.
	ErrVersionConflict = errors.New("organization: version conflict")
	// ErrDirectoryUser: the operation does not apply to a directory-linked User (no local credential).
	ErrDirectoryUser = errors.New("organization: user is managed by the directory")
	// ErrEmergencyAccount: the emergency account has a CLI-only lifecycle.
	ErrEmergencyAccount = errors.New("organization: emergency accounts are managed with turaco-admin")
	// ErrLastAdministrator: the change would leave no active platform administrator.
	ErrLastAdministrator = errors.New("organization: last active platform administrator")
	// ErrSelfOperation: nobody deactivates or departs their own account.
	ErrSelfOperation = errors.New("organization: not allowed on your own account")
	// ErrTeamMembershipSelf: nobody changes their own Team membership (review rule R2).
	ErrTeamMembershipSelf = errors.New("organization: nobody changes their own team membership")
	// ErrDominanceRequired: review rule R1.
	ErrDominanceRequired = errors.New("organization: dominance required")
	// ErrDirectoryIdentityDisabled: the directory identity of the User is disabled, so the User stays inactive.
	ErrDirectoryIdentityDisabled = errors.New("organization: the directory identity is disabled")
	// ErrWrongState: the operation does not apply to the current status.
	ErrWrongState = errors.New("organization: the user is not in a state this operation applies to")
	// ErrExternalNeedsPermission: adding an external account to a Team needs organization.external_parties.manage.
	ErrExternalNeedsPermission = errors.New("organization: external accounts need organization.external_parties.manage")
	// ErrHierarchy: a move would create a cycle or exceed the maximum depth.
	ErrHierarchy = errors.New("organization: invalid hierarchy")
	// ErrTargetInactive: the referenced department, location or manager is not active.
	ErrTargetInactive = errors.New("organization: the referenced record is not active")
	// ErrNoGuards: an operation that needs the access guards ran on a repository without them (fail closed).
	ErrNoGuards = errors.New("organization: access guards are not configured")
)

// FieldDirectoryOwnedError names the directory-owned fields a request tried to change.
type FieldDirectoryOwnedError struct{ Fields []string }

func (e *FieldDirectoryOwnedError) Error() string {
	return "organization: directory-owned fields: " + strings.Join(e.Fields, ", ")
}

// ImpactError reports records that still point at a record being deactivated; the caller repeats the request with
// confirmImpact to proceed.
type ImpactError struct{ Counts map[string]int }

func (e *ImpactError) Error() string {
	return "organization: deactivation has impact; confirmation required"
}

// AccessGuards are the authorization rules of user operations (review rules R1 and R6), evaluated inside the
// operation's transaction. platform/authorization/roles.Guards implements it.
type AccessGuards interface {
	RequireDominance(ctx context.Context, tx pgx.Tx, actor audit.Actor, targetUserID string) error
	WouldLoseLastAdministrator(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
	HoldsAnyRole(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
	IsAdministrator(ctx context.Context, tx pgx.Tx, userID string) (bool, error)
}

// SessionRevoker ends the sessions of a User and the open credential tokens; implemented over
// platform/authentication so the module never touches platform tables.
type SessionRevoker interface {
	RevokeSessions(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, at time.Time) (int, error)
	RevokeCredentialTokens(ctx context.Context, tx pgx.Tx, userID, reason string, actor audit.Actor, correlationID string, at time.Time) (int, error)
}

// Credential links (review rules R1 and R7). Organization decides who may be invited or reset and writes its audit
// trail; the token itself is created by platform/authentication through CredentialIssuer, and the mail by
// CredentialMailer (wired in the composition root; links are built from EMAIL_BASE_URL only).
var (
	// ErrMailNotConfigured: a reset needs a working mail channel (never shown to the administrator).
	ErrMailNotConfigured = errors.New("organization: mail is not configured")
	// ErrBaseURLNotConfigured: links are built only from EMAIL_BASE_URL.
	ErrBaseURLNotConfigured = errors.New("organization: EMAIL_BASE_URL is not configured")
	// ErrMailFailed: the mail could not be handed to the relay; the token stays valid until it expires.
	ErrMailFailed = errors.New("organization: the mail could not be sent")
	// ErrNoEmail: the User has no primary email address.
	ErrNoEmail = errors.New("organization: the user has no primary email address")
)

// CredentialPurpose values.
const (
	CredentialInvitation = "invitation"
	CredentialReset      = "reset"
)

// CredentialIssuer is implemented by platform/authentication.LocalCredentials.
type CredentialIssuer interface {
	EnsureLocalCredential(ctx context.Context, tx pgx.Tx, userID string) (activated bool, err error)
	IssueToken(ctx context.Context, tx pgx.Tx, userID, purpose string, actor audit.Actor, correlationID string) (token string, expires time.Time, err error)
}

// CredentialMailer delivers credential links.
type CredentialMailer interface {
	// BaseURLConfigured says EMAIL_BASE_URL is set.
	BaseURLConfigured() bool
	// MailConfigured says the SMTP channel is configured.
	MailConfigured() bool
	// Link builds the link from the configured base URL (the token travels in the fragment).
	Link(token, purpose string) string
	// Send mails the link to the stored primary address.
	Send(ctx context.Context, to, displayName, purpose, link string) error
}

// CredentialLink is the result of an invitation or reset.
type CredentialLink struct {
	ExpiresAt time.Time
	// Mailed: the link was sent to the primary address.
	Mailed bool
	// Link is set only for the invitation of a never-activated account when no mail channel exists, and is shown
	// to the administrator once. It is never set for resets.
	Link string
}

// ReferenceCounter counts the records of another module that point at a Location (Buildings, Assets). The
// composition root wires counters from the owning modules' public contracts; Organization never reads their tables.
type ReferenceCounter interface {
	// Name is the key of the count in ImpactError.Counts, for example "buildings".
	Name() string
	CountLocationReferences(ctx context.Context, locationID string) (int, error)
}

// User extensions of the F14 model (the base fields are in read.go).
const (
	AccountKindEmployee = "employee"
	AccountKindExternal = "external"

	OriginDirectory = "directory"
	OriginLocal     = "local"
	OriginEmergency = "emergency"

	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusDeparted = "departed"
)

// OptString distinguishes an absent JSON field from null (clear) and from a value.
type OptString struct {
	Set   bool
	Value *string
}

func (o *OptString) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if string(raw) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// OptID is OptString for a reference.
type OptID = OptString

// NewUserInput creates a local User.
type NewUserInput struct {
	DisplayName    string
	GivenName      *string
	FamilyName     *string
	PrimaryEmail   *string
	EmployeeNumber *string
	DepartmentID   *string
	LocationID     *string
	// AccountKind is "employee" (default). "external" arrives with the External Party slice (A-G) and is refused.
	AccountKind string
}

// ProfileChange names the profile fields to change; ExpectedVersion is required.
type ProfileChange struct {
	ExpectedVersion int
	DisplayName     OptString
	GivenName       OptString
	FamilyName      OptString
	PrimaryEmail    OptString
	EmployeeNumber  OptString
}

func (p ProfileChange) fields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(p.DisplayName.Set, "displayName")
	add(p.GivenName.Set, "givenName")
	add(p.FamilyName.Set, "familyName")
	add(p.PrimaryEmail.Set, "primaryEmail")
	add(p.EmployeeNumber.Set, "employeeNumber")
	return out
}

// Status operations.
const (
	OpDeactivate    = "deactivate"
	OpReactivate    = "reactivate"
	OpMarkDeparted  = "mark_departed"
	maxReasonLength = 40
)

// Reason codes (closed lists, no free text in the audit trail).
var statusReasons = map[string][]string{
	OpDeactivate:   {"left_organization", "extended_leave", "security_concern", "duplicate_account", "no_longer_needed"},
	OpMarkDeparted: {"left_organization", "contract_ended", "retired"},
	OpReactivate:   {"returned", "mistake", "contract_renewed"},
}

// StatusReasons returns the allowed reason codes of a status operation.
func StatusReasons(op string) []string { return append([]string(nil), statusReasons[op]...) }

// Location kinds and limits.
const (
	LocationSite = "site"
	LocationArea = "area"
	// MaxLocationDepth counts the site: site, area, sub-area, sub-sub-area.
	MaxLocationDepth = 4
)

// NewLocationInput creates a Location. A site has no parent; an area needs one.
type NewLocationInput struct {
	Kind        string
	ParentID    *string
	Name        string
	Code        *string
	Description string
}

// LocationChange renames or redescribes a Location.
type LocationChange struct {
	ExpectedVersion int
	Name            *string
	Code            OptString
	Description     *string
}

// NewDepartmentInput creates a Department.
type NewDepartmentInput struct {
	Name     string
	Code     *string
	ParentID *string
}

// DepartmentChange renames a Department or changes its code.
type DepartmentChange struct {
	ExpectedVersion int
	Name            *string
	Code            OptString
}

// DirectoryOwned lists the profile fields the directory owns for directory-origin Users.
var DirectoryOwned = []string{"displayName", "givenName", "familyName", "primaryEmail", "employeeNumber", "manager"}

// PeopleStore is the persistence port of the people operations. Each method runs in one transaction with its audit
// event; the repository implements the rules of the design (ownership, dominance, last administrator).
type PeopleStore interface {
	CreateLocalUser(ctx context.Context, c Caller, in NewUserInput) (User, error)
	UpdateProfile(ctx context.Context, c Caller, id string, in ProfileChange) (User, error)
	SetDepartment(ctx context.Context, c Caller, id string, version int, departmentID *string) (User, error)
	SetPrimaryLocation(ctx context.Context, c Caller, id string, version int, locationID *string) (User, error)
	SetManager(ctx context.Context, c Caller, id string, version int, managerID *string) (User, error)
	ChangeStatus(ctx context.Context, c Caller, id string, version int, op, reason string) (User, error)

	CreateLocation(ctx context.Context, c Caller, in NewLocationInput) (Location, error)
	UpdateLocation(ctx context.Context, c Caller, id string, in LocationChange) (Location, error)
	MoveLocation(ctx context.Context, c Caller, id string, version int, parentID *string) (Location, error)
	SetLocationActive(ctx context.Context, c Caller, id string, version int, active, confirmImpact bool) (Location, error)

	CreateDepartment(ctx context.Context, c Caller, in NewDepartmentInput) (Department, error)
	UpdateDepartment(ctx context.Context, c Caller, id string, in DepartmentChange) (Department, error)
	MoveDepartment(ctx context.Context, c Caller, id string, version int, parentID *string) (Department, error)
	SetDepartmentActive(ctx context.Context, c Caller, id string, version int, active, confirmImpact bool) (Department, error)

	IssueCredentialLink(ctx context.Context, c Caller, id, purpose string) (CredentialLink, error)

	SetTeamDescription(ctx context.Context, c Caller, id string, version int, description string) (Team, error)
	SetMemberRole(ctx context.Context, c Caller, teamID, userID, role string) (TeamMember, error)
}

// People validates and performs the People operations.
type People struct{ store PeopleStore }

func NewPeople(store PeopleStore) *People { return &People{store: store} }

func requireVersion(v int) error {
	if v < 1 {
		return invalid("expectedVersion is required")
	}
	return nil
}

func optText(field string, in *string, max int) (*string, error) {
	if in == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*in)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return nil, invalid("%s must be at most %d characters without control or invisible formatting characters", field, max)
	}
	return &s, nil
}

func optOpt(field string, in OptString, max int) (OptString, error) {
	if !in.Set {
		return in, nil
	}
	v, err := optText(field, in.Value, max)
	return OptString{Set: true, Value: v}, err
}

// cleanEmail parses with net/mail and rejects anything that is not a bare address; CR and LF never pass.
func cleanEmail(in *string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*in)
	if s == "" {
		return nil, nil
	}
	if len(s) > 254 || strings.ContainsAny(s, "\r\n\x00") || safetext.ContainsUnsafe(s, false) {
		return nil, invalid("primaryEmail is not a valid address")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || strings.Count(s, "@") != 1 {
		return nil, invalid("primaryEmail is not a valid address")
	}
	return &s, nil
}

const (
	maxPersonNameLength = 200
	maxCodeLength       = 40
	maxOrgNameLength    = 200
	maxLocationDescr    = 500
	maxTeamDescr        = 500
)

func (p *People) CreateUser(ctx context.Context, c Caller, in NewUserInput) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	name, err := cleanText("displayName", in.DisplayName, maxPersonNameLength)
	if err != nil {
		return User{}, err
	}
	in.DisplayName = name
	if in.AccountKind == "" {
		in.AccountKind = AccountKindEmployee
	}
	if in.AccountKind != AccountKindEmployee {
		return User{}, invalid("accountKind must be employee; external accounts are not available yet")
	}
	if in.GivenName, err = optText("givenName", in.GivenName, maxPersonNameLength); err != nil {
		return User{}, err
	}
	if in.FamilyName, err = optText("familyName", in.FamilyName, maxPersonNameLength); err != nil {
		return User{}, err
	}
	if in.EmployeeNumber, err = optText("employeeNumber", in.EmployeeNumber, maxCodeLength); err != nil {
		return User{}, err
	}
	if in.PrimaryEmail, err = cleanEmail(in.PrimaryEmail); err != nil {
		return User{}, err
	}
	return p.store.CreateLocalUser(ctx, c, in)
}

func (p *People) UpdateProfile(ctx context.Context, c Caller, id string, in ProfileChange) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if err := requireVersion(in.ExpectedVersion); err != nil {
		return User{}, err
	}
	var err error
	if in.DisplayName.Set {
		if in.DisplayName.Value == nil {
			return User{}, invalid("displayName cannot be cleared")
		}
		v, err := cleanText("displayName", *in.DisplayName.Value, maxPersonNameLength)
		if err != nil {
			return User{}, err
		}
		in.DisplayName.Value = &v
	}
	if in.GivenName, err = optOpt("givenName", in.GivenName, maxPersonNameLength); err != nil {
		return User{}, err
	}
	if in.FamilyName, err = optOpt("familyName", in.FamilyName, maxPersonNameLength); err != nil {
		return User{}, err
	}
	if in.EmployeeNumber, err = optOpt("employeeNumber", in.EmployeeNumber, maxCodeLength); err != nil {
		return User{}, err
	}
	if in.PrimaryEmail.Set {
		v, err := cleanEmail(in.PrimaryEmail.Value)
		if err != nil {
			return User{}, err
		}
		in.PrimaryEmail.Value = v
	}
	if len(in.fields()) == 0 {
		return User{}, invalid("at least one profile field is required")
	}
	return p.store.UpdateProfile(ctx, c, id, in)
}

func (p *People) SetDepartment(ctx context.Context, c Caller, id string, version int, departmentID *string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	return p.store.SetDepartment(ctx, c, id, version, departmentID)
}

func (p *People) SetPrimaryLocation(ctx context.Context, c Caller, id string, version int, locationID *string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	return p.store.SetPrimaryLocation(ctx, c, id, version, locationID)
}

func (p *People) SetManager(ctx context.Context, c Caller, id string, version int, managerID *string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	return p.store.SetManager(ctx, c, id, version, managerID)
}

// ChangeStatus runs deactivate, reactivate or mark_departed. reason is a code of StatusReasons(op).
func (p *People) ChangeStatus(ctx context.Context, c Caller, id string, version int, op, reason string) (User, error) {
	if err := c.validate(); err != nil {
		return User{}, err
	}
	if err := requireVersion(version); err != nil {
		return User{}, err
	}
	allowed, ok := statusReasons[op]
	if !ok {
		return User{}, invalid("unknown operation")
	}
	found := false
	for _, r := range allowed {
		found = found || r == reason
	}
	if !found || len(reason) > maxReasonLength {
		return User{}, invalid("reason must be one of: %s", strings.Join(allowed, ", "))
	}
	return p.store.ChangeStatus(ctx, c, id, version, op, reason)
}

func (p *People) CreateLocation(ctx context.Context, c Caller, in NewLocationInput) (Location, error) {
	if err := c.validate(); err != nil {
		return Location{}, err
	}
	name, err := cleanText("name", in.Name, maxOrgNameLength)
	if err != nil {
		return Location{}, err
	}
	in.Name = name
	if in.Kind == "" {
		in.Kind = LocationSite
		if in.ParentID != nil {
			in.Kind = LocationArea
		}
	}
	if in.Kind != LocationSite && in.Kind != LocationArea {
		return Location{}, invalid("kind must be site or area")
	}
	if (in.Kind == LocationSite) != (in.ParentID == nil) {
		return Location{}, invalid("a site has no parent and an area needs a parent")
	}
	if in.Code, err = optText("code", in.Code, maxCodeLength); err != nil {
		return Location{}, err
	}
	d, err := plainDescription(in.Description, maxLocationDescr)
	if err != nil {
		return Location{}, err
	}
	in.Description = d
	return p.store.CreateLocation(ctx, c, in)
}

func plainDescription(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return "", invalid("description must be at most %d characters", max)
	}
	return s, nil
}

func (p *People) UpdateLocation(ctx context.Context, c Caller, id string, in LocationChange) (Location, error) {
	if err := c.validate(); err != nil {
		return Location{}, err
	}
	if err := requireVersion(in.ExpectedVersion); err != nil {
		return Location{}, err
	}
	var err error
	if in.Name != nil {
		n, err := cleanText("name", *in.Name, maxOrgNameLength)
		if err != nil {
			return Location{}, err
		}
		in.Name = &n
	}
	if in.Code, err = optOpt("code", in.Code, maxCodeLength); err != nil {
		return Location{}, err
	}
	if in.Description != nil {
		d, err := plainDescription(*in.Description, maxLocationDescr)
		if err != nil {
			return Location{}, err
		}
		in.Description = &d
	}
	if in.Name == nil && !in.Code.Set && in.Description == nil {
		return Location{}, invalid("name, code or description is required")
	}
	return p.store.UpdateLocation(ctx, c, id, in)
}

func (p *People) MoveLocation(ctx context.Context, c Caller, id string, version int, parentID *string) (Location, error) {
	if err := c.validate(); err != nil {
		return Location{}, err
	}
	if err := requireVersion(version); err != nil {
		return Location{}, err
	}
	return p.store.MoveLocation(ctx, c, id, version, parentID)
}

func (p *People) SetLocationActive(ctx context.Context, c Caller, id string, version int, active, confirmImpact bool) (Location, error) {
	if err := c.validate(); err != nil {
		return Location{}, err
	}
	if err := requireVersion(version); err != nil {
		return Location{}, err
	}
	return p.store.SetLocationActive(ctx, c, id, version, active, confirmImpact)
}

func (p *People) CreateDepartment(ctx context.Context, c Caller, in NewDepartmentInput) (Department, error) {
	if err := c.validate(); err != nil {
		return Department{}, err
	}
	name, err := cleanText("name", in.Name, maxOrgNameLength)
	if err != nil {
		return Department{}, err
	}
	in.Name = name
	if in.Code, err = optText("code", in.Code, maxCodeLength); err != nil {
		return Department{}, err
	}
	return p.store.CreateDepartment(ctx, c, in)
}

func (p *People) UpdateDepartment(ctx context.Context, c Caller, id string, in DepartmentChange) (Department, error) {
	if err := c.validate(); err != nil {
		return Department{}, err
	}
	if err := requireVersion(in.ExpectedVersion); err != nil {
		return Department{}, err
	}
	var err error
	if in.Name != nil {
		n, err := cleanText("name", *in.Name, maxOrgNameLength)
		if err != nil {
			return Department{}, err
		}
		in.Name = &n
	}
	if in.Code, err = optOpt("code", in.Code, maxCodeLength); err != nil {
		return Department{}, err
	}
	if in.Name == nil && !in.Code.Set {
		return Department{}, invalid("name or code is required")
	}
	return p.store.UpdateDepartment(ctx, c, id, in)
}

func (p *People) MoveDepartment(ctx context.Context, c Caller, id string, version int, parentID *string) (Department, error) {
	if err := c.validate(); err != nil {
		return Department{}, err
	}
	if err := requireVersion(version); err != nil {
		return Department{}, err
	}
	return p.store.MoveDepartment(ctx, c, id, version, parentID)
}

func (p *People) SetDepartmentActive(ctx context.Context, c Caller, id string, version int, active, confirmImpact bool) (Department, error) {
	if err := c.validate(); err != nil {
		return Department{}, err
	}
	if err := requireVersion(version); err != nil {
		return Department{}, err
	}
	return p.store.SetDepartmentActive(ctx, c, id, version, active, confirmImpact)
}

func (t *Teams) SetDescription(ctx context.Context, c Caller, id string, version int, description string) (Team, error) {
	if err := c.validate(); err != nil {
		return Team{}, err
	}
	if err := requireVersion(version); err != nil {
		return Team{}, err
	}
	d, err := plainDescription(description, maxTeamDescr)
	if err != nil {
		return Team{}, err
	}
	people, ok := t.store.(PeopleStore)
	if !ok {
		return Team{}, errors.New("organization: store does not support team descriptions")
	}
	return people.SetTeamDescription(ctx, c, id, version, d)
}

// Team member roles.
const (
	TeamRoleLead   = "lead"
	TeamRoleMember = "member"
)

func (t *Teams) SetMemberRole(ctx context.Context, c Caller, teamID, userID, role string) (TeamMember, error) {
	if err := c.validate(); err != nil {
		return TeamMember{}, err
	}
	if role != TeamRoleLead && role != TeamRoleMember {
		return TeamMember{}, invalid("role must be lead or member")
	}
	people, ok := t.store.(PeopleStore)
	if !ok {
		return TeamMember{}, errors.New("organization: store does not support team member roles")
	}
	return people.SetMemberRole(ctx, c, teamID, userID, role)
}

// SendInvitation issues the invitation of a never-activated local account.
func (p *People) SendInvitation(ctx context.Context, c Caller, id string) (CredentialLink, error) {
	if err := c.validate(); err != nil {
		return CredentialLink{}, err
	}
	return p.store.IssueCredentialLink(ctx, c, id, CredentialInvitation)
}

// ResetPassword issues a reset for an activated local account; it is only ever mailed.
func (p *People) ResetPassword(ctx context.Context, c Caller, id string) (CredentialLink, error) {
	if err := c.validate(); err != nil {
		return CredentialLink{}, err
	}
	return p.store.IssueCredentialLink(ctx, c, id, CredentialReset)
}
