package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

type recordingStore struct {
	TeamStore
	name string
	role *string
	n    int
}

func (s *recordingStore) CreateTeam(_ context.Context, _ Caller, name string) (Team, error) {
	s.n++
	s.name = name
	return Team{Name: name}, nil
}

func (s *recordingStore) AddTeamMember(_ context.Context, _ Caller, _, _ string, role *string) (TeamMember, error) {
	s.n++
	s.role = role
	return TeamMember{}, nil
}

func goodCaller() Caller { return Caller{Actor: audit.UserActor("u"), CorrelationID: "c"} }

func TestTeamNameValidation(t *testing.T) {
	for name, in := range map[string]string{
		"empty": "", "blank": "   ", "control": "a\x00b", "newline": "a\nb",
		"too long": strings.Repeat("x", 101), "invalid utf8": "a\xffb", "rtl override": "Help\u202Edesk",
	} {
		s := &recordingStore{}
		_, err := NewTeams(s).Create(context.Background(), goodCaller(), in)
		var inv *InvalidInputError
		if !errors.As(err, &inv) || s.n != 0 {
			t.Errorf("%s: err=%v store calls=%d, want InvalidInputError without a store call", name, err, s.n)
		}
	}
	s := &recordingStore{}
	if _, err := NewTeams(s).Create(context.Background(), goodCaller(), "  Service Desk  "); err != nil || s.name != "Service Desk" {
		t.Errorf("name must be trimmed: %q %v", s.name, err)
	}
}

func TestCallerIsRequired(t *testing.T) {
	s := &recordingStore{}
	teams := NewTeams(s)
	for name, c := range map[string]Caller{
		"no actor":          {CorrelationID: "c"},
		"no correlation id": {Actor: audit.UserActor("u")},
		"two actors":        {Actor: audit.Actor{UserID: "u", System: "s"}, CorrelationID: "c"},
	} {
		if _, err := teams.Create(context.Background(), c, "x"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if s.n != 0 {
		t.Errorf("store called %d times for invalid callers", s.n)
	}
}

func TestMemberRoleValidation(t *testing.T) {
	s := &recordingStore{}
	teams := NewTeams(s)
	for _, bad := range []string{"", "Lead", "  lead ", "owner"} {
		bad := bad
		if _, err := teams.AddMember(context.Background(), goodCaller(), "t", "u", &bad); err == nil {
			t.Errorf("role %q must be rejected: only lead and member exist", bad)
		}
	}
	role := "lead"
	if _, err := teams.AddMember(context.Background(), goodCaller(), "t", "u", &role); err != nil || s.role == nil || *s.role != "lead" {
		t.Errorf("lead must pass: %v %v", s.role, err)
	}
	s.role = nil
	if _, err := teams.AddMember(context.Background(), goodCaller(), "t", "u", nil); err != nil || s.role != nil {
		t.Errorf("nil role must pass through (the store makes it a member): %v %v", s.role, err)
	}
}
