package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Team write operations. Teams and their memberships are platform-owned:
// directory synchronization never writes them. Every mutation is audited in
// the same transaction as the change (organization.team.* actions).

const (
	maxTeamNameLength = 100
)

var (
	// ErrConflict means the operation conflicts with current state (duplicate
	// active team name, user already a member, team already in that state).
	ErrConflict = errors.New("organization: conflict")
	// ErrUserNotActive means a membership may only be added for an active User.
	ErrUserNotActive = errors.New("organization: user is not active")
	// ErrTeamInactive means members can only be added to an active Team.
	ErrTeamInactive = errors.New("organization: team is not active")
)

// InvalidInputError carries a user-safe validation message.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return "organization: invalid input: " + e.Message }

func invalid(format string, args ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, args...)}
}

// Caller identifies who performs a mutation and the request it belongs to.
type Caller struct {
	Actor         audit.Actor
	CorrelationID string
	// ExternalPartiesManage says the actor holds organization.external_parties.manage (adding an external
	// account to a Team needs it, review rule R2). The transport fills it from the principal.
	ExternalPartiesManage bool
	// PlatformAdmin says the actor holds platform.admin. Only an administrator may be shown an invitation link
	// when no mail channel exists (ADR-0034); the transport fills it from the principal.
	PlatformAdmin bool
	// Has reports whether the actor holds a permission. Operations that check a permission of their own (batch
	// previews and applies re-authorize per batch kind) fail closed when it is nil.
	Has func(permission string) bool
}

func (c Caller) can(permission string) bool { return c.Has != nil && c.Has(permission) }

func (c Caller) validate() error {
	if err := c.Actor.Validate(); err != nil {
		return err
	}
	if c.CorrelationID == "" {
		return errors.New("organization: correlation id is required")
	}
	return nil
}

// TeamStore is the persistence port of Team mutations. Each method runs in
// one transaction together with its audit event.
type TeamStore interface {
	CreateTeam(ctx context.Context, c Caller, name string) (Team, error)
	RenameTeam(ctx context.Context, c Caller, id, name string) (Team, error)
	// SetTeamActive activates or deactivates; a no-op change is ErrConflict.
	SetTeamActive(ctx context.Context, c Caller, id string, active bool) (Team, error)
	// AddTeamMember adds a current membership with source "platform".
	AddTeamMember(ctx context.Context, c Caller, teamID, userID string, role *string) (TeamMember, error)
	// RemoveTeamMember ends the current membership of userID.
	RemoveTeamMember(ctx context.Context, c Caller, teamID, userID string) error
}

// Teams validates and performs Team mutations.
type Teams struct{ store TeamStore }

func NewTeams(store TeamStore) *Teams { return &Teams{store: store} }

func cleanText(field, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return "", invalid("%s must be 1-%d characters", field, max)
	}
	if safetext.ContainsUnsafe(s, false) {
		return "", invalid("%s must not contain control or invisible formatting characters", field)
	}
	return s, nil
}

func (t *Teams) Create(ctx context.Context, c Caller, name string) (Team, error) {
	if err := c.validate(); err != nil {
		return Team{}, err
	}
	name, err := cleanText("name", name, maxTeamNameLength)
	if err != nil {
		return Team{}, err
	}
	return t.store.CreateTeam(ctx, c, name)
}

func (t *Teams) Rename(ctx context.Context, c Caller, id, name string) (Team, error) {
	if err := c.validate(); err != nil {
		return Team{}, err
	}
	name, err := cleanText("name", name, maxTeamNameLength)
	if err != nil {
		return Team{}, err
	}
	return t.store.RenameTeam(ctx, c, id, name)
}

func (t *Teams) Activate(ctx context.Context, c Caller, id string) (Team, error) {
	if err := c.validate(); err != nil {
		return Team{}, err
	}
	return t.store.SetTeamActive(ctx, c, id, true)
}

func (t *Teams) Deactivate(ctx context.Context, c Caller, id string) (Team, error) {
	if err := c.validate(); err != nil {
		return Team{}, err
	}
	return t.store.SetTeamActive(ctx, c, id, false)
}

func (t *Teams) AddMember(ctx context.Context, c Caller, teamID, userID string, role *string) (TeamMember, error) {
	if err := c.validate(); err != nil {
		return TeamMember{}, err
	}
	if role != nil && *role != TeamRoleLead && *role != TeamRoleMember {
		return TeamMember{}, invalid("role must be lead or member")
	}
	return t.store.AddTeamMember(ctx, c, teamID, userID, role)
}

func (t *Teams) RemoveMember(ctx context.Context, c Caller, teamID, userID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	return t.store.RemoveTeamMember(ctx, c, teamID, userID)
}
